package main

// takeover_reg.go：账号级接管登记表。
// 两条硬规则：
//   1) 一个账号同一时刻只能接管一辆车；
//   2) 一辆车同一时刻只能被一个账号接管。
// 租约仍由 DurableAuthority 安全链路签发（fencing 单调），本表只负责
// 「账号 ↔ 车辆」的互斥映射、续租心跳与运营列表展示。
// 它不是安全真相源：实际租约只在 DurableAuthority/PostgreSQL 中存在。这里
// 只保存 UI 投影；真正的续租宽限由 Authority 事务按策略计算。

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const takeoverGraceNS = int64(0)

type ActiveTakeover struct {
	VehicleID string `json:"vehicle_id"`
	GatewayID string `json:"gateway_id,omitempty"`
	Driver    string `json:"driver"`
	DeviceID  string `json:"device_id"`
	LeaseID   string `json:"lease_id"`
	Fencing   int64  `json:"fencing"`
	UntilNS   int64  `json:"until_ns"`
	StartedNS int64  `json:"started_ns"`
}

type TakeoverReg struct {
	mu        sync.Mutex
	byUser    map[string]*ActiveTakeover
	byVehicle map[string]*ActiveTakeover
	db        *sql.DB
}

func NewTakeoverReg(databases ...*sql.DB) *TakeoverReg {
	var db *sql.DB
	if len(databases) > 0 {
		db = databases[0]
	}
	return &TakeoverReg{byUser: map[string]*ActiveTakeover{}, byVehicle: map[string]*ActiveTakeover{}, db: db}
}

// refreshFromDB hydrates the UI projection from the PostgreSQL authority. It
// is intentionally read-only: lease issuance, renewal and revocation remain
// DurableAuthority operations. A Fleet restart must not make a still-active
// database lease disappear from the operator view.
func (t *TakeoverReg) refreshFromDB() {
	if t == nil || t.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	rows, err := t.db.QueryContext(ctx, `SELECT id, vehicle_id, gateway_id, driver_id, device_id,
		fencing_token, issued_at, valid_until FROM control_leases
		WHERE state='active' AND valid_until > now()`)
	if err != nil {
		return
	}
	defer rows.Close()
	byUser := make(map[string]*ActiveTakeover)
	byVehicle := make(map[string]*ActiveTakeover)
	for rows.Next() {
		var a ActiveTakeover
		var started, until time.Time
		if err := rows.Scan(&a.LeaseID, &a.VehicleID, &a.GatewayID, &a.Driver, &a.DeviceID, &a.Fencing, &started, &until); err != nil {
			return
		}
		a.StartedNS, a.UntilNS = started.UnixNano(), until.UnixNano()
		copy := a
		byUser[a.Driver] = &copy
		byVehicle[a.VehicleID] = &copy
	}
	if err := rows.Err(); err != nil {
		return
	}
	t.mu.Lock()
	t.byUser, t.byVehicle = byUser, byVehicle
	t.mu.Unlock()
}

// gcLocked 清理租约+宽限均已到期的条目（调用方持锁）。
func (t *TakeoverReg) gcLocked() {
	now := time.Now().UnixNano()
	for u, a := range t.byUser {
		if now > a.UntilNS+takeoverGraceNS {
			delete(t.byUser, u)
			if t.byVehicle[a.VehicleID] == a {
				delete(t.byVehicle, a.VehicleID)
			}
		}
	}
}

func (t *TakeoverReg) put(a *ActiveTakeover) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gcLocked()
	t.byUser[a.Driver] = a
	t.byVehicle[a.VehicleID] = a
}

func (t *TakeoverReg) Mine(user string) *ActiveTakeover {
	t.refreshFromDB()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gcLocked()
	return copyTakeover(t.byUser[user])
}

func (t *TakeoverReg) ByVehicle(vehicle string) *ActiveTakeover {
	t.refreshFromDB()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gcLocked()
	return copyTakeover(t.byVehicle[vehicle])
}

func (t *TakeoverReg) All() []*ActiveTakeover {
	t.refreshFromDB()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gcLocked()
	out := make([]*ActiveTakeover, 0, len(t.byUser))
	for _, a := range t.byUser {
		out = append(out, copyTakeover(a))
	}
	return out
}

func copyTakeover(a *ActiveTakeover) *ActiveTakeover {
	if a == nil {
		return nil
	}
	cp := *a
	return &cp
}

