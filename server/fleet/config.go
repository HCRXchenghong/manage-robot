package main

// config.go：系统配置中心（右下角「管理员 → 设置」的全部可配置项）。
// 语义：服务端保存一份权威默认值 + 运行时覆盖；前端设置页按分类渲染，
//      保存走 POST /api/config。重启后回到默认值（阶段 2 可落 PG/文件）。

import (
	"sync"
	"time"
)

type ConfigStore struct {
	mu       sync.RWMutex
	defaults map[string]any
	values   map[string]any
	updated  int64
}

func NewConfigStore() *ConfigStore {
	d := map[string]any{
		// —— 地图与底图 ——
		"tdt_tk":     "",                 // 天地图密钥
		"tdt_origin": "39.9042,116.4074", // 车辆坐标原点（纬度,经度）
		"bev_cell":   0.1,                // 3D→2D 默认格宽（米）
		"maps_dir":   "data/maps",        // 地图仓库目录（服务器存储）
		// —— 平台服务 ——
		"fleet_addr":     ":9800",
		"mqtt_addr":      "localhost:8883",
		"authority_addr": "127.0.0.1:9300",
		"mqtt_tls":       true,
		// —— 车辆与底盘 ——
		"chassis_type": "ackermann", // ackermann|4w4s|diff_agv
		"telemetry_hz": 2.0,
		"map_push_s":   15.0, // 车端地图上报周期（秒）
		// —— 安全与等保 ——
		"open_api_enabled": true,
		"audit_enabled":    true,
		"replay_window_s":  300.0, // 签名时间戳允许偏差（防重放窗口）
		"rate_limit":       20.0,  // 每 Key 每 10s 请求上限
	}
	return &ConfigStore{defaults: d, values: map[string]any{}, updated: time.Now().UnixNano()}
}

func (c *ConfigStore) All() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]any, len(c.defaults))
	for k, v := range c.defaults {
		out[k] = v
	}
	for k, v := range c.values {
		out[k] = v
	}
	out["_updated_ns"] = c.updated
	return out
}

// Set 覆盖键值；未知键拒绝（防止乱塞）。
func (c *ConfigStore) Set(kv map[string]any) (map[string]any, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var rejected []string
	for k, v := range kv {
		if _, ok := c.defaults[k]; !ok {
			rejected = append(rejected, k)
			continue
		}
		c.values[k] = v
	}
	c.updated = time.Now().UnixNano()
	out := make(map[string]any, len(c.defaults))
	for k, v := range c.defaults {
		out[k] = v
	}
	for k, v := range c.values {
		out[k] = v
	}
	return out, rejected
}
