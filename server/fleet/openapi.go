package main

// openapi.go：开放 API 平台 v2（对外开放面，给第三方平台调用）。
//
// 等保三级控制点落实：
//   身份鉴别   —— API Key（服务端只存 SHA-256 哈希，明文只在创建时返回一次）；
//                 窗口内鉴权失败超阈值即临时锁定（次数/窗口/时长可配置）
//   访问控制   —— 每 Key 功能白名单（scopes）：未勾选的功能一律拒绝（默认拒绝）；
//                 Key 管理/审计查询等管理面仅限管理员角色（职责分离）
//   数据隔离   —— 每 Key 车辆白名单（vehicles）：只能查询/调度白名单内车辆，
//                 跨车访问一律 403；"*" 表示全部车辆
//   安全审计   —— 每次调用（含失败原因、调用备注、涉及车辆、trace）进审计环 + PG audit_log
//   抗重放     —— 时间戳 ±5 分钟窗口 + nonce 一次性（10 分钟内去重）
//   数据完整性 —— HMAC-SHA256 签名覆盖 方法/路径/时间戳/nonce/请求体哈希
//   传输保密   —— 生产经 nginx+TLS 入口（deploy/compose/ingress），文档注明
//   资源限制   —— 每 Key 滑动窗口限流（默认 20 次/10s，可配置）
//
// 调用备注：任意请求可带 X-Remark 请求头（POST 也可放 body.remark，GET 可用 ?remark=），
// 原文随审计日志留存，便于追溯「这次调用是干什么的」。
//
// 签名算法（对外文档同款）：
//   key  = hex( SHA256(secret) )          // 签名密钥；服务端只存这个哈希，不落明文
//   sign = hex( HMAC_SHA256(secret,
//        METHOD + "\n" + PATH + "\n" + TIMESTAMP + "\n" + NONCE + "\n" + hex(SHA256(body)) ) )
//   请求头：X-API-Key / X-Timestamp(unix 秒) / X-Nonce / X-Signature

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- 功能权限目录（每 Key 勾选制：未勾选的功能一律不允许） ----------

