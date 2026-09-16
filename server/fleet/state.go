package main

// state.go：车队状态实时缓存 + PostgreSQL 持久化 + 事件推导。
//
// 判定口径（与 docs/plan-operations-dashboard.md 任务 2 一致）：
//   - 在线 = 6 秒内收到过遥测。遥测由车端仲裁器驱动，车端进程死亡则
//     遥测立停；gateway 的 status 心跳只代表网关怀着，不用于车辆在线
//     判定——车端进程或网络中断后会在 6 秒内反映为离线。
//   - 巡检 1Hz；车速历史环 150 点；事件环上限 200；
//   - 入库优先走异步队列（容量 256）；队列满时执行有界同步回退并计量背压，
//     不静默丢弃持久化写入。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"
	platformv1 "robot-agent/protocols/platform/v1"
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
	Valid bool    `json:"valid"`
	Frame string  `json:"frame,omitempty"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Yaw   float64 `json:"yaw"`
}

// GpsSnap 车端 GPS 定位（WGS-84）；前端负责 GCJ-02 纠偏后上高德底图。
type GpsSnap struct {
	Fix bool    `json:"fix"`
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
	Alt float64 `json:"alt"`
}

type VehicleSnap struct {
	VehicleID         string            `json:"vehicle_id"`
	Group             string            `json:"group"`
	Chassis           string            `json:"chassis"`
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
	ThrottleHistory   []float64         `json:"throttle_history"`
	BrakeHistory      []float64         `json:"brake_history"`
	ThrottlePct       float64           `json:"throttle_pct"`
	BrakePct          float64           `json:"brake_pct"`
	AccelMps2         float64           `json:"accel_mps2"`
	AccelHistory      []float64         `json:"accel_history"`
	CabinTempC        float64           `json:"cabin_temp_c"`
	CabinHumidityPct  float64           `json:"cabin_humidity_pct"`
	Gps               GpsSnap           `json:"gps"`
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
	Kind      string `json:"kind"` // veh=车辆侧（告警与事件）；sys=系统侧（系统审计）
	VehicleID string `json:"vehicle_id,omitempty"`
	Text      string `json:"text"`
}

type FleetSnap struct {
	ServerTimeNS int64         `json:"server_time_ns"`
	Vehicles     []VehicleSnap `json:"vehicles"`
	Takeover     TakeoverSnap  `json:"takeover"`
	Events       []EventSnap   `json:"events"`
}

// RuntimeStatus exposes dependency readiness separately from vehicle telemetry.
// A reachable HTTP process is not evidence that its database or MQTT transport
// is usable.
type RuntimeStatus struct {
	DatabaseReady         bool   `json:"database_ready"`
	MQTTReady             bool   `json:"mqtt_ready"`
	PersistentWritesReady bool   `json:"persistent_writes_ready"`
	Mode                  string `json:"mode"`
}

// signalVal 是解析后的单条 SignalUpdate 信号。
type signalVal struct {
	Path     string
	Num      *float64
	Text     *string
	SampleAt time.Time
}

// vehicleState 单车可变状态（受 State.mu 保护）。
type vehicleState struct {
	id              string
	gatewayID       string
	group           string
	chassis         string
	online          bool
	lastSeen        time.Time // 最近一次遥测时刻
	mode            string
	speedMPS        float64
	soc             float64
	voltage         float64
	gear            string
	steerRad        float64
	wheelSpeeds     [4]float64
	speedHist       []float64
	throttleHist    []float64
	brakeHist       []float64
	throttlePct     float64
	brakePct        float64
	accelMps2       float64
	accelHist       []float64
	cabinTempC      float64
	cabinHumidity   float64
	gpsFix          bool
	gpsLat          float64
	gpsLon          float64
	gpsAlt          float64
	poseValid       bool
	poseValiditySet bool
	poseXSeen       bool
	poseYSeen       bool
	poseYawSeen     bool
	poseFrame       string
	poseX           float64
	poseY           float64
	poseYaw         float64
	capabilities    map[string]string
	lastSampleAt    time.Time // 上次入库时刻（抽稀用）
}

type dbJob func(ctx context.Context, db *sql.DB)

// State 车队全局状态。
type State struct {
	mu       sync.RWMutex
	vehicles map[string]*vehicleState
	events   []EventSnap // 头部最新
	takeover TakeoverSnap

	// MQTT callbacks are configured with SetOrderMatters(false). A per-vehicle
	// mutex keeps the pure projection, durable transaction, and in-memory
	// publication in one order even when two valid packets arrive concurrently.
	vehicleLocksMu sync.Mutex
	vehicleLocks   map[string]*sync.Mutex

	hub  *Hub    // WS 广播器（非 nil）
	db   *sql.DB // 仅显式开发诊断模式下可以为 nil
	dbCh chan dbJob

	mqttPub           mqtt.Client // OnConnect 后由 mqtt.go 注入（发布车端下行话题）
	envelopeAuthKey   []byte
	outboundSessionID string
	outboundSeq       map[string]uint64
	outboxWake        chan struct{}
	navStore          *NavStore
	mapStore          *MapStore
	metrics           *RuntimeMetrics
	metricsToken      string
	startAt           time.Time
}

func NewState(hub *Hub, db *sql.DB, authKeys ...[]byte) *State {
	var envelopeAuthKey []byte
	if len(authKeys) > 0 {
		envelopeAuthKey = append([]byte(nil), authKeys[0]...)
	}
	s := &State{
		vehicles:          map[string]*vehicleState{},
		vehicleLocks:      map[string]*sync.Mutex{},
		events:            []EventSnap{},
		hub:               hub,
		db:                db,
		dbCh:              make(chan dbJob, dbQueueCap),
		envelopeAuthKey:   envelopeAuthKey,
		outboundSessionID: newOutboundSessionID(),
		outboundSeq:       map[string]uint64{},
		outboxWake:        make(chan struct{}, 1),
		metrics:           newRuntimeMetrics(),
		startAt:           time.Now(),
	}
	go s.dbLoop()
	go s.sweepLoop()
	go s.broadcastLoop()
	go s.retentionLoop()
	go s.outboxLoop()
	s.loadVehiclesFromDB()
	return s
}

func (s *State) lockVehicle(vehicleID string) *sync.Mutex {
	if s == nil {
		return &sync.Mutex{}
	}
	s.vehicleLocksMu.Lock()
	defer s.vehicleLocksMu.Unlock()
	if s.vehicleLocks == nil {
		s.vehicleLocks = map[string]*sync.Mutex{}
	}
	lock := s.vehicleLocks[vehicleID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.vehicleLocks[vehicleID] = lock
	}
	return lock
}

func (s *State) setNavStore(ns *NavStore) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.navStore = ns
	s.mu.Unlock()
}

func (s *State) setMapStore(ms *MapStore) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.mapStore = ms
	s.mu.Unlock()
}

// newOutboundSessionID prevents a fleet-hub restart from reusing the same
// navigation session with sequence numbers starting at one again. A fresh
// session makes downstream deduplication safe across process restarts; failure
// to obtain randomness fails navigation publication closed.
func newOutboundSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return "fleet-navigation-" + hex.EncodeToString(b)
}

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

func cloneVehicleState(v *vehicleState) *vehicleState {
	if v == nil {
		return &vehicleState{capabilities: map[string]string{}}
	}
	copy := *v
	copy.speedHist = append([]float64(nil), v.speedHist...)
	copy.throttleHist = append([]float64(nil), v.throttleHist...)
	copy.brakeHist = append([]float64(nil), v.brakeHist...)
	copy.accelHist = append([]float64(nil), v.accelHist...)
	copy.capabilities = copyMap(v.capabilities)
	return &copy
}

// applySignalUpdateLocked applies only a validated SignalUpdate to a private
// projection. It deliberately contains no I/O and no implicit defaults: a
// missing signal leaves its last confirmed value unchanged, while the online
// bit is driven only by receipt of a validated packet.
func applySignalUpdateLocked(v *vehicleState, sigs []signalVal, now time.Time) {
	if v == nil {
		return
	}
	v.online = true
	v.lastSeen = now
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
		case "Vehicle.Chassis.Throttle.Pct":
			if sg.Num != nil {
				v.throttlePct = *sg.Num
				v.throttleHist = append(v.throttleHist, round2(*sg.Num))
				if len(v.throttleHist) > speedHistCap {
					v.throttleHist = v.throttleHist[len(v.throttleHist)-speedHistCap:]
				}
			}
		case "Vehicle.Chassis.Brake.Pct":
			if sg.Num != nil {
				v.brakePct = *sg.Num
				v.brakeHist = append(v.brakeHist, round2(*sg.Num))
				if len(v.brakeHist) > speedHistCap {
					v.brakeHist = v.brakeHist[len(v.brakeHist)-speedHistCap:]
				}
			}
		case "Vehicle.GPS.Fix":
			if sg.Num != nil {
				v.gpsFix = *sg.Num > 0.5
			}
		case "Vehicle.Chassis.Accel.Longitudinal":
			if sg.Num != nil {
				v.accelMps2 = *sg.Num
				v.accelHist = append(v.accelHist, round2(*sg.Num))
				if len(v.accelHist) > speedHistCap {
					v.accelHist = v.accelHist[len(v.accelHist)-speedHistCap:]
				}
			}
		case "Vehicle.Cabin.Temperature.C":
			if sg.Num != nil {
				v.cabinTempC = *sg.Num
			}
		case "Vehicle.Cabin.Humidity.Pct":
			if sg.Num != nil {
				v.cabinHumidity = *sg.Num
			}
		case "Vehicle.GPS.Latitude":
			if sg.Num != nil {
				v.gpsLat = *sg.Num
			}
		case "Vehicle.GPS.Longitude":
			if sg.Num != nil {
				v.gpsLon = *sg.Num
			}
		case "Vehicle.GPS.Altitude":
			if sg.Num != nil {
				v.gpsAlt = *sg.Num
			}
		case "Platform.Autonomy.Localization.Pose.Valid":
			if sg.Num != nil {
				v.poseValiditySet = true
				v.poseValid = *sg.Num > 0.5
			}
		case "Platform.Autonomy.Localization.Pose.X":
			if sg.Num != nil {
				v.poseX = *sg.Num
				v.poseXSeen = true
			}
		case "Platform.Autonomy.Localization.Pose.Y":
			if sg.Num != nil {
				v.poseY = *sg.Num
				v.poseYSeen = true
			}
		case "Platform.Autonomy.Localization.Pose.Yaw":
			if sg.Num != nil {
				v.poseYaw = *sg.Num
				v.poseYawSeen = true
			}
		case "Platform.Autonomy.Localization.Pose.Frame":
			if sg.Text != nil {
				v.poseFrame = *sg.Text
			}
		}
	}
	if !v.poseValiditySet {
		v.poseValid = v.poseXSeen && v.poseYSeen && v.poseYawSeen
	}
}

// HandleRegister 处理 vehicle/{id}/register（retained 能力注册）。
func (s *State) HandleRegister(id, gatewayID string, caps map[string]string, group string) {
	vehicleLock := s.lockVehicle(id)
	vehicleLock.Lock()
	defer vehicleLock.Unlock()
	s.mu.Lock()
	v := s.ensureVehicleLocked(id)
	if gatewayID != "" {
		v.gatewayID = gatewayID
	}
	if group != "" {
		v.group = group
	}
	if caps["chassis"] != "" {
		v.chassis = caps["chassis"]
	}
	regGroup, regChassis := v.group, v.chassis
	for k, val := range caps {
		if val != "" {
			v.capabilities[k] = val
		}
	}
	s.mu.Unlock()

	s.pushEvent("info", id, fmt.Sprintf("车辆注册（网关 %s，栈 %s）", gatewayID, caps["stack"]), "veh")
	s.submitDB(func(ctx context.Context, db *sql.DB) {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO vehicles(id, gateway_id, stack, last_seen, group_id, chassis) VALUES ($1,$2,$3,now(),$4,$5) ON CONFLICT (id) DO UPDATE SET gateway_id=$2, stack=$3, last_seen=now(), group_id=COALESCE(NULLIF($4,''), vehicles.group_id), chassis=COALESCE(NULLIF($5,''), vehicles.chassis)",
			id, gatewayID, caps["stack"], regGroup, regChassis); err != nil {
			log.Printf("vehicles 入库失败: %v", err)
		}
	})
}

// HandleTelemetry 处理 vehicle/{id}/telemetry（SignalUpdate，2Hz）。
func (s *State) HandleTelemetry(id string, sigs []signalVal) {
	now := time.Now()
	vehicleLock := s.lockVehicle(id)
	vehicleLock.Lock()
	defer vehicleLock.Unlock()

	s.mu.Lock()
	v := s.ensureVehicleLocked(id)
	v = cloneVehicleState(v)
	v.id = id
	cameOnline := !v.online
	v.online = true
	v.lastSeen = now
	modeBefore := v.mode

	applySignalUpdateLocked(v, sigs, now)

	doSample := now.Sub(v.lastSampleAt) >= sampleEveryS*time.Second
	if doSample {
		v.lastSampleAt = now
	}
	stMode, stSpeed := v.mode, v.speedMPS
	stSOC, stVolt, stGear, stSteer := v.soc, v.voltage, v.gear, v.steerRad
	stGPSFix, stGPSLat, stGPSLon, stGPSAlt := v.gpsFix, v.gpsLat, v.gpsLon, v.gpsAlt
	stPoseValid, stPoseFrame, stPoseX, stPoseY, stPoseYaw := v.poseValid, v.poseFrame, v.poseX, v.poseY, v.poseYaw
	online := v.online
	s.vehicles[id] = v
	s.mu.Unlock()

	if cameOnline {
		s.pushEvent("info", id, "车辆上线（遥测恢复）", "veh")
	}
	if modeBefore != "" && modeBefore != stMode {
		s.onModeChange(id, modeBefore, stMode)
	}
	if doSample {
		s.persistSample(id, sigs)
		s.persistVehicleState(id, online, stMode, stSpeed, stSOC, stVolt, stGear, stSteer,
			stGPSFix, stGPSLat, stGPSLon, stGPSAlt, stPoseValid, stPoseFrame, stPoseX, stPoseY, stPoseYaw)
	}
}

// ApplyTelemetryDurably is the production MQTT projection path. The validated
// envelope's deduplication claim, vehicle last-seen/lifecycle projection,
// latest vehicle_state and the sampled telemetry rows commit in one
// PostgreSQL transaction. If any write fails, the sequence is not claimed and
// the caller can safely accept a later QoS retry without silently losing a
// vehicle state update.
func (s *State) ApplyTelemetryDurably(ctx context.Context, registry *GatewayRegistry, vehicleID, gatewayID, messageType, sessionID string, sequence uint64, sigs []signalVal) (bool, error) {
	if s == nil || s.db == nil || registry == nil || ctx == nil || vehicleID == "" || gatewayID == "" ||
		messageType == "" || sessionID == "" || sequence == 0 || len(sigs) == 0 {
		return false, fmt.Errorf("遥测权威投影字段无效")
	}
	vehicleLock := s.lockVehicle(vehicleID)
	vehicleLock.Lock()
	defer vehicleLock.Unlock()

	receivedAt := time.Now().UTC()
	s.mu.RLock()
	current := s.vehicles[vehicleID]
	next := cloneVehicleState(current)
	if next.id == "" {
		next.id = vehicleID
	}
	modeBefore := next.mode
	cameOnline := !next.online
	s.mu.RUnlock()

	applySignalUpdateLocked(next, sigs, receivedAt)
	doSample := next.lastSampleAt.IsZero() || receivedAt.Sub(next.lastSampleAt) >= sampleEveryS*time.Second
	if doSample {
		next.lastSampleAt = receivedAt
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("开启遥测权威事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	accepted, err := registry.AcceptInboundSequenceTx(ctx, tx, vehicleID, gatewayID, messageType, sessionID, sequence)
	if err != nil {
		return false, err
	}
	if !accepted {
		return false, nil
	}
	if err := persistTelemetryTx(ctx, tx, next, sigs, receivedAt, doSample); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("提交遥测权威事务: %w", err)
	}

	s.mu.Lock()
	s.vehicles[vehicleID] = next
	s.mu.Unlock()
	if cameOnline {
		s.pushEvent("info", vehicleID, "车辆上线（遥测恢复）", "veh")
	}
	if modeBefore != "" && modeBefore != next.mode {
		s.onModeChange(vehicleID, modeBefore, next.mode)
	}
	return true, nil
}

func persistTelemetryTx(ctx context.Context, tx *sql.Tx, v *vehicleState, sigs []signalVal, receivedAt time.Time, doSample bool) error {
	if tx == nil || v == nil || v.id == "" {
		return fmt.Errorf("遥测事务字段无效")
	}
	res, err := tx.ExecContext(ctx, `UPDATE vehicles SET last_seen=$2,
		lifecycle_state=CASE WHEN lifecycle_state IN ('quarantined','retired','maintenance')
			THEN lifecycle_state ELSE 'online' END
		WHERE id=$1`, v.id, receivedAt)
	if err != nil {
		return fmt.Errorf("更新车辆在线事实: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return fmt.Errorf("确认车辆在线事实: %w", err)
		}
		return fmt.Errorf("车辆不存在，拒绝写入遥测权威事实")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO vehicle_state
		(vehicle_id, online, mode, speed_mps, soc, voltage, gear, steer_rad,
		 gps_fix, gps_lat, gps_lon, gps_alt, pose_valid, pose_frame, pose_x, pose_y, pose_yaw, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (vehicle_id) DO UPDATE SET
		 online=$2, mode=$3, speed_mps=$4, soc=$5, voltage=$6, gear=$7, steer_rad=$8,
		 gps_fix=$9, gps_lat=$10, gps_lon=$11, gps_alt=$12, pose_valid=$13,
		 pose_frame=$14, pose_x=$15, pose_y=$16, pose_yaw=$17, updated_at=$18`,
		v.id, v.online, nullable(v.mode), v.speedMPS, v.soc, v.voltage, nullable(v.gear), v.steerRad,
		v.gpsFix, nullableFloat(v.gpsLat, v.gpsFix), nullableFloat(v.gpsLon, v.gpsFix), nullableFloat(v.gpsAlt, v.gpsFix),
		v.poseValid, nullable(v.poseFrame), nullableFloat(v.poseX, v.poseValid), nullableFloat(v.poseY, v.poseValid), nullableFloat(v.poseYaw, v.poseValid), receivedAt)
	if err != nil {
		return fmt.Errorf("更新 vehicle_state 权威事实: %w", err)
	}
	if !doSample {
		return nil
	}
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
		sampleAt := sg.SampleAt
		if sampleAt.IsZero() {
			sampleAt = receivedAt
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO telemetry_samples(vehicle_id, ts, path, num, txt) VALUES ($1,$2,$3,$4,$5)`,
			v.id, sampleAt, sg.Path, num, txt); err != nil {
			return fmt.Errorf("写入 telemetry_samples 权威事实: %w", err)
		}
	}
	return nil
}

func nullableFloat(value float64, valid bool) any {
	if !valid {
		return nil
	}
	return value
}

// HandleControlAck persists the vehicle-side execution result before the
// message is considered part of the control evidence stream. The UDP relay
// may return the same ACK to multiple operator links, while the MQTT dedup
// ledger makes this operation idempotent across Fleet restarts.
func (s *State) HandleControlAck(vehicleID, gatewayID, sourceLink string, ack *platformv1.ControlAck) error {
	if s == nil || s.db == nil || ack == nil || vehicleID == "" || gatewayID == "" ||
		ack.GetControlSessionId() == "" || ack.GetCommandSequence() == 0 {
		return fmt.Errorf("ControlAck 持久化字段无效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `INSERT INTO control_acks
		(control_session_id, command_sequence, vehicle_id, gateway_id, result, detail,
		 applied_monotonic_ns, source_link)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (control_session_id, command_sequence, source_link) DO NOTHING`,
		ack.GetControlSessionId(), ack.GetCommandSequence(), vehicleID, gatewayID,
		ack.GetResult().String(), ack.GetDetail(), ack.GetAppliedMonotonicNs(), sourceLink)
	return err
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
	s.pushEvent(level, id, txt, "veh")
}

// ---------- 巡检与接管 ----------

// sweepLoop 1Hz：在线巡检 + 接管状态只读轮询。
func (s *State) sweepLoop() {
	t := time.NewTicker(time.Second / sweepHz)
	defer t.Stop()
	for range t.C {
		s.onlineSweep()
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
		s.pushEvent("critical", id, fmt.Sprintf("链路丢失：%d 秒无遥测，判定离线", onlineWindowS), "veh")
		s.submitDB(func(ctx context.Context, db *sql.DB) {
			// A quarantined/retired/maintenance vehicle must not be silently
			// reclassified by a stale telemetry sweep.
			_, _ = db.ExecContext(ctx, `UPDATE vehicles SET lifecycle_state='offline'
				WHERE id=$1 AND lifecycle_state NOT IN ('quarantined','retired','maintenance')`, id)
		})
	}
}

// ---------- 事件 ----------

// pushEvent 追加事件环（头最新）+ WS 即时推送 + 入库。kind：veh/sys（事件分流）。
func (s *State) pushEvent(level, vehicleID, txt, kind string) {
	ev := EventSnap{TsNS: time.Now().UnixNano(), Level: level, Kind: kind, VehicleID: vehicleID, Text: txt}
	s.mu.Lock()
	evGroup := ""
	if vehicleID != "" {
		if vv, ok := s.vehicles[vehicleID]; ok {
			evGroup = vv.group
		}
	}
	s.events = append([]EventSnap{ev}, s.events...)
	if len(s.events) > eventRingCap {
		s.events = s.events[:eventRingCap]
	}
	s.mu.Unlock()

	log.Printf("[event] %s %s %s", level, vehicleID, txt)
	s.hub.PublishEvent(ev)
	s.submitDB(func(ctx context.Context, db *sql.DB) {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO events(ts, vehicle_id, level, text, group_id, kind) VALUES ($1,$2,$3,$4,$5,$6)",
			time.Unix(0, ev.TsNS), nullable(vehicleID), level, txt, evGroup, kind); err != nil {
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
			Chassis:           v.chassis,
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
			ThrottleHistory:   append([]float64(nil), v.throttleHist...),
			BrakeHistory:      append([]float64(nil), v.brakeHist...),
			ThrottlePct:       round2(v.throttlePct),
			BrakePct:          round2(v.brakePct),
			AccelMps2:         round2(v.accelMps2),
			AccelHistory:      append([]float64(nil), v.accelHist...),
			CabinTempC:        round2(v.cabinTempC),
			CabinHumidityPct:  round2(v.cabinHumidity),
			Gps:               GpsSnap{Fix: v.gpsFix, Lat: round6(v.gpsLat), Lon: round6(v.gpsLon), Alt: round2(v.gpsAlt)},
			Capabilities:      copyMap(v.capabilities),
			Pose: Pose{
				Valid: v.poseValid,
				Frame: v.poseFrame,
				X:     round2(v.poseX),
				Y:     round2(v.poseY),
				Yaw:   round2(v.poseYaw),
			},
		})
	}
	sort.Slice(out.Vehicles, func(i, j int) bool {
		return out.Vehicles[i].VehicleID < out.Vehicles[j].VehicleID
	})
	return out
}

func (s *State) RuntimeStatus() RuntimeStatus {
	status := RuntimeStatus{}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		status.DatabaseReady = s.db.PingContext(ctx) == nil
		cancel()
	}
	if status.DatabaseReady {
		ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		status.PersistentWritesReady = probePersistentWrite(ctx, s.db)
		cancel()
	}
	if status.DatabaseReady {
		status.Mode = "persistent"
	} else {
		status.Mode = "volatile-diagnostic"
	}
	s.mu.RLock()
	client := s.mqttPub
	s.mu.RUnlock()
	status.MQTTReady = client != nil && client.IsConnected()
	return status
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
		s.hub.PublishState(s.Snapshot())
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

// submitDB 优先非阻塞入队；队列满时在当前调用上下文执行一次有界同步回退。
// 这样高峰期会产生可观测背压，但不会把数据库事实静默丢掉。调用方位于
// MQTT 回调时可能短暂阻塞，这是持久性优先于吞吐的明确取舍；控制 ACK
// 本身仍走更强的同步持久化路径。
func (s *State) submitDB(job dbJob) {
	if s.db == nil {
		return
	}
	select {
	case s.dbCh <- job:
	default:
		if s.metrics != nil {
			s.metrics.dbWriteQueueFallbacks.Add(1)
		}
		log.Printf("DB 写入队列满，执行有界同步回退")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		job(ctx, s.db)
		cancel()
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
func (s *State) persistVehicleState(id string, online bool, mode string, speed, soc, voltage float64, gear string, steer float64,
	gpsFix bool, gpsLat, gpsLon, gpsAlt float64, poseValid bool, poseFrame string, poseX, poseY, poseYaw float64) {
	s.submitDB(func(ctx context.Context, db *sql.DB) {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO vehicles(id, lifecycle_state, last_seen)
				VALUES ($1, CASE WHEN $2 THEN 'online' ELSE 'offline' END,
					CASE WHEN $2 THEN now() ELSE NULL END)
				ON CONFLICT (id) DO UPDATE SET
				last_seen=CASE WHEN $2 THEN now() ELSE vehicles.last_seen END,
				lifecycle_state=CASE
					WHEN vehicles.lifecycle_state IN ('quarantined','retired','maintenance') THEN vehicles.lifecycle_state
					WHEN $2 THEN 'online' ELSE 'offline' END`, id, online); err != nil {
			log.Printf("vehicles 入库失败: %v", err)
			return
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO vehicle_state(vehicle_id, online, mode, speed_mps, soc, voltage, gear, steer_rad, gps_fix, gps_lat, gps_lon, gps_alt, pose_valid, pose_frame, pose_x, pose_y, pose_yaw, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,now()) ON CONFLICT (vehicle_id) DO UPDATE SET online=$2, mode=$3, speed_mps=$4, soc=$5, voltage=$6, gear=$7, steer_rad=$8, gps_fix=$9, gps_lat=$10, gps_lon=$11, gps_alt=$12, pose_valid=$13, pose_frame=$14, pose_x=$15, pose_y=$16, pose_yaw=$17, updated_at=now()",
			id, online, nullable(mode), speed, soc, voltage, nullable(gear), steer, gpsFix, gpsLat, gpsLon, gpsAlt, poseValid, nullable(poseFrame), poseX, poseY, poseYaw); err != nil {
			log.Printf("vehicle_state 入库失败: %v", err)
		}
	})
}

