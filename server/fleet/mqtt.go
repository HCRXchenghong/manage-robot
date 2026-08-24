package main

// mqtt.go：车云 MQTT 接入（mTLS，订阅 vehicle/#）。
// Broker 以客户端证书身份鉴权（use_identity_as_username），云端用 access 证书。

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type mqttOptions struct {
	host     string
	port     int
	caFile   string
	certFile string
	keyFile  string
	clientID string
}

// envelope 车云 JSON 信封（protocols Protobuf 的 JSON 镜像，字段见计划 §2）。
type envelope struct {
	MessageType string          `json:"message_type"`
	VehicleID   string          `json:"vehicle_id"`
	GatewayID   string          `json:"gateway_id"`
	UTCTimeNS   int64           `json:"utc_time_ns"`
	Payload     json.RawMessage `json:"payload"`
}

// startMQTT 后台连接并订阅；首连失败不阻塞启动（ConnectRetry 会持续重试）。
func startMQTT(o mqttOptions, st *State) error {
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
		if t := c.Subscribe("vehicle/#", 1, nil); t.Wait() && t.Error() != nil {
			log.Printf("MQTT 订阅 vehicle/# 失败: %v", t.Error())
			return
		}
		log.Printf("MQTT 已就绪：%s，订阅 vehicle/#", broker)
	}
	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		log.Printf("MQTT 连接丢失: %v（自动重连）", err)
	}
	opts.SetDefaultPublishHandler(func(_ mqtt.Client, m mqtt.Message) {
		routeMQTT(st, m.Topic(), m.Payload())
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

// routeMQTT 按话题段 vehicle/{id}/{kind} 分发；以话题段 id 为准（信封字段仅辅助）。
func routeMQTT(st *State, topic string, payload []byte) {
	parts := strings.Split(topic, "/")
	if len(parts) != 3 || parts[0] != "vehicle" {
		return
	}
	id, kind := parts[1], parts[2]
	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		log.Printf("MQTT %s 信封解析失败: %v", topic, err)
		return
	}
	switch kind {
	case "register":
		var caps struct {
			Stack               string `json:"stack"`
			StackVersion        string `json:"stack_version"`
			GatewayVersion      string `json:"gateway_version"`
			TopicMappingVersion string `json:"topic_mapping_version"`
			Group               string `json:"group"`
		}
		if err := json.Unmarshal(env.Payload, &caps); err != nil {
			log.Printf("register payload 解析失败: %v", err)
			return
		}
		st.HandleRegister(id, env.GatewayID, map[string]string{
			"stack":           caps.Stack,
			"stack_version":   caps.StackVersion,
			"gateway_version": caps.GatewayVersion,
			"topic_mapping":   caps.TopicMappingVersion,
		}, caps.Group)
	case "telemetry":
		var su struct {
			Signals []struct {
				Path  string `json:"path"`
				Value struct {
					Number *float64 `json:"number"`
					Text   *string  `json:"text"`
				} `json:"value"`
			} `json:"signals"`
		}
		if err := json.Unmarshal(env.Payload, &su); err != nil {
			log.Printf("telemetry payload 解析失败: %v", err)
			return
		}
		sigs := make([]signalVal, 0, len(su.Signals))
		for _, s := range su.Signals {
			sigs = append(sigs, signalVal{Path: s.Path, Num: s.Value.Number, Text: s.Value.Text})
		}
		st.HandleTelemetry(id, sigs)
	case "status":
		// 网关心跳：仅代表网关存活，不参与车辆在线判定（见 state.go 文件头）。
	}
}