type OpenScope struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"desc"`
	Kind string `json:"kind"` // read|write
}

var openScopeCatalog = []OpenScope{
	{ID: "vehicle.read", Name: "车辆状态查询", Desc: "查询车辆列表/详情/在线状态", Kind: "read"},
	{ID: "telemetry.read", Name: "遥测数据查询", Desc: "查询单车实时遥测（速度/电量/GPS/历史曲线）", Kind: "read"},
	{ID: "alarm.read", Name: "告警事件查询", Desc: "查询指定车辆的告警与事件流水", Kind: "read"},
	{ID: "task.read", Name: "任务查询", Desc: "查询循迹任务状态与任务列表", Kind: "read"},
	{ID: "task.write", Name: "任务下发", Desc: "下发/取消循迹任务（某车从 A 点到 B 点）", Kind: "write"},
	{ID: "control.write", Name: "安全控制", Desc: "触发紧急停车（高危能力，谨慎授予）", Kind: "write"},
}

func validScope(id string) bool {
	for _, s := range openScopeCatalog {
		if s.ID == id {
			return true
		}
	}
	return false
}

// ---------- Key 与审计模型 ----------

type APIKey struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Remark     string   `json:"remark,omitempty"`
	Prefix     string   `json:"prefix"` // 明文前 12 位（便于识别，不含机密）
	KeyHash    string   `json:"-"`
	Scopes     []string `json:"scopes"`   // 功能白名单：只允许调用已勾选的功能
	Vehicles   []string `json:"vehicles"` // 车辆白名单：含 "*" 表示全部；空 = 任何车辆不可访问
	IPs        []string `json:"ips"`      // 来源 IP 白名单：固定允许调用的一或多个 IP/网段；空 = 不限
	CreatedNS  int64    `json:"created_ns"`
	ExpiresNS  int64    `json:"expires_ns,omitempty"`
	RevokedNS  int64    `json:"revoked_ns,omitempty"`
	LastUsedNS int64    `json:"last_used_ns,omitempty"`
}

const (
	defaultAPIKeyLifetime = 90 * 24 * time.Hour
	maxAPIKeyLifetime     = 365 * 24 * time.Hour
)

type AuditEntry struct {
	ID        int64  `json:"id,omitempty"`
	TsNS      int64  `json:"ts_ns"`
	KeyID     string `json:"key_id,omitempty"`
	KeyName   string `json:"key_name,omitempty"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Result    string `json:"result"` // ok|auth_failed|denied|locked|replay|rate_limited|bad_request|expired|error
	HTTP      int    `json:"http"`
	IP        string `json:"ip,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	VehicleID string `json:"vehicle_id,omitempty"`
	Remark    string `json:"remark,omitempty"`
	Detail    string `json:"detail,omitempty"`
	Source    string `json:"source,omitempty"` // api|platform
	Actor     string `json:"actor,omitempty"`
	Action    string `json:"action,omitempty"`
}

type AuditFilter struct {
	Limit  int
	KeyID  string
	Result string
	Source string // api|platform|all; empty is treated as api
	Cursor string
	From   *time.Time
	To     *time.Time
}

// auditCursor is an opaque, stable keyset-pagination position. Offset
// pagination is deliberately not used: audit records are append-only and new
// rows arrive while an operator is paging through an export. The timestamp,
// row id and source together form a strict ordering key.
type auditCursor struct {
	TsNS   int64  `json:"ts_ns"`
	ID     int64  `json:"id"`
	Source string `json:"source"`
}

func encodeAuditCursor(c auditCursor) string {
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeAuditCursor(raw string) (auditCursor, error) {
	var c auditCursor
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 {
		return c, fmt.Errorf("审计游标无效")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(b, &c) != nil || c.TsNS <= 0 || c.ID <= 0 || (c.Source != "api" && c.Source != "platform") {
		return c, fmt.Errorf("审计游标无效")
	}
	return c, nil
}

type auditPage struct {
	Entries    []AuditEntry
	NextCursor string
	HasMore    bool
}

// lockEntry 鉴权失败锁定（等保三级「身份鉴别」：登录失败处理）。
type lockEntry struct {
	fails []int64 // 窗口内的失败时刻（unix 秒）
	until int64   // 锁定到期时刻（unix 秒），0 = 未锁定
}

type OpenAPI struct {
	mu        sync.Mutex
	keys      map[string]*APIKey // id -> key
	byHash    map[string]string  // sha256(key) -> id
	nonces    map[string]int64   // nonce -> 到期时刻（抗重放）
	hits      map[string][]int64 // keyID -> 最近请求时刻（限流）
	locks     map[string]*lockEntry
	audit     []AuditEntry // 头最新
	st        *State
	cfg       *ConfigStore
	nav       *NavStore
	authority *DurableAuthority
}

func (o *OpenAPI) SetAuthority(authority *DurableAuthority) { o.authority = authority }

func NewOpenAPI(st *State, cfg *ConfigStore, nav *NavStore) *OpenAPI {
	o := &OpenAPI{keys: map[string]*APIKey{}, byHash: map[string]string{},
		nonces: map[string]int64{}, hits: map[string][]int64{}, locks: map[string]*lockEntry{},
		audit: []AuditEntry{}, st: st, cfg: cfg, nav: nav}
	o.loadKeys()
	o.loadAudit()
	return o
}

func cloneAPIKey(k *APIKey) *APIKey {
	if k == nil {
		return nil
	}
	out := *k
	out.Scopes = append([]string(nil), k.Scopes...)
	out.Vehicles = append([]string(nil), k.Vehicles...)
	out.IPs = append([]string(nil), k.IPs...)
	return &out
}

func (o *OpenAPI) database() *sql.DB {
	if o == nil || o.st == nil {
		return nil
	}
	return o.st.db
}

// loadKeys restores the authorization projection from PostgreSQL. A restart
// must not silently disable a key or recreate a key from source code. Malformed
// rows are ignored (fail closed) and remain available for operator repair.
func (o *OpenAPI) loadKeys() {
	db := o.database()
	if db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT id, name, prefix, key_hash, remark, scopes, vehicles, ips,
		(EXTRACT(EPOCH FROM created_at) * 1000000000)::bigint,
		(EXTRACT(EPOCH FROM expires_at) * 1000000000)::bigint,
		COALESCE((EXTRACT(EPOCH FROM revoked_at) * 1000000000)::bigint, 0),
		COALESCE((EXTRACT(EPOCH FROM last_used_at) * 1000000000)::bigint, 0)
		FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		log.Printf("[openapi] 加载 API Key 失败: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		k := &APIKey{}
		var scopes, vehicles, ips string
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.KeyHash, &k.Remark, &scopes, &vehicles, &ips,
			&k.CreatedNS, &k.ExpiresNS, &k.RevokedNS, &k.LastUsedNS); err != nil {
			log.Printf("[openapi] 读取 API Key 失败: %v", err)
			continue
		}
		if json.Unmarshal([]byte(scopes), &k.Scopes) != nil || json.Unmarshal([]byte(vehicles), &k.Vehicles) != nil || json.Unmarshal([]byte(ips), &k.IPs) != nil ||
			k.ID == "" || k.KeyHash == "" {
			log.Printf("[openapi] 忽略格式无效的 API Key %q", k.ID)
			continue
		}
		k.Scopes, k.Vehicles, k.IPs = dedupeStr(k.Scopes), dedupeStr(k.Vehicles), dedupeStr(k.IPs)
		o.keys[k.ID] = k
		if k.RevokedNS == 0 {
			o.byHash[k.KeyHash] = k.ID
		}
	}
}

// loadAudit restores the durable API-call evidence projection. The database
// remains the retention authority; the in-memory ring is only a bounded read
// cache for the dashboard and never replaces an audit row.
func (o *OpenAPI) loadAudit() {
	loaded, err := o.queryAuditDB(AuditFilter{Limit: 500, Source: "api"})
	if err != nil {
		log.Printf("[openapi] 加载 API 审计失败: %v", err)
		return
	}
	o.mu.Lock()
	o.audit = loaded
	o.mu.Unlock()
}

func (o *OpenAPI) queryAuditDB(f AuditFilter) ([]AuditEntry, error) {
	page, err := o.queryAuditDBPage(f)
	if err != nil {
		return nil, err
	}
	return page.Entries, nil
}

func (o *OpenAPI) queryAuditDBPage(f AuditFilter) (auditPage, error) {
	var page auditPage
	db := o.database()
	if db == nil {
		return page, sql.ErrConnDone
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	source := strings.TrimSpace(f.Source)
	if source == "" {
		source = "api"
	}
	if source != "api" && source != "platform" && source != "all" {
		return page, fmt.Errorf("审计来源无效")
	}
	if source == "all" {
		source = ""
	}
	where := []string{"($1 = '' OR source = $1)", "($2 = '' OR key_id = $2)", "($3 = '' OR result = $3)"}
	args := []any{source, f.KeyID, f.Result}
	arg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.From != nil {
		where = append(where, "ts >= "+arg(f.From.UTC()))
	}
	if f.To != nil {
		where = append(where, "ts < "+arg(f.To.UTC()))
	}
	if strings.TrimSpace(f.Cursor) != "" {
		cursor, err := decodeAuditCursor(f.Cursor)
		if err != nil {
			return page, err
		}
		tsArg := arg(time.Unix(0, cursor.TsNS).UTC())
		idArg := arg(cursor.ID)
		sourceArg := arg(cursor.Source)
		where = append(where, fmt.Sprintf("(ts < %s OR (ts = %s AND (id < %s OR (id = %s AND source < %s)))", tsArg, tsArg, idArg, idArg, sourceArg))
	}
	limitArg := arg(limit + 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `WITH audit_union AS (
		SELECT id, ts, key_id, key_name, method, path, result, http_code,
			COALESCE(ip, '') AS ip, trace_id, vehicle_id, remark, detail,
			'api'::text AS source, ''::text AS actor, ''::text AS action
		FROM audit_log
		UNION ALL
		SELECT id, ts, ''::text AS key_id, ''::text AS key_name,
			'SYSTEM'::text AS method, action AS path, 'ok'::text AS result, 0 AS http_code,
			''::text AS ip, ''::text AS trace_id, ''::text AS vehicle_id, ''::text AS remark,
			COALESCE(detail::text, '') AS detail, 'platform'::text AS source,
			actor, action
		FROM audit_logs
	)
	SELECT id, ts, key_id, key_name, method, path, result, http_code, ip,
		trace_id, vehicle_id, remark, detail, source, actor, action
	FROM audit_union WHERE `+strings.Join(where, " AND ")+` ORDER BY ts DESC, id DESC, source DESC LIMIT `+limitArg, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	entries := make([]AuditEntry, 0, limit+1)
	for rows.Next() {
		var ts time.Time
		var e AuditEntry
		if err := rows.Scan(&e.ID, &ts, &e.KeyID, &e.KeyName, &e.Method, &e.Path, &e.Result,
			&e.HTTP, &e.IP, &e.TraceID, &e.VehicleID, &e.Remark, &e.Detail, &e.Source, &e.Actor, &e.Action); err != nil {
			return page, err
		}
		e.TsNS = ts.UnixNano()
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	page.HasMore = len(entries) > limit
	if page.HasMore {
		entries = entries[:limit]
	}
	page.Entries = entries
	if page.HasMore && len(entries) > 0 {
		last := entries[len(entries)-1]
		page.NextCursor = encodeAuditCursor(auditCursor{TsNS: last.TsNS, ID: last.ID, Source: last.Source})
	}
	return page, nil
}

func sameAuditEntry(a, b AuditEntry) bool {
	return a.TsNS == b.TsNS && a.KeyID == b.KeyID && a.KeyName == b.KeyName &&
		a.Method == b.Method && a.Path == b.Path && a.Result == b.Result &&
		a.HTTP == b.HTTP && a.IP == b.IP && a.TraceID == b.TraceID &&
		a.VehicleID == b.VehicleID && a.Remark == b.Remark && a.Detail == b.Detail && a.Source == b.Source && a.Actor == b.Actor && a.Action == b.Action
}

func (o *OpenAPI) persistKey(k *APIKey, actor string) error {
	db := o.database()
	if db == nil {
		return nil
	}
	sc, err := json.Marshal(k.Scopes)
	if err != nil {
		return err
	}
	vh, err := json.Marshal(k.Vehicles)
	if err != nil {
		return err
	}
	ips, err := json.Marshal(k.IPs)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = db.ExecContext(ctx, `INSERT INTO api_keys
		(id, name, prefix, key_hash, remark, scopes, vehicles, ips, expires_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		k.ID, k.Name, k.Prefix, k.KeyHash, k.Remark, string(sc), string(vh), string(ips), time.Unix(0, k.ExpiresNS).UTC(), actor)
	return err
}

