package main

// http.go：REST + WS + 静态站路由（标准库 net/http，无 Web 框架）。
// 路由一览：
//   GET /api/fleet          全量快照（前端唯一契约）
//   GET /api/vehicles/{id}  单车详情
//   GET /api/events?limit=  事件列表（默认 50）
//   GET /api/pointcloud     车辆上传的真实 3D 地图点云
//   GET /ws/fleet           WebSocket（1Hz 状态 + 即时事件）
//   GET /debug/pprof/       goroutine 自检（验收用）
//   /                       go:embed 前端产物（SPA 回退）

import (
	"crypto/rand"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/pprof"
	"net/url"
	"path"
	"strings"
	"time"
)

//go:embed all:web/dist
var webDist embed.FS

// Services 聚合第 10 步之后新增的后端模块（地图/配置/循迹/开放 API/设备绑定/接管登记）。
type Services struct {
	maps      *MapStore
	cfg       *ConfigStore
	nav       *NavStore
	open      *OpenAPI
	auth      *AuthStore
	groups    *GroupStore
	devices   *DeviceStore
	tkreg     *TakeoverReg
	registry  *GatewayRegistry
	authority *DurableAuthority
	st        *State
}

func buildHandler(st *State, hub *Hub, svc *Services) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		runtime := st.RuntimeStatus()
		if !runtime.DatabaseReady || !runtime.PersistentWritesReady || !runtime.MQTTReady {
			writeJSON(w, http.StatusServiceUnavailable, runtime)
			return
		}
		writeJSON(w, http.StatusOK, runtime)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		serveMetrics(st, w, r)
	})
	mux.HandleFunc("GET /api/fleet", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, filterSnapFor(st.Snapshot(), sessOf(r)))
	})
	mux.HandleFunc("GET /api/runtime", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, st.RuntimeStatus())
	})
	mux.HandleFunc("GET /api/auth/csrf", func(w http.ResponseWriter, r *http.Request) {
		tokenBytes := make([]byte, 32)
		if _, err := rand.Read(tokenBytes); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法生成 CSRF 令牌"})
			return
		}
		token := b64(tokenBytes)
		http.SetCookie(w, &http.Cookie{
			Name: csrfCookieName, Value: token, Path: "/", MaxAge: 3600,
			HttpOnly: false, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
		})
		writeJSON(w, http.StatusOK, map[string]string{"csrf_token": token})
	})
	mux.HandleFunc("GET /api/vehicles/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := st.Vehicle(r.PathValue("id"))
		if !ok || !canAccessVehicle(sessOf(r), st, v.VehicleID) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "vehicle not found"})
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	mux.HandleFunc("GET /api/pointcloud", func(w http.ResponseWriter, r *http.Request) {
		vehicleID := r.URL.Query().Get("vehicle_id")
		if vehicleID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "vehicle_id 必填；点云仅返回指定车辆上传的真实地图"})
			return
		}
		if !canAccessVehicle(sessOf(r), st, vehicleID) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "vehicle not found"})
			return
		}
		points, err := svc.maps.LatestPointsForVehicle(vehicleID, 100000)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, pointCloudResp{
			SceneID:     points.MapID,
			MapID:       points.MapID,
			VehicleID:   vehicleID,
			Version:     points.Version,
			Name:        points.Name,
			GeneratedNS: time.Now().UnixNano(),
			Static: cloudPart{
				Count:       points.Count,
				Positions:   points.Positions,
				Intensities: points.Intensities,
			},
			Vehicles: []vehicleCloud{},
		})
	})
	mux.HandleFunc("GET /ws/fleet", func(w http.ResponseWriter, r *http.Request) {
		hub.ServeWS(st, svc.auth, sessOf(r)).ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /ws/terminal", ServeWSTerminal)

	// 历史 UDP 接管桥接已停用：它绕过受信控制设备准入，也不能提供
	// PostgreSQL fencing/幂等续租保证。唯一允许的接管入口是 /takeover/take。
	mux.HandleFunc("POST /api/takeover/request", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusGone, map[string]string{"error": "旧接管入口已停用；请绑定并验证受信控制设备后使用 /api/takeover/take"})
	})
	mux.HandleFunc("POST /api/takeover/renew", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusGone, map[string]string{"error": "旧续租入口已停用；驾驶页使用 /api/takeover/keepalive 的幂等续租"})
	})
	mux.HandleFunc("POST /api/takeover/release", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		current := svc.tkreg.Mine(sess.Username)
		if current == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "当前账号没有可交还的接管租约"})
			return
		}
		err := svc.authority.Release(r.Context(), current.LeaseID, sess.Username, current.DeviceID, "operator_release")
		if err == nil {
			if rel := svc.tkreg.Release(sess.Username); rel != nil {
				st.pushEvent("info", rel.VehicleID, fmt.Sprintf("控制权已交还：%s 释放 %s", sess.Username, rel.VehicleID), "sys")
			}
		}
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/emergency-stop", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		vehicleID, _ := body["vehicle_id"].(string)
		v, ok := st.Vehicle(vehicleID)
		if vehicleID == "" || !ok || !v.Online || !canAccessVehicle(sess, st, vehicleID) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "只能对本分组在线车辆执行紧急停车"})
			return
		}
		if err := svc.authority.EmergencyStop(r.Context(), vehicleID, sess.Username); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "紧急停车签名撤销送达失败：" + err.Error()})
			return
		}
		st.pushEvent("critical", vehicleID, "紧急停车已触发：已签名撤销控制租约，车端进入最小风险流程", "veh")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": "signed_lease_revoke"})
	})

	mux.HandleFunc("GET /debug/pprof/", guardedPprof(pprof.Index))
	mux.HandleFunc("GET /debug/pprof/cmdline", guardedPprof(pprof.Cmdline))
	mux.HandleFunc("GET /debug/pprof/profile", guardedPprof(pprof.Profile))
	mux.HandleFunc("GET /debug/pprof/symbol", guardedPprof(pprof.Symbol))
	mux.HandleFunc("GET /debug/pprof/trace", guardedPprof(pprof.Trace))

	registerMapRoutes(mux, svc)
	registerConfigRoutes(mux, svc)
	registerNavRoutes(mux, svc)
	registerOpenAPIRoutes(mux, svc)
	registerAuthRoutes(mux, svc)
	registerAdminRoutes(mux, svc)
	registerEventRoutes(mux, svc)
	registerDeviceRoutes(mux, svc)
	registerGatewayRegistryRoutes(mux, st, svc)
	registerTakeoverRoutes(mux, st, svc)
	registerWorkspaceRoutes(mux, svc)

	mux.Handle("/", spaHandler())
	// 顺序：鉴权（最外）→ 安全头/防索引 → CORS → 路由
	return withMetrics(st, svc.auth.Middleware(withSecurityHeaders(withCORS(mux))))
}

