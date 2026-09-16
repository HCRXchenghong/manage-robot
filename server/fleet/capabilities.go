package main

// capabilities.go：能力声明的持久化快照与控制准入查询。
//
// GatewayCapabilities 是车端声明，不是平台授权。快照只保存可审计的
// 版本/能力摘要，不保存 ICCID 等敏感凭据；车辆和 Gateway 的绑定仍由
// vehicle_registry.go 负责，控制 Authority 只信任 control_allowed=true。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	platformv1 "robot-agent/protocols/platform/v1"
)

type capabilityCameraSnapshot struct {
	ID             string `json:"id"`
	Width          uint32 `json:"width"`
	Height         uint32 `json:"height"`
	FPS            uint32 `json:"fps"`
	Codec          string `json:"codec"`
	MaxBitrateKbps uint32 `json:"max_bitrate_kbps"`
}

type capabilityModemSnapshot struct {
	ID      string `json:"id"`
	Carrier string `json:"carrier"`
}

type capabilitySnapshot struct {
	VehicleID             string                     `json:"vehicle_id"`
	GatewayID             string                     `json:"gateway_id"`
	Stack                 string                     `json:"stack"`
	StackVersion          string                     `json:"stack_version"`
	GatewayVersion        string                     `json:"gateway_version"`
	AdapterVersion        string                     `json:"adapter_version"`
	SafetyArbiterVersion  string                     `json:"safety_arbiter_version"`
	TopicMappingVersion   string                     `json:"topic_mapping_version"`
	SupportedControlModes []string                   `json:"supported_control_modes"`
	Cameras               []capabilityCameraSnapshot `json:"cameras"`
	Modems                []capabilityModemSnapshot  `json:"modems"`
	TPMAvailable          bool                       `json:"tpm_available"`
	CertificateInstalled  bool                       `json:"certificate_installed"`
	WorkspaceFeatures     []string                   `json:"workspace_features"`
	MapFormats            []string                   `json:"map_formats"`
	CoordinateFrame       string                     `json:"coordinate_frame"`
	ChassisType           string                     `json:"chassis_type"`
	DeclaredGroupID       string                     `json:"declared_group_id"`
}

func buildCapabilitySnapshot(gatewayID string, caps *platformv1.GatewayCapabilities) ([]byte, error) {
	if caps == nil || strings.TrimSpace(gatewayID) == "" {
		return nil, fmt.Errorf("能力快照缺少 Gateway 身份")
	}
	snapshot := capabilitySnapshot{
		VehicleID: caps.GetVehicleId(), GatewayID: gatewayID,
		Stack: caps.GetStack().String(), StackVersion: caps.GetStackVersion(),
		GatewayVersion: caps.GetGatewayVersion(), AdapterVersion: caps.GetAdapterVersion(),
		SafetyArbiterVersion: caps.GetSafetyArbiterVersion(), TopicMappingVersion: caps.GetTopicMappingVersion(),
		TPMAvailable: caps.GetTpmAvailable(), CertificateInstalled: caps.GetCertificateInstalled(),
		ChassisType: caps.GetChassisType(), DeclaredGroupID: caps.GetGroupId(),
		WorkspaceFeatures: append([]string(nil), caps.GetWorkspaceFeatures()...),
	}
	for _, mode := range caps.GetSupportedControlModes() {
		snapshot.SupportedControlModes = append(snapshot.SupportedControlModes, mode.String())
	}
	for _, camera := range caps.GetCameras() {
		if camera == nil {
			continue
		}
		snapshot.Cameras = append(snapshot.Cameras, capabilityCameraSnapshot{
			ID: camera.GetCameraId(), Width: camera.GetWidth(), Height: camera.GetHeight(),
			FPS: camera.GetFps(), Codec: camera.GetCodec(), MaxBitrateKbps: camera.GetMaxBitrateKbps(),
		})
	}
	for _, modem := range caps.GetModems() {
		if modem == nil {
			continue
		}
		// ICCID is deliberately excluded: it is a carrier credential/identifier,
		// not necessary for compatibility or operational display.
		snapshot.Modems = append(snapshot.Modems, capabilityModemSnapshot{ID: modem.GetModemId(), Carrier: modem.GetCarrier()})
	}
	if caps.GetMap() != nil {
		snapshot.MapFormats = append(snapshot.MapFormats, caps.GetMap().GetMapFormats()...)
		snapshot.CoordinateFrame = caps.GetMap().GetCoordinateFrame()
	}
	return json.Marshal(snapshot)
}