func (o *OpenAPI) persistKeyUpdate(ctx context.Context, k *APIKey) error {
	db := o.database()
	if db == nil {
		return nil
	}
	sc, err := json.Marshal(k.Scopes)
	if err != nil {
		return err
	}
	vh, err := json.Marshal(k.Vehicles)
	if err != nil {
		return err
	}
	ips, err := json.Marshal(k.IPs)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `UPDATE api_keys
		SET name=$2, remark=$3, scopes=$4, vehicles=$5, ips=$6, expires_at=$7
		WHERE id=$1 AND revoked_at IS NULL`, k.ID, k.Name, k.Remark, string(sc), string(vh), string(ips), time.Unix(0, k.ExpiresNS).UTC()); err != nil {
		return err
	}
	return nil
}

func (o *OpenAPI) cfgNum(key string, def float64) float64 {
	if v, ok := o.cfg.All()[key].(float64); ok && v > 0 {
		return v
	}
	return def
}

func (o *OpenAPI) ScopeCatalog() []OpenScope { return openScopeCatalog }

// resolveAPIKeyExpiry applies the single lifecycle policy used by both the
// browser management API and programmatic callers. A nil requested time means
// the configured default; there is deliberately no permanent-key option.
func (o *OpenAPI) resolveAPIKeyExpiry(now time.Time, requested *time.Time) (int64, error) {
	defaultS := o.cfgNum("open_api_key_default_lifetime_s", defaultAPIKeyLifetime.Seconds())
	maxS := o.cfgNum("open_api_key_max_lifetime_s", maxAPIKeyLifetime.Seconds())
	if defaultS <= 0 || maxS <= 0 || defaultS > maxS || maxS > maxAPIKeyLifetime.Seconds() {
		return 0, fmt.Errorf("API Key 有效期策略配置无效：默认有效期必须不大于最大有效期，且最大不超过 365 天")
	}
	if requested == nil {
		exp := now.Add(time.Duration(int64(defaultS)) * time.Second)
		return exp.UnixNano(), nil
	}
	exp := requested.UTC()
	if !exp.After(now) {
		return 0, fmt.Errorf("API Key 到期时间必须晚于当前时间")
	}
	if exp.After(now.Add(time.Duration(int64(maxS)) * time.Second)) {
		return 0, fmt.Errorf("API Key 到期时间不能超过最大有效期（%.0f 秒）", maxS)
	}
	return exp.UnixNano(), nil
}

// parseAPIKeyExpiry parses the management API representation. The two input
// forms are intentionally mutually exclusive: RFC3339 expires_at is useful
// for an expiry agreed with an external platform, while expires_in_s is safer
// for clients that do not have a synchronized wall-clock.
func (o *OpenAPI) parseAPIKeyExpiry(body map[string]any) (*time.Time, bool, error) {
	_, hasAt := body["expires_at"]
	_, hasIn := body["expires_in_s"]
	if !hasAt && !hasIn {
		return nil, false, nil
	}
	if hasAt && hasIn {
		return nil, true, fmt.Errorf("expires_at 与 expires_in_s 只能填写一个")
	}
	if hasAt {
		s, ok := body["expires_at"].(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, true, fmt.Errorf("expires_at 必须是 RFC3339 时间字符串")
		}
		exp, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
		if err != nil {
			return nil, true, fmt.Errorf("expires_at 必须是 RFC3339 时间字符串：%w", err)
		}
		return &exp, true, nil
	}
	seconds, ok := body["expires_in_s"].(float64)
	if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || math.Trunc(seconds) != seconds || seconds > maxAPIKeyLifetime.Seconds() {
		return nil, true, fmt.Errorf("expires_in_s 必须是 1 到 31536000 的整数")
	}
	exp := time.Now().UTC().Add(time.Duration(int64(seconds)) * time.Second)
	return &exp, true, nil
}

// CreateKey 新建 Key；未指定到期时间时使用默认有效期。
func (o *OpenAPI) CreateKey(name, remark string, scopes, vehicles, ips []string, actor string) (*APIKey, string, error) {
	return o.CreateKeyWithExpiry(name, remark, scopes, vehicles, ips, actor, nil)
}

// ---------- Key 生命周期（管理面，仅限管理员角色调用） ----------

func dedupeStr(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func trimRune(s string, n int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) > n {
		return string(rs[:n])
	}
	return string(rs)
}

// CreateKey 新建 Key；明文只返回这一次。scopes/vehicles/ips 即白名单：
// 未选的功能/车辆一律不允许；来源 IP 不在名单内一律拒绝（ips 为空 = 不限来源）。
func (o *OpenAPI) CreateKeyWithExpiry(name, remark string, scopes, vehicles, ips []string, actor string, requestedExpiry *time.Time) (*APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "open-api"
	}
	if len([]rune(name)) > 100 {
		return nil, "", fmt.Errorf("Key 名称不能超过 100 个字符")
	}
	scopes = dedupeStr(scopes)
	for _, s := range scopes {
		if !validScope(s) {
			return nil, "", fmt.Errorf("未知功能权限：%s", s)
		}
	}
	vehicles = dedupeStr(vehicles)
	ips = dedupeStr(ips)
	if err := validateIPs(ips); err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	expiresNS, err := o.resolveAPIKeyExpiry(now, requestedExpiry)
	if err != nil {
		return nil, "", err
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	secret := "ra_ak_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))
	k := &APIKey{
		ID: "key-" + hex.EncodeToString(sum[:4]), Name: name, Remark: trimRune(remark, 200),
		Prefix: secret[:12], KeyHash: hex.EncodeToString(sum[:]),
		Scopes: scopes, Vehicles: vehicles, IPs: ips, CreatedNS: now.UnixNano(), ExpiresNS: expiresNS,
	}
	if k.Scopes == nil {
		k.Scopes = []string{}
	}
	if k.Vehicles == nil {
		k.Vehicles = []string{}
	}
	if k.IPs == nil {
		k.IPs = []string{}
	}
	// PostgreSQL is authoritative whenever it is configured. Persist before
	// publishing the in-memory projection, so a successful response can never
	// describe a key that disappears on restart.
	if err := o.persistKey(k, actor); err != nil {
		return nil, "", fmt.Errorf("API Key 持久化失败：%w", err)
	}
	o.mu.Lock()
	if _, exists := o.keys[k.ID]; exists {
		o.mu.Unlock()
		return nil, "", fmt.Errorf("API Key ID 冲突，请重试")
	}
	o.keys[k.ID] = k
	o.byHash[k.KeyHash] = k.ID
	o.mu.Unlock()
	o.logAudit(AuditEntry{TsNS: k.CreatedNS, KeyID: k.ID, KeyName: name,
		Method: "-", Path: "/api/openkeys", Result: "ok", HTTP: 200,
		Remark: k.Remark, Detail: fmt.Sprintf("创建 API Key（操作人 %s）", actor)})
	return k, secret, nil
}

