package main

// events.go：告警与事件成熟化（分页/关键词/权限隔离/按日期导出/超管二次确认清除/已读水位）
// + 车辆手动注册 + 数字孪生 GLB 模型上传下载。

import (
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const modelsDir = "data/models"

type eventRow struct {
	ID        int64  `json:"id"`
	TSNS      int64  `json:"ts_ns"`
	VehicleID string `json:"vehicle_id"`
	Level     string `json:"level"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	GroupID   string `json:"group_id"`
}

// eventScope 权限隔离：超管看全部（含平台级事件）；
// 其他角色只看本分组事件，平台级事件（group_id 为空）不对其展示。
func eventScope(sess *Session) (cond string, args []any, denied bool) {
	if sess != nil && sess.Role == "super" {
		return "1=1", nil, false
	}
	if sess == nil || len(sess.Groups) == 0 {
		return "1=0", nil, true
	}
	ph := make([]string, len(sess.Groups))
	for i, g := range sess.Groups {
		args = append(args, g)
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	return "group_id IN (" + strings.Join(ph, ",") + ")", args, false
}

func registerEventRoutes(mux *http.ServeMux, svc *Services) {
	st := svc.auth.st

	// 事件列表：分页 + 级别 + 车辆 + 关键词 + 日期范围，权限隔离。
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		ps, _ := strconv.Atoi(q.Get("page_size"))
		if ps < 1 || ps > 200 {
			ps = 20
		}
		cond, args, denied := eventScope(sess)
		if denied || st.db == nil {
			writeJSON(w, http.StatusOK, map[string]any{"items": []eventRow{}, "total": 0, "page": page, "page_size": ps})
			return
		}
		conds := []string{cond}
		if lv := q.Get("level"); lv != "" && lv != "all" {
			args = append(args, lv)
			conds = append(conds, fmt.Sprintf("level=$%d", len(args)))
		}
		if veh := q.Get("vehicle"); veh != "" && veh != "all" {
			args = append(args, veh)
			conds = append(conds, fmt.Sprintf("vehicle_id=$%d", len(args)))
		}
		if kw := strings.TrimSpace(q.Get("q")); kw != "" {
			args = append(args, "%"+kw+"%")
			conds = append(conds, fmt.Sprintf("text ILIKE $%d", len(args)))
		}
		if from := strings.TrimSpace(q.Get("from")); from != "" {
			args = append(args, from+" 00:00:00")
			conds = append(conds, fmt.Sprintf("ts >= $%d::timestamptz", len(args)))
		}
		if to := strings.TrimSpace(q.Get("to")); to != "" {
			args = append(args, to+" 23:59:59")
			conds = append(conds, fmt.Sprintf("ts <= $%d::timestamptz", len(args)))
		}
		if kind := q.Get("kind"); kind == "veh" || kind == "sys" {
			args = append(args, kind)
			conds = append(conds, fmt.Sprintf("kind=$%d", len(args)))
		}
		where := strings.Join(conds, " AND ")

		var total int
		if err := st.db.QueryRow("SELECT count(*) FROM events WHERE "+where, args...).Scan(&total); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		args = append(args, ps)
		limIdx := len(args)
		args = append(args, (page-1)*ps)
		offIdx := len(args)
		rows, err := st.db.Query(
			fmt.Sprintf("SELECT id, ts, coalesce(vehicle_id,''), level, kind, text, group_id FROM events WHERE %s ORDER BY ts DESC, id DESC LIMIT $%d OFFSET $%d", where, limIdx, offIdx),
			args...)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		items := make([]eventRow, 0, ps)
		for rows.Next() {
			var er eventRow
			var ts time.Time
			if err := rows.Scan(&er.ID, &ts, &er.VehicleID, &er.Level, &er.Kind, &er.Text, &er.GroupID); err != nil {
				continue
			}
			er.TSNS = ts.UnixNano()
			items = append(items, er)
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "page_size": ps})
	})

	// 事件分级统计（系统审计页概览卡）：按 kind 返回总数/今日数/各级别数。
	mux.HandleFunc("GET /api/events/stats", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		kind := r.URL.Query().Get("kind")
		if kind != "veh" && kind != "sys" {
			kind = "sys"
		}
		cond, args, denied := eventScope(sess)
		if denied || st.db == nil {
			writeJSON(w, http.StatusOK, map[string]any{"total": 0, "today": 0, "critical": 0, "warn": 0, "info": 0})
			return
		}
		args = append(args, kind)
		kindIdx := len(args)
		var total, today, critical, warn, info int
		_ = st.db.QueryRow(
			fmt.Sprintf("SELECT count(*), count(*) FILTER (WHERE ts >= date_trunc('day', now())), count(*) FILTER (WHERE level='critical'), count(*) FILTER (WHERE level='warn'), count(*) FILTER (WHERE level='info') FROM events WHERE %s AND kind=$%d", cond, kindIdx),
			args...).Scan(&total, &today, &critical, &warn, &info)
		writeJSON(w, http.StatusOK, map[string]any{"total": total, "today": today, "critical": critical, "warn": warn, "info": info})
	})

	// 已读水位：总览小窗只展示水位之后的事件。
	mux.HandleFunc("GET /api/events/readmark", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil || st.db == nil {
			writeJSON(w, http.StatusOK, map[string]any{"read_until_ns": 0})
			return
		}
		var ts time.Time
		err := st.db.QueryRow("SELECT read_until FROM event_read_marks WHERE user_id=$1", sess.Username).Scan(&ts)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"read_until_ns": 0})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"read_until_ns": ts.UnixNano()})
	})
	mux.HandleFunc("POST /api/events/read", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil || st.db == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		_, _ = st.db.Exec("INSERT INTO event_read_marks(user_id, read_until) VALUES ($1, now()) ON CONFLICT (user_id) DO UPDATE SET read_until=now()", sess.Username)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// 按日期导出 CSV（带 BOM，Excel 中文不乱码），权限隔离同上。
	mux.HandleFunc("GET /api/events/export", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		date := strings.TrimSpace(r.URL.Query().Get("date"))
		if date == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date 必填，格式 YYYY-MM-DD"})
			return
		}
		cond, args, denied := eventScope(sess)
		if denied || st.db == nil {
			writeJSON(w, http.StatusOK, map[string]any{"count": 0})
			return
		}
		args = append(args, date+" 00:00:00")
		fromIdx := len(args)
		args = append(args, date+" 23:59:59")
		toIdx := len(args)
		kind := r.URL.Query().Get("kind")
		kindCond := ""
		if kind == "veh" || kind == "sys" {
			args = append(args, kind)
			kindCond = fmt.Sprintf(" AND kind=$%d", len(args))
		}
		rows, err := st.db.Query(
			fmt.Sprintf("SELECT ts, coalesce(vehicle_id,''), level, kind, text, group_id FROM events WHERE %s AND ts >= $%d::timestamptz AND ts <= $%d::timestamptz%s ORDER BY ts ASC", cond, fromIdx, toIdx, kindCond),
			args...)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=events-"+date+".csv")
		_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"时间", "车辆", "级别", "分类", "内容", "分组"})
		n := 0
		for rows.Next() {
			var ts time.Time
			var veh, level, ek, text, group string
			if err := rows.Scan(&ts, &veh, &level, &ek, &text, &group); err != nil {
				continue
			}
			kl := "系统审计"
			if ek == "veh" {
				kl = "车辆告警"
			}
			_ = cw.Write([]string{ts.Format("2006-01-02 15:04:05"), veh, level, kl, text, group})
			n++
		}
		cw.Flush()
	})

	// 清除日志：超管账号密码二次确认；可按单条 id 或日期段清除；写审计。
	mux.HandleFunc("POST /api/events/clear", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		username, _ := body["username"].(string)
		password, _ := body["password"].(string)
		role, ok := svc.auth.CheckCreds(username, password)
		audit := func(detail string) {
			if st.db != nil {
				_, _ = st.db.Exec("INSERT INTO audit_logs(actor, action, detail) VALUES ($1,'events.clear',$2)",
					sess.Username, fmt.Sprintf("%q", detail))
			}
		}
		if !ok || role != "super" {
			audit("失败：超管账密校验不通过")
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "超级管理员账号或密码不正确"})
			return
		}
		if st.db == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": 0})
			return
		}
		var res sql.Result
		var err error
		var detail string
		kind, _ := body["kind"].(string)
		if kind != "veh" && kind != "sys" {
			kind = ""
		}
		if idf, has := body["id"].(float64); has && idf > 0 {
			res, err = st.db.Exec("DELETE FROM events WHERE id=$1", int64(idf))
			detail = fmt.Sprintf("单条 id=%d", int64(idf))
		} else {
			from, _ := body["from"].(string)
			to, _ := body["to"].(string)
			if from == "" || to == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需提供 id 或 from/to 日期段"})
				return
			}
			if kind != "" {
				res, err = st.db.Exec("DELETE FROM events WHERE ts >= $1::timestamptz AND ts <= $2::timestamptz AND kind=$3",
					from+" 00:00:00", to+" 23:59:59", kind)
				detail = fmt.Sprintf("日期段 %s ~ %s（%s）", from, to, kind)
			} else {
				res, err = st.db.Exec("DELETE FROM events WHERE ts >= $1::timestamptz AND ts <= $2::timestamptz",
					from+" 00:00:00", to+" 23:59:59")
				detail = fmt.Sprintf("日期段 %s ~ %s", from, to)
			}
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		n, _ := res.RowsAffected()
		audit(detail + fmt.Sprintf("，删除 %d 条", n))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": n})
	})

	// 车辆手动注册（超管/管理员）。
	mux.HandleFunc("POST /api/vehicles", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super", "group_admin")
		if sess == nil {
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		str := func(k string) string { s, _ := body[k].(string); return strings.TrimSpace(s) }
		id := str("id")
		chassis := str("chassis")
		group := str("group")
		if id == "" || chassis == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "车辆 ID 与底盘类型必填"})
			return
		}
		if sess.Role == "group_admin" {
			inGroup := false
			for _, g := range sess.Groups {
				if g == group {
					inGroup = true
				}
			}
			if !inGroup {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "只能注册到自己管理的分组"})
				return
			}
		}
		if err := st.RegisterVehicle(id, str("vin"), str("gateway_id"), chassis, group); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// 分组名称（登录即可查；超管全部、其他角色仅本分组）——前端归属展示用。
	mux.HandleFunc("GET /api/groups", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		allowed := map[string]bool{}
		for _, g := range sess.Groups {
			allowed[g] = true
		}
		out := make([]map[string]string, 0)
		for _, g := range svc.groups.List() {
			if sess.Role == "super" || allowed[g.ID] {
				out = append(out, map[string]string{"id": g.ID, "name": g.Name})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"groups": out})
	})

	// 数字孪生 GLB 模型上传（超管/管理员），JSON+base64，与地图上传同链路。
	mux.HandleFunc("POST /api/vehicles/{id}/model", func(w http.ResponseWriter, r *http.Request) {
		sess := requireRole(w, r, "super", "group_admin")
		if sess == nil {
			return
		}
		id := r.PathValue("id")
		var body struct {
			Data string `json:"data"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 96<<20))
		if err := dec.Decode(&body); err != nil || body.Data == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "data(base64) 必填"})
			return
		}
		buf, err := base64.StdEncoding.DecodeString(body.Data)
		if err != nil || len(buf) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "base64 解码失败"})
			return
		}
		if err := os.MkdirAll(modelsDir, 0o755); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		p := filepath.Join(modelsDir, mapIDRe.ReplaceAllString(id, "_")+".glb")
		if err := os.WriteFile(p, buf, 0o644); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if st.db != nil {
			_, _ = st.db.Exec("UPDATE vehicles SET model_updated_at=now() WHERE id=$1", id)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "size": len(buf)})
	})

	// 模型下载：登录且有权访问该车辆即可；无自定义模型返回 404，前端必须显示真实不可用态。
	mux.HandleFunc("GET /api/vehicles/{id}/model", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		id := r.PathValue("id")
		if sess == nil || !canAccessVehicle(sess, st, id) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		p := filepath.Join(modelsDir, mapIDRe.ReplaceAllString(id, "_")+".glb")
		f, err := os.Open(p)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no custom model"})
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "model/gltf-binary")
		http.ServeContent(w, r, p, time.Now(), f)
	})
}
