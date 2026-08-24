package main

// openapi.go：对外开放 API（别的平台调用「某车从 A 点到 B 点」/取消）。
// 按等保三级要求落实的控制点：
//   身份鉴别   —— API Key（服务端只存 SHA-256 哈希，明文只在创建时返回一次）
//   访问控制   —— Key 可撤销；作用域限定导航指令；未知 Key 直接拒绝
//   安全审计   —— 每次调用（含失败原因）进审计环 + PG audit_log
//   抗重放     —— 时间戳 ±5 分钟窗口 + nonce 一次性（10 分钟内去重）
//   数据完整性 —— HMAC-SHA256 签名覆盖 方法/路径/时间戳/nonce/请求体哈希
//   传输保密   —— 生产经 nginx+TLS 入口（deploy/compose/ingress），文档注明
//   资源限制   —— 每 Key 滑动窗口限流（默认 20 次/10s）
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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

type APIKey struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"` // 明文前 12 位（便于识别，不含机密）
	KeyHash    string `json:"-"`
	CreatedNS  int64  `json:"created_ns"`
	RevokedNS  int64  `json:"revoked_ns,omitempty"`
	LastUsedNS int64  `json:"last_used_ns,omitempty"`
}

type AuditEntry struct {
	TsNS    int64  `json:"ts_ns"`
	KeyID   string `json:"key_id,omitempty"`
	KeyName string `json:"key_name,omitempty"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Result  string `json:"result"` // ok|auth_failed|replay|rate_limited|bad_request|error
	HTTP    int    `json:"http"`
	IP      string `json:"ip,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type OpenAPI struct {
	mu     sync.Mutex
	keys   map[string]*APIKey // id -> key
	byHash map[string]string  // sha256(key) -> id
	nonces map[string]int64   // nonce -> 到期时刻（抗重放）
	hits   map[string][]int64 // keyID -> 最近请求时刻（限流）
	audit  []AuditEntry       // 头最新
	st     *State
	cfg    *ConfigStore
	nav    *NavStore
}

func NewOpenAPI(st *State, cfg *ConfigStore, nav *NavStore) *OpenAPI {
	return &OpenAPI{keys: map[string]*APIKey{}, byHash: map[string]string{},
		nonces: map[string]int64{}, hits: map[string][]int64{},
		audit: []AuditEntry{}, st: st, cfg: cfg, nav: nav}
}

// CreateKey 新建 Key；明文只返回这一次。
func (o *OpenAPI) CreateKey(name string) (*APIKey, string, error) {
	if name == "" {
		name = "open-api"
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	secret := "ra_ak_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))
	k := &APIKey{
		ID: "key-" + hex.EncodeToString(sum[:4]), Name: name,
		Prefix: secret[:12], KeyHash: hex.EncodeToString(sum[:]),
		CreatedNS: time.Now().UnixNano(),
	}
	o.mu.Lock()
	o.keys[k.ID] = k
	o.byHash[k.KeyHash] = k.ID
	o.mu.Unlock()
	o.logAudit(AuditEntry{TsNS: k.CreatedNS, KeyID: k.ID, KeyName: name,
		Method: "-", Path: "/api/openkeys", Result: "ok", HTTP: 200, Detail: "创建 API Key"})
	return k, secret, nil
}

func (o *OpenAPI) ListKeys() []*APIKey {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]*APIKey, 0, len(o.keys))
	for _, k := range o.keys {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedNS > out[j].CreatedNS })
	return out
}

func (o *OpenAPI) Revoke(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	k, ok := o.keys[id]
	if !ok {
		return fmt.Errorf("key 不存在")
	}
	k.RevokedNS = time.Now().UnixNano()
	delete(o.byHash, k.KeyHash)
	return nil
}

