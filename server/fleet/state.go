package main

// state.go：车队状态模型（内存真相 + 异步入库 + 事件推导）。
//
// 判定口径（与 docs/plan-operations-dashboard.md 任务 2 一致）：
//   - 在线 = 6 秒内收到过遥测。遥测由车端仲裁器驱动，车端进程死亡则
//     遥测立停；gateway 的 status 心跳只代表网关怀着，不用于车辆在线
//     判定——这样才能成立「杀 vehicle_side -> 6s 内离线」的演示剧本。
//   - 巡检 1Hz；车速历史环 150 点；事件环上限 200；
//   - 入库走异步队列（容量 256），绝不阻塞 MQTT 回调。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"sort"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

const (
	onlineWindowS = 6   // 6 秒无遥测判离线
	sweepHz       = 1   // 在线巡检 + 接管状态轮询频率
	speedHistCap  = 150 // 车速历史环长度
	eventRingCap  = 200 // 事件环上限
	dbQueueCap    = 256 // 异步入库队列容量
	sampleEveryS  = 1   // 遥测入库抽稀：每秒一条（上行 2Hz）
)

// ---------- /api/fleet 契约类型（前端唯一契约，字段名冻结勿改） ----------

type Pose struct {
	X   float64 `json:"x"`
	Y   float64 `json:"y"`
	Yaw float64 `json:"yaw"`
}

type VehicleSnap struct {
	VehicleID         string            `json:"vehicle_id"`
	Group             string            `json:"group"`
	Online            bool              `json:"online"`
	LastHeartbeatAgeS float64           `json:"last_heartbeat_age_s"`
	Mode              string            `json:"mode"`
	SpeedMPS          float64           `json:"speed_mps"`
	SOC               float64           `json:"soc"`
	Voltage           float64           `json:"voltage"`
	Gear              string            `json:"gear"`
	SteerRad          float64           `json:"steer_rad"`
	WheelSpeeds       []float64         `json:"wheel_speeds"`
	SpeedHistory      []float64         `json:"speed_history"`
	Capabilities      map[string]string `json:"capabilities"`
	Pose              Pose              `json:"pose"`
}

type TakeoverSnap struct {
	Active      bool    `json:"active"`
	Driver      string  `json:"driver,omitempty"`
	LeaseID     string  `json:"lease_id,omitempty"`
	Fencing     int64   `json:"fencing,omitempty"`
	SecondsLeft float64 `json:"seconds_left,omitempty"`
}

type EventSnap struct {
	TsNS      int64  `json:"ts_ns"`
	Level     string `json:"level"`
	VehicleID string `json:"vehicle_id,omitempty"`
	Text      string `json:"text"`
}

type FleetSnap struct {
	ServerTimeNS int64         `json:"server_time_ns"`
	Vehicles     []VehicleSnap `json:"vehicles"`
	Takeover     TakeoverSnap  `json:"takeover"`
	Events       []EventSnap   `json:"events"`
}

// signalVal 是解析后的单条 SignalUpdate 信号。
type signalVal struct {
	Path string
	Num  *float64
	Text *string
}

// vehicleState 单车可变状态（受 State.mu 保护）。
type vehicleState struct {
	id           string
	group        string
	online       bool
	lastSeen     time.Time // 最近一次遥测时刻
	mode         string
	speedMPS     float64
	soc          float64
	voltage      float64
	gear         string
	steerRad     float64
	wheelSpeeds  [4]float64
	speedHist    []float64
	capabilities map[string]string
	lastSampleAt time.Time // 上次入库时刻（抽稀用）
}

type dbJob func(ctx context.Context, db *sql.DB)

// State 车队全局状态。
type State struct {
	mu       sync.RWMutex
	vehicles map[string]*vehicleState
	events   []EventSnap // 头部最新
	takeover TakeoverSnap

	hub  *Hub    // WS 广播器（非 nil）
	db   *sql.DB // nil = 纯内存模式
	dbCh chan dbJob

	authorityAddr string
	mqttPub       mqtt.Client // OnConnect 后由 mqtt.go 注入（发布车端下行话题）
	startAt       time.Time
}

