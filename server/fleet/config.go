package main

// config.go：系统配置中心（右下角「管理员 → 设置」的全部可配置项）。
// 语义：服务端保存一份权威默认值 + 运行时覆盖；前端设置页按分类渲染，
//      保存走 POST /api/config。重启后回到默认值（阶段 2 可落 PG/文件）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ConfigStore struct {
	mu       sync.RWMutex
	defaults map[string]any
	values   map[string]any
	updated  int64
	db       *sql.DB
}

func NewConfigStore(dbs ...*sql.DB) *ConfigStore {
	d := map[string]any{
		// —— 地图与底图 ——
		"tdt_tk":     "",                 // 天地图密钥
		"tdt_origin": "39.9042,116.4074", // 车辆坐标原点（纬度,经度）
		"bev_cell":   0.1,                // 3D→2D 默认格宽（米）
		"maps_dir":   "data/maps",        // 地图仓库目录（服务器存储）
		// —— 平台服务 ——
		"fleet_addr": ":9800",
		"mqtt_addr":  "localhost:8883",
		"mqtt_tls":   true,
		// 只有这里配置的反向代理网段可以提供可信 X-Forwarded-For；空值时
		// 开放 API 始终使用 TCP 对端地址，避免客户端伪造来源 IP 绕过白名单。
		"trusted_proxy_cidrs": "",
		// Control Authority is embedded in fleet-hub in this deployment. It is
		// intentionally descriptive/read-only; changing it in a browser cannot
		// silently redirect safety-critical traffic.
		"authority_addr": "embedded://fleet-hub",
		// —— 车辆与底盘 ——
		"chassis_type": "ackermann", // ackermann|4w4s|diff_agv
		"telemetry_hz": 2.0,
		"map_push_s":   15.0, // 车端地图上报周期（秒）
		// —— 安全与等保 ——
		"open_api_enabled": true,
		"audit_enabled":    true,
		"replay_window_s":  300.0, // 签名时间戳允许偏差（防重放窗口）
		"rate_limit":       20.0,  // 每 Key 每 10s 请求上限
		// 开放 API 鉴权失败锁定（等保三级「身份鉴别」：登录失败处理）
		"open_lockout_threshold": 5.0,   // 窗口内失败多少次触发锁定
		"open_lockout_window_s":  900.0, // 失败计数窗口（秒）
		"open_lockout_seconds":   300.0, // 锁定时长（秒）
		// API Key 生命周期：永久 Key 不允许创建；默认 90 天，最长 365 天。
		"open_api_key_default_lifetime_s": 90.0 * 24 * 3600,
		"open_api_key_max_lifetime_s":     365.0 * 24 * 3600,
	}
	var db *sql.DB
	if len(dbs) > 0 {
		db = dbs[0]
	}
	c := &ConfigStore{defaults: d, values: map[string]any{}, updated: time.Now().UnixNano(), db: db}
	c.load()
	return c
}

func (c *ConfigStore) allLocked() map[string]any {
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

func (c *ConfigStore) load() {
	if c == nil || c.db == nil {
		return
	}
	rows, err := c.db.Query("SELECT key, value::text FROM platform_config")
	if err != nil {
		log.Printf("[config] 恢复配置失败: %v", err)
		return
	}
	defer rows.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			continue
		}
		if _, ok := c.defaults[key]; !ok {
			continue
		}
		var value any
		if json.Unmarshal([]byte(raw), &value) == nil {
			c.values[key] = value
		}
	}
}

func (c *ConfigStore) All() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.allLocked()
}

func configNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func configString(v any) (string, bool) {
	s, ok := v.(string)
	return strings.TrimSpace(s), ok
}

