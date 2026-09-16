package main

// routes.go：第 10 步后新增模块的 REST 路由（地图中心/系统配置/循迹导航/开放 API 管理面）。
// 开放面 /open/v1/* 的鉴权路由在 openapi.go 的 ServeOpen。

import (
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func contentTypeOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".pgm":
		return "image/x-portable-graymap"
	case ".yaml", ".yml":
		return "text/yaml; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	}
	return "application/octet-stream"
}

func canAccessMap(sess *Session, st *State, m *MapEntry) bool {
	return m != nil && canAccessVehicle(sess, st, m.VehicleID)
}

// ---------- 地图中心 ----------

func registerMapRoutes(mux *http.ServeMux, svc *Services) {
	mux.HandleFunc("GET /api/maps/publications", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		vehicleID := strings.TrimSpace(r.URL.Query().Get("vehicle_id"))
		publications, err := svc.maps.ListMapPublications(r.Context(), vehicleID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "地图发布查询失败"})
			return
		}
		out := make([]mapPublication, 0, len(publications))
		for _, publication := range publications {
			if canAccessVehicle(sess, svc.st, publication.VehicleID) {
				out = append(out, publication)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"publications": out})
	})

	mux.HandleFunc("GET /api/maps", func(w http.ResponseWriter, r *http.Request) {
		all := svc.maps.List()
		sess := sessOf(r)
		if sess != nil && sess.Role != "super" {
			vGroup := map[string]string{}
			if svc.auth != nil && svc.auth.st != nil {
				for _, v := range svc.auth.st.Snapshot().Vehicles {
					vGroup[v.VehicleID] = v.Group
				}
			}
			allowed := map[string]bool{}
			for _, g := range sess.Groups {
				allowed[g] = true
			}
			out := make([]*MapEntry, 0, len(all))
			for _, m := range all {
				if allowed[vGroup[m.VehicleID]] {
					out = append(out, m)
				}
			}
			all = out
		}
		writeJSON(w, http.StatusOK, map[string]any{"maps": all})
	})

	// 车端/前端统一上传口：JSON + base64（避免 multipart，链路更简单）。
	// 上限 128MB：覆盖单张建图产物；更大的走阶段 3 COPC 流式。
	mux.HandleFunc("POST /api/maps/upload", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super", "group_admin")
		if sess == nil {
			return
		}
		body := readRawBody(w, r, 128<<20)
		if body == nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "请求体过大（上限 128MB）"})
			return
		}
		var req struct {
			VehicleID  string `json:"vehicle_id"`
			Name       string `json:"name"`
			Source     string `json:"source"` // vehicle_push|manual
			Author     string `json:"author"`
			DataBase64 string `json:"data_base64"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		data, err := base64.StdEncoding.DecodeString(req.DataBase64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "data_base64 解码失败"})
			return
		}
		if req.Source != "" && req.Source != "manual" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "控制台仅允许人工上传；车端地图必须经受信设备通道上报"})
			return
		}
		if !canAccessVehicle(sess, svc.st, req.VehicleID) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "无权上传该车辆的地图"})
			return
		}
		m, changed, err := svc.maps.Register(req.VehicleID, req.Name, "manual", sess.Username, data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed, "map": m})
	})

	mux.HandleFunc("GET /api/maps/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		m := svc.maps.Get(r.PathValue("id"))
		if !canAccessMap(sessOf(r), svc.st, m) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "map not found"})
			return
		}
		name := r.URL.Query().Get("name")
		v, _ := strconv.Atoi(r.URL.Query().Get("v"))
		b, err := svc.maps.File(r.PathValue("id"), name, v)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", contentTypeOf(name))
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})

	// 一行命令 3D→2D：服务端调 map_convert.py，产物落新版本。
	mux.HandleFunc("POST /api/maps/{id}/convert", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super", "group_admin")
		if sess == nil {
			return
		}
		if !canAccessMap(sess, svc.st, svc.maps.Get(r.PathValue("id"))) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "无权转换该地图"})
			return
		}
		v, err := svc.maps.Convert(r.PathValue("id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": v})
	})

	// 编辑页保存：原始只读，编辑结果落新版本。
	mux.HandleFunc("POST /api/maps/{id}/edit", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super", "group_admin")
		if sess == nil {
			return
		}
		if !canAccessMap(sess, svc.st, svc.maps.Get(r.PathValue("id"))) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "无权编辑该地图"})
			return
		}
		body := readRawBody(w, r, 64<<20)
		if body == nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "请求体过大"})
			return
		}
		var req struct {
			Author    string `json:"author"`
			PNGBase64 string `json:"png_base64"`
			Ops       any    `json:"ops"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		png, err := base64.StdEncoding.DecodeString(req.PNGBase64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "png_base64 解码失败"})
			return
		}
		v, err := svc.maps.SaveEdit(r.PathValue("id"), sess.Username, png, req.Ops)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": v})
	})

	// 3D 预览点云：服务端解析最新 3D 版本（pcd/csv），返回降采样点云
	// （与 /api/pointcloud 同形状，前端 three.js 直接渲染）。
	mux.HandleFunc("GET /api/maps/{id}/points", func(w http.ResponseWriter, r *http.Request) {
		if !canAccessMap(sessOf(r), svc.st, svc.maps.Get(r.PathValue("id"))) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "map not found"})
			return
		}
		v, _ := strconv.Atoi(r.URL.Query().Get("v"))
		max, _ := strconv.Atoi(r.URL.Query().Get("max"))
		p, err := svc.maps.Points(r.PathValue("id"), v, max)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	// 删除地图：整张地图连同全部版本与磁盘文件删除（车上原件不动）。
	// 可见性与 GET /api/maps 一致：非超管仅能删本分组车辆的地图。
	mux.HandleFunc("DELETE /api/maps/{id}", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super", "group_admin")
		if sess == nil {
			return
		}
		id := r.PathValue("id")
		m := svc.maps.Get(id)
		if m == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "地图不存在：" + id})
			return
		}
		if !canAccessMap(sess, svc.st, m) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "仅可删除本分组车辆的地图"})
			return
		}
		if err := svc.maps.Delete(id); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// 发布先生成 requested 审批记录；不能把上传成功或平台审批成功
	// 误当成车辆已安装。车端确认由 MQTT map_ack 完成。
	mux.HandleFunc("POST /api/maps/{id}/publish", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super", "group_admin")
		if sess == nil {
			return
		}
		m := svc.maps.Get(r.PathValue("id"))
		if m == nil || !canAccessMap(sess, svc.st, m) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "地图不存在或无权访问"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		vehicleID := strings.TrimSpace(stringOf(body["vehicle_id"]))
		version := 0
		switch value := body["version"].(type) {
		case float64:
			version = int(value)
		case string:
			version, _ = strconv.Atoi(strings.TrimSpace(value))
		}
		frame := strings.TrimSpace(stringOf(body["coordinate_frame"]))
		if vehicleID == "" || !canAccessVehicle(sess, svc.st, vehicleID) || vehicleID != m.VehicleID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "发布目标车辆不属于当前地图或无权访问"})
			return
		}
		publication, err := svc.maps.RequestMapPublication(r.Context(), m.ID, vehicleID, version, "apply", frame, sess.Username)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "publication": publication,
			"warning": "必须经独立审批且收到真实车端 APPLIED ACK 后才会标记 active"})
	})

	mux.HandleFunc("POST /api/maps/publications/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super", "group_admin")
		if sess == nil {
			return
		}
		publications, err := svc.maps.ListMapPublications(r.Context(), "")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "地图发布查询失败"})
			return
		}
		var target *mapPublication
		for i := range publications {
			if publications[i].ID == r.PathValue("id") {
				target = &publications[i]
				break
			}
		}
		if target == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "地图发布不存在"})
			return
		}
		if target.RequestedBy == sess.Username {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "申请人不能批准自己的地图发布"})
			return
		}
		if !canAccessVehicle(sess, svc.st, target.VehicleID) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "无权批准该车辆地图发布"})
			return
		}
		approved, err := svc.maps.ApproveMapPublication(r.Context(), target.ID, sess.Username)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "publication": approved,
			"warning": "approved 仅表示已进入可靠下行队列；车端 APPLIED ACK 前不会生效"})
	})

	mux.HandleFunc("POST /api/maps/publications/{id}/rollback", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super", "group_admin")
		if sess == nil {
			return
		}
		publications, err := svc.maps.ListMapPublications(r.Context(), "")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "地图发布查询失败"})
			return
		}
		var target *mapPublication
		for i := range publications {
			if publications[i].ID == r.PathValue("id") {
				target = &publications[i]
				break
			}
		}
		if target == nil || !canAccessVehicle(sess, svc.st, target.VehicleID) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "地图发布不存在或无权访问"})
			return
		}
		rollback, err := svc.maps.RollbackMapPublication(r.Context(), target.ID, sess.Username)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "publication": rollback,
			"warning": "回滚仍须独立审批，并等待真实车端 APPLIED ACK"})
	})
}

