package main

// mqtt.go：车云 MQTT 接入（mTLS，订阅受绑定 Gateway namespace）。
// Broker 以客户端证书身份鉴权（use_identity_as_username）并按 Gateway ACL
// 限制 topic；Fleet-hub 再与 PostgreSQL registry 交叉校验。

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"
	platformv1 "robot-agent/protocols/platform/v1"
)

type mqttOptions struct {
	host            string
	port            int
	caFile          string
	certFile        string
	keyFile         string
	clientID        string
	envelopeAuthKey []byte
}

// startMQTT 后台连接并订阅；首连失败不阻塞启动（ConnectRetry 会持续重试）。
func startMQTT(o mqttOptions, st *State, registry *GatewayRegistry) error {
	if len(o.envelopeAuthKey) < 32 {
		return fmt.Errorf("MQTT 生产链路必须配置至少 256 位 Envelope HMAC 密钥")
	}
	tlsCfg, err := loadTLS(o.caFile, o.certFile, o.keyFile)
	if err != nil {
		return err
	}
	broker := fmt.Sprintf("tls://%s:%d", o.host, o.port)
	opts := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(o.clientID).
		SetTLSConfig(tlsCfg).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetOrderMatters(false). // 回调互不阻塞（paho 推荐配置）
		SetKeepAlive(15 * time.Second)
	opts.OnConnect = func(c mqtt.Client) {
		st.SetMQTTPub(c) // 开放下行发布（循迹导航等）
		if t := c.Subscribe("gateway/+/vehicle/+/+", 1, nil); t.Wait() && t.Error() != nil {
			log.Printf("MQTT 订阅 Gateway 上行失败: %v", t.Error())
			return
		}
		log.Printf("MQTT 已就绪：%s，订阅受绑定 Gateway 上行", broker)
	}
	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		log.Printf("MQTT 连接丢失: %v（自动重连）", err)
	}
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
		routeMQTT(st, registry, m.Topic(), m.Payload(), o.envelopeAuthKey)
	})
	mqtt.NewClient(opts).Connect()
	log.Printf("MQTT 连接中：%s（后台重试，就绪后自动订阅）", broker)
	return nil
}