func NewState(hub *Hub, db *sql.DB, authorityAddr string) *State {
	s := &State{
		vehicles:      map[string]*vehicleState{},
		events:        []EventSnap{},
		hub:           hub,
		db:            db,
		dbCh:          make(chan dbJob, dbQueueCap),
		authorityAddr: authorityAddr,
		startAt:       time.Now(),
	}
	go s.dbLoop()
	go s.sweepLoop()
	go s.broadcastLoop()
	return s
}

// demoPoses 阶段 1 内置演示位姿表（每车固定坐标）；阶段 2 接真实定位。
var demoPoses = []Pose{
	{X: 120, Y: 80, Yaw: 0.6},
	{X: 45, Y: 140, Yaw: 2.1},
	{X: 195, Y: 55, Yaw: 4.0},
	{X: 85, Y: 30, Yaw: 1.2},
}

func poseFor(id string) Pose { return demoPoses[int(fnvOf(id))%len(demoPoses)] }

// ---------- 入口（MQTT 侧调用） ----------

// ensureVehicleLocked 调用方需已持写锁。
func (s *State) ensureVehicleLocked(id string) *vehicleState {
	v, ok := s.vehicles[id]
	if !ok {
		v = &vehicleState{id: id, capabilities: map[string]string{}}
		s.vehicles[id] = v
	}
	return v
}

// HandleRegister 处理 vehicle/{id}/register（retained 能力注册）。
func (s *State) HandleRegister(id, gatewayID string, caps map[string]string, group string) {
	s.mu.Lock()
	v := s.ensureVehicleLocked(id)
	if group != "" {
		v.group = group
	}
	for k, val := range caps {
		if val != "" {
			v.capabilities[k] = val
		}
	}
	s.mu.Unlock()

	s.pushEvent("info", id, fmt.Sprintf("车辆注册（网关 %s，栈 %s）", gatewayID, caps["stack"]))
	s.submitDB(func(ctx context.Context, db *sql.DB) {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO vehicles(id, gateway_id, stack, last_seen) VALUES ($1,$2,$3,now()) ON CONFLICT (id) DO UPDATE SET gateway_id=$2, stack=$3, last_seen=now()",
			id, gatewayID, caps["stack"]); err != nil {
			log.Printf("vehicles 入库失败: %v", err)
		}
	})
}

// HandleTelemetry 处理 vehicle/{id}/telemetry（SignalUpdate，2Hz）。
func (s *State) HandleTelemetry(id string, sigs []signalVal) {
	now := time.Now()

	s.mu.Lock()
	v := s.ensureVehicleLocked(id)
	cameOnline := !v.online
	v.online = true
	v.lastSeen = now
	modeBefore := v.mode

	for _, sg := range sigs {
		switch sg.Path {
		case "Vehicle.Speed":
			if sg.Num != nil {
				v.speedMPS = *sg.Num
				v.speedHist = append(v.speedHist, round2(*sg.Num))
				if len(v.speedHist) > speedHistCap {
					v.speedHist = v.speedHist[len(v.speedHist)-speedHistCap:]
				}
			}
		case "Vehicle.Chassis.SteeringWheel.Angle":
			if sg.Num != nil {
				v.steerRad = *sg.Num
			}
		case "Vehicle.Chassis.WheelSpeeds.FL":
			if sg.Num != nil {
				v.wheelSpeeds[0] = *sg.Num
			}
		case "Vehicle.Chassis.WheelSpeeds.FR":
			if sg.Num != nil {
				v.wheelSpeeds[1] = *sg.Num
			}
		case "Vehicle.Chassis.WheelSpeeds.RL":
			if sg.Num != nil {
				v.wheelSpeeds[2] = *sg.Num
			}
		case "Vehicle.Chassis.WheelSpeeds.RR":
			if sg.Num != nil {
				v.wheelSpeeds[3] = *sg.Num
			}
		case "Vehicle.Powertrain.Transmission.CurrentGear":
			if sg.Text != nil {
				v.gear = gearShort(*sg.Text)
			}
		case "Platform.Autonomy.OperationMode":
			if sg.Text != nil {
				v.mode = *sg.Text
			}
		case "Vehicle.Powertrain.TractionBattery.StateOfCharge":
			if sg.Num != nil {
				v.soc = *sg.Num
			}
		case "Vehicle.Powertrain.TractionBattery.Voltage":
			if sg.Num != nil {
				v.voltage = *sg.Num
			}
		}
	}

	doSample := now.Sub(v.lastSampleAt) >= sampleEveryS*time.Second
	if doSample {
		v.lastSampleAt = now
	}
	stMode, stSpeed := v.mode, v.speedMPS
	stSOC, stVolt, stGear, stSteer := v.soc, v.voltage, v.gear, v.steerRad
	online := v.online
	s.mu.Unlock()

	if cameOnline {
		s.pushEvent("info", id, "车辆上线（遥测恢复）")
	}
	if modeBefore != "" && modeBefore != stMode {
		s.onModeChange(id, modeBefore, stMode)
	}
	if doSample {
		s.persistSample(id, sigs)
		s.persistVehicleState(id, online, stMode, stSpeed, stSOC, stVolt, stGear, stSteer)
	}
}

