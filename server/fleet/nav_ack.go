package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	platformv1 "robot-agent/protocols/platform/v1"
)

// navigationAckTransition is deliberately strict. A vehicle ACK is an
// authenticated fact, but it is not allowed to move a route backwards or to
// turn a cancellation request into a successful cancellation without the
// vehicle saying so.
func navigationAckTransition(current string, action platformv1.NavigationAction, result platformv1.NavigationResult) (string, error) {
	switch action {
	case platformv1.NavigationAction_NAVIGATION_ACTION_DISPATCH:
		switch result {
		case platformv1.NavigationResult_NAVIGATION_RESULT_ACCEPTED:
			switch current {
			case "queued", "dispatched", "accepted":
				return "accepted", nil
			default:
				return "", fmt.Errorf("状态 %s 不允许车端接受导航任务", current)
			}
		case platformv1.NavigationResult_NAVIGATION_RESULT_STARTED:
			switch current {
			case "queued", "dispatched", "accepted", "running":
				return "running", nil
			default:
				return "", fmt.Errorf("状态 %s 不允许车端开始导航任务", current)
			}
		case platformv1.NavigationResult_NAVIGATION_RESULT_COMPLETED:
			switch current {
			case "queued", "dispatched", "accepted", "running", "completed":
				return "completed", nil
			default:
				return "", fmt.Errorf("状态 %s 不允许车端完成导航任务", current)
			}
		case platformv1.NavigationResult_NAVIGATION_RESULT_REJECTED:
			switch current {
			case "queued", "dispatched", "accepted", "running", "rejected":
				return "rejected", nil
			default:
				return "", fmt.Errorf("状态 %s 不允许车端拒绝导航任务", current)
			}
		case platformv1.NavigationResult_NAVIGATION_RESULT_FAILED:
			switch current {
			case "queued", "dispatched", "accepted", "running", "failed":
				return "failed", nil
			default:
				return "", fmt.Errorf("状态 %s 不允许车端报告导航失败", current)
			}
		}
	case platformv1.NavigationAction_NAVIGATION_ACTION_CANCEL:
		switch result {
		case platformv1.NavigationResult_NAVIGATION_RESULT_ACCEPTED:
			if current == "cancel_requested" {
				return "cancel_requested", nil
			}
		case platformv1.NavigationResult_NAVIGATION_RESULT_CANCELLED:
			if current == "cancel_requested" || current == "cancelled" {
				return "cancelled", nil
			}
		case platformv1.NavigationResult_NAVIGATION_RESULT_REJECTED,
			platformv1.NavigationResult_NAVIGATION_RESULT_FAILED:
			if current == "cancel_requested" || current == "cancel_rejected" {
				return "cancel_rejected", nil
			}
		}
	}
	return "", fmt.Errorf("导航 ACK action/result 不允许：action=%s result=%s status=%s",
		action.String(), result.String(), current)
}

// HandleNavigationAck atomically advances a route and consumes the inbound
// sequence. The cursor, dedup ledger and route projection are committed or
// rolled back together; a retry after a database failure therefore remains
// possible and a late MQTT callback cannot overwrite newer state.
func (s *State) HandleNavigationAck(ctx context.Context, registry *GatewayRegistry,
	vehicleID, gatewayID, sourceLink, sessionID string, sequence uint64,
	ack *platformv1.NavigationAck) (bool, error) {
	if s == nil || s.db == nil || registry == nil || ack == nil || vehicleID == "" ||
		gatewayID == "" || sourceLink == "" || sessionID == "" || sequence == 0 ||
		ack.GetVersion() != 1 || ack.GetVehicleId() != vehicleID || ack.GetRouteId() == "" ||
		ack.GetAction() == platformv1.NavigationAction_NAVIGATION_ACTION_UNSPECIFIED ||
		ack.GetResult() == platformv1.NavigationResult_NAVIGATION_RESULT_UNSPECIFIED {
		return false, fmt.Errorf("NavigationAck 持久化字段无效")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, fmt.Errorf("开启导航 ACK 事务: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var routeVehicle, current, pointsJSON string
	err = tx.QueryRowContext(ctx, `SELECT vehicle_id, status, points::text
		FROM navigation_routes WHERE id=$1 FOR UPDATE`, ack.GetRouteId()).
		Scan(&routeVehicle, &current, &pointsJSON)
	if err == sql.ErrNoRows {
		return false, fmt.Errorf("导航任务不存在")
	}
	if err != nil {
		return false, fmt.Errorf("读取导航任务: %w", err)
	}
	if routeVehicle != vehicleID {
		return false, fmt.Errorf("NavigationAck 车辆与任务不一致")
	}
	var points []NavPoint
	if err := json.Unmarshal([]byte(pointsJSON), &points); err != nil || len(points) < 2 {
		return false, fmt.Errorf("导航任务路点记录无效")
	}
	if int(ack.GetCurrentPoint()) > len(points) {
		return false, fmt.Errorf("NavigationAck current_point 超出路点范围")
	}
	next, err := navigationAckTransition(current, ack.GetAction(), ack.GetResult())
	if err != nil {
		return false, err
	}
	accepted, err := registry.AcceptInboundSequenceTx(ctx, tx, vehicleID, gatewayID,
		"platform.v1.NavigationAck", sessionID, sequence)
	if err != nil {
		return false, err
	}
	if !accepted {
		return false, nil
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE navigation_routes SET status=$1,
		vehicle_ack_result=$2, vehicle_ack_detail=$3, vehicle_ack_current_point=$4,
		vehicle_ack_at=$5, updated_at=$5 WHERE id=$6`, next, ack.GetResult().String(),
		trimRune(ack.GetDetail(), 1000), int(ack.GetCurrentPoint()), now, ack.GetRouteId()); err != nil {
		return false, fmt.Errorf("写入导航 ACK 投影: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("提交导航 ACK 事务: %w", err)
	}

	s.mu.RLock()
	ns := s.navStore
	s.mu.RUnlock()
	if ns != nil {
		ns.mu.Lock()
		if route := ns.routes[ack.GetRouteId()]; route != nil {
			route.Status = next
			route.VehicleAckResult = ack.GetResult().String()
			route.VehicleAckDetail = trimRune(ack.GetDetail(), 1000)
			route.CurrentPoint = int(ack.GetCurrentPoint())
			route.VehicleAckNS = now.UnixNano()
			route.UpdatedNS = now.UnixNano()
		}
		ns.mu.Unlock()
	}
	level := "info"
	switch next {
	case "cancelled":
		level = "warn"
	case "rejected", "failed", "cancel_rejected":
		level = "critical"
	}
	s.pushEvent(level, vehicleID, fmt.Sprintf("车端导航任务 %s 状态变更为 %s：%s",
		ack.GetRouteId(), next, trimRune(ack.GetDetail(), 240)), "veh")
	return true, nil
}
