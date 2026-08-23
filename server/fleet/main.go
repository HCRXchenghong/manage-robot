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

	if err := startMQTT(mqttOptions{
		host: *mqttHost, port: *mqttPort,
		caFile: *caFile, certFile: *certFile, keyFile: *keyFile,
		clientID: *clientID,
	}, st); err != nil {
		log.Printf("[fleet] MQTT 启动失败: %v（HTTP 服务不受影响）", err)
	}

	log.Printf("[fleet] fleet-hub 监听 %s（REST /api/*、WS /ws/fleet、静态站 /）", *addr)
	if err := http.ListenAndServe(*addr, buildHandler(st, hub, pc)); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}