// ---------- 系统配置 ----------

func registerConfigRoutes(mux *http.ServeMux, svc *Services) {
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		if requireRole(w, r, "super") == nil {
			return
		}
		writeJSON(w, http.StatusOK, svc.cfg.All())
	})
	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		if requireRole(w, r, "super") == nil {
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		sess := sessOf(r)
		actor := ""
		if sess != nil {
			actor = sess.Username
		}
		merged, rejected, err := svc.cfg.SetForActor(body, actor)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "保存配置") || strings.Contains(err.Error(), "提交配置") || strings.Contains(err.Error(), "开始配置") {
				status = http.StatusInternalServerError
			}
			writeJSON(w, status, map[string]any{"ok": false, "config": merged, "rejected": rejected, "error": err.Error()})
			return
		}
		if svc.st != nil && svc.st.db != nil {
			_, _ = svc.st.db.ExecContext(r.Context(), "INSERT INTO audit_logs(actor, action, detail) VALUES ($1,'config.update',$2)", actor, mustJSON(map[string]any{"keys": sortedKeys(body)}))
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": merged})
	})
}

// ---------- 循迹导航 ----------

func registerNavRoutes(mux *http.ServeMux, svc *Services) {
	mux.HandleFunc("GET /api/nav/routes", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		routes := make([]*NavRoute, 0)
		for _, route := range svc.nav.List() {
			if canAccessVehicle(sess, svc.st, route.VehicleID) {
				routes = append(routes, route)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"routes": routes})
	})
	mux.HandleFunc("POST /api/nav/routes", func(w http.ResponseWriter, r *http.Request) {
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		vid, _ := body["vehicle_id"].(string)
		name, _ := body["name"].(string)
		trace, _ := body["trace_id"].(string)
		if !canAccessVehicle(sessOf(r), svc.auth.st, vid) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "无权调度该车辆（不在你的分组）"})
			return
		}
		var pts []NavPoint
		if raw, ok := body["points"]; ok {
			b, _ := json.Marshal(raw)
			if err := json.Unmarshal(b, &pts); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "points 解析失败"})
				return
			}
		}
		remark, _ := body["remark"].(string)
		rt, err := svc.nav.Submit(vid, name, "console", trace, remark, pts)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "route": rt})
	})
	mux.HandleFunc("POST /api/nav/routes/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		route, ok := svc.nav.Get(r.PathValue("id"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在"})
			return
		}
		if !canAccessVehicle(sess, svc.st, route.VehicleID) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "无权取消该车辆的任务"})
			return
		}
		rt, err := svc.nav.Cancel(route.ID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "route": rt})
	})
}

