package main

// map_publication.go：地图发布的权威状态机。
//
// 地图版本本身不可变；发布是另一条可审计的业务流，不能用“最新地图”
// 这样的进程内推断代替。每次发布都必须经过车辆/Gateway 能力准入、人工
// 审批和 PostgreSQL transactional outbox，只有车端 Map Agent 返回 APPLIED
// 才能把版本标记为 active。没有真实 Map Agent 时状态会停在 dispatched 或
// confirmed，平台不会把 Broker 接收误报成车辆生效。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	platformv1 "robot-agent/protocols/platform/v1"
)

type mapPublication struct {
	ID                    string `json:"id"`
	MapID                 string `json:"map_id"`
	Version               int    `json:"version"`
	VehicleID             string `json:"vehicle_id"`
	State                 string `json:"state"`
	Action                string `json:"action"`
	Compatibility         any    `json:"compatibility,omitempty"`
	PreviousPublicationID string `json:"previous_publication_id,omitempty"`
	RollbackTargetID      string `json:"rollback_target_publication_id,omitempty"`
	RequestedBy           string `json:"requested_by"`
	ApprovedBy            string `json:"approved_by,omitempty"`
	ContentSHA256         string `json:"content_sha256"`
	CoordinateFrame       string `json:"coordinate_frame"`
	VehicleAckResult      string `json:"vehicle_ack_result,omitempty"`
	VehicleAckDetail      string `json:"vehicle_ack_detail,omitempty"`
	VehicleAckNS          int64  `json:"vehicle_ack_ns,omitempty"`
	CreatedNS             int64  `json:"created_ns"`
	UpdatedNS             int64  `json:"updated_ns"`
}

func mapPublicationID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "mp-" + fmt.Sprintf("%x", b), nil
}