// onModeChange 推导模式切换事件（计划：minimum_risk=critical、stopped=warn）。
func (s *State) onModeChange(id, from, to string) {
	level, txt := "info", fmt.Sprintf("模式变化：%s -> %s", from, to)
	switch to {
	case "minimum_risk":
		level, txt = "critical", "进入最小风险状态（MRC）"
	case "stopped":
		level, txt = "warn", "车辆已停车"
	case "remote_control":
		level, txt = "warn", "切换远程驾驶（接管中）"
	case "autonomous":
		level, txt = "info", "回到自动驾驶"
	}
	s.pushEvent(level, id, txt)
}

// ---------- 巡检与接管 ----------

// sweepLoop 1Hz：在线巡检 + 接管状态只读轮询。
func (s *State) sweepLoop() {
	t := time.NewTicker(time.Second / sweepHz)
	defer t.Stop()
	for range t.C {
		s.onlineSweep()
		s.pollAuthorityOnce()
	}
}

func (s *State) onlineSweep() {
	now := time.Now()
	s.mu.Lock()
	var gone []string
	for _, v := range s.vehicles {
		if v.online && now.Sub(v.lastSeen) > onlineWindowS*time.Second {
			v.online = false
			gone = append(gone, v.id)
		}
	}
	s.mu.Unlock()
	for _, id := range gone {
		s.pushEvent("critical", id, fmt.Sprintf("链路丢失：%d 秒无遥测，判定离线", onlineWindowS))
	}
}

// pollAuthorityOnce 向 control-authority 发 {"op":"status"}（UDP 只读，不改状态）。
// 失败/超时保留上一次状态，下一周期重试。
func (s *State) pollAuthorityOnce() {
	conn, err := net.DialTimeout("udp", s.authorityAddr, 300*time.Millisecond)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := conn.Write([]byte(`{"op":"status"}`)); err != nil {
		return
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	var resp struct {
		OK               bool   `json:"ok"`
		Active           bool   `json:"active"`
		Driver           string `json:"driver"`
		LeaseID          string `json:"lease_id"`
		Fencing          int64  `json:"fencing"`
		ValidUntilUnixNS int64  `json:"valid_until_unix_ns"`
	}
	if err := json.Unmarshal(buf[:n], &resp); err != nil || !resp.OK {
		return
	}
	tk := TakeoverSnap{Active: resp.Active, Driver: resp.Driver, LeaseID: resp.LeaseID, Fencing: resp.Fencing}
	if resp.Active && resp.ValidUntilUnixNS > 0 {
		tk.SecondsLeft = math.Max(0, float64(resp.ValidUntilUnixNS-time.Now().UnixNano())/1e9)
	}

	s.mu.Lock()
	prev := s.takeover
	s.takeover = tk
	s.mu.Unlock()

	switch {
	case !prev.Active && tk.Active:
		s.pushEvent("warn", "", fmt.Sprintf("接管开始：%s（租约 %s）", tk.Driver, tk.LeaseID))
		s.persistLease(tk)
	case prev.Active && !tk.Active:
		s.pushEvent("info", "", "控制权已交还（接管结束）")
	case prev.Active && tk.Active && prev.Driver != tk.Driver:
		s.pushEvent("warn", "", fmt.Sprintf("接管驾驶员变更：%s -> %s", prev.Driver, tk.Driver))
	}
}

// ---------- 事件 ----------

// pushEvent 追加事件环（头最新）+ WS 即时推送 + 入库。
func (s *State) pushEvent(level, vehicleID, txt string) {
	ev := EventSnap{TsNS: time.Now().UnixNano(), Level: level, VehicleID: vehicleID, Text: txt}
	s.mu.Lock()
	s.events = append([]EventSnap{ev}, s.events...)
	if len(s.events) > eventRingCap {
		s.events = s.events[:eventRingCap]
	}
	s.mu.Unlock()

	log.Printf("[event] %s %s %s", level, vehicleID, txt)
	if b, err := json.Marshal(map[string]any{"type": "event", "data": ev}); err == nil {
		s.hub.Broadcast(b)
	}
	s.submitDB(func(ctx context.Context, db *sql.DB) {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO events(ts, vehicle_id, level, text) VALUES ($1,$2,$3,$4)",
			time.Unix(0, ev.TsNS), nullable(vehicleID), level, txt); err != nil {
			log.Printf("events 入库失败: %v", err)
		}
	})
}

