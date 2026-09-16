package main

// nav.go：循迹导航（多路点任务）后端。
// 流程：前端/开放 API 建任务 -> 存任务表 -> 经 MQTT 下发 vehicle/{id}/nav
//      （车端 gateway 转发到导航栈）-> 事件环记录。
// 取消：同一 NavigationCommand action=CANCEL。任务状态以 Broker 投递和
// 车端 NavigationAck 分层记录：queued/dispatched/accepted/running/completed/
// cancel_requested/cancelled/cancel_rejected/rejected/failed。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	platformv1 "robot-agent/protocols/platform/v1"
)

type NavPoint struct {
	Name   string  `json:"name"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	AtNS   int64   `json:"at_ns,omitempty"`   // 计划到达时刻（可选，定时路点）
	DwellS float64 `json:"dwell_s,omitempty"` // 到点停留秒数（可选，0=不停）
}

type NavRoute struct {
	ID               string     `json:"id"`
	VehicleID        string     `json:"vehicle_id"`
	Name             string     `json:"name"`
	Remark           string     `json:"remark,omitempty"`
	Points           []NavPoint `json:"points"`
	Status           string     `json:"status"` // queued|dispatched|accepted|running|completed|cancel_requested|cancelled|cancel_rejected|rejected|failed
	CreatedNS        int64      `json:"created_ns"`
	DispatchedNS     int64      `json:"dispatched_ns,omitempty"`
	VehicleAckResult string     `json:"vehicle_ack_result,omitempty"`
	VehicleAckDetail string     `json:"vehicle_ack_detail,omitempty"`
	CurrentPoint     int        `json:"current_point,omitempty"`
	VehicleAckNS     int64      `json:"vehicle_ack_ns,omitempty"`
	UpdatedNS        int64      `json:"updated_ns"`
	TraceID          string     `json:"trace_id,omitempty"`
	Origin           string     `json:"origin"` // console|open_api
}

type NavStore struct {
	mu     sync.Mutex
	routes map[string]*NavRoute
	st     *State
	// publish is nil in production and always points to the authenticated
	// PostgreSQL-backed outbox path. Tests may inject a deterministic transport
	// result without making an unauthenticated volatile path reachable.
	publish func(*platformv1.NavigationCommand) bool
}

func NewNavStore(st *State) *NavStore {
	ns := &NavStore{routes: map[string]*NavRoute{}, st: st}
	if st != nil {
		st.setNavStore(ns)
	}
	ns.load()
	return ns
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Submit 建任务并尝试下发（remark 为调用备注，随任务与审计留存）。
func (ns *NavStore) Submit(vehicleID, name, origin, traceID, remark string, pts []NavPoint) (*NavRoute, error) {
	if vehicleID == "" {
		return nil, fmt.Errorf("vehicle_id 必填")
	}
	if len(pts) < 2 || len(pts) > 1000 {
		return nil, fmt.Errorf("至少需要两个路点（从哪到哪）")
	}
	for i, p := range pts {
		if math.IsNaN(p.X) || math.IsInf(p.X, 0) || math.IsNaN(p.Y) || math.IsInf(p.Y, 0) || p.DwellS < 0 || math.IsNaN(p.DwellS) || math.IsInf(p.DwellS, 0) || (p.AtNS < 0) {
			return nil, fmt.Errorf("路点 %d 坐标或停留时间无效", i+1)
		}
		if p.Name == "" {
			pts[i].Name = fmt.Sprintf("P%d", i+1)
		}
	}
	if name == "" {
		name = fmt.Sprintf("%s → %s", pts[0].Name, pts[len(pts)-1].Name)
	}
	rt := &NavRoute{
		ID: "nav-" + randHex(4), VehicleID: vehicleID, Name: name,
		Remark: trimRune(remark, 200),
		Points: pts, Status: "queued", CreatedNS: time.Now().UnixNano(),
		TraceID: traceID, Origin: origin,
	}
	rt.UpdatedNS = rt.CreatedNS
	command := &platformv1.NavigationCommand{
		Version: 1, Action: platformv1.NavigationAction_NAVIGATION_ACTION_DISPATCH,
		RouteId: rt.ID, VehicleId: rt.VehicleID, Name: rt.Name, TraceId: rt.TraceID,
	}
	for _, p := range pts {
		command.Points = append(command.Points, &platformv1.NavigationPoint{
			Name: p.Name, XM: p.X, YM: p.Y, AtUnixNs: p.AtNS, DwellS: p.DwellS,
		})
	}
	// In production the route row and the signed outbox record share one
	// transaction. Test transports are intentionally kept outside this branch
	// so unit tests cannot create an unauthenticated runtime path.
	if ns.publish == nil && ns.st != nil && ns.st.db != nil {
		if err := ns.persistAndQueue(rt, command); err != nil {
			return nil, fmt.Errorf("路线与下行队列原子持久化失败：%w", err)
		}
	} else {
		if err := ns.persist(rt); err != nil {
			return nil, fmt.Errorf("路线持久化失败：%w", err)
		}
		if !ns.publishNavigation(command) {
			return nil, fmt.Errorf("车辆没有可用的已认证 Gateway 下行通道")
		}
	}
	ns.mu.Lock()
	ns.routes[rt.ID] = rt
	ns.mu.Unlock()

	txt := fmt.Sprintf("循迹任务 %s 已进入可靠下行队列（等待车端确认）", rt.ID)
	if ns.st != nil {
		ns.st.pushEvent("info", vehicleID, txt, "sys")
	}
	return rt, nil
}

func (ns *NavStore) Cancel(id string) (*NavRoute, error) {
	ns.mu.Lock()
	rt, ok := ns.routes[id]
	if !ok {
		ns.mu.Unlock()
		return nil, fmt.Errorf("任务不存在：%s", id)
	}
	if rt.Status == "cancel_requested" {
		copy := *rt
		copy.Points = append([]NavPoint(nil), rt.Points...)
		ns.mu.Unlock()
		return &copy, nil
	}
	switch rt.Status {
	case "completed", "cancelled", "cancel_rejected", "rejected", "failed":
		ns.mu.Unlock()
		return nil, fmt.Errorf("任务 %s 当前状态为 %s，不能取消", id, rt.Status)
	}
	copy := *rt
	// Cancellation is a request until the vehicle explicitly confirms it.
	// Reporting cancelled here would make a lost downlink look successful.
	copy.Status = "cancel_requested"
	copy.Points = append([]NavPoint(nil), rt.Points...)
	ns.mu.Unlock()
	cancelCommand := &platformv1.NavigationCommand{
		Version: 1, Action: platformv1.NavigationAction_NAVIGATION_ACTION_CANCEL,
		RouteId: copy.ID, VehicleId: copy.VehicleID, Name: copy.Name, TraceId: copy.TraceID,
	}
	queued := false
	if ns.publish == nil && ns.st != nil && ns.st.db != nil {
		if err := ns.persistAndQueue(&copy, cancelCommand); err != nil {
			return nil, fmt.Errorf("取消状态与下行队列原子持久化失败：%w", err)
		}
		queued = true
	} else {
		if err := ns.persist(&copy); err != nil {
			return nil, fmt.Errorf("取消状态持久化失败：%w", err)
		}
		queued = ns.publishNavigation(cancelCommand)
	}
	ns.mu.Lock()
	rt.Status = "cancel_requested"
	rt.UpdatedNS = time.Now().UnixNano()
	ns.mu.Unlock()
	if ns.st != nil {
		if queued {
			ns.st.pushEvent("warn", rt.VehicleID, fmt.Sprintf("循迹任务 %s 已请求取消，等待车端确认", id), "sys")
		} else {
			ns.st.pushEvent("critical", rt.VehicleID, fmt.Sprintf("循迹任务 %s 取消请求未能入队", id), "sys")
		}
	}
	copy.UpdatedNS = time.Now().UnixNano()
	return &copy, nil
}

func (ns *NavStore) publishNavigation(command *platformv1.NavigationCommand) bool {
	if ns == nil {
		return false
	}
	if ns.publish != nil {
		return ns.publish(command)
	}
	return ns.st != nil && ns.st.PublishNavigation(command)
}

// markDispatched projects broker QoS1 acceptance. It must never be confused
// with vehicle acceptance; the latter is a future signed navigation ACK.
func (ns *NavStore) markDispatched(id string) {
	if ns == nil || id == "" {
		return
	}
	now := time.Now().UTC()
	if ns.st != nil && ns.st.db != nil {
		res, err := ns.st.db.ExecContext(context.Background(), `UPDATE navigation_routes
			SET status=CASE WHEN status='queued' THEN 'dispatched' ELSE status END,
				dispatched_at=CASE WHEN status='queued' THEN $1 ELSE dispatched_at END,
				updated_at=$1
			WHERE id=$2 AND status IN ('queued','cancel_requested')`, now, id)
		if err != nil {
			log.Printf("[nav] 投递状态持久化失败 id=%s: %v", id, err)
			return
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return
		}
	}
	ns.mu.Lock()
	rt := ns.routes[id]
	if rt != nil {
		if rt.Status == "queued" {
			rt.Status = "dispatched"
			rt.DispatchedNS = now.UnixNano()
		}
		rt.UpdatedNS = now.UnixNano()
	}
	vehicleID := ""
	if rt != nil {
		vehicleID = rt.VehicleID
	}
	ns.mu.Unlock()
	if ns.st != nil && vehicleID != "" {
		ns.st.pushEvent("info", vehicleID, fmt.Sprintf("循迹任务 %s 已被 MQTT Broker 接收，等待车端确认", id), "sys")
	}
}

func (ns *NavStore) persist(rt *NavRoute) error {
	if ns == nil || ns.st == nil || ns.st.db == nil || rt == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := ns.st.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ns.persistTx(ctx, tx, rt); err != nil {
		return err
	}
	return tx.Commit()
}

func (ns *NavStore) persistTx(ctx context.Context, tx *sql.Tx, rt *NavRoute) error {
	if ns == nil || tx == nil || rt == nil {
		return fmt.Errorf("路线持久化字段无效")
	}
	points, err := json.Marshal(rt.Points)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO navigation_routes
		(id, vehicle_id, name, remark, points, status, created_at, dispatched_at,
		 trace_id, origin, vehicle_ack_result, vehicle_ack_detail,
		 vehicle_ack_current_point, vehicle_ack_at, updated_at)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (id) DO UPDATE SET name=$3, remark=$4, points=$5::jsonb,
			status=$6, dispatched_at=$8, trace_id=$9, origin=$10,
			vehicle_ack_result=$11, vehicle_ack_detail=$12,
			vehicle_ack_current_point=$13, vehicle_ack_at=$14, updated_at=$15`,
		rt.ID, rt.VehicleID, rt.Name, rt.Remark, string(points), rt.Status,
		time.Unix(0, rt.CreatedNS), nullableTime(rt.DispatchedNS), rt.TraceID, rt.Origin,
		rt.VehicleAckResult, rt.VehicleAckDetail, rt.CurrentPoint, nullableTime(rt.VehicleAckNS),
		updatedTime(rt.UpdatedNS, rt.CreatedNS))
	return err
}