func mapVersionOf(entry *MapEntry, version int) (*MapVersion, error) {
	if entry == nil {
		return nil, fmt.Errorf("地图不存在")
	}
	if version <= 0 && len(entry.Versions) > 0 {
		version = entry.Versions[len(entry.Versions)-1].Version
	}
	for i := range entry.Versions {
		if entry.Versions[i].Version == version {
			copy := entry.Versions[i]
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("地图 %s 不存在 v%d", entry.ID, version)
}

func normalizeMapFormat(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, ".")
	switch value {
	case "3d_pcd":
		return "pcd"
	case "3d_csv":
		return "csv"
	case "2d_png", "pgm":
		return "png"
	default:
		return value
	}
}

func mapFormatAllowed(kind string, formats []string) bool {
	want := normalizeMapFormat(kind)
	for _, format := range formats {
		if normalizeMapFormat(format) == want {
			return true
		}
	}
	return false
}

func mapPublicationCompatibility(m *MapEntry, v *MapVersion, coordinateFrame string, snapshot capabilitySnapshot, matrixEntry string) (map[string]any, error) {
	if m == nil || v == nil {
		return nil, fmt.Errorf("地图版本无效")
	}
	if strings.TrimSpace(coordinateFrame) == "" {
		return nil, fmt.Errorf("coordinate_frame 必填；禁止在未知坐标系下发布地图")
	}
	if snapshot.CoordinateFrame != "" && snapshot.CoordinateFrame != coordinateFrame {
		return nil, fmt.Errorf("车辆坐标系 %s 与地图坐标系 %s 不一致", snapshot.CoordinateFrame, coordinateFrame)
	}
	if len(snapshot.MapFormats) == 0 || !mapFormatAllowed(v.Kind, snapshot.MapFormats) {
		return nil, fmt.Errorf("车辆 Gateway 未声明支持地图格式 %s", v.Kind)
	}
	if matrixEntry == "" {
		return nil, fmt.Errorf("车辆尚未通过协议兼容矩阵准入")
	}
	meta := map[string]any{
		"map_id":              m.ID,
		"map_version":         v.Version,
		"kind":                v.Kind,
		"content_sha256":      v.ContentSHA256,
		"coordinate_frame":    coordinateFrame,
		"compatibility_entry": matrixEntry,
	}
	return meta, nil
}

func scanMapPublication(row interface{ Scan(...any) error }) (*mapPublication, error) {
	p := &mapPublication{}
	var compatibility string
	var created, updated time.Time
	var ackAt sql.NullTime
	if err := row.Scan(&p.ID, &p.MapID, &p.Version, &p.VehicleID, &p.State, &p.Action,
		&compatibility, &p.PreviousPublicationID, &p.RollbackTargetID, &p.RequestedBy, &p.ApprovedBy,
		&p.ContentSHA256, &p.CoordinateFrame, &p.VehicleAckResult, &p.VehicleAckDetail,
		&ackAt, &created, &updated); err != nil {
		return nil, err
	}
	if compatibility != "" {
		if err := json.Unmarshal([]byte(compatibility), &p.Compatibility); err != nil {
			return nil, fmt.Errorf("地图发布兼容性记录无效: %w", err)
		}
	}
	p.CreatedNS, p.UpdatedNS = created.UnixNano(), updated.UnixNano()
	if ackAt.Valid {
		p.VehicleAckNS = ackAt.Time.UnixNano()
	}
	return p, nil
}

const mapPublicationSelect = `SELECT id, map_id, version, vehicle_id, state, action,
	compatibility::text, COALESCE(previous_publication_id,''), COALESCE(rollback_target_publication_id,''), requested_by,
	COALESCE(approved_by,''), content_sha256, coordinate_frame,
	COALESCE(vehicle_ack_result,''), COALESCE(vehicle_ack_detail,''), vehicle_ack_at,
	created_at, updated_at FROM map_publications`

func (ms *MapStore) loadMapPublicationTargetTx(ctx context.Context, tx *sql.Tx, p *mapPublication) (string, capabilitySnapshot, string, error) {
	if ms == nil || ms.st == nil || ms.st.db == nil || tx == nil || p == nil {
		return "", capabilitySnapshot{}, "", fmt.Errorf("地图发布需要 PostgreSQL")
	}
	var gatewayID, lifecycle, snapshotJSON, matrixEntry string
	var monitoring bool
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(v.gateway_id,''), COALESCE(v.lifecycle_state,'offline'),
		COALESCE(vc.monitoring_allowed,false), COALESCE(vc.snapshot::text,'{}'), COALESCE(vc.matrix_entry,'')
		FROM vehicles v LEFT JOIN vehicle_capabilities vc
		ON vc.vehicle_id=v.id AND vc.gateway_id=v.gateway_id
		WHERE v.id=$1 FOR UPDATE OF v`, p.VehicleID).Scan(&gatewayID, &lifecycle, &monitoring, &snapshotJSON, &matrixEntry)
	if err == sql.ErrNoRows {
		return "", capabilitySnapshot{}, "", fmt.Errorf("车辆不存在")
	}
	if err != nil {
		return "", capabilitySnapshot{}, "", fmt.Errorf("读取车辆地图能力: %w", err)
	}
	if gatewayID == "" || !monitoring {
		return "", capabilitySnapshot{}, "", fmt.Errorf("车辆 Gateway 未通过监控能力准入")
	}
	if lifecycle == "quarantined" || lifecycle == "retired" {
		return "", capabilitySnapshot{}, "", fmt.Errorf("车辆生命周期为 %s，拒绝地图发布", lifecycle)
	}
	var snapshot capabilitySnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		return "", capabilitySnapshot{}, "", fmt.Errorf("车辆地图能力快照无效: %w", err)
	}
	return gatewayID, snapshot, matrixEntry, nil
}

func (ms *MapStore) RequestMapPublication(ctx context.Context, mapID, vehicleID string, version int, action, coordinateFrame, requestedBy string) (*mapPublication, error) {
	if ms == nil || ms.st == nil || ms.st.db == nil {
		return nil, fmt.Errorf("地图发布需要 PostgreSQL")
	}
	if action != "apply" && action != "rollback" {
		return nil, fmt.Errorf("地图发布 action 无效")
	}
	entry := ms.Get(mapID)
	if entry == nil || entry.VehicleID != vehicleID {
		return nil, fmt.Errorf("地图不存在或不属于目标车辆")
	}
	v, err := mapVersionOf(entry, version)
	if err != nil {
		return nil, err
	}
	id, err := mapPublicationID()
	if err != nil {
		return nil, fmt.Errorf("生成地图发布 ID: %w", err)
	}
	compatibility := map[string]any{"coordinate_frame": strings.TrimSpace(coordinateFrame)}
	compatibilityJSON, _ := json.Marshal(compatibility)
	var previous string
	if err := ms.st.db.QueryRowContext(ctx, `SELECT id FROM map_publications
		WHERE vehicle_id=$1 AND state='active' ORDER BY updated_at DESC LIMIT 1`, vehicleID).Scan(&previous); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("读取当前生效地图发布: %w", err)
	}
	if action == "rollback" && previous == "" {
		return nil, fmt.Errorf("没有可回滚的当前生效地图")
	}
	now := time.Now().UTC()
	_, err = ms.st.db.ExecContext(ctx, `INSERT INTO map_publications
		(id, map_id, version, vehicle_id, state, action, compatibility, previous_publication_id,
		 requested_by, content_sha256, coordinate_frame, created_at, updated_at)
		VALUES ($1,$2,$3,$4,'requested',$5,$6::jsonb,$7,$8,$9,$10,$11,$11)`, id, mapID, v.Version,
		vehicleID, action, string(compatibilityJSON), previous, requestedBy, v.ContentSHA256,
		strings.TrimSpace(coordinateFrame), now)
	if err != nil {
		return nil, fmt.Errorf("创建地图发布审批: %w", err)
	}
	return &mapPublication{ID: id, MapID: mapID, Version: v.Version, VehicleID: vehicleID,
		State: "requested", Action: action, Compatibility: compatibility,
		PreviousPublicationID: previous, RequestedBy: requestedBy, ContentSHA256: v.ContentSHA256,
		CoordinateFrame: strings.TrimSpace(coordinateFrame), CreatedNS: now.UnixNano(), UpdatedNS: now.UnixNano()}, nil
}

func (ms *MapStore) ApproveMapPublication(ctx context.Context, id, approver string) (*mapPublication, error) {
	if ms == nil || ms.st == nil || ms.st.db == nil {
		return nil, fmt.Errorf("地图发布需要 PostgreSQL")
	}
	tx, err := ms.st.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := scanMapPublication(tx.QueryRowContext(ctx, mapPublicationSelect+` WHERE id=$1 FOR UPDATE`, id))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("地图发布不存在")
	}
	if err != nil {
		return nil, err
	}
	if p.State != "requested" {
		return nil, fmt.Errorf("地图发布当前状态为 %s，不能重复审批", p.State)
	}
	entry := ms.Get(p.MapID)
	v, err := mapVersionOf(entry, p.Version)
	if err != nil {
		return nil, err
	}
	var compatibility map[string]any
	if p.Compatibility != nil {
		if raw, marshalErr := json.Marshal(p.Compatibility); marshalErr == nil {
			_ = json.Unmarshal(raw, &compatibility)
		}
	}
	coordinateFrame := p.CoordinateFrame
	if compatibility != nil {
		if value, ok := compatibility["coordinate_frame"].(string); ok && coordinateFrame == "" {
			coordinateFrame = value
		}
	}
	gatewayID, snapshot, matrixEntry, err := ms.loadMapPublicationTargetTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	compatibility, err = mapPublicationCompatibility(entry, v, coordinateFrame, snapshot, matrixEntry)
	if err != nil {
		return nil, err
	}
	compatibilityJSON, _ := json.Marshal(compatibility)
	now := time.Now().UTC()
	command := &platformv1.MapPublicationCommand{Version: 1, PublicationId: p.ID, MapId: p.MapID,
		VehicleId: p.VehicleID, MapVersion: uint32(p.Version), ContentSha256: v.ContentSHA256,
		CoordinateFrame: coordinateFrame, CompatibilityEntry: matrixEntry, TraceId: p.ID}
	if p.Action == "rollback" {
		command.Action = platformv1.MapPublicationAction_MAP_PUBLICATION_ACTION_ROLLBACK
		command.RollbackPublicationId = p.PreviousPublicationID
	} else {
		command.Action = platformv1.MapPublicationAction_MAP_PUBLICATION_ACTION_APPLY
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(command)
	if err != nil {
		return nil, fmt.Errorf("序列化地图发布命令: %w", err)
	}
	sequenceBytes := make([]byte, 8)
	if _, err := rand.Read(sequenceBytes); err != nil {
		return nil, err
	}
	var sequence uint64
	for _, b := range sequenceBytes {
		sequence = sequence<<8 | uint64(b)
	}
	env := &platformv1.Envelope{SchemaMajor: 1, SchemaMinor: 0,
		MessageType: "platform.v1.MapPublicationCommand", VehicleId: p.VehicleID, GatewayId: gatewayID,
		SessionId: "fleet-map-" + p.ID, Sequence: sequence, UtcTimeNs: now.UnixNano(),
		MonotonicTimeNs: platformv1.MonotonicNowNS(), TtlMs: 30000, TraceId: p.ID, Payload: payload}
	if err := platformv1.SignEnvelope(env, ms.st.envelopeAuthKey); err != nil {
		return nil, fmt.Errorf("地图发布信封签名: %w", err)
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(env)
	if err != nil {
		return nil, err
	}
	if err := ms.st.enqueueOutboxTx(ctx, tx, gatewayID, "map", "map_publication", p.ID, encoded); err != nil {
		return nil, fmt.Errorf("地图发布 outbox: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE map_publications SET state='approved', approved_by=$1,
		compatibility=$2::jsonb, content_sha256=$3, coordinate_frame=$4, updated_at=$5 WHERE id=$6`,
		approver, string(compatibilityJSON), v.ContentSHA256, coordinateFrame, now, p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	p.State, p.ApprovedBy, p.Compatibility, p.ContentSHA256, p.CoordinateFrame, p.UpdatedNS = "approved", approver, compatibility, v.ContentSHA256, coordinateFrame, now.UnixNano()
	ms.st.wakeOutbox()
	ms.st.pushEvent("info", p.VehicleID, fmt.Sprintf("地图发布 %s 已审批并进入车端可靠下行队列", p.ID), "sys")
	return p, nil
}

func (ms *MapStore) RollbackMapPublication(ctx context.Context, sourceID, actor string) (*mapPublication, error) {
	if ms == nil || ms.st == nil || ms.st.db == nil {
		return nil, fmt.Errorf("地图发布需要 PostgreSQL")
	}
	source, err := scanMapPublication(ms.st.db.QueryRowContext(ctx, mapPublicationSelect+` WHERE id=$1`, sourceID))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("地图发布不存在")
		}
		return nil, err
	}
	if source.State != "active" || source.PreviousPublicationID == "" {
		return nil, fmt.Errorf("只有有前一生效版本的 active 发布可以回滚")
	}
	target, err := scanMapPublication(ms.st.db.QueryRowContext(ctx, mapPublicationSelect+` WHERE id=$1`, source.PreviousPublicationID))
	if err != nil {
		return nil, fmt.Errorf("读取回滚目标: %w", err)
	}
	return ms.RequestMapPublicationWithPrevious(ctx, target.MapID, target.VehicleID, target.Version, "rollback", target.CoordinateFrame, actor, source.ID, target.ID)
}