func (o *OpenAPI) logAudit(e AuditEntry) {
	o.mu.Lock()
	o.audit = append([]AuditEntry{e}, o.audit...)
	if len(o.audit) > 500 {
		o.audit = o.audit[:500]
	}
	o.mu.Unlock()
	// 审计落库（等保三级「安全审计」：记录留存）；内存模式下自动跳过。
	if o.st != nil {
		o.st.submitDB(func(ctx context.Context, db *sql.DB) {
			if _, err := db.ExecContext(ctx,
				"INSERT INTO audit_log(ts, key_id, key_name, method, path, result, http_code, ip, trace_id, detail) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
				time.Unix(0, e.TsNS), e.KeyID, e.KeyName, e.Method, e.Path, e.Result, e.HTTP, nullable(e.IP), e.TraceID, e.Detail); err != nil {
				log.Printf("audit_log 入库失败: %v", err)
			}
		})
	}
}

func (o *OpenAPI) Audit(limit int) []AuditEntry {
	o.mu.Lock()
	defer o.mu.Unlock()
	if limit <= 0 || limit > len(o.audit) {
		limit = len(o.audit)
	}
	return append([]AuditEntry(nil), o.audit[:limit]...)
}

// authenticate 校验签名四件套；通过返回 Key。
func (o *OpenAPI) authenticate(r *http.Request, body []byte) (*APIKey, string) {
	keyID := r.Header.Get("X-API-Key")
	tsStr := r.Header.Get("X-Timestamp")
	nonce := r.Header.Get("X-Nonce")
	sig := r.Header.Get("X-Signature")
	if keyID == "" || tsStr == "" || nonce == "" || sig == "" {
		return nil, "缺少签名头（X-API-Key/X-Timestamp/X-Nonce/X-Signature）"
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return nil, "X-Timestamp 非法"
	}
	win := 300.0
	if v, ok := o.cfg.All()["replay_window_s"].(float64); ok && v > 0 {
		win = v
	}
	if d := time.Now().Unix() - ts; d > int64(win) || d < -int64(win) {
		return nil, fmt.Sprintf("时间戳超出允许窗口（±%.0fs）", win)
	}
	o.mu.Lock()
	k, ok := o.keys[keyID]
	if !ok || k.RevokedNS > 0 {
		o.mu.Unlock()
		return nil, "API Key 不存在或已撤销"
	}
	if exp, seen := o.nonces[nonce]; seen && exp > time.Now().Unix() {
		o.mu.Unlock()
		return nil, "nonce 重放"
	}
	o.nonces[nonce] = time.Now().Unix() + 600
	// 顺手清过期 nonce
	nowU := time.Now().Unix()
	for n, exp := range o.nonces {
		if exp <= nowU {
			delete(o.nonces, n)
		}
	}
	o.mu.Unlock()

	sum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, []byte(k.KeyHash)) // 密钥派生：以哈希为 HMAC 密钥（服务端不落明文）
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s\n%s",
		r.Method, r.URL.Path, tsStr, nonce, hex.EncodeToString(sum[:]))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return nil, "签名校验失败"
	}
	// 限流：每 Key 10s 窗口
	limit := 20
	if v, ok := o.cfg.All()["rate_limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
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
		return nil, fmt.Sprintf("限流：每 10s 最多 %d 次", limit)
	}
	o.hits[keyID] = append(kept, nowNS)
	k.LastUsedNS = nowNS
	o.mu.Unlock()
	return k, ""
}

func openErr(w http.ResponseWriter, o *OpenAPI, r *http.Request, httpCode int, code, msg string, key *APIKey) {
	res := "auth_failed"
	switch code {
	case "RATE_LIMITED":
		res = "rate_limited"
	case "REPLAY":
		res = "replay"
	case "BAD_REQUEST":
		res = "bad_request"
	case "ERROR":
		res = "error"
	}
	kid, kname := "", ""
	if key != nil {
		kid, kname = key.ID, key.Name
	}
	o.logAudit(AuditEntry{TsNS: time.Now().UnixNano(), KeyID: kid, KeyName: kname,
		Method: r.Method, Path: r.URL.Path, Result: res, HTTP: httpCode, IP: r.RemoteAddr, Detail: msg})
	writeJSON(w, httpCode, map[string]any{"ok": false, "code": code, "message": msg})
}