// ---------- 开放 API 管理面（Key 管理 + 审计查询；调用面在 /open/v1） ----------
// API Key 可以跨越控制台会话并代表平台访问，属于全局高权限凭据；其创建、修改、撤销和
// 审计仅限超级管理员。分组管理员不能借由 "*" 车辆白名单越过自身数据范围。

func strSliceOf(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func parseAuditBound(raw string, endOfDay bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) == len("2006-01-02") {
		d, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("审计日期必须是 YYYY-MM-DD 或 RFC3339")
		}
		if endOfDay {
			d = d.AddDate(0, 0, 1)
		}
		return &d, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, fmt.Errorf("审计时间必须是 YYYY-MM-DD 或 RFC3339")
	}
	t = t.UTC()
	return &t, nil
}

func auditFilterFromQuery(r *http.Request) (AuditFilter, error) {
	q := r.URL.Query()
	source := strings.TrimSpace(q.Get("source"))
	if source == "" {
		source = "api"
	}
	if source != "api" && source != "platform" && source != "all" {
		return AuditFilter{}, fmt.Errorf("source 必须是 api、platform 或 all")
	}
	f := AuditFilter{Limit: 100, KeyID: strings.TrimSpace(q.Get("key_id")), Result: strings.TrimSpace(q.Get("result")), Source: source, Cursor: strings.TrimSpace(q.Get("cursor"))}
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			return f, fmt.Errorf("limit 必须是 1 到 500 的整数")
		}
		f.Limit = n
	}
	var err error
	if f.From, err = parseAuditBound(q.Get("from"), false); err != nil {
		return f, err
	}
	if f.To, err = parseAuditBound(q.Get("to"), true); err != nil {
		return f, err
	}
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		return f, fmt.Errorf("审计 from 必须早于 to")
	}
	return f, nil
}