func (ms *MapStore) RequestMapPublicationWithPrevious(ctx context.Context, mapID, vehicleID string, version int, action, coordinateFrame, requestedBy, previousID, rollbackTargetID string) (*mapPublication, error) {
	entry := ms.Get(mapID)
	v, err := mapVersionOf(entry, version)
	if err != nil {
		return nil, err
	}
	id, err := mapPublicationID()
	if err != nil {
		return nil, err
	}
	compatibilityJSON, _ := json.Marshal(map[string]any{"coordinate_frame": strings.TrimSpace(coordinateFrame)})
	now := time.Now().UTC()
	if _, err := ms.st.db.ExecContext(ctx, `INSERT INTO map_publications
		(id, map_id, version, vehicle_id, state, action, compatibility, previous_publication_id,
		 rollback_target_publication_id, requested_by, content_sha256, coordinate_frame, created_at, updated_at)
		VALUES ($1,$2,$3,$4,'requested',$5,$6::jsonb,$7,$8,$9,$10,$11,$12,$12)`, id, mapID, v.Version,
		vehicleID, action, string(compatibilityJSON), previousID, rollbackTargetID, requestedBy, v.ContentSHA256,
		strings.TrimSpace(coordinateFrame), now); err != nil {
		return nil, fmt.Errorf("创建回滚审批: %w", err)
	}
	return &mapPublication{ID: id, MapID: mapID, Version: v.Version, VehicleID: vehicleID,
		State: "requested", Action: action, Compatibility: map[string]any{"coordinate_frame": coordinateFrame},
		PreviousPublicationID: previousID, RollbackTargetID: rollbackTargetID, RequestedBy: requestedBy, ContentSHA256: v.ContentSHA256,
		CoordinateFrame: coordinateFrame, CreatedNS: now.UnixNano(), UpdatedNS: now.UnixNano()}, nil
}

