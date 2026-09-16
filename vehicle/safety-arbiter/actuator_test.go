package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	platformv1 "robot-agent/protocols/platform/v1"
)

func TestUnixActuatorRequiresAdapterAck(t *testing.T) {
	// Keep the endpoint isolated per test so parallel/race runs never collide
	// with a developer process or another test. Use the process temp directory
	// so the test remains portable and can be redirected by TMPDIR in sandboxed
	// CI environments.
	tmp, err := os.MkdirTemp("", "robot-agent-actuator-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	path := filepath.Join(tmp, "actuator.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	requests := make(chan *platformv1.LocalFrame, 2)
	go func() {
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if req, err := readLocalFrame(conn, maxActuatorReplyBytes); err == nil {
				requests <- req
				reply := &platformv1.LocalFrame{Kind: platformv1.LocalFrame_KIND_ACTUATOR_REPLY,
					Component: "adapter", Protocol: localProtocol,
					Body: &platformv1.LocalFrame_ActuatorReply{ActuatorReply: &platformv1.ActuatorReply{
						Ok: true, AppliedMonotonicNs: 12345,
					}},
				}
				if b, err := marshalLocalFrame(reply); err == nil {
					_, _ = conn.Write(b)
				}
			}
			_ = conn.Close()
		}
	}()

	actuator := UnixActuator{path: path, timeout: time.Second}
	if got, err := actuator.ApplyMotion(Motion{TargetSpeedMPS: 0.5}); err != nil || got.AppliedMonotonicNS != 12345 {
		t.Fatalf("ApplyMotion got=%+v err=%v", got, err)
	}
	if got, err := actuator.MinimalRisk("test_watchdog"); err != nil || got.AppliedMonotonicNS != 12345 {
		t.Fatalf("MinimalRisk got=%+v err=%v", got, err)
	}
	for i := 0; i < 2; i++ {
		select {
		case req := <-requests:
			if req.GetKind() != platformv1.LocalFrame_KIND_ACTUATOR_REQUEST || req.GetActuatorRequest() == nil {
				t.Fatalf("unexpected request: %#v", req)
			}
		case <-time.After(time.Second):
			t.Fatal("adapter did not receive actuator request")
		}
	}
}