func logAuditExportFailure(err error) {
	if err != nil {
		log.Printf("[audit] 导出中止：%v", err)
	}
}

func registerOpenAPIRoutes(mux *http.ServeMux, svc *Services) {
	// 创建 Key：名称/备注 + 功能白名单 + 车辆白名单（未勾选即不允许）。
	mux.HandleFunc("POST /api/openkeys", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super")
		if sess == nil {
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		name, _ := body["name"].(string)
		remark, _ := body["remark"].(string)
		expiry, hasExpiry, err := svc.open.parseAPIKeyExpiry(body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var k *APIKey
		var secret string
		if hasExpiry {
			k, secret, err = svc.open.CreateKeyWithExpiry(name, remark, strSliceOf(body["scopes"]), strSliceOf(body["vehicles"]), strSliceOf(body["ips"]), sess.Username, expiry)
		} else {
			k, secret, err = svc.open.CreateKey(name, remark, strSliceOf(body["scopes"]), strSliceOf(body["vehicles"]), strSliceOf(body["ips"]), sess.Username)
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// 明文只在创建响应里出现这一次（等保：密钥不落明文存储）
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": k, "secret": secret})
	})
	mux.HandleFunc("GET /api/openkeys", func(w http.ResponseWriter, r *http.Request) {
		if requireRole(w, r, "super") == nil {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": svc.open.ListKeys()})
	})
	// 修改 Key：名称/备注/功能白名单/车辆白名单（字段缺省即不动）。
	mux.HandleFunc("POST /api/openkeys/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super")
		if sess == nil {
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		var p KeyPatch
		if v, ok := body["name"].(string); ok {
			p.Name = &v
		}
		if v, ok := body["remark"].(string); ok {
			p.Remark = &v
		}
		if v, ok := body["scopes"]; ok {
			s := strSliceOf(v)
			p.Scopes = &s
		}
		if v, ok := body["vehicles"]; ok {
			s := strSliceOf(v)
			p.Vehicles = &s
		}
		if v, ok := body["ips"]; ok {
			s := strSliceOf(v)
			p.IPs = &s
		}
		if _, hasAt := body["expires_at"]; hasAt {
			if _, hasIn := body["expires_in_s"]; hasIn {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expires_at 与 expires_in_s 只能填写一个"})
				return
			}
		}
		if _, hasAt := body["expires_at"]; hasAt {
			expiry, _, err := svc.open.parseAPIKeyExpiry(body)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			ns := expiry.UnixNano()
			p.ExpiresNS = &ns
		} else if _, hasIn := body["expires_in_s"]; hasIn {
			expiry, _, err := svc.open.parseAPIKeyExpiry(body)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			ns := expiry.UnixNano()
			p.ExpiresNS = &ns
		}
		k, err := svc.open.UpdateKey(r.PathValue("id"), p, sess.Username)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": k})
	})
	mux.HandleFunc("POST /api/openkeys/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super")
		if sess == nil {
			return
		}
		if err := svc.open.Revoke(r.PathValue("id"), sess.Username); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	// 功能权限目录（建 Key 时的勾选项来源）。
	mux.HandleFunc("GET /api/openscopes", func(w http.ResponseWriter, r *http.Request) {
		if requireRole(w, r, "super") == nil {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"scopes": svc.open.ScopeCatalog()})
	})
	// 审计查询：支持按 Key / 结果过滤与 keyset 游标分页。审计证据来自
	// PostgreSQL；进程内 ring 只在短暂数据库读故障时作为有界降级读缓存。
	mux.HandleFunc("GET /api/audit", func(w http.ResponseWriter, r *http.Request) {
		if requireRole(w, r, "super") == nil {
			return
		}
		f, err := auditFilterFromQuery(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		page, pageErr := svc.open.AuditPage(f)
		if pageErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": pageErr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"audit": page.Entries, "next_cursor": page.NextCursor, "has_more": page.HasMore})
	})
	// 审计导出：只从不可变 audit_log 读取，严格限制单次行数并使用 keyset
	// 游标，防止大表 OFFSET 扫描和一次性把全库加载进内存。导出不含任何
	// API secret，只包含调用证据字段；权限与查询接口相同，仅限超级管理员。
	mux.HandleFunc("GET /api/audit/export", func(w http.ResponseWriter, r *http.Request) {
		if requireRole(w, r, "super") == nil {
			return
		}
		f, err := auditFilterFromQuery(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		maxRows := 10000
		if q := r.URL.Query().Get("limit"); q != "" {
			n, parseErr := strconv.Atoi(q)
			if parseErr != nil || n < 1 || n > 10000 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit 必须是 1 到 10000 的整数"})
				return
			}
			maxRows = n
		}
		if svc.open == nil || svc.open.database() == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "审计导出需要 PostgreSQL 持久化"})
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="robot-agent-audit.csv"`)
		cw := csv.NewWriter(w)
		// BOM 仅用于兼容中文办公软件；字段仍由 csv.Writer 正确引用。
		_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
		if err := cw.Write([]string{"id", "timestamp", "source", "actor", "action", "key_id", "key_name", "method", "path", "result", "http", "ip", "trace_id", "vehicle_id", "remark", "detail"}); err != nil {
			return
		}
		written := 0
		for written < maxRows {
			f.Limit = minInt(500, maxRows-written)
			page, pageErr := svc.open.queryAuditDBPage(f)
			if pageErr != nil {
				// Headers may already be committed. The connection is closed after
				// the partial export; the server log contains the precise failure.
				logAuditExportFailure(pageErr)
				return
			}
			for _, e := range page.Entries {
				if err := cw.Write([]string{
					strconv.FormatInt(e.ID, 10), time.Unix(0, e.TsNS).UTC().Format(time.RFC3339Nano),
					e.Source, e.Actor, e.Action, e.KeyID, e.KeyName, e.Method, e.Path, e.Result, strconv.Itoa(e.HTTP),
					e.IP, e.TraceID, e.VehicleID, e.Remark, e.Detail,
				}); err != nil {
					return
				}
				written++
			}
			if !page.HasMore || len(page.Entries) == 0 || written >= maxRows {
				break
			}
			if page.NextCursor == "" {
				logAuditExportFailure(fmt.Errorf("审计分页缺少下一游标"))
				return
			}
			f.Cursor = page.NextCursor
		}
		cw.Flush()
	})
	mux.HandleFunc("/open/v1/", svc.open.ServeOpen)
}