// ---------- 读侧（HTTP/WS） ----------

// Snapshot 构造 /api/fleet 全量快照。
func (s *State) Snapshot() FleetSnap {
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := FleetSnap{
		ServerTimeNS: now.UnixNano(),
		Vehicles:     make([]VehicleSnap, 0, len(s.vehicles)),
		Takeover:     s.takeover,
		Events:       append([]EventSnap(nil), s.events...),
	}
	for _, v := range s.vehicles {
		age := 0.0
		if !v.lastSeen.IsZero() {
			age = now.Sub(v.lastSeen).Seconds()
		}
		out.Vehicles = append(out.Vehicles, VehicleSnap{
			VehicleID:         v.id,
			Group:             v.group,
			Online:            v.online,
			LastHeartbeatAgeS: round2(age),
			Mode:              v.mode,
			SpeedMPS:          round2(v.speedMPS),
			SOC:               round2(v.soc),
			Voltage:           round2(v.voltage),
			Gear:              v.gear,
			SteerRad:          round2(v.steerRad),
			WheelSpeeds:       []float64{round2(v.wheelSpeeds[0]), round2(v.wheelSpeeds[1]), round2(v.wheelSpeeds[2]), round2(v.wheelSpeeds[3])},
			SpeedHistory:      append([]float64(nil), v.speedHist...),
			Capabilities:      copyMap(v.capabilities),
			Pose:              poseFor(v.id),
		})
	}
	sort.Slice(out.Vehicles, func(i, j int) bool {
		return out.Vehicles[i].VehicleID < out.Vehicles[j].VehicleID
	})
	return out
}

// Vehicle 单车快照（/api/vehicles/:id）。
func (s *State) Vehicle(id string) (VehicleSnap, bool) {
	for _, v := range s.Snapshot().Vehicles {
		if v.VehicleID == id {
			return v, true
		}
	}
	return VehicleSnap{}, false
}

// Events 事件环拷贝（头最新，限条数）。
func (s *State) Events(limit int) []EventSnap {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.events) {
		limit = len(s.events)
	}
	return append([]EventSnap(nil), s.events[:limit]...)
}

// broadcastLoop 1Hz 全量状态推给所有 WS 客户端。
func (s *State) broadcastLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		if b, err := json.Marshal(map[string]any{"type": "state", "data": s.Snapshot()}); err == nil {
			s.hub.Broadcast(b)
		}
	}
}

// ---------- 入库（异步队列） ----------

func (s *State) dbLoop() {
	for job := range s.dbCh {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		job(ctx, s.db)
		cancel()
	}
}

// submitDB 非阻塞入队；满则丢弃并记日志（大屏真相在内存，入库缺失只影响历史）。
func (s *State) submitDB(job dbJob) {
	if s.db == nil {
		return
	}
	select {
	case s.dbCh <- job:
	default:
		log.Printf("DB 写入队列满，丢弃一次写入")
	}
}