// KeyPatch 更新 Key（名称/备注/功能白名单/车辆白名单），nil 字段保持不变。
type KeyPatch struct {
	Name      *string
	Remark    *string
	Scopes    *[]string
	Vehicles  *[]string
	IPs       *[]string
	ExpiresNS *int64
}

func (o *OpenAPI) UpdateKey(id string, p KeyPatch, actor string) (*APIKey, error) {
	o.mu.Lock()
	current, ok := o.keys[id]
	if !ok {
		o.mu.Unlock()
		return nil, fmt.Errorf("key 不存在")
	}
	if current.RevokedNS != 0 {
		o.mu.Unlock()
		return nil, fmt.Errorf("已撤销的 Key 不可修改")
	}
	k := cloneAPIKey(current)
	if p.Name != nil && strings.TrimSpace(*p.Name) != "" {
		k.Name = strings.TrimSpace(*p.Name)
	} else if p.Name != nil {
		o.mu.Unlock()
		return nil, fmt.Errorf("Key 名称不能为空")
	}
	if len([]rune(k.Name)) > 100 {
		o.mu.Unlock()
		return nil, fmt.Errorf("Key 名称不能超过 100 个字符")
	}
	if p.Remark != nil {
		k.Remark = trimRune(*p.Remark, 200)
	}
	if p.Scopes != nil {
		sc := dedupeStr(*p.Scopes)
		for _, s := range sc {
			if !validScope(s) {
				o.mu.Unlock()
				return nil, fmt.Errorf("未知功能权限：%s", s)
			}
		}
		if sc == nil {
			sc = []string{}
		}
		k.Scopes = sc
	}
	if p.Vehicles != nil {
		vh := dedupeStr(*p.Vehicles)
		if vh == nil {
			vh = []string{}
		}
		k.Vehicles = vh
	}
	if p.IPs != nil {
		ip := dedupeStr(*p.IPs)
		if err := validateIPs(ip); err != nil {
			o.mu.Unlock()
			return nil, err
		}
		if ip == nil {
			ip = []string{}
		}
		k.IPs = ip
	}
	if p.ExpiresNS != nil {
		exp := time.Unix(0, *p.ExpiresNS)
		expiresNS, err := o.resolveAPIKeyExpiry(time.Now().UTC(), &exp)
		if err != nil {
			o.mu.Unlock()
			return nil, err
		}
		k.ExpiresNS = expiresNS
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	err := o.persistKeyUpdate(ctx, k)
	cancel()
	if err != nil {
		o.mu.Unlock()
		return nil, fmt.Errorf("API Key 更新未持久化：%w", err)
	}
	*current = *k
	name := current.Name
	out := cloneAPIKey(current)
	o.mu.Unlock()
	o.logAudit(AuditEntry{TsNS: time.Now().UnixNano(), KeyID: id, KeyName: name,
		Method: "-", Path: "/api/openkeys/" + id + "/update", Result: "ok", HTTP: 200,
		Detail: fmt.Sprintf("修改 API Key 权限/备注（操作人 %s）", actor)})
	return out, nil
}

func (o *OpenAPI) ListKeys() []*APIKey {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]*APIKey, 0, len(o.keys))
	for _, k := range o.keys {
		out = append(out, cloneAPIKey(k))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedNS > out[j].CreatedNS })
	return out
}

func (o *OpenAPI) Revoke(id, actor string) error {
	o.mu.Lock()
	k, ok := o.keys[id]
	if !ok {
		o.mu.Unlock()
		return fmt.Errorf("key 不存在")
	}
	if k.RevokedNS != 0 {
		o.mu.Unlock()
		return fmt.Errorf("Key 已撤销")
	}
	now := time.Now().UnixNano()
	if db := o.database(); db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		res, err := db.ExecContext(ctx, "UPDATE api_keys SET revoked_at=to_timestamp($2::double precision) WHERE id=$1 AND revoked_at IS NULL", id, float64(now)/1e9)
		cancel()
		if err != nil {
			o.mu.Unlock()
			return fmt.Errorf("API Key 撤销未持久化：%w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			o.mu.Unlock()
			if err != nil {
				return fmt.Errorf("确认 API Key 撤销结果失败：%w", err)
			}
			return fmt.Errorf("API Key 已在数据库中不存在或已撤销")
		}
	}
	k.RevokedNS = now
	delete(o.byHash, k.KeyHash)
	name := k.Name
	o.mu.Unlock()
	o.logAudit(AuditEntry{TsNS: time.Now().UnixNano(), KeyID: id, KeyName: name,
		Method: "-", Path: "/api/openkeys/" + id + "/revoke", Result: "ok", HTTP: 200,
		Detail: fmt.Sprintf("撤销 API Key（操作人 %s）", actor)})
	return nil
}

// ---------- 审计 ----------

func (o *OpenAPI) logAudit(e AuditEntry) {
	if e.Source == "" {
		e.Source = "api"
	}
	o.mu.Lock()
	o.audit = append([]AuditEntry{e}, o.audit...)
	if len(o.audit) > 500 {
		o.audit = o.audit[:500]
	}
	o.mu.Unlock()
	// 审计落库（等保三级「安全审计」：记录留存 >= 6 个月由运维策略保证）。
	// 审计证据不能走满载可丢弃的遥测队列；在响应返回前完成一次有界写入
	// 尝试。数据库不可用时记录明确错误，readyz 会保持未就绪。
	if db := o.database(); db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx,
			"INSERT INTO audit_log(ts, key_id, key_name, method, path, result, http_code, ip, trace_id, vehicle_id, remark, detail) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)",
			time.Unix(0, e.TsNS), e.KeyID, e.KeyName, e.Method, e.Path, e.Result, e.HTTP, nullable(e.IP), e.TraceID, e.VehicleID, e.Remark, e.Detail); err != nil {
			log.Printf("audit_log 入库失败: %v", err)
		}
	}
}