// ---------- 小工具 ----------

func round2(f float64) float64 { return math.Round(f*100) / 100 }
func round6(f float64) float64 { return math.Round(f*1e6) / 1e6 }

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
	select {
	case s.outboxWake <- struct{}{}:
	default:
	}
}

// PublishNavigation publishes a signed protobuf NavigationCommand to the
// vehicle's registered Gateway. JSON business commands are deliberately not
// supported: an unavailable key, Gateway or broker makes the operation fail
// closed and the caller keeps the task pending.
func (s *State) PublishNavigation(cmd *platformv1.NavigationCommand) bool {
	gatewayID, b, err := s.navigationEnvelope(cmd)
	if err != nil {
		return false
	}
	return s.enqueueOutboxFor(gatewayID, "navigation", "navigation_route", cmd.GetRouteId(), b)
}

// publishNavigationTx builds and queues a navigation envelope inside the same
// PostgreSQL transaction as the route row. This closes the crash window
// between "route committed" and "downlink queued".
func (s *State) publishNavigationTx(ctx context.Context, tx *sql.Tx, cmd *platformv1.NavigationCommand) error {
	gatewayID, b, err := s.navigationEnvelope(cmd)
	if err != nil {
		return err
	}
	return s.enqueueOutboxTx(ctx, tx, gatewayID, "navigation", "navigation_route", cmd.GetRouteId(), b)
}