// persistSample 遥测 1Hz 抽稀入库（上行 2Hz，存储减半）。
// 保留策略：telemetry_samples 存原始采样，滚动清理由运维负责（阶段 2 上分区 + TTL）。
func (s *State) persistSample(id string, sigs []signalVal) {
	ts := time.Now()
	s.submitDB(func(ctx context.Context, db *sql.DB) {
		for _, sg := range sigs {
			var num, txt any
			if sg.Num != nil {
				num = *sg.Num
			}
			if sg.Text != nil {
				txt = *sg.Text
			}
			if num == nil && txt == nil {
				continue
			}
			if _, err := db.ExecContext(ctx,
				"INSERT INTO telemetry_samples(vehicle_id, ts, path, num, txt) VALUES ($1,$2,$3,$4,$5)",
				id, ts, sg.Path, num, txt); err != nil {
				log.Printf("telemetry_samples 入库失败: %v", err)
				return
			}
		}
	})
}

// persistVehicleState 车辆最新状态 upsert（随遥测抽稀节奏）。
func (s *State) persistVehicleState(id string, online bool, mode string, speed, soc, voltage float64, gear string, steer float64) {
	s.submitDB(func(ctx context.Context, db *sql.DB) {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO vehicles(id) VALUES ($1) ON CONFLICT DO NOTHING", id); err != nil {
			log.Printf("vehicles 入库失败: %v", err)
			return
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO vehicle_state(vehicle_id, online, mode, speed_mps, soc, voltage, gear, steer_rad, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now()) ON CONFLICT (vehicle_id) DO UPDATE SET online=$2, mode=$3, speed_mps=$4, soc=$5, voltage=$6, gear=$7, steer_rad=$8, updated_at=now()",
			id, online, nullable(mode), speed, soc, voltage, nullable(gear), steer); err != nil {
			log.Printf("vehicle_state 入库失败: %v", err)
		}
	})
}

// persistLease 接管租约入库（按 lease_id 幂等）。
func (s *State) persistLease(tk TakeoverSnap) {
	if tk.LeaseID == "" {
		return
	}
	until := time.Now().Add(time.Duration(tk.SecondsLeft * float64(time.Second)))
	s.submitDB(func(ctx context.Context, db *sql.DB) {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO leases(id, driver, fencing, valid_until, state) VALUES ($1,$2,$3,$4,'active') ON CONFLICT (id) DO UPDATE SET driver=$2, fencing=$3, valid_until=$4, state='active'",
			tk.LeaseID, tk.Driver, tk.Fencing, until); err != nil {
			log.Printf("leases 入库失败: %v", err)
		}
	})
}

// ---------- 小工具 ----------

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// gearShort 平台挡位文本 -> 大屏简写（GEAR_DRIVE -> D）。
func gearShort(g string) string {
	switch g {
	case "GEAR_DRIVE":
		return "D"
	case "GEAR_NEUTRAL":
		return "N"
	case "GEAR_REVERSE":
		return "R"
	case "GEAR_PARK":
		return "P"
	}
	return g
}

// ---------- 下行发布（循迹导航等车端指令） ----------

// SetMQTTPub 由 mqtt.go 的 OnConnect 注入客户端。
func (s *State) SetMQTTPub(c mqtt.Client) {
	s.mu.Lock()
	s.mqttPub = c
	s.mu.Unlock()
}

// PublishJSON 发布信封到 vehicle/{id}/{kind}；返回是否真的发出去了。
// 未连上 broker 时返回 false（调用方据此把任务留在队列）。
func (s *State) PublishJSON(vehicleID, kind string, payload []byte) bool {
	s.mu.RLock()
	c := s.mqttPub
	s.mu.RUnlock()
	if c == nil || !c.IsConnected() {
		return false
	}
	env := map[string]any{
		"message_type": "platform.v1.VehicleCommand",
		"vehicle_id":   vehicleID,
		"utc_time_ns":  time.Now().UnixNano(),
		"payload":      json.RawMessage(payload),
	}
	b, err := json.Marshal(env)
	if err != nil {
		return false
	}
	t := c.Publish(fmt.Sprintf("vehicle/%s/%s", vehicleID, kind), 1, false, b)
	return t.Wait() && t.Error() == nil
}
