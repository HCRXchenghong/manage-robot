package main

// state_ext.go：车辆手动注册 / 启动恢复分组与底盘 / 事件 30 天保留（等保三级日志留存）。

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// RegisterVehicle REST 手动注册车辆（超管/管理员）。注册后车辆离线，等遥测接入。
func (s *State) RegisterVehicle(id, vin, gatewayID, chassis, group string) error {
	id, vin, gatewayID, chassis, group = strings.TrimSpace(id), strings.TrimSpace(vin), strings.TrimSpace(gatewayID), strings.TrimSpace(chassis), strings.TrimSpace(group)
	if id == "" {
		return fmt.Errorf("车辆 ID 必填")
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("车辆注册需要 PostgreSQL 持久化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO vehicles(id, gateway_id, stack, vin, group_id, chassis) VALUES ($1,$2,'manual',$3,$4,$5) ON CONFLICT (id) DO UPDATE SET gateway_id=COALESCE(NULLIF($2,''), vehicles.gateway_id), group_id=COALESCE(NULLIF($4,''), vehicles.group_id), chassis=COALESCE(NULLIF($5,''), vehicles.chassis), vin=COALESCE(NULLIF($3,''), vehicles.vin)",
		id, nullable(gatewayID), nullable(vin), group, chassis); err != nil {
		return fmt.Errorf("车辆注册未持久化：%w", err)
	}
	s.mu.Lock()
	v := s.ensureVehicleLocked(id)
	if gatewayID != "" {
		v.gatewayID = gatewayID
	}
	if group != "" {
		v.group = group
	}
	if chassis != "" {
		v.chassis = chassis
	}
	s.mu.Unlock()
	s.pushEvent("info", id, fmt.Sprintf("车辆注册（底盘 %s，分组 %s）", chassis, group), "veh")
	return nil
}

// loadVehiclesFromDB 启动时恢复登记和最后一份真实遥测。在线状态始终从
// false 开始，只有当前进程收到新的车端遥测后才允许标记为在线。
func (s *State) loadVehiclesFromDB() {
	if s.db == nil {
		return
	}
	rows, err := s.db.Query(`SELECT v.id, COALESCE(v.gateway_id, ''), v.group_id, v.chassis,
		COALESCE(vs.mode, ''), COALESCE(vs.speed_mps, 0), COALESCE(vs.soc, 0), COALESCE(vs.voltage, 0), COALESCE(vs.gear, ''), COALESCE(vs.steer_rad, 0),
		COALESCE(vs.gps_fix, false), COALESCE(vs.gps_lat, 0), COALESCE(vs.gps_lon, 0), COALESCE(vs.gps_alt, 0),
		COALESCE(vs.pose_valid, false), COALESCE(vs.pose_frame, ''), COALESCE(vs.pose_x, 0), COALESCE(vs.pose_y, 0), COALESCE(vs.pose_yaw, 0)
		FROM vehicles v LEFT JOIN vehicle_state vs ON vs.vehicle_id = v.id`)
	if err != nil {
		return
	}
	defer rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var id, gatewayID, g, c, mode, gear, frame string
		var speed, soc, voltage, steer, gpsLat, gpsLon, gpsAlt, poseX, poseY, poseYaw float64
		var gpsFix, poseValid bool
		if err := rows.Scan(&id, &gatewayID, &g, &c, &mode, &speed, &soc, &voltage, &gear, &steer,
			&gpsFix, &gpsLat, &gpsLon, &gpsAlt, &poseValid, &frame, &poseX, &poseY, &poseYaw); err != nil {
			continue
		}
		v := s.ensureVehicleLocked(id)
		v.gatewayID = gatewayID
		v.group = g
		v.chassis = c
		v.mode, v.speedMPS, v.soc, v.voltage, v.gear, v.steerRad = mode, speed, soc, voltage, gear, steer
		v.gpsFix, v.gpsLat, v.gpsLon, v.gpsAlt = gpsFix, gpsLat, gpsLon, gpsAlt
		v.poseValid, v.poseFrame, v.poseX, v.poseY, v.poseYaw = poseValid, frame, poseX, poseY, poseYaw
		v.poseValiditySet = true
	}
}

// retentionLoop 每小时清理 > 30 天的事件（等保三级日志留存策略：告警页保留一个月）。
func (s *State) retentionLoop() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		s.submitDB(func(ctx context.Context, db *sql.DB) {
			res, err := db.ExecContext(ctx, "DELETE FROM events WHERE ts < now() - interval '30 days'")
			if err != nil {
				return
			}
			if n, _ := res.RowsAffected(); n > 0 {
				log.Printf("[retention] 清理 %d 条超过 30 天的事件", n)
			}
		})
		s.submitDB(func(ctx context.Context, db *sql.DB) {
			_, _ = db.ExecContext(ctx, "DELETE FROM inbound_message_dedup WHERE received_at < now() - interval '15 minutes'")
		})
		s.submitDB(func(ctx context.Context, db *sql.DB) {
			_, _ = db.ExecContext(ctx, "DELETE FROM inbound_message_cursors WHERE last_received_at < now() - interval '24 hours'")
		})
	}
}