func (s *State) navigationEnvelope(cmd *platformv1.NavigationCommand) (string, []byte, error) {
	if s == nil || cmd == nil || cmd.GetVehicleId() == "" || cmd.GetRouteId() == "" || len(s.envelopeAuthKey) < 32 || s.outboundSessionID == "" {
		return "", nil, fmt.Errorf("导航 Envelope 字段或认证密钥无效")
	}
	s.mu.Lock()
	if s.outboundSeq == nil {
		s.outboundSeq = make(map[string]uint64)
	}
	v := s.vehicles[cmd.GetVehicleId()]
	if v == nil || v.gatewayID == "" {
		s.mu.Unlock()
		return "", nil, fmt.Errorf("车辆没有已绑定 Gateway")
	}
	s.outboundSeq[cmd.GetVehicleId()]++
	sequence := s.outboundSeq[cmd.GetVehicleId()]
	gatewayID := v.gatewayID
	s.mu.Unlock()
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(cmd)
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC()
	env := &platformv1.Envelope{
		SchemaMajor: 1, SchemaMinor: 0,
		MessageType: "platform.v1.NavigationCommand",
		VehicleId:   cmd.GetVehicleId(), GatewayId: gatewayID,
		SessionId: s.outboundSessionID, Sequence: sequence,
		UtcTimeNs: now.UnixNano(), MonotonicTimeNs: platformv1.MonotonicNowNS(),
		TtlMs: 30000, TraceId: cmd.GetTraceId(), Payload: payload,
	}
	if env.TraceId == "" {
		env.TraceId = cmd.GetRouteId()
	}
	if env.TraceId == "" || platformv1.SignEnvelope(env, s.envelopeAuthKey) != nil {
		return "", nil, fmt.Errorf("导航 Envelope 签名失败")
	}
	b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(env)
	if err != nil {
		return "", nil, err
	}
	return gatewayID, b, nil
}

// PublishGatewayEnvelope 把已经过 Authority 签名的控制面消息投递至某一
// 受绑定 Gateway。Broker ACL 还会把该 topic 限制为 Gateway 证书身份可读。
func (s *State) PublishGatewayEnvelope(gatewayID, channel string, env []byte) bool {
	if gatewayID == "" || channel == "" || len(env) == 0 {
		return false
	}
	// 所有生产下行先进入 PostgreSQL outbox。这样 Fleet/MQTT 短暂重启不会
	// 丢失已签名消息，也不会把“进程尝试发送”冒充成“车辆已执行”。
	if s.db == nil {
		return false
	}
	return s.enqueueOutbox(gatewayID, channel, env)
}