func (o *OpenAPI) auditMemoryPage(f AuditFilter) auditPage {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	o.mu.Lock()
	memory := append([]AuditEntry(nil), o.audit...)
	o.mu.Unlock()
	sort.SliceStable(memory, func(i, j int) bool {
		if memory[i].TsNS != memory[j].TsNS {
			return memory[i].TsNS > memory[j].TsNS
		}
		return memory[i].ID > memory[j].ID
	})
	out := make([]AuditEntry, 0, minInt(limit, len(memory)))
	for _, e := range memory {
		if f.Source != "" && f.Source != "all" && e.Source != f.Source {
			continue
		}
		if f.KeyID != "" && e.KeyID != f.KeyID {
			continue
		}
		if f.Result != "" && e.Result != f.Result {
			continue
		}
		if f.Cursor != "" {
			cursor, err := decodeAuditCursor(f.Cursor)
			if err != nil || e.TsNS < cursor.TsNS || (e.TsNS == cursor.TsNS && e.ID < cursor.ID) {
				if err != nil {
					return auditPage{}
				}
			} else {
				continue
			}
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return auditPage{Entries: out}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// AuditPage reads the durable API-call evidence on every query. PostgreSQL is
// the pagination authority; the bounded in-memory ring is only a safe fallback
// during a temporary read outage and never fabricates a cursor for it.
func (o *OpenAPI) AuditPage(f AuditFilter) (auditPage, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	f.Limit = limit
	if strings.TrimSpace(f.Source) == "" {
		f.Source = "api"
	}
	durable, err := o.queryAuditDBPage(f)
	if err == nil {
		return durable, nil
	}
	if strings.TrimSpace(f.Cursor) != "" {
		if _, cursorErr := decodeAuditCursor(f.Cursor); cursorErr != nil {
			return auditPage{}, cursorErr
		}
	}
	return o.auditMemoryPage(f), nil
}

// Audit 按条件查询审计（头最新）：可按 Key / 结果过滤。
func (o *OpenAPI) Audit(f AuditFilter) []AuditEntry {
	page, _ := o.AuditPage(f)
	return page.Entries
}

// ---------- 鉴权（签名四件套 + 抗重放 + 限流 + 失败锁定） ----------

func remoteIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// clientIP trusts X-Forwarded-For only when the immediate peer belongs to an
// explicitly configured reverse-proxy CIDR. Direct callers cannot spoof the
// source address by adding a forwarding header. The default is intentionally
// empty; deployments behind ingress must configure trusted_proxy_cidrs.
func (o *OpenAPI) clientIP(r *http.Request) string {
	peer := remoteIP(r)
	if o == nil || o.cfg == nil {
		return peer
	}
	settings := o.cfg.All()
	trusted, _ := settings["trusted_proxy_cidrs"].(string)
	peerIP := net.ParseIP(strings.Trim(peer, "[]"))
	if peerIP == nil {
		return peer
	}
	for _, raw := range strings.Split(trusted, ",") {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err == nil && network.Contains(peerIP) {
			for _, forwarded := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
				if ip := net.ParseIP(strings.TrimSpace(forwarded)); ip != nil {
					return ip.String()
				}
			}
			return peer
		}
	}
	return peer
}

// locked 来源（IP 或 Key）是否处于失败锁定期。
func (o *OpenAPI) locked(ip, keyID string) (string, int64) {
	nowU := time.Now().Unix()
	o.mu.Lock()
	defer o.mu.Unlock()
	targets := []string{"ip:" + ip}
	if keyID != "" {
		targets = append(targets, "id:"+keyID)
	}
	for _, t := range targets {
		if e := o.locks[t]; e != nil {
			if e.until > nowU {
				return "鉴权失败次数过多，来源已临时锁定", e.until - nowU
			}
			if e.until > 0 {
				delete(o.locks, t) // 锁定期已过，清记录
			}
		}
	}
	return "", 0
}

// recordFail 记一次鉴权失败；窗口内累计超阈值即锁定（等保三级：登录失败处理）。
func (o *OpenAPI) recordFail(targets ...string) {
	th := int(o.cfgNum("open_lockout_threshold", 5))
	win := int64(o.cfgNum("open_lockout_window_s", 900))
	lockS := int64(o.cfgNum("open_lockout_seconds", 300))
	nowU := time.Now().Unix()
	o.mu.Lock()
	defer o.mu.Unlock()
	for t, e := range o.locks { // 顺手清理早已过期的锁记录
		if e != nil && e.until > 0 && e.until < nowU-3600 {
			delete(o.locks, t)
		}
	}
	for _, t := range targets {
		e := o.locks[t]
		if e == nil {
			e = &lockEntry{}
			o.locks[t] = e
		}
		if e.until > nowU {
			continue // 已锁定期间不再累计
		}
		kept := e.fails[:0]
		for _, ts := range e.fails {
			if ts > nowU-win {
				kept = append(kept, ts)
			}
		}
		e.fails = append(kept, nowU)
		if len(e.fails) >= th {
			e.until = nowU + lockS
			e.fails = nil
		}
	}
}

// authenticate 校验签名四件套；通过返回 Key。
// 返回 (key, 失败原因, 审计结果码)。
func (o *OpenAPI) authenticate(r *http.Request, body []byte) (*APIKey, string, string) {
	ip := o.clientIP(r)
	keyID := r.Header.Get("X-API-Key")
	tsStr := r.Header.Get("X-Timestamp")
	nonce := r.Header.Get("X-Nonce")
	sig := r.Header.Get("X-Signature")

	if why, left := o.locked(ip, keyID); why != "" {
		return nil, fmt.Sprintf("%s（剩余 %ds）", why, left), "locked"
	}
	failTargets := []string{"ip:" + ip}
	if keyID != "" {
		failTargets = append(failTargets, "id:"+keyID)
	}
	fail := func(msg string) (*APIKey, string, string) {
		o.recordFail(failTargets...)
		return nil, msg, "auth_failed"
	}

	if keyID == "" || tsStr == "" || nonce == "" || sig == "" {
		return fail("缺少签名头（X-API-Key/X-Timestamp/X-Nonce/X-Signature）")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return fail("X-Timestamp 非法")
	}
	win := o.cfgNum("replay_window_s", 300)
	if d := time.Now().Unix() - ts; d > int64(win) || d < -int64(win) {
		return fail(fmt.Sprintf("时间戳超出允许窗口（±%.0fs）", win))
	}
	o.mu.Lock()
	stored, ok := o.keys[keyID]
	if !ok || stored.RevokedNS > 0 {
		o.mu.Unlock()
		return fail("API Key 不存在或已撤销")
	}
	if stored.ExpiresNS <= 0 || stored.ExpiresNS <= time.Now().UnixNano() {
		k := cloneAPIKey(stored)
		o.mu.Unlock()
		return k, "API Key 已过期", "expired"
	}
	if !ipAllowed(stored, ip) { // 来源 IP 白名单：固定允许的一个或多个 IP/网段
		o.mu.Unlock()
		return fail(fmt.Sprintf("来源 IP %s 不在该 Key 的允许名单内", ip))
	}
	if exp, seen := o.nonces[nonce]; seen && exp > time.Now().Unix() {
		o.mu.Unlock()
		o.recordFail(failTargets...)
		return nil, "nonce 重放", "replay"
	}
	o.nonces[nonce] = time.Now().Unix() + 600
	nowU := time.Now().Unix() // 顺手清过期 nonce
	for n, exp := range o.nonces {
		if exp <= nowU {
			delete(o.nonces, n)
		}
	}
	o.mu.Unlock()

	sum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(stored.KeyHash)) // 密钥派生：以哈希为 HMAC 密钥（服务端不落明文）
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s\n%s\n",
		r.Method, r.URL.Path, tsStr, nonce, hex.EncodeToString(sum[:]))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return fail("签名校验失败")
	}

	// 限流：每 Key 10s 窗口
	limit := int(o.cfgNum("rate_limit", 20))
	nowNS := time.Now().UnixNano()
	o.mu.Lock()
	recent := o.hits[keyID]
	cut := nowNS - int64(10*time.Second)
	kept := recent[:0]
	for _, t := range recent {
		if t > cut {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		o.mu.Unlock()
		return nil, fmt.Sprintf("限流：每 10s 最多 %d 次", limit), "rate_limited"
	}
	o.hits[keyID] = append(kept, nowNS)
	stored.LastUsedNS = nowNS
	delete(o.locks, "id:"+keyID) // 鉴权通过：清除该 Key 与来源 IP 的失败计数
	delete(o.locks, "ip:"+ip)
	k := cloneAPIKey(stored)
	o.mu.Unlock()
	o.touchKeyLastUsed(keyID, nowNS)
	return k, "", ""
}

