package main

// routes.go：第 10 步后新增模块的 REST 路由（地图中心/系统配置/循迹导航/开放 API 管理面）。
// 开放面 /open/v1/* 的鉴权路由在 openapi.go 的 ServeOpen。

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

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

// ---------- 地图中心 ----------

func registerMapRoutes(mux *http.ServeMux, svc *Services) {
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
		if req.Source == "" {
			req.Source = "manual"
		}
		m, changed, err := svc.maps.Register(req.VehicleID, req.Name, req.Source, req.Author, data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed, "map": m})
	})

	mux.HandleFunc("GET /api/maps/{id}/file", func(w http.ResponseWriter, r *http.Request) {
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
		v, err := svc.maps.Convert(r.PathValue("id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": v})
	})

	// 编辑页保存：原始只读，编辑结果落新版本。
	mux.HandleFunc("POST /api/maps/{id}/edit", func(w http.ResponseWriter, r *http.Request) {
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
		if req.Author == "" {
			req.Author = "admin"
		}
		v, err := svc.maps.SaveEdit(r.PathValue("id"), req.Author, png, req.Ops)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": v})
	})
}

// ---------- 系统配置 ----------

func registerConfigRoutes(mux *http.ServeMux, svc *Services) {
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, svc.cfg.All())
	})
	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		merged, rejected := svc.cfg.Set(body)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": merged, "rejected": rejected})
	})
}

// ---------- 循迹导航 ----------

func registerNavRoutes(mux *http.ServeMux, svc *Services) {
	mux.HandleFunc("GET /api/nav/routes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"routes": svc.nav.List()})
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
		rt, err := svc.nav.Submit(vid, name, "console", trace, pts)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "route": rt})
	})
	mux.HandleFunc("POST /api/nav/routes/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		rt, err := svc.nav.Cancel(r.PathValue("id"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "route": rt})
	})
}

// ---------- 开放 API 管理面（Key 管理 + 审计查询；调用面在 /open/v1） ----------

func registerOpenAPIRoutes(mux *http.ServeMux, svc *Services) {
	mux.HandleFunc("POST /api/openkeys", func(w http.ResponseWriter, r *http.Request) {
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		name, _ := body["name"].(string)
		k, secret, err := svc.open.CreateKey(name)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// 明文只在创建响应里出现这一次（等保：密钥不落明文存储）
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": k, "secret": secret})
	})
	mux.HandleFunc("GET /api/openkeys", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"keys": svc.open.ListKeys()})
	})
	mux.HandleFunc("POST /api/openkeys/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := svc.open.Revoke(r.PathValue("id")); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/audit", func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if q := r.URL.Query().Get("limit"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 {
				limit = n
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"audit": svc.open.Audit(limit)})
	})
	mux.HandleFunc("/open/v1/", svc.open.ServeOpen)
}
