package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	platformv1 "robot-agent/protocols/platform/v1"
)

const maxActuatorReplyBytes = 64 * 1024
const localProtocol = "platform.v1.local"

// UnixActuator is the production side-effect boundary. The adapter owns this
// socket and publishes to the real ROS/ECU topic only after Arbiter approval.
type UnixActuator struct {
	path    string
	timeout time.Duration
}

func (u UnixActuator) call(request *platformv1.LocalFrame) (ActuationResult, error) {
	conn, err := net.DialTimeout("unix", u.path, u.timeout)
	if err != nil {
		return ActuationResult{}, fmt.Errorf("连接 Adapter 执行器: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(u.timeout))
	b, err := marshalLocalFrame(request)
	if err != nil {
		return ActuationResult{}, err
	}
	if len(b) > maxActuatorReplyBytes+4 {
		return ActuationResult{}, fmt.Errorf("执行器请求过大")
	}
	if _, err := conn.Write(b); err != nil {
		return ActuationResult{}, fmt.Errorf("发送执行器请求: %w", err)
	}
	frame, err := readLocalFrame(conn, maxActuatorReplyBytes)
	if err != nil {
		return ActuationResult{}, fmt.Errorf("读取执行器回执: %w", err)
	}
	if frame.GetProtocol() != localProtocol || frame.GetKind() != platformv1.LocalFrame_KIND_ACTUATOR_REPLY {
		return ActuationResult{}, fmt.Errorf("执行器回执帧类型无效")
	}
	reply := frame.GetActuatorReply()
	if reply == nil || !reply.GetOk() || reply.GetAppliedMonotonicNs() == 0 {
		detail := "执行器拒绝或缺少应用时间"
		if reply != nil && reply.GetError() != "" {
			detail = reply.GetError()
		}
		return ActuationResult{}, fmt.Errorf("%s", detail)
	}
	return ActuationResult{AppliedMonotonicNS: int64(reply.GetAppliedMonotonicNs())}, nil
}

func (u UnixActuator) ApplyMotion(m Motion) (ActuationResult, error) {
	return u.call(&platformv1.LocalFrame{
		Kind: platformv1.LocalFrame_KIND_ACTUATOR_REQUEST, Component: "arbiter", Protocol: localProtocol,
		Body: &platformv1.LocalFrame_ActuatorRequest{ActuatorRequest: &platformv1.ActuatorRequest{
			Action: &platformv1.ActuatorRequest_TargetMotion{TargetMotion: &platformv1.TargetMotion{
				TargetSpeedMps: m.TargetSpeedMPS, TargetAccelerationMps2: m.TargetAccelerationMPS,
				TargetCurvatureInvM: m.TargetCurvatureInvM, TargetYawRateRadps: m.TargetYawRateRadPS,
			}},
		}},
	})
}

func (u UnixActuator) MinimalRisk(reason string) (ActuationResult, error) {
	if strings.TrimSpace(reason) == "" {
		return ActuationResult{}, fmt.Errorf("最小风险原因不能为空")
	}
	return u.call(&platformv1.LocalFrame{
		Kind: platformv1.LocalFrame_KIND_ACTUATOR_REQUEST, Component: "arbiter", Protocol: localProtocol,
		Body: &platformv1.LocalFrame_ActuatorRequest{ActuatorRequest: &platformv1.ActuatorRequest{
			Action: &platformv1.ActuatorRequest_MinimalRiskReason{MinimalRiskReason: reason},
		}},
	})
}

func marshalLocalFrame(frame *platformv1.LocalFrame) ([]byte, error) {
	if frame == nil || frame.GetProtocol() != localProtocol {
		return nil, fmt.Errorf("本机帧协议无效")
	}
	raw, err := (proto.MarshalOptions{Deterministic: true}).Marshal(frame)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > maxActuatorReplyBytes {
		return nil, fmt.Errorf("本机帧大小无效")
	}
	out := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(out[:4], uint32(len(raw)))
	copy(out[4:], raw)
	return out, nil
}

func readLocalFrame(r io.Reader, max int) (*platformv1.LocalFrame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > uint32(max) {
		return nil, fmt.Errorf("本机帧长度无效")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, err
	}
	frame := &platformv1.LocalFrame{}
	if err := proto.Unmarshal(raw, frame); err != nil {
		return nil, err
	}
	if frame.GetProtocol() != localProtocol {
		return nil, fmt.Errorf("本机帧协议版本不受支持")
	}
	return frame, nil
}

func loadAuthorityKey(spec string) (string, ed25519.PublicKey, error) {
	parts := strings.SplitN(spec, "=", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", nil, fmt.Errorf("authority-public-key 格式应为 key_id=/secure/path/key.b64")
	}
	b, err := os.ReadFile(parts[1])
	if err != nil {
		return "", nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return "", nil, fmt.Errorf("Authority 公钥必须为 base64 Ed25519 公钥")
	}
	return parts[0], ed25519.PublicKey(raw), nil
}

func main() {
	uds := flag.String("uds", "/run/robot-agent/gateway.sock", "Vehicle Gateway Unix socket")
	vehicleID := flag.String("vehicle-id", "", "已激活的车辆 ID")
	gatewayID := flag.String("gateway-id", "", "已激活的 Gateway ID")
	var authorityKeys multiFlag
	flag.Var(&authorityKeys, "authority-public-key", "可重复：key_id=/secure/path/key.b64")
	macKeyFile := flag.String("command-mac-key", "", "受信控制代理提供的会话 MAC 密钥（测试/集成用）")
	actuatorUDS := flag.String("actuator-uds", "", "ROS 1 Adapter 执行器 Unix socket（生产必填）")
	watchdog := flag.Duration("watchdog", 500*time.Millisecond, "本地控制流看门狗")
	maxSpeed := flag.Float64("max-speed-mps", 1.0, "已标定 ODD 最大速度")
	flag.Parse()
	if *vehicleID == "" || *gatewayID == "" || *actuatorUDS == "" {
		fatal(fmt.Errorf("vehicle-id、gateway-id、actuator-uds 均为必填，拒绝无绑定启动"))
	}

	keys := map[string]ed25519.PublicKey{}
	for _, spec := range authorityKeys {
		id, key, err := loadAuthorityKey(spec)
		if err != nil {
			fatal(err)
		}
		keys[id] = key
	}
	var macKey []byte
	if *macKeyFile != "" {
		b, err := os.ReadFile(*macKeyFile)
		if err != nil {
			fatal(err)
		}
		macKey, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil {
			fatal(fmt.Errorf("读取命令 MAC 密钥: %w", err))
		}
	}
	arb, err := NewArbiter(Config{VehicleID: *vehicleID, GatewayID: *gatewayID, AuthorityKeys: keys,
		CommandMACKey: macKey, RequireMAC: true, Watchdog: *watchdog,
		Limits:   Limits{MaxSpeedMPS: *maxSpeed, MaxAccelerationMPS: 1, MaxCurvatureInvM: 0.5, MaxYawRateRadPS: 0.8},
		Actuator: UnixActuator{path: *actuatorUDS, timeout: 250 * time.Millisecond}})
	if err != nil {
		fatal(err)
	}
	conn, err := net.Dial("unix", *uds)
	if err != nil {
		fatal(fmt.Errorf("连接 Gateway UDS: %w", err))
	}
	defer conn.Close()
	hello := &platformv1.LocalFrame{Kind: platformv1.LocalFrame_KIND_HELLO,
		Component: "arbiter", Protocol: localProtocol}
	helloBytes, err := marshalLocalFrame(hello)
	if err != nil {
		fatal(fmt.Errorf("构造 Gateway 组件握手: %w", err))
	}
	if _, err := conn.Write(helloBytes); err != nil {
		fatal(fmt.Errorf("Gateway 组件握手失败: %w", err))
	}
	go watchdogLoop(arb)
	for {
		frame, err := readLocalFrame(conn, 1024*1024)
		if err == io.EOF {
			break
		}
		if err != nil {
			fatal(fmt.Errorf("读取 Gateway 本机帧: %w", err))
		}
		if frame.GetKind() != platformv1.LocalFrame_KIND_ENVELOPE || frame.GetEnvelope() == nil {
			continue
		}
		var d Decision
		switch frame.GetEnvelope().GetMessageType() {
		case "platform.v1.LeaseGrant":
			d = arb.HandleProtoLease(frame.GetEnvelope())
		case "platform.v1.ControlCommand":
			d = arb.HandleProtoCommand(frame.GetEnvelope())
		default:
			continue
		}
		if frame.GetEnvelope().GetMessageType() == "platform.v1.ControlCommand" {
			writeAck(conn, frame.GetEnvelope(), d, macKey)
		}
		fmt.Printf("[arbiter] %s: %s\n", d.Result, d.Detail)
		if d.MinimalRisk {
			fmt.Printf("[arbiter] MINIMAL_RISK: %s\n", d.RiskReason)
		}
	}
}

func writeAck(conn net.Conn, control *platformv1.Envelope, d Decision, macKey []byte) {
	if control == nil {
		return
	}
	command := &platformv1.ControlCommand{}
	if err := proto.Unmarshal(control.GetPayload(), command); err != nil {
		return
	}
	result := platformv1.ControlResult_CONTROL_RESULT_FAILED
	switch d.Result {
	case Accepted:
		result = platformv1.ControlResult_CONTROL_RESULT_ACCEPTED
	case RejectedStale:
		result = platformv1.ControlResult_CONTROL_RESULT_REJECTED_STALE
	case RejectedFencing:
		result = platformv1.ControlResult_CONTROL_RESULT_REJECTED_FENCING
	case RejectedLease:
		result = platformv1.ControlResult_CONTROL_RESULT_REJECTED_LEASE
	case RejectedLimit:
		result = platformv1.ControlResult_CONTROL_RESULT_REJECTED_LIMIT
	case RejectedAuth:
		result = platformv1.ControlResult_CONTROL_RESULT_FAILED
	}
	ack := &platformv1.ControlAck{
		ControlSessionId:   command.GetControlSessionId(),
		CommandSequence:    command.GetCommandSequence(),
		Result:             result,
		Detail:             d.Detail,
		AppliedMonotonicNs: uint64(maxInt64(d.AppliedMonotonicNS, 0)),
	}
	ackPayload, err := proto.Marshal(ack)
	if err != nil {
		return
	}
	wire := &platformv1.Envelope{
		SchemaMajor:     control.GetSchemaMajor(),
		SchemaMinor:     control.GetSchemaMinor(),
		MessageType:     "platform.v1.ControlAck",
		VehicleId:       control.GetVehicleId(),
		GatewayId:       control.GetGatewayId(),
		SessionId:       control.GetSessionId(),
		Sequence:        command.GetCommandSequence(),
		UtcTimeNs:       time.Now().UnixNano(),
		MonotonicTimeNs: platformv1.MonotonicNowNS(),
		TtlMs:           1000,
		TraceId:         control.GetTraceId(),
		Payload:         ackPayload,
	}
	if wire.SchemaMajor == 0 {
		wire.SchemaMajor = 1
	}
	if wire.TraceId == "" {
		wire.TraceId = control.GetSessionId()
	}
	_ = platformv1.SignEnvelope(wire, macKey)
	frame := &platformv1.LocalFrame{Kind: platformv1.LocalFrame_KIND_ENVELOPE,
		Component: "arbiter", Protocol: localProtocol, Envelope: wire}
	if b, err := marshalLocalFrame(frame); err == nil {
		_, _ = conn.Write(b)
	}
}

func maxInt64(v, floor int64) int64 {
	if v < floor {
		return floor
	}
	return v
}

func watchdogLoop(arb *Arbiter) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		if d := arb.Tick(); d.MinimalRisk {
			fmt.Printf("[arbiter] MINIMAL_RISK: %s\n", d.RiskReason)
		}
	}
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "[arbiter]", err)
	os.Exit(2)
}
