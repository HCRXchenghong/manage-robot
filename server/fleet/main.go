package main

// main.go：fleet-hub 入口（第 10 步运营大屏云端服务）。
// 职责：MQTT(mTLS) 接入车端数据 -> 内存状态模型（+ PostgreSQL 持久化）
//      -> REST /api/* + WebSocket /ws/fleet + 内嵌前端静态站。
// 用法：
//   go build -o fleet-hub . && ./fleet-hub -dsn <pg> -ca <ca.pem> -cert <client.crt> -key <client.key>
// PostgreSQL 是生产真相源，启动失败即退出。MQTT 可以在服务启动后恢复，
// 但运行状态会明确暴露为未就绪，绝不伪造车辆或遥测。

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	addr := flag.String("addr", ":9800", "HTTP 监听地址")
	dsn := flag.String("dsn", "postgres://robot:robot@127.0.0.1:5433/robot?sslmode=disable",
		"PostgreSQL DSN（生产必须可连接）")
	allowVolatile := flag.Bool("allow-volatile", false, "仅本地诊断：允许 PostgreSQL 不可用；不得用于生产")
	migrations := flag.String("migrations", "server/migrations", "SQL 迁移目录")
	mqttHost := flag.String("mqtt-host", "localhost", "MQTT broker 主机")
	mqttPort := flag.Int("mqtt-port", 8883, "MQTT broker 端口（mTLS）")
	mqttDisabled := flag.Bool("disable-mqtt", false, "仅本地诊断：显式关闭 MQTT；运行状态保持未就绪")
	caFile := flag.String("ca", "", "MQTT Broker CA 证书（生产必填）")
	certFile := flag.String("cert", "", "Fleet MQTT 客户端证书（生产必填）")
	keyFile := flag.String("key", "", "Fleet MQTT 客户端私钥（生产必填）")
	clientID := flag.String("client-id", fmt.Sprintf("fleet-hub-%d", time.Now().Unix()%100000),
		"MQTT ClientID（必须唯一）")
	leaseSigningKey := flag.String("lease-signing-key", "", "Authority Ed25519 私钥（仅测试文件；生产应接 HSM/KMS）")
	envelopeAuthKeyFile := flag.String("envelope-auth-key", "", "Envelope HMAC-SHA256 密钥文件（0600、base64；Fleet/Gateway/Adapter 必须一致）")
	leaseSeconds := flag.Duration("lease-ttl", defaultLeaseTTL, "商用逻辑租约有效期；正常续租不更换 lease/fencing")
	leaseGrace := flag.Duration("lease-renewal-grace", defaultLeaseGrace, "续租超时宽限；命令看门狗仍由车端独立执行")
	mapsDir := flag.String("maps-dir", "data/maps", "地图仓库目录（服务器侧地图存储）")
	pyBin := flag.String("python", ".venv/bin/python", "Python 解释器（3D→2D 转换脚本用）")
	mapConvert := flag.String("map-convert", "", "3D→2D 地图引擎脚本；留空时从仓库路径解析")
	bootstrapAddr := flag.String("gateway-bootstrap-addr", "", "Gateway mTLS 激活监听地址；生产必须独立于浏览器入口")
	bootstrapCert := flag.String("gateway-bootstrap-cert", "", "Gateway mTLS 激活服务端证书")
	bootstrapKey := flag.String("gateway-bootstrap-key", "", "Gateway mTLS 激活服务端私钥")
	bootstrapCA := flag.String("gateway-bootstrap-client-ca", "", "允许 Gateway mTLS 证书的 CA")
	flag.Parse()
	if *mapConvert == "" {
		for _, candidate := range []string{"map-engine/tools/map_convert.py", "../../map-engine/tools/map_convert.py"} {
			if _, err := os.Stat(candidate); err == nil {
				*mapConvert, _ = filepath.Abs(candidate)
				break
			}
		}
	}

	hub := NewHub()

	db, err := OpenDB(*dsn)
	if err != nil {
		if !*allowVolatile {
			log.Fatalf("[fleet] PostgreSQL 不可用：%v；生产启动已拒绝（本地只读诊断可显式传 --allow-volatile）", err)
		}
		log.Printf("[fleet] 警告：PostgreSQL 不可用（%v）；当前为显式 volatile 诊断模式，不可用于生产", err)
	} else if err := RunMigrations(db, *migrations); err != nil {
		_ = db.Close()
		log.Fatalf("[fleet] 迁移失败，拒绝以未知 schema 启动：%v", err)
	} else {
		defer db.Close()
		log.Printf("[fleet] PostgreSQL 就绪，迁移已幂等执行（%s）", *migrations)
	}

	var envelopeAuthKey []byte
	if *envelopeAuthKeyFile != "" {
		envelopeAuthKey, err = loadSymmetricKey(*envelopeAuthKeyFile)
		if err != nil {
			log.Fatalf("[fleet] Envelope 认证密钥不可用：%v", err)
		}
	}
	st := NewState(hub, db, envelopeAuthKey)
	st.setMetricsToken(os.Getenv("ROBOT_AGENT_METRICS_TOKEN"))

	maps, err := NewMapStore(*mapsDir, *pyBin, *mapConvert, hub, st)
	if err != nil {
		log.Fatalf("地图仓库初始化失败: %v", err)
	}
	cfg := NewConfigStore(db)
	nav := NewNavStore(st)
	open := NewOpenAPI(st, cfg, nav)
	groups := NewGroupStore(db)
	auth := NewAuthStore(groups, st, open)
	if user, password := os.Getenv("RA_BOOTSTRAP_ADMIN"), os.Getenv("RA_BOOTSTRAP_PASSWORD"); user != "" || password != "" {
		if user == "" || password == "" {
			log.Fatal("[fleet] RA_BOOTSTRAP_ADMIN 与 RA_BOOTSTRAP_PASSWORD 必须同时设置")
		}
		if err := PasswordOK(password); err != nil {
			log.Fatalf("[fleet] bootstrap 管理员密码不符合策略：%v", err)
		}
		auth.SeedUser(user, "初始管理员", password, "super", nil)
		log.Printf("[fleet] 已从环境变量初始化超级管理员 %q；密码不会输出到日志", user)
	} else {
		log.Printf("[fleet] 未配置 bootstrap 管理员；请经受控运维流程创建首个账号")
	}
	devices := NewDeviceStore(db)
	tkreg := NewTakeoverReg(db)
	registry := NewGatewayRegistry(db)
	var signer *leaseSigner
	if *leaseSigningKey != "" {
		signer, err = loadLeaseSigner(*leaseSigningKey)
		if err != nil {
			log.Fatalf("[fleet] 商用 Authority 签名密钥不可用：%v", err)
		}
	}
	durableAuthority := NewDurableAuthority(db, st, signer, envelopeAuthKey, *leaseSeconds, *leaseGrace)
	open.SetAuthority(durableAuthority)
	if err := durableAuthority.ready(); err != nil {
		log.Printf("[fleet] 商用远程接管未启用：%v；车辆监控仍可用，真实控制默认拒绝", err)
	} else {
		log.Printf("[fleet] 商用 Control Authority 就绪：lease=%s grace=%s key=%s", *leaseSeconds, *leaseGrace, signer.keyID)
	}
	svc := &Services{maps: maps, cfg: cfg, nav: nav, open: open, auth: auth, groups: groups, devices: devices, tkreg: tkreg, registry: registry, authority: durableAuthority, st: st}

	if *mqttDisabled {
		log.Printf("[fleet] MQTT 已由 --disable-mqtt 显式关闭；当前实例不会接收车辆数据")
	} else if err := startMQTT(mqttOptions{
		host: *mqttHost, port: *mqttPort,
		caFile: *caFile, certFile: *certFile, keyFile: *keyFile,
		clientID:        *clientID,
		envelopeAuthKey: envelopeAuthKey,
	}, st, registry); err != nil {
		log.Printf("[fleet] MQTT 启动失败: %v（HTTP 服务不受影响，但 readyz 保持未就绪）", err)
	}
	if err := startGatewayBootstrap(*bootstrapAddr, *bootstrapCert, *bootstrapKey, *bootstrapCA, registry, st); err != nil {
		log.Fatalf("[fleet] Gateway bootstrap 启动失败：%v", err)
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           buildHandler(st, hub, svc),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("[fleet] fleet-hub 监听 %s（REST /api/*、WS /ws/fleet、静态站 /）", *addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("HTTP 服务退出: %v", err)
	}
}