// PersistCapabilities atomically records the latest declaration and its
// compatibility decision. The Gateway-declared group is retained as evidence
// only; organization ownership is never changed by a vehicle message.
func (r *GatewayRegistry) PersistCapabilities(ctx context.Context, gatewayID string, caps *platformv1.GatewayCapabilities, decision platformv1.CapabilityDecision) error {
	if r == nil || r.db == nil || caps == nil || strings.TrimSpace(gatewayID) == "" {
		return fmt.Errorf("能力声明持久化需要 PostgreSQL 与 Gateway 身份")
	}
	if decision.ControlAllowed && !decision.MonitoringAllowed {
		return fmt.Errorf("能力准入结果违反控制必须先具备监控资格的不变量")
	}
	snapshot, err := buildCapabilitySnapshot(gatewayID, caps)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM vehicle_gateways WHERE vehicle_id=$1 AND gateway_id=$2 AND status='active'
	)`, caps.GetVehicleId(), gatewayID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("Gateway 未处于 active 注册状态，拒绝保存能力")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vehicle_capabilities
		(vehicle_id, gateway_id, matrix_entry, stack, stack_version, gateway_version,
		 adapter_version, safety_arbiter_version, topic_mapping_version,
		 monitoring_allowed, control_allowed, control_reason, snapshot, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,now())
		ON CONFLICT (vehicle_id, gateway_id) DO UPDATE SET
		 matrix_entry=EXCLUDED.matrix_entry, stack=EXCLUDED.stack,
		 stack_version=EXCLUDED.stack_version, gateway_version=EXCLUDED.gateway_version,
		 adapter_version=EXCLUDED.adapter_version, safety_arbiter_version=EXCLUDED.safety_arbiter_version,
		 topic_mapping_version=EXCLUDED.topic_mapping_version,
		 monitoring_allowed=EXCLUDED.monitoring_allowed, control_allowed=EXCLUDED.control_allowed,
		 control_reason=EXCLUDED.control_reason, snapshot=EXCLUDED.snapshot, updated_at=now()`,
		caps.GetVehicleId(), gatewayID, decision.MatrixEntry, caps.GetStack().String(), caps.GetStackVersion(),
		caps.GetGatewayVersion(), caps.GetAdapterVersion(), caps.GetSafetyArbiterVersion(), caps.GetTopicMappingVersion(),
		decision.MonitoringAllowed, decision.ControlAllowed, decision.ControlReason, snapshot); err != nil {
		return fmt.Errorf("保存 Gateway 能力快照: %w", err)
	}
	// Only platform-owned registration fields are updated here. In particular,
	// group_id and chassis are never taken from GatewayCapabilities. A rejected
	// declaration remains evidence but cannot update the live vehicle view.
	if decision.MonitoringAllowed {
		if _, err := tx.ExecContext(ctx, `UPDATE vehicles SET gateway_id=$1, stack=$2
			WHERE id=$3`, gatewayID, caps.GetStack().String(), caps.GetVehicleId()); err != nil {
			return fmt.Errorf("更新车辆能力版本: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO control_security_events
		(vehicle_id, gateway_id, actor, event_type, outcome, detail)
		VALUES ($1,$2,$3,'gateway_capabilities_evaluated',$4,$5::jsonb)`, caps.GetVehicleId(), gatewayID,
		"gateway:"+gatewayID, capabilityOutcome(decision), mustJSON(map[string]any{
			"matrix_entry": decision.MatrixEntry, "monitoring_allowed": decision.MonitoringAllowed,
			"control_allowed": decision.ControlAllowed, "control_reason": decision.ControlReason,
		})); err != nil {
		return fmt.Errorf("记录 Gateway 能力审计: %w", err)
	}
	return tx.Commit()
}

func capabilityOutcome(decision platformv1.CapabilityDecision) string {
	if !decision.MonitoringAllowed {
		return "rejected"
	}
	if decision.ControlAllowed {
		return "control_ready"
	}
	return "monitoring_only"
}

func requireControlCapabilityTx(ctx context.Context, tx *sql.Tx, vehicleID, gatewayID string) error {
	var allowed bool
	var reason string
	err := tx.QueryRowContext(ctx, `SELECT control_allowed, control_reason FROM vehicle_capabilities
		WHERE vehicle_id=$1 AND gateway_id=$2`, vehicleID, gatewayID).Scan(&allowed, &reason)
	if err == sql.ErrNoRows {
		return fmt.Errorf("车辆 Gateway 尚未通过能力兼容性准入")
	}
	if err != nil {
		return fmt.Errorf("读取车辆能力准入: %w", err)
	}
	if !allowed {
		return fmt.Errorf("车辆 Gateway 仅允许监控，禁止接管：%s", strings.TrimSpace(reason))
	}
	return nil
}

func requireVehicleLiveForControlTx(ctx context.Context, tx *sql.Tx, vehicleID string) error {
	var lifecycle string
	var online bool
	var updatedAt time.Time
	err := tx.QueryRowContext(ctx, `SELECT v.lifecycle_state, COALESCE(vs.online, false),
		COALESCE(vs.updated_at, to_timestamp(0))
		FROM vehicles v LEFT JOIN vehicle_state vs ON vs.vehicle_id=v.id
		WHERE v.id=$1 FOR UPDATE OF v`, vehicleID).Scan(&lifecycle, &online, &updatedAt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("车辆不存在")
	}
	if err != nil {
		return fmt.Errorf("读取车辆运行状态: %w", err)
	}
	if lifecycle == "quarantined" || lifecycle == "retired" || lifecycle == "maintenance" {
		return fmt.Errorf("车辆生命周期为 %s，禁止接管", lifecycle)
	}
	if !online || time.Since(updatedAt) > onlineWindowS*time.Second {
		return fmt.Errorf("车辆没有最近 %d 秒内的真实遥测，禁止接管", onlineWindowS)
	}
	return nil
}

func (r *GatewayRegistry) RequireMonitoring(ctx context.Context, vehicleID, gatewayID string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("车辆能力注册中心不可用")
	}
	var allowed bool
	var reason, lifecycle string
	err := r.db.QueryRowContext(ctx, `SELECT c.monitoring_allowed, c.control_reason, v.lifecycle_state
		FROM vehicle_capabilities c JOIN vehicles v ON v.id=c.vehicle_id
		WHERE c.vehicle_id=$1 AND c.gateway_id=$2`, vehicleID, gatewayID).Scan(&allowed, &reason, &lifecycle)
	if err == sql.ErrNoRows {
		return fmt.Errorf("车辆尚未完成 Gateway 能力兼容性注册")
	}
	if err != nil {
		return fmt.Errorf("读取车辆监控准入: %w", err)
	}
	if lifecycle == "quarantined" || lifecycle == "retired" {
		return fmt.Errorf("车辆生命周期为 %s，拒绝进入监控面", lifecycle)
	}
	if !allowed {
		return fmt.Errorf("车辆 Gateway 能力不兼容，拒绝进入监控面：%s", strings.TrimSpace(reason))
	}
	return nil
}
