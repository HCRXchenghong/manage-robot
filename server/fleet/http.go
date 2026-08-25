package main

// http.go：REST + WS + 静态站路由（标准库 net/http，无 Web 框架）。
// 路由一览：
//   GET /api/fleet          全量快照（前端唯一契约）
//   GET /api/vehicles/{id}  单车详情
//   GET /api/events?limit=  事件列表（默认 50）
//   GET /api/pointcloud     激光雷达点云（阶段 1 合成）
//   GET /ws/fleet           WebSocket（1Hz 状态 + 即时事件）
//   GET /debug/pprof/       goroutine 自检（验收用）
//   /                       go:embed 前端产物（SPA 回退）

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/pprof"
	"path"
	"strings"
)

//go:embed all:web/dist
var webDist embed.FS

// Services 聚合第 10 步之后新增的后端模块（地图/配置/循迹/开放 API）。
type Services struct {
	maps   *MapStore
	cfg    *ConfigStore
	nav    *NavStore
	open   *OpenAPI
	auth   *AuthStore
	groups *GroupStore
}

func buildHandler(st *State, hub *Hub, pc *pointCloudGen, svc *Services) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/fleet", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, filterSnapFor(st.Snapshot(), sessOf(r)))
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
		writeJSON(w, http.StatusOK, pc.generate(st.Snapshot().Vehicles, r.URL.Query().Get("vehicle_id")))
	})
	mux.HandleFunc("GET /ws/fleet", hub.ServeWS(st))
	mux.HandleFunc("GET /ws/terminal", ServeWSTerminal)

	// 控制桥接（第 10 步任务 4）：转发到 control-authority，安全链路不变。
	mux.HandleFunc("POST /api/takeover/request", func(w http.ResponseWriter, r *http.Request) {
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		resp, err := st.authorityCall(map[string]any{"op": "request", "driver": driverOf(body)})
		writeControlResult(w, resp, err)
	})
	mux.HandleFunc("POST /api/takeover/renew", func(w http.ResponseWriter, r *http.Request) {
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		// authority 的 request 语义即「签发新租约顶掉旧的」，等价于续租。
		resp, err := st.authorityCall(map[string]any{"op": "request", "driver": driverOf(body)})
		writeControlResult(w, resp, err)
	})
	mux.HandleFunc("POST /api/takeover/release", func(w http.ResponseWriter, r *http.Request) {
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		resp, err := st.authorityCall(map[string]any{"op": "release", "driver": driverOf(body)})
		writeControlResult(w, resp, err)
	})
	mux.HandleFunc("POST /api/emergency-stop", func(w http.ResponseWriter, r *http.Request) {
		resp, err := st.authorityCall(map[string]any{"op": "emergency_stop"})
		if err == nil {
			if ok, _ := resp["ok"].(bool); ok {
				// 车端语义：零速指令被接受即立即执行，随后看门狗收敛到 stopped。
				// 大屏侧在此即刻记录 critical 事件（不等模式绕行一圈）。
				st.pushEvent("critical", "", "紧急停车已触发：零速指令送达车端并被接受，车辆进入停车流程")
			}
		}
		writeControlResult(w, resp, err)
	})

	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)

	registerMapRoutes(mux, svc)
	registerConfigRoutes(mux, svc)
	registerNavRoutes(mux, svc)
	registerOpenAPIRoutes(mux, svc)
	registerAuthRoutes(mux, svc)
	registerAdminRoutes(mux, svc)
	registerEventRoutes(mux, svc)

	mux.Handle("/", spaHandler())
	// 顺序：鉴权（最外）→ 安全头/防索引 → CORS → 路由
	return svc.auth.Middleware(withSecurityHeaders(withCORS(mux)))
}

// withCORS 开发期放开 /api/*（vite dev 代理本就同源）；
// 生产入口由 nginx 收敛，可按需收紧。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers",
				"Content-Type, Authorization, X-API-Key, X-Signature, X-Timestamp, X-Nonce")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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

// readJSONBody 读取并解析请求体（限 4KB，防滥用）；失败已回 400。
func readJSONBody(w http.ResponseWriter, r *http.Request) map[string]any {
	defer r.Body.Close()
	body := make([]byte, 0, 256)
	buf := make([]byte, 256)
	for len(body) < 4096 {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	var m map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &m); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return nil
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func driverOf(body map[string]any) string {
	if d, ok := body["driver"].(string); ok && d != "" {
		return d
	}
	return "admin" // 阶段 1 未接登录，默认操作者（阶段 2 由 OIDC 注入）
}

func writeControlResult(w http.ResponseWriter, resp map[string]any, err error) {
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