func (ms *MapStore) ListMapPublications(ctx context.Context, vehicleID string) ([]mapPublication, error) {
	if ms == nil || ms.st == nil || ms.st.db == nil {
		return []mapPublication{}, nil
	}
	query := mapPublicationSelect
	args := []any{}
	if vehicleID != "" {
		query += ` WHERE vehicle_id=$1`
		args = append(args, vehicleID)
	}
	query += ` ORDER BY updated_at DESC LIMIT 200`
	rows, err := ms.st.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]mapPublication, 0)
	for rows.Next() {
		p, err := scanMapPublication(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (ms *MapStore) markMapPublicationDispatched(id string) {
	if ms == nil || ms.st == nil || ms.st.db == nil || id == "" {
		return
	}
	_, _ = ms.st.db.ExecContext(context.Background(), `UPDATE map_publications SET state='dispatched', updated_at=now()
		WHERE id=$1 AND state='approved'`, id)
}

func (s *State) HandleMapPublicationAck(vehicleID, gatewayID, sourceLink string, ack *platformv1.MapPublicationAck) error {
	if s == nil || s.db == nil || ack == nil || vehicleID == "" || gatewayID == "" ||
		ack.GetPublicationId() == "" || ack.GetMapId() == "" || ack.GetVehicleId() != vehicleID || ack.GetMapVersion() == 0 {
		return fmt.Errorf("MapPublicationAck 字段无效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var mapID, pubVehicle, pubState, action, previousID, rollbackTargetID string
	var version int
	err = tx.QueryRowContext(ctx, `SELECT map_id, vehicle_id, version, state, action, COALESCE(previous_publication_id,''), COALESCE(rollback_target_publication_id,'')
		FROM map_publications WHERE id=$1 FOR UPDATE`, ack.GetPublicationId()).Scan(&mapID, &pubVehicle, &version, &pubState, &action, &previousID, &rollbackTargetID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("地图发布不存在")
	}
	if err != nil {
		return err
	}
	if mapID != ack.GetMapId() || pubVehicle != vehicleID || version != int(ack.GetMapVersion()) {
		return fmt.Errorf("MapPublicationAck 身份或版本不一致")
	}
	result := ack.GetResult()
	nextState := pubState
	switch result {
	case platformv1.MapPublicationResult_MAP_PUBLICATION_RESULT_ACCEPTED:
		nextState = "confirmed"
	case platformv1.MapPublicationResult_MAP_PUBLICATION_RESULT_APPLIED:
		if pubState != "approved" && pubState != "dispatched" && pubState != "confirmed" && pubState != "active" {
			return fmt.Errorf("发布状态 %s 不允许车端应用确认", pubState)
		}
		nextState = "active"
		if action == "rollback" {
			// The rollback operation is an audit record; the historical target
			// publication is the one that represents the active map.
			nextState = "confirmed"
		}
	case platformv1.MapPublicationResult_MAP_PUBLICATION_RESULT_REJECTED,
		platformv1.MapPublicationResult_MAP_PUBLICATION_RESULT_FAILED:
		nextState = "rejected"
	default:
		return fmt.Errorf("MapPublicationAck result 无效")
	}
	now := time.Now().UTC()
	if result == platformv1.MapPublicationResult_MAP_PUBLICATION_RESULT_APPLIED {
		if action == "rollback" && previousID != "" {
			if rollbackTargetID == "" {
				return fmt.Errorf("回滚发布缺少目标发布记录")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE map_publications SET state='superseded', updated_at=now()
				WHERE vehicle_id=$1 AND state='active' AND id NOT IN ($2,$3)`, vehicleID, previousID, rollbackTargetID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE map_publications SET state='rolled_back', updated_at=now()
				WHERE id=$1 AND vehicle_id=$2 AND state='active'`, previousID, vehicleID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE map_publications SET state='active', updated_at=now()
				WHERE id=$1 AND vehicle_id=$2 AND state IN ('superseded','rolled_back','confirmed')`, rollbackTargetID, vehicleID); err != nil {
				return err
			}
		}
		if action != "rollback" {
			if _, err := tx.ExecContext(ctx, `UPDATE map_publications SET state='superseded', updated_at=now()
				WHERE vehicle_id=$1 AND state='active' AND id<>$2`, vehicleID, ack.GetPublicationId()); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE map_publications SET state=$1, vehicle_ack_result=$2,
		vehicle_ack_detail=$3, vehicle_ack_at=$4, updated_at=$4 WHERE id=$5`, nextState,
		result.String(), trimRune(ack.GetDetail(), 1000), now, ack.GetPublicationId()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if nextState == "active" {
		s.pushEvent("info", vehicleID, fmt.Sprintf("车端已应用地图发布 %s（v%d）", ack.GetPublicationId(), version), "veh")
	} else if nextState == "rejected" {
		s.pushEvent("critical", vehicleID, fmt.Sprintf("车端拒绝地图发布 %s：%s", ack.GetPublicationId(), ack.GetDetail()), "veh")
	}
	_ = sourceLink // source is included in the MQTT dedup ledger, not business state.
	return nil
}