func (o *OpenAPI) touchKeyLastUsed(id string, ns int64) {
	db := o.database()
	if db == nil || id == "" || ns <= 0 {
		return
	}
	o.st.submitDB(func(ctx context.Context, db *sql.DB) {
		if _, err := db.ExecContext(ctx, "UPDATE api_keys SET last_used_at=$2 WHERE id=$1", id, time.Unix(0, ns).UTC()); err != nil {
			log.Printf("[openapi] 更新 API Key 最近使用时间失败: %v", err)
		}
	})
}

// ---------- 权限判定（功能白名单 + 车辆白名单，默认拒绝） ----------

func (o *OpenAPI) hasScope(k *APIKey, scope string) bool {
	for _, s := range k.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func (o *OpenAPI) vehicleAllowed(k *APIKey, vid string) bool {
	for _, v := range k.Vehicles {
		if v == "*" || v == vid {
			return true
		}
	}
	return false
}

// ---------- 请求辅助 ----------

// readRawBody 读取原始请求体（超限返回 nil）。
func readRawBody(w http.ResponseWriter, r *http.Request, limit int64) []byte {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil
	}
	return b
}

// extractRemark 提取调用备注：X-Remark 头 > ?remark= > body.remark；截断 200 字。
func extractRemark(r *http.Request, body []byte) string {
	rem := r.Header.Get("X-Remark")
	if rem == "" {
		rem = r.URL.Query().Get("remark")
	}
	if rem == "" && len(body) > 0 {
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			if s, ok := m["remark"].(string); ok {
				rem = s
			}
		}
	}
	if dec, err := url.QueryUnescape(rem); err == nil {
		rem = dec // 支持 URL 编码的中文备注
	}
	return trimRune(rem, 200)
}

// auditOK / auditErr：统一响应 + 审计落账。
func (o *OpenAPI) auditOK(w http.ResponseWriter, r *http.Request, key *APIKey, traceID, vehicleID, remark string, data any) {
	if traceID != "" {
		w.Header().Set("X-Trace-ID", traceID)
	}
	o.logAudit(AuditEntry{TsNS: time.Now().UnixNano(), KeyID: key.ID, KeyName: key.Name,
		Method: r.Method, Path: r.URL.Path, Result: "ok", HTTP: 200, IP: o.clientIP(r),
		TraceID: traceID, VehicleID: vehicleID, Remark: remark})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": "0", "data": data})
}

func (o *OpenAPI) auditErr(w http.ResponseWriter, r *http.Request, key *APIKey, httpCode int, code, result, msg, vehicleID, remark string) {
	kid, kname := "", ""
	if key != nil {
		kid, kname = key.ID, key.Name
	}
	o.logAudit(AuditEntry{TsNS: time.Now().UnixNano(), KeyID: kid, KeyName: kname,
		Method: r.Method, Path: r.URL.Path, Result: result, HTTP: httpCode, IP: o.clientIP(r),
		VehicleID: vehicleID, Remark: remark, Detail: msg})
	if code == "RATE_LIMITED" {
		w.Header().Set("Retry-After", "10")
	}
	writeJSON(w, httpCode, map[string]any{"ok": false, "code": code, "message": msg})
}

// ---------- 开放接口面（/open/v1/*） ----------
//
// 每个接口绑定一个功能权限（scope）：Key 未勾选该功能即 403（默认拒绝）；
// 涉及车辆的接口再过一遍车辆白名单（数据隔离，跨车 403）。

// vehicleSummary 车辆列表用的摘要视图（遥测全量走 /telemetry）。
type vehicleSummary struct {
	VehicleID         string  `json:"vehicle_id"`
	Group             string  `json:"group"`
	Online            bool    `json:"online"`
	Mode              string  `json:"mode"`
	SpeedMPS          float64 `json:"speed_mps"`
	SOC               float64 `json:"soc"`
	Gear              string  `json:"gear"`
	Gps               GpsSnap `json:"gps"`
	Pose              Pose    `json:"pose"`
	LastHeartbeatAgeS float64 `json:"last_heartbeat_age_s"`
}

func summarize(v VehicleSnap) vehicleSummary {
	return vehicleSummary{VehicleID: v.VehicleID, Group: v.Group, Online: v.Online,
		Mode: v.Mode, SpeedMPS: v.SpeedMPS, SOC: v.SOC, Gear: v.Gear,
		Gps: v.Gps, Pose: v.Pose, LastHeartbeatAgeS: v.LastHeartbeatAgeS}
}

func (o *OpenAPI) denyScope(w http.ResponseWriter, r *http.Request, key *APIKey, scope, remark string) {
	o.auditErr(w, r, key, http.StatusForbidden, "PERMISSION_DENIED", "denied",
		fmt.Sprintf("该 Key 未开通功能权限 %s（未勾选的功能默认不允许）", scope), "", remark)
}

func (o *OpenAPI) denyVehicle(w http.ResponseWriter, r *http.Request, key *APIKey, vid, remark string) {
	o.auditErr(w, r, key, http.StatusForbidden, "VEHICLE_DENIED", "denied",
		"该 Key 无权访问车辆 "+vid+"（车辆白名单隔离）", vid, remark)
}

