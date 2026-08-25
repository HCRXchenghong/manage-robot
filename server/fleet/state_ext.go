package main

// state_ext.go：车辆手动注册 / 启动恢复分组与底盘 / 事件 30 天保留（等保三级日志留存）。

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// RegisterVehicle REST 手动注册车辆（超管/管理员）。注册后车辆离线，等遥测接入。
func (s *State) RegisterVehicle(id, vin, gatewayID, chassis, group string) error {
	if id == "" {
		return fmt.Errorf("车辆 ID 必填")
	}
	s.mu.Lock()
	v := s.ensureVehicleLocked(id)
	if group != "" {
		v.group = group
	}
	if chassis != "" {
		v.chassis = chassis
	}
	s.mu.Unlock()
	s.pushEvent("info", id, fmt.Sprintf("车辆注册（底盘 %s，分组 %s）", chassis, group))
	s.submitDB(func(ctx context.Context, db *sql.DB) {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO vehicles(id, gateway_id, stack, vin, group_id, chassis) VALUES ($1,$2,'manual',$3,$4,$5) ON CONFLICT (id) DO UPDATE SET group_id=COALESCE(NULLIF($4,''), vehicles.group_id), chassis=COALESCE(NULLIF($5,''), vehicles.chassis), vin=COALESCE(NULLIF($3,''), vehicles.vin)",
			id, nullable(gatewayID), nullable(vin), group, chassis); err != nil {
			log.Printf("vehicles 入库失败: %v", err)
		}
	})
	return nil
}

// loadVehiclesFromDB 启动时恢复分组/底盘，保证分组隔离重启不丢。
func (s *State) loadVehiclesFromDB() {
	if s.db == nil {
		return
	}
	rows, err := s.db.Query("SELECT id, group_id, chassis FROM vehicles")
	if err != nil {
		return
	}
	defer rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var id, g, c string
		if err := rows.Scan(&id, &g, &c); err != nil {
			continue
		}
		v := s.ensureVehicleLocked(id)
		v.group = g
		v.chassis = c
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
	}
}
