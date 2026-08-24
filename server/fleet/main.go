package main

// main.go：fleet-hub 入口（第 10 步运营大屏云端服务）。
// 职责：MQTT(mTLS) 接入车端数据 -> 内存状态模型（+ PostgreSQL 持久化）
//      -> REST /api/* + WebSocket /ws/fleet + 内嵌前端静态站。
// 用法：
//   go build -o fleet-hub . && ./fleet-hub
// 降级策略：PG 连不上 -> 纯内存模式（大屏可用，不落库）；
//          MQTT 连不上 -> 后台持续重试，HTTP 照常服务。

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", ":9800", "HTTP 监听地址")
	dsn := flag.String("dsn", "postgres://robot:robot@127.0.0.1:5433/robot?sslmode=disable",
		"PostgreSQL DSN（连不上自动降级纯内存模式）")
	migrations := flag.String("migrations", "server/migrations", "SQL 迁移目录")
	mqttHost := flag.String("mqtt-host", "localhost", "MQTT broker 主机")
	mqttPort := flag.Int("mqtt-port", 8883, "MQTT broker 端口（mTLS）")
	caFile := flag.String("ca", "deploy/pki/dev/ca.crt", "CA 证书")
	certFile := flag.String("cert", "deploy/pki/dev/access.crt", "云端 access 证书")
	keyFile := flag.String("key", "deploy/pki/dev/access.key", "云端 access 私钥")
	clientID := flag.String("client-id", fmt.Sprintf("fleet-hub-%d", time.Now().Unix()%100000),
		"MQTT ClientID（必须唯一）")
	authority := flag.String("authority", "127.0.0.1:9300", "control-authority UDP 地址（op=status 只读）")
	mapsDir := flag.String("maps-dir", "data/maps", "地图仓库目录（服务器侧地图存储）")
	pyBin := flag.String("python", ".venv/bin/python", "Python 解释器（3D→2D 转换脚本用）")
	mapConvert := flag.String("map-convert", "deploy/demo/map_convert.py", "3D→2D 一行命令脚本")
	flag.Parse()

	hub := NewHub()

	db, err := OpenDB(*dsn)
	if err != nil {
		log.Printf("[fleet] PostgreSQL 不可用（%v）——降级纯内存模式", err)
	} else if err := RunMigrations(db, *migrations); err != nil {
		log.Printf("[fleet] 迁移失败: %v（继续以当前库状态启动）", err)
	} else {
		log.Printf("[fleet] PostgreSQL 就绪，迁移已幂等执行（%s）", *migrations)
	}

	st := NewState(hub, db, *authority)
	pc := newPointCloudGen()

	maps, err := NewMapStore(*mapsDir, *pyBin, *mapConvert, hub, st)
	if err != nil {
		log.Fatalf("地图仓库初始化失败: %v", err)
	}
	cfg := NewConfigStore()
	nav := NewNavStore(st)
	open := NewOpenAPI(st, cfg, nav)
	groups := NewGroupStore()
	auth := NewAuthStore(groups, st, open)
	// 演示账号（首次启动注入；等保：密码须复杂度达标，登录有验证码与锁定策略）
	auth.SeedUser("superadmin", "超级管理员", "Super@2026", "super", nil)
	auth.SeedUser("boss_a", "甲方负责人", "Boss@2026", "group_admin", []string{"g-2"})
	auth.SeedUser("ops_a", "运维调度员", "Ops@2026", "user", []string{"g-2"})
	auth.SetPhone("superadmin", "13800000000")
	auth.SetPhone("boss_a", "13800000001")
	auth.SetPhone("ops_a", "13800000002")
	log.Printf("[fleet] 演示账号：superadmin/Super@2026（超管）· boss_a/Boss@2026（分组管理员）· ops_a/Ops@2026（用户）")
	svc := &Services{maps: maps, cfg: cfg, nav: nav, open: open, auth: auth, groups: groups}

	if err := startMQTT(mqttOptions{
		host: *mqttHost, port: *mqttPort,
		caFile: *caFile, certFile: *certFile, keyFile: *keyFile,
		clientID: *clientID,
	}, st); err != nil {
		log.Printf("[fleet] MQTT 启动失败: %v（HTTP 服务不受影响）", err)
	}

	log.Printf("[fleet] fleet-hub 监听 %s（REST /api/*、WS /ws/fleet、静态站 /）", *addr)
	if err := http.ListenAndServe(*addr, buildHandler(st, hub, pc, svc)); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}
