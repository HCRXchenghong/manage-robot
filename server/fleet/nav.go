package main

// nav.go：循迹导航（多路点任务）后端。
// 流程：前端/开放 API 建任务 -> 存任务表 -> 经 MQTT 下发 vehicle/{id}/nav
//      （车端 gateway 转发到导航栈；演示链路打印到网关日志）-> 事件环记录。
// 取消：vehicle/{id}/nav_cancel。任务状态：queued/dispatched/cancelled。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

type NavPoint struct {
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	AtNS int64   `json:"at_ns,omitempty"` // 计划到达时刻（可选，定时路点）
}

type NavRoute struct {
	ID           string     `json:"id"`
	VehicleID    string     `json:"vehicle_id"`
	Name         string     `json:"name"`
	Points       []NavPoint `json:"points"`
	Status       string     `json:"status"` // queued|dispatched|cancelled
	CreatedNS    int64      `json:"created_ns"`
	DispatchedNS int64      `json:"dispatched_ns,omitempty"`
	TraceID      string     `json:"trace_id,omitempty"`
	Origin       string     `json:"origin"` // console|open_api
}

type NavStore struct {
	mu     sync.Mutex
	routes map[string]*NavRoute
	st     *State
}

func NewNavStore(st *State) *NavStore {
	return &NavStore{routes: map[string]*NavRoute{}, st: st}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Submit 建任务并尝试下发。
func (ns *NavStore) Submit(vehicleID, name, origin, traceID string, pts []NavPoint) (*NavRoute, error) {
	if vehicleID == "" {
		return nil, fmt.Errorf("vehicle_id 必填")
	}
	if len(pts) < 2 {
		return nil, fmt.Errorf("至少需要两个路点（从哪到哪）")
	}
	for i, p := range pts {
		if p.Name == "" {
			pts[i].Name = fmt.Sprintf("P%d", i+1)
		}
	}
	if name == "" {
		name = fmt.Sprintf("%s → %s", pts[0].Name, pts[len(pts)-1].Name)
	}
	rt := &NavRoute{
		ID: "nav-" + randHex(4), VehicleID: vehicleID, Name: name,
		Points: pts, Status: "queued", CreatedNS: time.Now().UnixNano(),
		TraceID: traceID, Origin: origin,
	}
	b, _ := json.Marshal(map[string]any{
		"route_id": rt.ID, "name": rt.Name, "trace_id": rt.TraceID, "points": pts,
	})
	ok := false
	if ns.st != nil {
		ok = ns.st.PublishJSON(vehicleID, "nav", b)
	}
	rt.DispatchedNS = time.Now().UnixNano()
	if ok {
		rt.Status = "dispatched"
	}
	ns.mu.Lock()
	ns.routes[rt.ID] = rt
	ns.mu.Unlock()

	txt := fmt.Sprintf("循迹任务 %s（%d 路点）已下发", rt.ID, len(pts))
	if !ok {
		txt = fmt.Sprintf("循迹任务 %s 已排队（MQTT 未就绪，待链路恢复下发）", rt.ID)
	}
	if ns.st != nil {
		ns.st.pushEvent("info", vehicleID, txt)
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
	rt.Status = "cancelled"
	ns.mu.Unlock()
	if ns.st != nil {
		b, _ := json.Marshal(map[string]any{"route_id": id})
		ns.st.PublishJSON(rt.VehicleID, "nav_cancel", b)
		ns.st.pushEvent("warn", rt.VehicleID, fmt.Sprintf("循迹任务 %s 已取消", id))
	}
	return rt, nil
}

func (ns *NavStore) List() []*NavRoute {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	out := make([]*NavRoute, 0, len(ns.routes))
	for _, rt := range ns.routes {
		out = append(out, rt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedNS > out[j].CreatedNS })
	return out
}

func (ns *NavStore) Get(id string) (*NavRoute, bool) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	rt, ok := ns.routes[id]
	return rt, ok
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
