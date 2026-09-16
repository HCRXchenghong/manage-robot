package main

// arbiter.go：独立车端 Safety Arbiter 的安全内核。
//
// 生产入口只接受 platform.v1 Protobuf（见 proto_kernel.go）。它不信任
// Gateway、MQTT、浏览器或控制中继：每条命令都必须通过租约、fencing、
// 序号、TTL、MAC、ODD 限幅和本地看门狗检查。任何失败都不执行，严重
// 失效进入最小风险状态。

import (
	"crypto/ed25519"
	"fmt"
	"math"
	"sync"
	"time"
)

type Motion struct {
	TargetSpeedMPS        float64
	TargetAccelerationMPS float64
	TargetCurvatureInvM   float64
	TargetYawRateRadPS    float64
}

type Result string

const (
	Accepted        Result = "CONTROL_RESULT_ACCEPTED"
	RejectedStale   Result = "CONTROL_RESULT_REJECTED_STALE"
	RejectedFencing Result = "CONTROL_RESULT_REJECTED_FENCING"
	RejectedLease   Result = "CONTROL_RESULT_REJECTED_LEASE"
	RejectedLimit   Result = "CONTROL_RESULT_REJECTED_LIMIT"
	RejectedAuth    Result = "CONTROL_RESULT_REJECTED_AUTH"
	Failed          Result = "CONTROL_RESULT_FAILED"
)

type Decision struct {
	Result             Result
	Detail             string
	MinimalRisk        bool
	RiskReason         string
	AppliedSpeed       float64
	AppliedMonotonicNS int64
}

// Actuator is the only side-effect boundary of the safety kernel. Production
// implementations must talk to the vehicle adapter/ECU over a protected local
// channel. A nil actuator is rejected at startup; a log line or in-memory
// value is never treated as execution.
type ActuationResult struct {
	AppliedMonotonicNS int64
}

type Actuator interface {
	ApplyMotion(Motion) (ActuationResult, error)
	MinimalRisk(string) (ActuationResult, error)
}

type Limits struct {
	MaxSpeedMPS        float64
	MaxAccelerationMPS float64
	MaxCurvatureInvM   float64
	MaxYawRateRadPS    float64
}

type Config struct {
	VehicleID     string
	GatewayID     string
	AuthorityKeys map[string]ed25519.PublicKey
	CommandMACKey []byte
	RequireMAC    bool
	Watchdog      time.Duration
	MaxClockSkew  time.Duration
	Limits        Limits
	Actuator      Actuator
	Now           func() time.Time
}

type activeLease struct {
	LeaseID     string
	Fencing     int64
	ValidUntil  time.Time
	LastCommand time.Time
	LastSeq     int64
	SessionID   string
}

type Arbiter struct {
	mu          sync.Mutex
	cfg         Config
	lastFencing int64
	lease       *activeLease
	minimalRisk string
}

func NewArbiter(cfg Config) (*Arbiter, error) {
	if cfg.VehicleID == "" || cfg.GatewayID == "" || len(cfg.AuthorityKeys) == 0 {
		return nil, fmt.Errorf("vehicle_id、gateway_id 与 Authority 公钥必填")
	}
	if cfg.RequireMAC && len(cfg.CommandMACKey) < 32 {
		return nil, fmt.Errorf("启用命令 MAC 时必须配置至少 256 位会话密钥")
	}
	if cfg.Watchdog <= 0 {
		cfg.Watchdog = 500 * time.Millisecond
	}
	if cfg.MaxClockSkew <= 0 {
		cfg.MaxClockSkew = 2 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Limits.MaxSpeedMPS <= 0 || cfg.Limits.MaxAccelerationMPS <= 0 ||
		cfg.Limits.MaxCurvatureInvM <= 0 || cfg.Limits.MaxYawRateRadPS <= 0 {
		return nil, fmt.Errorf("所有车辆控制限幅必须由已标定配置提供")
	}
	if cfg.Actuator == nil {
		return nil, fmt.Errorf("必须配置真实车辆执行器接口")
	}
	return &Arbiter{cfg: cfg}, nil
}

func (a *Arbiter) checkLimits(m Motion) error {
	vals := []float64{m.TargetSpeedMPS, m.TargetAccelerationMPS,
		m.TargetCurvatureInvM, m.TargetYawRateRadPS}
	for _, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("控制值不能为 NaN 或 Inf")
		}
	}
	if m.TargetSpeedMPS < 0 || m.TargetSpeedMPS > a.cfg.Limits.MaxSpeedMPS ||
		math.Abs(m.TargetAccelerationMPS) > a.cfg.Limits.MaxAccelerationMPS ||
		math.Abs(m.TargetCurvatureInvM) > a.cfg.Limits.MaxCurvatureInvM ||
		math.Abs(m.TargetYawRateRadPS) > a.cfg.Limits.MaxYawRateRadPS {
		return fmt.Errorf("控制命令超出已标定 ODD 限幅")
	}
	return nil
}

// Tick must be called from the independent local watchdog, not from browser
// heartbeats. It does not preserve the previous actuation after control loss.
func (a *Arbiter) Tick() Decision {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.cfg.Now()
	if a.lease == nil {
		return Decision{Result: RejectedLease, Detail: "无有效租约", RiskReason: a.minimalRisk}
	}
	if !a.lease.ValidUntil.After(now) {
		a.lease, a.minimalRisk = nil, "lease_expired"
		applied, err := a.cfg.Actuator.MinimalRisk("lease_expired")
		detail := "租约到期，已触发最小风险动作"
		if err != nil {
			detail = "租约到期，但最小风险执行失败: " + err.Error()
		}
		return Decision{Result: RejectedLease, Detail: detail, MinimalRisk: true,
			RiskReason: "lease_expired", AppliedMonotonicNS: applied.AppliedMonotonicNS}
	}
	if now.Sub(a.lease.LastCommand) > a.cfg.Watchdog {
		// Link loss is latched. A delayed packet cannot resume control; Authority
		// must issue a new, higher-fencing lease to re-arm the vehicle.
		a.lease, a.minimalRisk = nil, "control_watchdog_timeout"
		applied, err := a.cfg.Actuator.MinimalRisk("control_watchdog_timeout")
		detail := "控制流看门狗超时，已触发最小风险动作"
		if err != nil {
			detail = "控制流看门狗超时，但最小风险执行失败: " + err.Error()
		}
		return Decision{Result: RejectedStale, Detail: detail, MinimalRisk: true,
			RiskReason: a.minimalRisk, AppliedMonotonicNS: applied.AppliedMonotonicNS}
	}
	return Decision{Result: Accepted, Detail: "车端控制状态健康"}
}