func (ns *NavStore) persistAndQueue(rt *NavRoute, command *platformv1.NavigationCommand) error {
	if ns == nil || ns.st == nil || ns.st.db == nil {
		return fmt.Errorf("路线持久化需要 PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := ns.st.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ns.persistTx(ctx, tx, rt); err != nil {
		return err
	}
	if err := ns.st.publishNavigationTx(ctx, tx, command); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ns.st.wakeOutbox()
	return nil
}

func nullableTime(ns int64) any {
	if ns <= 0 {
		return nil
	}
	return time.Unix(0, ns)
}

func updatedTime(updatedNS, createdNS int64) time.Time {
	if updatedNS <= 0 {
		updatedNS = createdNS
	}
	return time.Unix(0, updatedNS).UTC()
}

func (ns *NavStore) load() {
	if ns == nil || ns.st == nil || ns.st.db == nil {
		return
	}
	rows, err := ns.st.db.Query(`SELECT id, vehicle_id, name, remark, points::text, status,
		(EXTRACT(EPOCH FROM created_at) * 1000000000)::bigint,
		COALESCE((EXTRACT(EPOCH FROM dispatched_at) * 1000000000)::bigint, 0), trace_id, origin,
		COALESCE(vehicle_ack_result,''), COALESCE(vehicle_ack_detail,''), vehicle_ack_current_point,
		COALESCE((EXTRACT(EPOCH FROM vehicle_ack_at) * 1000000000)::bigint, 0),
		(EXTRACT(EPOCH FROM updated_at) * 1000000000)::bigint
		FROM navigation_routes ORDER BY created_at DESC`)
	if err != nil {
		log.Printf("[nav] 恢复路线失败: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var rt NavRoute
		var points string
		if err := rows.Scan(&rt.ID, &rt.VehicleID, &rt.Name, &rt.Remark, &points, &rt.Status,
			&rt.CreatedNS, &rt.DispatchedNS, &rt.TraceID, &rt.Origin,
			&rt.VehicleAckResult, &rt.VehicleAckDetail, &rt.CurrentPoint,
			&rt.VehicleAckNS, &rt.UpdatedNS); err != nil {
			log.Printf("[nav] 读取路线失败: %v", err)
			continue
		}
		if err := json.Unmarshal([]byte(points), &rt.Points); err != nil || len(rt.Points) < 2 {
			log.Printf("[nav] 忽略无效路线 %s", rt.ID)
			continue
		}
		copy := rt
		ns.routes[rt.ID] = &copy
	}
}

func (ns *NavStore) List() []*NavRoute {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	out := make([]*NavRoute, 0, len(ns.routes))
	for _, rt := range ns.routes {
		copy := *rt
		copy.Points = append([]NavPoint(nil), rt.Points...)
		out = append(out, &copy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedNS > out[j].CreatedNS })
	return out
}

func (ns *NavStore) Get(id string) (*NavRoute, bool) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	rt, ok := ns.routes[id]
	if !ok {
		return nil, false
	}
	copy := *rt
	copy.Points = append([]NavPoint(nil), rt.Points...)
	return &copy, true
}

// ForVehicle 某车最近一条任务（开放 API /open/v1/nav/status 用）。
func (ns *NavStore) ForVehicle(vehicleID string) *NavRoute {
	for _, rt := range ns.List() {
		if rt.VehicleID == vehicleID {
			return rt
		}
	}
	return nil
}