// ServeOpen 处理 /open/v1/*（对外开放面）。
func (o *OpenAPI) ServeOpen(w http.ResponseWriter, r *http.Request) {
	if v, ok := o.cfg.All()["open_api_enabled"].(bool); ok && !v {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "code": "DISABLED", "message": "开放 API 已被管理员停用"})
		return
	}
	body := readRawBody(w, r, 1<<20)
	if body == nil {
		o.auditErr(w, r, nil, http.StatusBadRequest, "BAD_REQUEST", "bad_request", "请求体过大或读取失败", "", "")
		return
	}
	remark := extractRemark(r, body)
	key, why, res := o.authenticate(r, body)
	if res == "expired" {
		o.auditErr(w, r, key, http.StatusUnauthorized, "EXPIRED", res, why, "", remark)
		return
	}
	if key == nil {
		code, httpCode := "AUTH_FAILED", http.StatusUnauthorized
		switch res {
		case "replay":
			code = "REPLAY"
		case "rate_limited":
			code, httpCode = "RATE_LIMITED", http.StatusTooManyRequests
		case "locked":
			code, httpCode = "LOCKED", http.StatusLocked
		case "expired":
			code = "EXPIRED"
		}
		o.auditErr(w, r, nil, httpCode, code, res, why, "", remark)
		return
	}
	o.serveRoute(w, r, key, body, remark)
}