// withCORS 开发期放开 /api/*（vite dev 代理本就同源）；
// 生产入口由 nginx 收敛，可按需收紧。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			origin := r.Header.Get("Origin")
			if origin != "" {
				u, err := url.Parse(origin)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
					!(strings.EqualFold(u.Host, r.Host) || (isLoopbackHost(r.Host) && isLoopbackHost(u.Host))) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "跨域来源不受信任"})
					return
				}
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers",
				"Content-Type, Authorization, X-API-Key, X-Signature, X-Timestamp, X-Nonce, X-CSRF-Token")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// guardedPprof keeps diagnostic profiles out of the public/static path. A
// profile can contain credentials, vehicle IDs and request data; loopback
// alone is not sufficient because a browser or a local untrusted process can
// still request it. Super-admin authentication and a loopback peer are both
// required, and a non-privileged/remote request receives no profile bytes.
func guardedPprof(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !loopbackRemote(r.RemoteAddr) {
			http.NotFound(w, r)
			return
		}
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "pprof 需要超级管理员会话"})
			return
		}
		if sess.Role != "super" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "pprof 仅限超级管理员"})
			return
		}
		handler(w, r)
	}
}

// withSecurityHeaders 等保三级配套响应头 + 防搜索引擎索引（X-Robots-Tag）。
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// filterSnapFor 按会话分组过滤全量快照：超管全量；其他角色只见本分组车辆/事件。
func filterSnapFor(snap FleetSnap, sess *Session) FleetSnap {
	if sess == nil || sess.Role == "super" {
		return snap
	}
	// Authority 的全局租约状态不携带 vehicle_id，无法可靠映射到分组。
	// 在没有可验证资源归属时按默认拒绝处理，避免泄露其他分组的驾驶员/租约元数据。
	snap.Takeover = TakeoverSnap{Active: false}
	allowed := map[string]bool{}
	for _, g := range sess.Groups {
		allowed[g] = true
	}
	vGroup := map[string]string{}
	for _, v := range snap.Vehicles {
		vGroup[v.VehicleID] = v.Group
	}
	vs := make([]VehicleSnap, 0, len(snap.Vehicles))
	for _, v := range snap.Vehicles {
		if allowed[v.Group] {
			vs = append(vs, v)
		}
	}
	snap.Vehicles = vs
	evs := make([]EventSnap, 0, len(snap.Events))
	for _, e := range snap.Events {
		if e.VehicleID == "" {
			continue // 平台级事件只给超管看
		}
		if allowed[vGroup[e.VehicleID]] {
			evs = append(evs, e)
		}
	}
	snap.Events = evs
	return snap
}

// canAccessVehicle 该会话能否操作此车（超管全量；否则须同分组）。
func canAccessVehicle(sess *Session, st *State, vehicleID string) bool {
	if sess == nil {
		return false
	}
	if sess.Role == "super" {
		return true
	}
	v, ok := st.Vehicle(vehicleID)
	if !ok {
		return false
	}
	for _, g := range sess.Groups {
		if g == v.Group {
			return true
		}
	}
	return false
}

// spaHandler 服务内嵌前端产物；未命中的路径回退 index.html（SPA 路由）。
func spaHandler() http.Handler {
	sub, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		panic(err) // embed 在编译期确定，运行期不可能失败
	}
	fileSrv := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if p != "." && p != "" {
			if _, err := sub.Open(p); err != nil {
				r.URL.Path = "/"
			}
		}
		fileSrv.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// readJSONBody 读取并解析请求体（严格限 4KB，防滥用）；失败已回 400/413。
func readJSONBody(w http.ResponseWriter, r *http.Request) map[string]any {
	const maxJSONBody = 4096
	defer r.Body.Close()
	if r.ContentLength > maxJSONBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "JSON 请求体超过 4KB 限制"})
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "读取请求体失败"})
		return nil
	}
	if len(body) > maxJSONBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "JSON 请求体超过 4KB 限制"})
		return nil
	}
	var m map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &m); err != nil || m == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return nil
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func writeControlResult(w http.ResponseWriter, resp map[string]any, err error) {
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
