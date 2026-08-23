package main

// control.go：大屏控制按钮 -> control-authority 的 UDP 桥接（第 10 步任务 4）。
// 职责边界：只做「转发 + 格式化」。批不批由 control-authority 说了算，
// 执不执行由车端仲裁器说了算——安全链路一环不动。

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// authorityCall 发送 req 到 authority 并等待应答（1.5s 超时）。
func (s *State) authorityCall(req map[string]any) (map[string]any, error) {
	conn, err := net.DialTimeout("udp", s.authorityAddr, 300*time.Millisecond)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(body); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("authority 无应答: %w", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
