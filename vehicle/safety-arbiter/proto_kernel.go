package main

// proto_kernel.go：Safety Arbiter 的正式 platform.v1 Protobuf 执行路径。
//
// 生产入口只调用这里的 HandleProtoLease/HandleProtoCommand。车端执行路径
// 不包含 JSON 兼容层，确保跨边界认证始终覆盖确定性的 Protobuf 字节。

import (
	"crypto/ed25519"
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/proto"
	platformv1 "robot-agent/protocols/platform/v1"
)

func (a *Arbiter) HandleProtoLease(env *platformv1.Envelope) Decision {
	if env == nil {
		return Decision{Result: RejectedAuth, Detail: "LeaseGrant Envelope 为空"}
	}
	if err := platformv1.ValidateEnvelope(env, "platform.v1.LeaseGrant", a.cfg.VehicleID, a.cfg.GatewayID, a.cfg.Now()); err != nil {
		return Decision{Result: RejectedAuth, Detail: "LeaseGrant Envelope 无效: " + err.Error()}
	}
	if !platformv1.VerifyEnvelopeAuth(env, a.cfg.CommandMACKey) {
		return Decision{Result: RejectedAuth, Detail: "LeaseGrant Envelope.auth_tag 无效"}
	}
	grant := &platformv1.LeaseGrant{}
	if err := proto.Unmarshal(env.GetPayload(), grant); err != nil {
		return Decision{Result: RejectedAuth, Detail: "LeaseGrant Protobuf 解析失败: " + err.Error()}
	}
	if err := a.validateProtoLease(env, grant); err != nil {
		return Decision{Result: RejectedAuth, Detail: err.Error()}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.cfg.Now()
	until := time.Unix(0, grant.GetValidUntilUnixNs())
	switch grant.GetAction() {
	case platformv1.LeaseAction_LEASE_ACTION_GRANT:
		if !until.After(now) {
			return Decision{Result: RejectedLease, Detail: "拒绝已过期 LeaseGrant", MinimalRisk: true, RiskReason: "lease_expired"}
		}
		fencing := int64(grant.GetFencingToken())
		if fencing < a.lastFencing {
			return Decision{Result: RejectedFencing, Detail: "LeaseGrant fencing 落后"}
		}
		if fencing == a.lastFencing {
			if a.lease == nil || a.lease.LeaseID != grant.GetLeaseId() || until.Before(a.lease.ValidUntil) {
				return Decision{Result: RejectedFencing, Detail: "同 fencing LeaseGrant 不可改变租约或缩短有效期"}
			}
			a.lease.ValidUntil = until
			return Decision{Result: Accepted, Detail: "LeaseGrant 幂等续期已接受"}
		}
		a.lastFencing = fencing
		a.lease = &activeLease{LeaseID: grant.GetLeaseId(), Fencing: fencing, ValidUntil: until, LastCommand: now, LastSeq: 0}
		a.minimalRisk = ""
		return Decision{Result: Accepted, Detail: "新 Protobuf LeaseGrant 已接受"}

	case platformv1.LeaseAction_LEASE_ACTION_REVOKE:
		fencing := int64(grant.GetFencingToken())
		if fencing < a.lastFencing {
			return Decision{Result: RejectedFencing, Detail: "撤销 fencing 落后"}
		}
		a.lastFencing, a.lease, a.minimalRisk = fencing, nil, "lease_revoked"
		applied, err := a.cfg.Actuator.MinimalRisk("lease_revoked")
		if err != nil {
			return Decision{Result: Failed, Detail: "租约已撤销，但最小风险执行失败: " + err.Error(), MinimalRisk: true, RiskReason: "lease_revoked"}
		}
		return Decision{Result: Accepted, Detail: "租约已撤销，已触发最小风险动作", MinimalRisk: true, RiskReason: "lease_revoked", AppliedMonotonicNS: applied.AppliedMonotonicNS}
	default:
		return Decision{Result: RejectedAuth, Detail: "未知 LeaseGrant action"}
	}
}

func (a *Arbiter) validateProtoLease(env *platformv1.Envelope, grant *platformv1.LeaseGrant) error {
	if grant == nil || grant.GetVersion() != 1 ||
		(grant.GetAction() != platformv1.LeaseAction_LEASE_ACTION_GRANT && grant.GetAction() != platformv1.LeaseAction_LEASE_ACTION_REVOKE) ||
		grant.GetVehicleId() != a.cfg.VehicleID || grant.GetGatewayId() != a.cfg.GatewayID ||
		grant.GetLeaseId() == "" || grant.GetFencingToken() == 0 || env.GetSequence() != grant.GetFencingToken() ||
		grant.GetIssuedAtUnixNs() <= 0 || grant.GetValidUntilUnixNs() == 0 {
		return fmt.Errorf("LeaseGrant Protobuf 字段不符合安全契约")
	}
	issued := time.Unix(0, grant.GetIssuedAtUnixNs())
	envelopeTime := time.Unix(0, env.GetUtcTimeNs())
	if issued.Before(envelopeTime.Add(-a.cfg.MaxClockSkew)) || issued.After(envelopeTime.Add(a.cfg.MaxClockSkew)) {
		return fmt.Errorf("LeaseGrant 签发时间与 Envelope 偏差过大")
	}
	if grant.GetAction() == platformv1.LeaseAction_LEASE_ACTION_GRANT && (grant.GetDriverId() == "" || grant.GetDeviceId() == "") {
		return fmt.Errorf("grant LeaseGrant 缺少 driver/device")
	}
	key, ok := a.cfg.AuthorityKeys[grant.GetAuthorityKeyId()]
	if !ok || len(key) != ed25519.PublicKeySize || len(grant.GetAuthoritySignature()) != ed25519.SignatureSize {
		return fmt.Errorf("Authority key id 或签名不受信任")
	}
	unsigned := proto.Clone(grant).(*platformv1.LeaseGrant)
	unsigned.AuthoritySignature = nil
	canonical, err := (proto.MarshalOptions{Deterministic: true}).Marshal(unsigned)
	if err != nil || !ed25519.Verify(key, canonical, grant.GetAuthoritySignature()) {
		return fmt.Errorf("LeaseGrant Authority Protobuf 签名校验失败")
	}
	return nil
}

func (a *Arbiter) HandleProtoCommand(env *platformv1.Envelope) Decision {
	if env == nil {
		return Decision{Result: RejectedAuth, Detail: "ControlCommand Envelope 为空"}
	}
	if err := platformv1.ValidateEnvelope(env, "platform.v1.ControlCommand", a.cfg.VehicleID, a.cfg.GatewayID, a.cfg.Now()); err != nil {
		return Decision{Result: RejectedAuth, Detail: "ControlCommand Envelope 无效: " + err.Error()}
	}
	cmd := &platformv1.ControlCommand{}
	if err := proto.Unmarshal(env.GetPayload(), cmd); err != nil {
		return Decision{Result: RejectedAuth, Detail: "ControlCommand Protobuf 解析失败: " + err.Error()}
	}
	if err := validateProtoCommand(env, cmd); err != nil {
		return Decision{Result: RejectedAuth, Detail: err.Error()}
	}
	if !platformv1.VerifyEnvelopeAuth(env, a.cfg.CommandMACKey) {
		return Decision{Result: RejectedAuth, Detail: "ControlCommand Envelope.auth_tag 无效"}
	}
	if a.cfg.RequireMAC && !platformv1.VerifyControlCommandMAC(env, cmd, a.cfg.CommandMACKey) {
		return Decision{Result: RejectedAuth, Detail: "ControlCommand 端到端 MAC 无效"}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.cfg.Now()
	if err := protoCommandFresh(env, cmd, now, a.cfg.MaxClockSkew); err != nil {
		return Decision{Result: RejectedStale, Detail: err.Error()}
	}
	if a.lease == nil || cmd.GetLeaseId() != a.lease.LeaseID {
		return Decision{Result: RejectedLease, Detail: "不存在匹配的有效租约"}
	}
	if int64(cmd.GetFencingToken()) != a.lease.Fencing || int64(cmd.GetFencingToken()) != a.lastFencing {
		return Decision{Result: RejectedFencing, Detail: "fencing token 无效"}
	}
	if !a.lease.ValidUntil.After(now) {
		return a.expireLeaseLocked("lease_expired")
	}
	if a.lease.SessionID != "" && a.lease.SessionID != cmd.GetControlSessionId() {
		return Decision{Result: RejectedAuth, Detail: "控制会话不可切换"}
	}
	if int64(cmd.GetCommandSequence()) <= a.lease.LastSeq {
		return Decision{Result: RejectedStale, Detail: "控制序号重复或回退"}
	}

	switch cmd.GetMode() {
	case platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION:
		motion := cmd.GetMotion()
		if motion == nil {
			return Decision{Result: RejectedLimit, Detail: "TargetMotion 缺少 motion 载荷"}
		}
		m := Motion{TargetSpeedMPS: motion.GetTargetSpeedMps(), TargetAccelerationMPS: motion.GetTargetAccelerationMps2(), TargetCurvatureInvM: motion.GetTargetCurvatureInvM(), TargetYawRateRadPS: motion.GetTargetYawRateRadps()}
		if err := a.checkLimits(m); err != nil {
			return Decision{Result: RejectedLimit, Detail: err.Error()}
		}
		applied, err := a.cfg.Actuator.ApplyMotion(m)
		if err != nil {
			a.lease, a.minimalRisk = nil, "actuator_failure"
			_, _ = a.cfg.Actuator.MinimalRisk("actuator_failure")
			return Decision{Result: Failed, Detail: "车辆执行器拒绝控制: " + err.Error(), MinimalRisk: true, RiskReason: "actuator_failure"}
		}
		a.lease.SessionID, a.lease.LastSeq, a.lease.LastCommand = cmd.GetControlSessionId(), int64(cmd.GetCommandSequence()), now
		return Decision{Result: Accepted, Detail: "Protobuf 控制命令已由 Safety Arbiter 接受并发送至执行器", AppliedSpeed: m.TargetSpeedMPS, AppliedMonotonicNS: applied.AppliedMonotonicNS}

	case platformv1.ControlMode_CONTROL_MODE_MINIMAL_RISK:
		reason := cmd.GetMinimalRisk().GetReason()
		if reason == platformv1.MinimalRiskReason_MINIMAL_RISK_REASON_UNSPECIFIED {
			return Decision{Result: RejectedLimit, Detail: "最小风险原因未指定"}
		}
		reasonName := reason.String()
		a.lease, a.minimalRisk = nil, reasonName
		applied, err := a.cfg.Actuator.MinimalRisk(reasonName)
		if err != nil {
			return Decision{Result: Failed, Detail: "最小风险执行失败: " + err.Error(), MinimalRisk: true, RiskReason: reasonName}
		}
		return Decision{Result: Accepted, Detail: "最小风险命令已执行", MinimalRisk: true, RiskReason: reasonName, AppliedMonotonicNS: applied.AppliedMonotonicNS}
	default:
		return Decision{Result: RejectedLimit, Detail: "当前车型执行器未声明该 ControlMode"}
	}
}

func validateProtoCommand(env *platformv1.Envelope, cmd *platformv1.ControlCommand) error {
	if cmd == nil || cmd.GetControlSessionId() == "" || cmd.GetControlSessionId() != env.GetSessionId() ||
		cmd.GetLeaseId() == "" || cmd.GetFencingToken() == 0 || cmd.GetCommandSequence() == 0 ||
		cmd.GetCommandSequence() != env.GetSequence() || cmd.GetIssuedMonotonicNs() == 0 ||
		cmd.GetTtlMs() == 0 || cmd.GetTtlMs() != env.GetTtlMs() || cmd.GetTtlMs() > 1000 ||
		cmd.GetMode() == platformv1.ControlMode_CONTROL_MODE_UNSPECIFIED || len(cmd.GetEndToEndMac()) != 32 {
		return fmt.Errorf("ControlCommand Protobuf 字段不符合安全契约")
	}
	switch cmd.GetMode() {
	case platformv1.ControlMode_CONTROL_MODE_TARGET_MOTION:
		if cmd.GetMotion() == nil {
			return fmt.Errorf("TargetMotion 载荷缺失")
		}
	case platformv1.ControlMode_CONTROL_MODE_MINIMAL_RISK:
		if cmd.GetMinimalRisk() == nil || cmd.GetMinimalRisk().GetReason() == platformv1.MinimalRiskReason_MINIMAL_RISK_REASON_UNSPECIFIED {
			return fmt.Errorf("MinimalRisk 载荷或原因缺失")
		}
	default:
		// DirectActuation/Trajectory need a vehicle-specific actuator contract.
		// Accepting them without that contract would be an unsafe implicit mapping.
	}
	return nil
}

func protoCommandFresh(env *platformv1.Envelope, cmd *platformv1.ControlCommand, now time.Time, skew time.Duration) error {
	issued := time.Unix(0, env.GetUtcTimeNs())
	age := now.Sub(issued)
	if age < -skew || age > time.Duration(env.GetTtlMs())*time.Millisecond+skew {
		return fmt.Errorf("命令超过 TTL 或车辆时钟偏移过大")
	}
	if cmd.GetIssuedMonotonicNs() == 0 || math.IsNaN(float64(cmd.GetIssuedMonotonicNs())) {
		return fmt.Errorf("issued_monotonic_ns 无效")
	}
	return nil
}

func (a *Arbiter) expireLeaseLocked(reason string) Decision {
	a.lease, a.minimalRisk = nil, reason
	applied, err := a.cfg.Actuator.MinimalRisk(reason)
	detail := "租约已经失效，已触发最小风险动作"
	if err != nil {
		detail = "租约已经失效，但最小风险执行失败: " + err.Error()
	}
	return Decision{Result: RejectedLease, Detail: detail, MinimalRisk: true, RiskReason: reason, AppliedMonotonicNS: applied.AppliedMonotonicNS}
}