func validateConfigValue(key string, value any) error {
	if value == nil {
		return fmt.Errorf("%s 不能为 null", key)
	}
	switch key {
	case "tdt_tk":
		s, ok := configString(value)
		if !ok || len([]rune(s)) > 512 {
			return fmt.Errorf("%s 必须是 512 字符以内的文本", key)
		}
	case "tdt_origin":
		s, ok := configString(value)
		parts := strings.Split(s, ",")
		if !ok || len(parts) != 2 {
			return fmt.Errorf("%s 必须是 纬度,经度", key)
		}
		lat, latErr := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		lon, lonErr := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if latErr != nil || lonErr != nil || math.IsNaN(lat) || math.IsNaN(lon) ||
			math.IsInf(lat, 0) || math.IsInf(lon, 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			return fmt.Errorf("%s 的纬度/经度超出范围", key)
		}
	case "maps_dir":
		s, ok := configString(value)
		if !ok || s == "" || len([]rune(s)) > 512 {
			return fmt.Errorf("%s 必须是非空路径", key)
		}
	case "fleet_addr", "mqtt_addr", "authority_addr":
		s, ok := configString(value)
		if !ok || s == "" || len([]rune(s)) > 255 {
			return fmt.Errorf("%s 必须是非空地址", key)
		}
	case "trusted_proxy_cidrs":
		s, ok := configString(value)
		if !ok || len([]rune(s)) > 2048 {
			return fmt.Errorf("%s 必须是逗号分隔的 CIDR 列表，长度不超过 2048", key)
		}
		for _, raw := range strings.Split(s, ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			if _, _, err := net.ParseCIDR(raw); err != nil {
				return fmt.Errorf("%s 包含非法 CIDR：%s", key, raw)
			}
		}
	case "mqtt_tls", "open_api_enabled", "audit_enabled":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s 必须是布尔值", key)
		}
	case "chassis_type":
		s, ok := configString(value)
		if !ok || (s != "ackermann" && s != "4w4s" && s != "diff_agv") {
			return fmt.Errorf("%s 不是受支持的底盘类型", key)
		}
	case "bev_cell":
		n, ok := configNumber(value)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0.01 || n > 2 {
			return fmt.Errorf("%s 必须在 0.01 到 2 米之间", key)
		}
	case "telemetry_hz":
		n, ok := configNumber(value)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0.1 || n > 20 {
			return fmt.Errorf("%s 必须在 0.1 到 20 Hz 之间", key)
		}
	case "map_push_s":
		n, ok := configNumber(value)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > 3600 {
			return fmt.Errorf("%s 必须在 1 到 3600 秒之间", key)
		}
	case "replay_window_s":
		n, ok := configNumber(value)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > 3600 {
			return fmt.Errorf("%s 必须在 1 到 3600 秒之间", key)
		}
	case "rate_limit":
		n, ok := configNumber(value)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > 10000 || math.Trunc(n) != n {
			return fmt.Errorf("%s 必须是 1 到 10000 的整数", key)
		}
	case "open_lockout_threshold":
		n, ok := configNumber(value)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > 100 || math.Trunc(n) != n {
			return fmt.Errorf("%s 必须是 1 到 100 的整数", key)
		}
	case "open_lockout_window_s", "open_lockout_seconds":
		n, ok := configNumber(value)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 10 || n > 86400 || math.Trunc(n) != n {
			return fmt.Errorf("%s 必须是 10 到 86400 的整数", key)
		}
	case "open_api_key_default_lifetime_s", "open_api_key_max_lifetime_s":
		n, ok := configNumber(value)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 3600 || n > 365*24*3600 || math.Trunc(n) != n {
			return fmt.Errorf("%s 必须是 3600 到 31536000 的整数", key)
		}
	default:
		return fmt.Errorf("未知配置项 %q", key)
	}
	return nil
}

// Set preserves the small unit-test API used by the OpenAPI tests. Production
// HTTP handlers use SetForActor so persistence failures cannot be reported as
// successful configuration changes.
func (c *ConfigStore) Set(kv map[string]any) (map[string]any, []string) {
	merged, rejected, _ := c.SetForActor(kv, "")
	return merged, rejected
}

// SetForActor validates and commits a complete configuration update. The
// in-memory projection is changed only after the PostgreSQL transaction has
// committed; a failed request therefore cannot leave a split-brain config.
func (c *ConfigStore) SetForActor(kv map[string]any, actor string) (map[string]any, []string, error) {
	if c == nil {
		return nil, nil, fmt.Errorf("配置中心不可用")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var rejected []string
	encoded := make(map[string][]byte, len(kv))
	values := make(map[string]any, len(kv))
	for k, v := range kv {
		if _, ok := c.defaults[k]; !ok {
			rejected = append(rejected, k)
			continue
		}
		if err := validateConfigValue(k, v); err != nil {
			rejected = append(rejected, k+": "+err.Error())
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			rejected = append(rejected, k+": 无法编码为 JSON")
			continue
		}
		encoded[k], values[k] = raw, v
	}
	if len(rejected) > 0 {
		sort.Strings(rejected)
		return c.allLocked(), rejected, fmt.Errorf("配置校验失败：%s", strings.Join(rejected, "；"))
	}
	if len(encoded) == 0 {
		return c.allLocked(), nil, fmt.Errorf("没有可更新的配置项")
	}
	// The two lifetime knobs form one policy. Validate the merged candidate
	// before opening a transaction so an individual update cannot leave the
	// service unable to create keys until a second request arrives.
	candidate := c.allLocked()
	for k, v := range values {
		candidate[k] = v
	}
	defaultLifetime, _ := configNumber(candidate["open_api_key_default_lifetime_s"])
	maxLifetime, _ := configNumber(candidate["open_api_key_max_lifetime_s"])
	if defaultLifetime > maxLifetime {
		return c.allLocked(), []string{"open_api_key_default_lifetime_s: 默认有效期不能大于最大有效期"}, fmt.Errorf("配置校验失败：API Key 默认有效期不能大于最大有效期")
	}
	if c.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		tx, err := c.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return c.allLocked(), nil, fmt.Errorf("开始配置事务失败：%w", err)
		}
		defer func() { _ = tx.Rollback() }()
		for k, raw := range encoded {
			if _, err := tx.ExecContext(ctx, `INSERT INTO platform_config(key, value, updated_by)
				VALUES ($1,$2::jsonb,$3)
				ON CONFLICT (key) DO UPDATE SET value=$2::jsonb, updated_by=$3, updated_at=now()`,
				k, string(raw), actor); err != nil {
				return c.allLocked(), nil, fmt.Errorf("保存配置 %s 失败：%w", k, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return c.allLocked(), nil, fmt.Errorf("提交配置事务失败：%w", err)
		}
	}
	for k, v := range values {
		c.values[k] = v
	}
	c.updated = time.Now().UnixNano()
	return c.allLocked(), nil, nil
}