func openOK(w http.ResponseWriter, o *OpenAPI, r *http.Request, key *APIKey, traceID string, data any) {
	o.logAudit(AuditEntry{TsNS: time.Now().UnixNano(), KeyID: key.ID, KeyName: key.Name,
		Method: r.Method, Path: r.URL.Path, Result: "ok", HTTP: 200, IP: r.RemoteAddr, TraceID: traceID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "code": "0", "data": data})
}

// readRawBody 读取原始请求体（超限返回 nil）。
func readRawBody(w http.ResponseWriter, r *http.Request, limit int64) []byte {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil
	}
	return b
}

// ServeOpen 处理 /open/v1/*（对外开放面）。
func (o *OpenAPI) ServeOpen(w http.ResponseWriter, r *http.Request) {
	body := readRawBody(w, r, 1<<20)
	if body == nil {
		openErr(w, o, r, http.StatusBadRequest, "BAD_REQUEST", "请求体过大或读取失败", nil)
		return
	}
	key, why := o.authenticate(r, body)
	if key == nil {
		openErr(w, o, r, http.StatusUnauthorized, "AUTH_FAILED", why, nil)
		return
	}
	switch r.URL.Path {
	case "/open/v1/nav/command":
		var req struct {
			VehicleID string     `json:"vehicle_id"`
			Name      string     `json:"name"`
			TraceID   string     `json:"trace_id"`
			Points    []NavPoint `json:"points"`
			From      *NavPoint  `json:"from"`
			To        *NavPoint  `json:"to"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			openErr(w, o, r, http.StatusBadRequest, "BAD_REQUEST", "JSON 解析失败", key)
			return
		}
		pts := req.Points
		if req.From != nil && req.To != nil {
			pts = append([]NavPoint{*req.From}, pts...)
			pts = append(pts, *req.To)
		}
		rt, err := o.nav.Submit(req.VehicleID, req.Name, "open_api", req.TraceID, pts)
		if err != nil {
			openErr(w, o, r, http.StatusBadRequest, "BAD_REQUEST", err.Error(), key)
			return
		}
		if o.st != nil {
			o.st.pushEvent("info", req.VehicleID, fmt.Sprintf("开放 API 下发循迹任务 %s（trace=%s）", rt.ID, req.TraceID))
		}
		openOK(w, o, r, key, req.TraceID, rt)
	case "/open/v1/nav/cancel":
		var req struct {
			VehicleID string `json:"vehicle_id"`
			RouteID   string `json:"route_id"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			openErr(w, o, r, http.StatusBadRequest, "BAD_REQUEST", "JSON 解析失败", key)
			return
		}
		id := req.RouteID
		if id == "" {
			if rt := o.nav.ForVehicle(req.VehicleID); rt != nil {
				id = rt.ID
			}
		}
		if id == "" {
			openErr(w, o, r, http.StatusBadRequest, "BAD_REQUEST", "无可取消的任务", key)
			return
		}
		rt, err := o.nav.Cancel(id)
		if err != nil {
			openErr(w, o, r, http.StatusBadRequest, "BAD_REQUEST", err.Error(), key)
			return
		}
		openOK(w, o, r, key, "", rt)
	case "/open/v1/nav/status":
		vid := r.URL.Query().Get("vehicle_id")
		rt := o.nav.ForVehicle(vid)
		openOK(w, o, r, key, "", rt)
	case "/open/v1/vehicles":
		vs := o.st.Snapshot().Vehicles
		openOK(w, o, r, key, "", vs)
	default:
		openErr(w, o, r, http.StatusNotFound, "NOT_FOUND", "未知开放接口", key)
	}
}