func loadTLS(caFile, certFile, keyFile string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("读 CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("解析 CA %s 失败", caFile)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("读客户端证书: %w", err)
	}
	return &tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func decodeMQTTEnvelope(topic string, payload, envelopeAuthKey []byte) (*platformv1.Envelope, string, string, string, error) {
	parts := strings.Split(topic, "/")
	if len(parts) != 5 || parts[0] != "gateway" || parts[2] != "vehicle" {
		return nil, "", "", "", fmt.Errorf("MQTT topic 格式无效")
	}
	gatewayID, id, kind := parts[1], parts[3], parts[4]
	if gatewayID == "" || id == "" || kind == "" || len(payload) == 0 || len(payload) > platformv1.MaxEnvelopePayload+4096 {
		return nil, "", "", "", fmt.Errorf("MQTT 消息大小或身份无效")
	}
	env := &platformv1.Envelope{}
	if err := proto.Unmarshal(payload, env); err != nil {
		return nil, "", "", "", fmt.Errorf("Envelope 解析失败: %w", err)
	}
	if env.GetVehicleId() != id || env.GetGatewayId() != gatewayID {
		return nil, "", "", "", fmt.Errorf("topic 与 Envelope 身份不一致")
	}
	if err := platformv1.ValidateEnvelope(env, "", id, gatewayID, time.Now()); err != nil {
		return nil, "", "", "", fmt.Errorf("Envelope 校验失败: %w", err)
	}
	if !platformv1.VerifyEnvelopeAuth(env, envelopeAuthKey) {
		return nil, "", "", "", fmt.Errorf("Envelope.auth_tag 校验失败")
	}
	switch kind {
	case "register":
		if env.GetMessageType() != "platform.v1.GatewayCapabilities" {
			return nil, "", "", "", fmt.Errorf("register 消息类型无效")
		}
	case "telemetry":
		if env.GetMessageType() != "platform.v1.SignalUpdate" {
			return nil, "", "", "", fmt.Errorf("telemetry 消息类型无效")
		}
	case "status":
		if env.GetMessageType() != "platform.v1.GatewayStatus" && env.GetMessageType() != "platform.v1.Heartbeat" {
			return nil, "", "", "", fmt.Errorf("status 消息类型无效")
		}
	case "ack":
		if env.GetMessageType() != "platform.v1.ControlAck" {
			return nil, "", "", "", fmt.Errorf("ack 消息类型无效")
		}
	case "nav_ack":
		if env.GetMessageType() != "platform.v1.NavigationAck" {
			return nil, "", "", "", fmt.Errorf("nav_ack 消息类型无效")
		}
	case "map_ack":
		if env.GetMessageType() != "platform.v1.MapPublicationAck" {
			return nil, "", "", "", fmt.Errorf("map_ack 消息类型无效")
		}
	default:
		return nil, "", "", "", fmt.Errorf("未知 MQTT 消息类别")
	}
	return env, gatewayID, id, kind, nil
}

func mqttTopicIdentity(topic string) (string, string) {
	parts := strings.Split(topic, "/")
	if len(parts) != 5 || parts[0] != "gateway" || parts[2] != "vehicle" {
		return "", ""
	}
	return parts[1], parts[3]
}

// recordMQTTDeadLetter keeps bounded rejection evidence without retaining the
// original message. Only the broker-authenticated gateway namespace and the
// payload digest are persisted, so this table cannot be used as a replay queue.
func recordMQTTDeadLetter(st *State, topic string, payload []byte, env *platformv1.Envelope, reason string) {
	if st == nil || st.db == nil {
		return
	}
	gatewayID, vehicleID := mqttTopicIdentity(topic)
	messageType, sessionID := "", ""
	var sequence uint64
	if env != nil {
		if gatewayID == "" {
			gatewayID = env.GetGatewayId()
		}
		if vehicleID == "" {
			vehicleID = env.GetVehicleId()
		}
		messageType, sessionID, sequence = env.GetMessageType(), env.GetSessionId(), env.GetSequence()
	}
	if len(topic) > 512 {
		topic = topic[:512]
	}
	if len(reason) > 512 {
		reason = reason[:512]
	}
	digest := sha256.Sum256(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := st.db.ExecContext(ctx, `INSERT INTO mqtt_ingress_dead_letters
		(topic, gateway_id, vehicle_id, message_type, session_id, sequence, reason, payload_sha256, payload_size)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, topic, gatewayID, vehicleID, messageType, sessionID, sequence,
		reason, hex.EncodeToString(digest[:]), len(payload)); err != nil {
		log.Printf("[mqtt] 写入死信证据失败：%v", err)
		return
	}
	if st.metrics != nil {
		st.metrics.mqttDeadLetters.Add(1)
	}
}

// routeMQTT 只处理由 Broker ACL 约束的
// gateway/{gateway_id}/vehicle/{vehicle_id}/{kind}。Topic、Envelope 和注册中心
// 三个身份必须一致；未激活 Gateway 不得借“自动建车”进入车队。
func routeMQTT(st *State, registry *GatewayRegistry, topic string, payload []byte, envelopeAuthKey []byte) {
	processed := false
	defer func() {
		if st == nil || st.metrics == nil {
			return
		}
		if processed {
			st.metrics.mqttAccepted.Add(1)
		} else {
			st.metrics.mqttRejected.Add(1)
		}
	}()
	env, gatewayID, id, kind, err := decodeMQTTEnvelope(topic, payload, envelopeAuthKey)
	if err != nil {
		recordMQTTDeadLetter(st, topic, payload, env, err.Error())
		log.Printf("拒绝 MQTT %s：%v", topic, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := registry.ValidateInbound(ctx, id, gatewayID); err != nil {
		recordMQTTDeadLetter(st, topic, payload, env, err.Error())
		log.Printf("拒绝 MQTT %s：%v", topic, err)
		return
	}
	// Parse the typed payload before claiming the deduplication slot. A malformed
	// packet must be observable as a dead letter and must remain retryable only
	// after the sender fixes it; it must never consume a valid sequence number.
	var caps *platformv1.GatewayCapabilities
	var su *platformv1.SignalUpdate
	var ack *platformv1.ControlAck
	var navAck *platformv1.NavigationAck
	var mapAck *platformv1.MapPublicationAck
	var status proto.Message
	switch kind {
	case "register":
		caps = &platformv1.GatewayCapabilities{}
		if err := proto.Unmarshal(env.GetPayload(), caps); err != nil {
			recordMQTTDeadLetter(st, topic, payload, env, "register payload 解析失败: "+err.Error())
			log.Printf("register payload 解析失败: %v", err)
			return
		}
		if caps.GetVehicleId() != id {
			recordMQTTDeadLetter(st, topic, payload, env, "车辆能力声明身份不一致")
			log.Printf("拒绝车辆能力声明身份不一致")
			return
		}
		decision, err := platformv1.AssessGatewayCapabilities(caps)
		if err != nil {
			recordMQTTDeadLetter(st, topic, payload, env, "Gateway 能力声明无效: "+err.Error())
			log.Printf("拒绝无效 Gateway 能力声明：%v", err)
			return
		}
		if err := registry.PersistCapabilities(ctx, gatewayID, caps, decision); err != nil {
			recordMQTTDeadLetter(st, topic, payload, env, "Gateway 能力快照持久化失败: "+err.Error())
			log.Printf("Gateway 能力快照持久化失败：%v", err)
			return
		}
		if !decision.MonitoringAllowed {
			recordMQTTDeadLetter(st, topic, payload, env, "Gateway 能力不兼容，不能进入监控面: "+decision.ControlReason)
			log.Printf("拒绝不兼容 Gateway 能力声明：%s", decision.ControlReason)
			return
		}
	case "telemetry":
		su = &platformv1.SignalUpdate{}
		if err := proto.Unmarshal(env.GetPayload(), su); err != nil {
			recordMQTTDeadLetter(st, topic, payload, env, "telemetry payload 解析失败: "+err.Error())
			log.Printf("telemetry payload 解析失败: %v", err)
			return
		}
		if err := platformv1.ValidateSignalUpdate(su, time.Now()); err != nil {
			recordMQTTDeadLetter(st, topic, payload, env, "telemetry 信号校验失败: "+err.Error())
			log.Printf("拒绝无效 telemetry：%v", err)
			return
		}
	case "status":
		if env.GetMessageType() == "platform.v1.GatewayStatus" {
			status = &platformv1.GatewayStatus{}
		} else {
			status = &platformv1.Heartbeat{}
		}
		if err := proto.Unmarshal(env.GetPayload(), status); err != nil {
			recordMQTTDeadLetter(st, topic, payload, env, "Gateway status payload 解析失败: "+err.Error())
			log.Printf("Gateway status payload 解析失败: %v", err)
			return
		}
	case "ack":
		ack = &platformv1.ControlAck{}
		if err := proto.Unmarshal(env.GetPayload(), ack); err != nil ||
			ack.GetControlSessionId() != env.GetSessionId() ||
			ack.GetCommandSequence() != env.GetSequence() {
			recordMQTTDeadLetter(st, topic, payload, env, "ControlAck 与 Envelope 不一致或 payload 无法解析")
			log.Printf("拒绝非法 ControlAck：Envelope 与载荷不一致")
			return
		}
	case "nav_ack":
		navAck = &platformv1.NavigationAck{}
		if err := proto.Unmarshal(env.GetPayload(), navAck); err != nil ||
			navAck.GetVehicleId() != id || navAck.GetRouteId() == "" ||
			navAck.GetVersion() != 1 || navAck.GetAction() == platformv1.NavigationAction_NAVIGATION_ACTION_UNSPECIFIED ||
			navAck.GetResult() == platformv1.NavigationResult_NAVIGATION_RESULT_UNSPECIFIED {
			recordMQTTDeadLetter(st, topic, payload, env, "NavigationAck 字段无效或身份不一致")
			log.Printf("拒绝非法 NavigationAck")
			return
		}
	case "map_ack":
		mapAck = &platformv1.MapPublicationAck{}
		if err := proto.Unmarshal(env.GetPayload(), mapAck); err != nil ||
			mapAck.GetVehicleId() != id || mapAck.GetPublicationId() == "" ||
			mapAck.GetMapId() == "" || mapAck.GetMapVersion() == 0 {
			recordMQTTDeadLetter(st, topic, payload, env, "MapPublicationAck 字段无效或身份不一致")
			log.Printf("拒绝非法 MapPublicationAck")
			return
		}
	}
	if kind != "register" {
		if err := registry.RequireMonitoring(ctx, id, gatewayID); err != nil {
			recordMQTTDeadLetter(st, topic, payload, env, err.Error())
			log.Printf("拒绝未通过监控能力准入的 MQTT %s：%v", topic, err)
			return
		}
	}
	// ACK persistence is idempotent on (session, sequence, link). Do it before
	// claiming the dedup slot so a database error cannot consume the only retry
	// opportunity for a vehicle execution result. A crash between these two
	// writes is safe: the second ACK insert is a no-op on retry.
	if ack != nil {
		if err := st.HandleControlAck(id, gatewayID, "mqtt", ack); err != nil {
			recordMQTTDeadLetter(st, topic, payload, env, "ControlAck 持久化失败: "+err.Error())
			log.Printf("ControlAck 持久化失败: %v", err)
			return
		}
	}
	if mapAck != nil {
		if err := st.HandleMapPublicationAck(id, gatewayID, "mqtt", mapAck); err != nil {
			recordMQTTDeadLetter(st, topic, payload, env, "MapPublicationAck 持久化失败: "+err.Error())
			log.Printf("MapPublicationAck 持久化失败: %v", err)
			return
		}
	}
	// Navigation ACK owns its ordering gate and route state transition in one
	// transaction. This prevents a late ACK from advancing a route after a
	// newer ACK has already been accepted, and prevents a DB failure from
	// consuming the MQTT deduplication slot.
	if navAck != nil {
		accepted, err := st.HandleNavigationAck(ctx, registry, id, gatewayID, "mqtt",
			env.GetSessionId(), env.GetSequence(), navAck)
		if err != nil {
			recordMQTTDeadLetter(st, topic, payload, env, "NavigationAck 持久化失败: "+err.Error())
			log.Printf("NavigationAck 持久化失败: %v", err)
			return
		}
		if !accepted {
			log.Printf("忽略 MQTT 重复或过期顺序 NavigationAck %s session=%s seq=%d", topic, env.GetSessionId(), env.GetSequence())
			return
		}
		processed = true
		return
	}
	if kind == "telemetry" {
		sigs := make([]signalVal, 0, len(su.GetSignals()))
		for _, signal := range su.GetSignals() {
			var num *float64
			var txt *string
			if value := signal.GetValue(); value != nil {
				switch value.GetKind().(type) {
				case *platformv1.Value_Number:
					v := value.GetNumber()
					num = &v
				case *platformv1.Value_Text:
					v := value.GetText()
					txt = &v
				}
			}
			var sampleAt time.Time
			if signal.GetSampleUtcNs() > 0 {
				sampleAt = time.Unix(0, signal.GetSampleUtcNs()).UTC()
			}
			sigs = append(sigs, signalVal{Path: signal.GetPath(), Num: num, Text: txt, SampleAt: sampleAt})
		}
		accepted, err := st.ApplyTelemetryDurably(ctx, registry, id, gatewayID, env.GetMessageType(), env.GetSessionId(), env.GetSequence(), sigs)
		if err != nil {
			recordMQTTDeadLetter(st, topic, payload, env, "遥测权威事务失败: "+err.Error())
			log.Printf("拒绝 MQTT %s：遥测权威事务失败：%v", topic, err)
			return
		}
		if !accepted {
			log.Printf("忽略 MQTT 重复或过期顺序遥测 %s session=%s seq=%d", topic, env.GetSessionId(), env.GetSequence())
			return
		}
		processed = true
		return
	}
	accepted, err := registry.AcceptInboundSequence(ctx, id, gatewayID, env.GetMessageType(), env.GetSessionId(), env.GetSequence())
	if err != nil {
		recordMQTTDeadLetter(st, topic, payload, env, "无法写入去重账本: "+err.Error())
		log.Printf("拒绝 MQTT %s：无法写入去重账本：%v", topic, err)
		return
	}
	if !accepted {
		log.Printf("忽略 MQTT 重复消息 %s session=%s seq=%d", topic, env.GetSessionId(), env.GetSequence())
		return
	}
	switch kind {
	case "register":
		st.HandleRegister(id, env.GetGatewayId(), map[string]string{
			"stack": caps.GetStack().String(), "stack_version": caps.GetStackVersion(),
			"gateway_version": caps.GetGatewayVersion(), "topic_mapping": caps.GetTopicMappingVersion(),
			"adapter_version": caps.GetAdapterVersion(), "safety_arbiter_version": caps.GetSafetyArbiterVersion(),
		}, "")
		processed = true
	case "telemetry":
		// telemetry is committed above together with its dedup claim and state
		// projection; this branch is intentionally unreachable.
	case "status":
		// 网关心跳：仅代表网关存活，不参与车辆在线判定（见 state.go 文件头）。
		processed = true
	case "ack":
		processed = true
	case "nav_ack":
		// Navigation ACK is committed and marked processed above.
	case "map_ack":
		processed = true
	}
}