func (o *OpenAPI) serveRoute(w http.ResponseWriter, r *http.Request, key *APIKey, body []byte, remark string) {
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segs) < 3 {
		o.auditErr(w, r, key, http.StatusNotFound, "NOT_FOUND", "bad_request", "未知开放接口", "", remark)
		return
	}
	rest := segs[2:]
	m := r.Method
	trace := r.URL.Query().Get("trace_id")

	switch {
	// —— 自检：服务器时间（对时）+ 本 Key 的权限画像 ——
	case m == "GET" && len(rest) == 1 && rest[0] == "meta":
		o.auditOK(w, r, key, trace, "", remark, map[string]any{
			"server_time_unix": time.Now().Unix(),
			"key":              map[string]any{"id": key.ID, "name": key.Name, "scopes": key.Scopes, "vehicles": key.Vehicles, "ips": key.IPs, "expires_ns": key.ExpiresNS},
			"limits": map[string]any{
				"rate_limit_per_10s":     int(o.cfgNum("rate_limit", 20)),
				"replay_window_s":        o.cfgNum("replay_window_s", 300),
				"key_default_lifetime_s": o.cfgNum("open_api_key_default_lifetime_s", defaultAPIKeyLifetime.Seconds()),
				"key_max_lifetime_s":     o.cfgNum("open_api_key_max_lifetime_s", maxAPIKeyLifetime.Seconds()),
			},
		})

	// —— 车辆列表（只返回白名单内车辆） ——
	case m == "GET" && len(rest) == 1 && rest[0] == "vehicles":
		if !o.hasScope(key, "vehicle.read") {
			o.denyScope(w, r, key, "vehicle.read", remark)
			return
		}
		out := []vehicleSummary{}
		for _, v := range o.st.Snapshot().Vehicles {
			if o.vehicleAllowed(key, v.VehicleID) {
				out = append(out, summarize(v))
			}
		}
		o.auditOK(w, r, key, trace, "", remark, out)

	// —— 单车详情（基本资料 + 能力声明） ——
	case m == "GET" && len(rest) == 2 && rest[0] == "vehicles":
		vid := rest[1]
		if !o.hasScope(key, "vehicle.read") {
			o.denyScope(w, r, key, "vehicle.read", remark)
			return
		}
		if !o.vehicleAllowed(key, vid) {
			o.denyVehicle(w, r, key, vid, remark)
			return
		}
		v, ok := o.st.Vehicle(vid)
		if !ok {
			o.auditErr(w, r, key, http.StatusNotFound, "NOT_FOUND", "bad_request", "车辆不存在", vid, remark)
			return
		}
		o.auditOK(w, r, key, trace, vid, remark, map[string]any{
			"vehicle_id": v.VehicleID, "group": v.Group, "chassis": v.Chassis,
			"online": v.Online, "mode": v.Mode, "capabilities": v.Capabilities,
			"last_heartbeat_age_s": v.LastHeartbeatAgeS,
		})

	// —— 单车遥测全量（实时值 + 历史曲线 + GPS） ——
	case m == "GET" && len(rest) == 3 && rest[0] == "vehicles" && rest[2] == "telemetry":
		vid := rest[1]
		if !o.hasScope(key, "telemetry.read") {
			o.denyScope(w, r, key, "telemetry.read", remark)
			return
		}
		if !o.vehicleAllowed(key, vid) {
			o.denyVehicle(w, r, key, vid, remark)
			return
		}
		v, ok := o.st.Vehicle(vid)
		if !ok {
			o.auditErr(w, r, key, http.StatusNotFound, "NOT_FOUND", "bad_request", "车辆不存在", vid, remark)
			return
		}
		o.auditOK(w, r, key, trace, vid, remark, v)

	// —— 单车告警与事件（可选 ?level= 过滤） ——
	case m == "GET" && len(rest) == 3 && rest[0] == "vehicles" && rest[2] == "alarms":
		vid := rest[1]
		if !o.hasScope(key, "alarm.read") {
			o.denyScope(w, r, key, "alarm.read", remark)
			return
		}
		if !o.vehicleAllowed(key, vid) {
			o.denyVehicle(w, r, key, vid, remark)
			return
		}
		level := r.URL.Query().Get("level")
		limit := 50
		if q := r.URL.Query().Get("limit"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		evs := []EventSnap{}
		for _, e := range o.st.Snapshot().Events {
			if e.VehicleID != vid {
				continue
			}
			if level != "" && e.Level != level {
				continue
			}
			evs = append(evs, e)
			if len(evs) >= limit {
				break
			}
		}
		o.auditOK(w, r, key, trace, vid, remark, map[string]any{"vehicle_id": vid, "events": evs})

	// —— 任务：某车最近一条循迹任务状态 ——
	case m == "GET" && len(rest) == 2 && rest[0] == "nav" && rest[1] == "status":
		if !o.hasScope(key, "task.read") {
			o.denyScope(w, r, key, "task.read", remark)
			return
		}
		vid := r.URL.Query().Get("vehicle_id")
		if vid == "" {
			o.auditErr(w, r, key, http.StatusBadRequest, "BAD_REQUEST", "bad_request", "vehicle_id 必填", "", remark)
			return
		}
		if !o.vehicleAllowed(key, vid) {
			o.denyVehicle(w, r, key, vid, remark)
			return
		}
		o.auditOK(w, r, key, trace, vid, remark, o.nav.ForVehicle(vid))

	// —— 任务列表（只含白名单车辆；可用 ?vehicle_id= 收窄） ——
	case m == "GET" && len(rest) == 2 && rest[0] == "nav" && rest[1] == "routes":
		if !o.hasScope(key, "task.read") {
			o.denyScope(w, r, key, "task.read", remark)
			return
		}
		vid := r.URL.Query().Get("vehicle_id")
		if vid != "" && !o.vehicleAllowed(key, vid) {
			o.denyVehicle(w, r, key, vid, remark)
			return
		}
		out := []*NavRoute{}
		for _, rt := range o.nav.List() {
			if !o.vehicleAllowed(key, rt.VehicleID) {
				continue
			}
			if vid != "" && rt.VehicleID != vid {
				continue
			}
			out = append(out, rt)
		}
		o.auditOK(w, r, key, trace, vid, remark, out)

	// —— 任务下发：某车从 A 点到 B 点（可带途经点 + 调用备注） ——
	case m == "POST" && len(rest) == 2 && rest[0] == "nav" && rest[1] == "command":
		if !o.hasScope(key, "task.write") {
			o.denyScope(w, r, key, "task.write", remark)
			return
		}
		var req struct {
			VehicleID string     `json:"vehicle_id"`
			Name      string     `json:"name"`
			TraceID   string     `json:"trace_id"`
			Remark    string     `json:"remark"`
			Points    []NavPoint `json:"points"`
			From      *NavPoint  `json:"from"`
			To        *NavPoint  `json:"to"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			o.auditErr(w, r, key, http.StatusBadRequest, "BAD_REQUEST", "bad_request", "JSON 解析失败", "", remark)
			return
		}
		if !o.vehicleAllowed(key, req.VehicleID) {
			o.denyVehicle(w, r, key, req.VehicleID, remark)
			return
		}
		pts := req.Points
		if req.From != nil && req.To != nil {
			pts = append([]NavPoint{*req.From}, pts...)
			pts = append(pts, *req.To)
		}
		tid := req.TraceID
		if tid == "" {
			tid = trace
		}
		rt, err := o.nav.Submit(req.VehicleID, req.Name, "open_api", tid, remark, pts)
		if err != nil {
			o.auditErr(w, r, key, http.StatusBadRequest, "BAD_REQUEST", "bad_request", err.Error(), req.VehicleID, remark)
			return
		}
		if o.st != nil {
			o.st.pushEvent("info", req.VehicleID, fmt.Sprintf("开放 API（%s）下发循迹任务 %s%s", key.Name, rt.ID, remarkSuffix(remark)), "sys")
		}
		o.auditOK(w, r, key, tid, req.VehicleID, remark, rt)

	// —— 任务取消 ——
	case m == "POST" && len(rest) == 2 && rest[0] == "nav" && rest[1] == "cancel":
		if !o.hasScope(key, "task.write") {
			o.denyScope(w, r, key, "task.write", remark)
			return
		}
		var req struct {
			VehicleID string `json:"vehicle_id"`
			RouteID   string `json:"route_id"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			o.auditErr(w, r, key, http.StatusBadRequest, "BAD_REQUEST", "bad_request", "JSON 解析失败", "", remark)
			return
		}
		id := req.RouteID
		if id == "" {
			if req.VehicleID == "" {
				o.auditErr(w, r, key, http.StatusBadRequest, "BAD_REQUEST", "bad_request", "vehicle_id 或 route_id 必填其一", "", remark)
				return
			}
			if !o.vehicleAllowed(key, req.VehicleID) {
				o.denyVehicle(w, r, key, req.VehicleID, remark)
				return
			}
			if rt := o.nav.ForVehicle(req.VehicleID); rt != nil {
				id = rt.ID
			}
		}
		if id == "" {
			o.auditErr(w, r, key, http.StatusBadRequest, "BAD_REQUEST", "bad_request", "无可取消的任务", req.VehicleID, remark)
			return
		}
		rt, ok := o.nav.Get(id)
		if !ok {
			o.auditErr(w, r, key, http.StatusBadRequest, "BAD_REQUEST", "bad_request", "任务不存在："+id, req.VehicleID, remark)
			return
		}
		if !o.vehicleAllowed(key, rt.VehicleID) { // 不允许借 route_id 越权取消他人车辆任务
			o.denyVehicle(w, r, key, rt.VehicleID, remark)
			return
		}
		rt, err := o.nav.Cancel(id)
		if err != nil {
			o.auditErr(w, r, key, http.StatusBadRequest, "BAD_REQUEST", "bad_request", err.Error(), rt.VehicleID, remark)
			return
		}
		o.auditOK(w, r, key, trace, rt.VehicleID, remark, rt)

	// —— 安全控制：紧急停车（高危，须单独授予 control.write） ——
	case m == "POST" && len(rest) == 2 && rest[0] == "control" && rest[1] == "emergency-stop":
		if !o.hasScope(key, "control.write") {
			o.denyScope(w, r, key, "control.write", remark)
			return
		}
		var req struct {
			VehicleID string `json:"vehicle_id"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.VehicleID == "" {
			o.auditErr(w, r, key, http.StatusBadRequest, "BAD_REQUEST", "bad_request", "vehicle_id 必填", req.VehicleID, remark)
			return
		}
		if !o.vehicleAllowed(key, req.VehicleID) {
			o.denyVehicle(w, r, key, req.VehicleID, remark)
			return
		}
		v, exists := o.st.Vehicle(req.VehicleID)
		if !exists || !v.Online {
			o.auditErr(w, r, key, http.StatusConflict, "VEHICLE_OFFLINE", "denied", "车辆不存在或离线，拒绝执行紧急停车", req.VehicleID, remark)
			return
		}
		if o.authority == nil {
			o.auditErr(w, r, key, http.StatusServiceUnavailable, "AUTHORITY_UNAVAILABLE", "error", "商用 Control Authority 未就绪", req.VehicleID, remark)
			return
		}
		err := o.authority.EmergencyStop(r.Context(), req.VehicleID, "openapi:"+key.ID)
		if err != nil {
			o.auditErr(w, r, key, http.StatusBadGateway, "ERROR", "error", "紧急停车指令送达失败："+err.Error(), req.VehicleID, remark)
			return
		}
		if o.st != nil {
			o.st.pushEvent("critical", req.VehicleID, fmt.Sprintf("开放 API（%s）触发紧急停车%s", key.Name, remarkSuffix(remark)), "sys")
		}
		o.auditOK(w, r, key, trace, req.VehicleID, remark, map[string]any{"ok": true, "action": "signed_lease_revoke"})

	default:
		o.auditErr(w, r, key, http.StatusNotFound, "NOT_FOUND", "bad_request", "未知开放接口", "", remark)
	}
}

func remarkSuffix(remark string) string {
	if remark == "" {
		return ""
	}
	return "（备注：" + remark + "）"
}

// ---------- 来源 IP 白名单 ----------

// validateIPs 校验来源 IP 名单：单项为 IP 或 CIDR 网段。
func validateIPs(ips []string) error {
	for _, s := range ips {
		if strings.Contains(s, "/") {
			if _, _, err := net.ParseCIDR(s); err != nil {
				return fmt.Errorf("来源 IP 格式不合法：%s（支持单个 IP 或 CIDR 网段）", s)
			}
			continue
		}
		if net.ParseIP(s) == nil {
			return fmt.Errorf("来源 IP 格式不合法：%s", s)
		}
	}
	return nil
}

// ipAllowed 来源 IP 是否在 Key 的允许名单内；名单为空 = 不限制。
func ipAllowed(k *APIKey, ip string) bool {
	if len(k.IPs) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	for _, item := range k.IPs {
		if item == ip {
			return true
		}
		if strings.Contains(item, "/") {
			if _, ipnet, err := net.ParseCIDR(item); err == nil && parsed != nil && ipnet.Contains(parsed) {
				return true
			}
		}
	}
	return false
}