func (t *TakeoverReg) Release(user string) *ActiveTakeover {
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.byUser[user]
	if a != nil {
		delete(t.byUser, user)
		if t.byVehicle[a.VehicleID] == a {
			delete(t.byVehicle, a.VehicleID)
		}
	}
	return a
}

// registerTakeoverRoutes 车辆接管路由（会话由全局中间件校验）。
func registerTakeoverRoutes(mux *http.ServeMux, st *State, svc *Services) {
	// 接管：账号↔车辆 互斥绑定；租约向 DurableAuthority 申请。
	mux.HandleFunc("POST /api/takeover/take", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		vid, _ := body["vehicle_id"].(string)
		devID, _ := body["device_id"].(string)
		if vid == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 vehicle_id"})
			return
		}
		if devID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少 device_id"})
			return
		}
		device, ok := svc.devices.Get(sess.Username, devID)
		if !ok {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "控制设备不存在或不属于当前账号"})
			return
		}
		if !device.Online {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "控制设备尚未由受信控制代理注册，拒绝签发接管租约"})
			return
		}
		v, ok := st.Vehicle(vid)
		if !ok || !canAccessVehicle(sess, st, vid) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "车辆不存在或无权接管（不在你的分组）"})
			return
		}
		if !v.Online {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "车辆离线，无法接管"})
			return
		}
		reg := svc.tkreg
		if mine := reg.Mine(sess.Username); mine != nil && mine.VehicleID != vid {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "一个账号一次只能接管一辆车，你当前已接管 " + mine.VehicleID})
			return
		}
		if other := reg.ByVehicle(vid); other != nil && other.Driver != sess.Username {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "该车正在被 " + other.Driver + " 接管"})
			return
		}
		lease, err := svc.authority.Request(r.Context(), sess.Username, devID, vid)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "租约签发被拒绝：" + err.Error()})
			return
		}
		a := &ActiveTakeover{VehicleID: vid, GatewayID: lease.GatewayID, Driver: sess.Username, DeviceID: devID, LeaseID: lease.LeaseID, Fencing: lease.Fencing, UntilNS: lease.ValidUntil.UnixNano(), StartedNS: lease.IssuedAt.UnixNano()}
		reg.put(a)
		st.pushEvent("warn", vid, fmt.Sprintf("远程接管：%s 接管 %s（设备 %s，租约 %s）", sess.Username, vid, devID, lease.LeaseID), "sys")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "takeover": a})
	})

	// 续租心跳：驾驶页每 3s 调一次；仅允许本人对已接管车辆续租。
	mux.HandleFunc("POST /api/takeover/keepalive", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		vid, _ := body["vehicle_id"].(string)
		mine := svc.tkreg.Mine(sess.Username)
		if mine == nil || mine.VehicleID != vid {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "你当前未接管该车"})
			return
		}
		device, ok := svc.devices.Get(sess.Username, mine.DeviceID)
		if !ok || !device.Online {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "控制设备已解除绑定或不再由受信控制代理在线确认"})
			return
		}
		requestID, _ := body["request_id"].(string)
		lease, err := svc.authority.Renew(r.Context(), mine.LeaseID, sess.Username, mine.DeviceID, requestID)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "续租被拒绝：" + err.Error()})
			return
		}
		a := &ActiveTakeover{VehicleID: vid, GatewayID: lease.GatewayID, Driver: sess.Username, DeviceID: mine.DeviceID, LeaseID: lease.LeaseID, Fencing: lease.Fencing, UntilNS: lease.ValidUntil.UnixNano(), StartedNS: mine.StartedNS}
		svc.tkreg.put(a)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "takeover": a})
	})

	// 我当前的接管（驾驶页刷新恢复用）。
	mux.HandleFunc("GET /api/takeover/mine", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		a := svc.tkreg.Mine(sess.Username)
		if a == nil {
			writeJSON(w, http.StatusOK, map[string]any{"active": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"active": true, "takeover": a})
	})

	// 全部活跃接管（车辆列表标注「被谁接管」用；分组过滤已在 /api/fleet 完成）。
	mux.HandleFunc("GET /api/takeover/active", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		active := make([]*ActiveTakeover, 0)
		for _, takeover := range svc.tkreg.All() {
			if canAccessVehicle(sess, st, takeover.VehicleID) {
				active = append(active, takeover)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"active": active})
	})
}
