package main

// openapi_test.go：开放 API 平台 v2 端到端自测（不落库、不起 MQTT）。
// 覆盖：签名鉴权 / 功能白名单 / 车辆白名单 / 调用备注 / 抗重放 / 失败锁定 / 撤销。

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformv1 "robot-agent/protocols/platform/v1"
)

// signReq 按对外文档同款算法给请求签名。
func signReq(req *http.Request, secret string, body []byte) {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	nonce := fmt.Sprintf("nonce-%d", time.Now().UnixNano())
	sum := sha256.Sum256(body)
	keyHash := sha256.Sum256([]byte(secret))
	mac := hmac.New(sha256.New, []byte(hex.EncodeToString(keyHash[:]))) // 签名密钥 = hex(SHA256(secret))，与服务端一致
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s\n%s\n", req.Method, req.URL.Path, ts, nonce, hex.EncodeToString(sum[:]))
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
}

func openFixture(t *testing.T) (*OpenAPI, *State) {
	t.Helper()
	hub := NewHub()
	st := NewState(hub, nil)
	st.HandleRegister("v1", "gw-1", nil, "g-1")
	st.HandleRegister("v2", "gw-2", nil, "g-2")
	cfg := NewConfigStore()
	nav := NewNavStore(st)
	// This unit test focuses on OpenAPI authorization and uses a deterministic
	// transport result. The production constructor leaves publish nil, so every
	// real command goes through the PostgreSQL outbox and MQTT.
	nav.publish = func(_ *platformv1.NavigationCommand) bool { return true }
	o := NewOpenAPI(st, cfg, nav)
	return o, st
}

func doOpen(t *testing.T, o *OpenAPI, keyID, secret, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if keyID != "" {
		req.Header.Set("X-API-Key", keyID)
		signReq(req, secret, raw)
	}
	w := httptest.NewRecorder()
	o.ServeOpen(w, req)
	return w
}

func TestOpenAPIv2(t *testing.T) {
	o, st := openFixture(t)

	// —— 建两个 Key：A 只读且只准 v1；B 可下发任务、全部车辆 ——
	kA, secA, err := o.CreateKey("平台A", "只读对接", []string{"vehicle.read"}, []string{"v1"}, nil, "tester")
	if err != nil {
		t.Fatal(err)
	}
	kB, secB, err := o.CreateKey("平台B", "调度对接", []string{"vehicle.read", "task.read", "task.write"}, []string{"*"}, nil, "tester")
	if err != nil {
		t.Fatal(err)
	}

	// 1. 车辆列表：A 只见白名单内车辆（车端/数据隔离）
	w := doOpen(t, o, kA.ID, secA, "GET", "/open/v1/vehicles", nil)
	if w.Code != 200 {
		t.Fatalf("vehicles A: want 200 got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []vehicleSummary `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].VehicleID != "v1" {
		t.Fatalf("A 应只见 v1，实得 %+v", resp.Data)
	}

	// 2. 功能白名单：A 未开通 task.write，下发被拒（默认拒绝）
	w = doOpen(t, o, kA.ID, secA, "POST", "/open/v1/nav/command", map[string]any{
		"vehicle_id": "v1",
		"from":       map[string]any{"name": "A", "x": 0, "y": 0},
		"to":         map[string]any{"name": "B", "x": 5, "y": 5},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("scope 拒绝: want 403 got %d: %s", w.Code, w.Body.String())
	}

	// 3. 车辆白名单：A 查询白名单外车辆的遥测被拒
	w = doOpen(t, o, kA.ID, secA, "GET", "/open/v1/vehicles/v2/telemetry", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("vehicle 拒绝: want 403 got %d: %s", w.Code, w.Body.String())
	}

	// 4. B 下发任务（带调用备注），任务与审计都带上 remark
	w = doOpen(t, o, kB.ID, secB, "POST", "/open/v1/nav/command", map[string]any{
		"vehicle_id": "v2", "trace_id": "trace-001", "remark": "夜班巡检第3轮",
		"from": map[string]any{"name": "A", "x": 0, "y": 0},
		"to":   map[string]any{"name": "B", "x": 20, "y": 5},
	})
	if w.Code != 200 {
		t.Fatalf("command B: want 200 got %d: %s", w.Code, w.Body.String())
	}
	rt := o.nav.ForVehicle("v2")
	if rt == nil || rt.Remark != "夜班巡检第3轮" {
		t.Fatalf("任务备注未落地: %+v", rt)
	}
	au := o.Audit(AuditFilter{Limit: 1})
	if len(au) != 1 || au[0].Remark != "夜班巡检第3轮" || au[0].VehicleID != "v2" || au[0].Result != "ok" {
		t.Fatalf("审计备注/车辆未落地: %+v", au)
	}

	// 5. 任务查询隔离：A（task.read 未开通）查任务被拒
	w = doOpen(t, o, kA.ID, secA, "GET", "/open/v1/nav/status?vehicle_id=v2", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("task.read 拒绝: want 403 got %d", w.Code)
	}

	// 6. 抗重放：同一请求（同 nonce）第二次被拒
	body := map[string]any{"vehicle_id": "v2"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/open/v1/nav/cancel", bytes.NewReader(raw))
	req.Header.Set("X-API-Key", kB.ID)
	signReq(req, secB, raw)
	w1 := httptest.NewRecorder()
	o.ServeOpen(w1, req)
	if w1.Code != 200 {
		t.Fatalf("cancel: want 200 got %d: %s", w1.Code, w1.Body.String())
	}
	req2 := httptest.NewRequest("POST", "/open/v1/nav/cancel", bytes.NewReader(raw))
	req2.Header.Set("X-API-Key", kB.ID)
	req2.Header.Set("X-Timestamp", req.Header.Get("X-Timestamp"))
	req2.Header.Set("X-Nonce", req.Header.Get("X-Nonce"))
	req2.Header.Set("X-Signature", req.Header.Get("X-Signature"))
	w2 := httptest.NewRecorder()
	o.ServeOpen(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("重放应被拒: got %d", w2.Code)
	}

	// 7. 失败锁定：错误签名连打 3 次（阈值调为 3）后来源被锁
	o.cfg.Set(map[string]any{"open_lockout_threshold": 3.0, "open_lockout_seconds": 60.0})
	o.mu.Lock()
	o.locks = map[string]*lockEntry{} // 清掉前面用例的失败计数，单独验证锁定
	o.mu.Unlock()
	for i := 0; i < 3; i++ {
		w = doOpen(t, o, kA.ID, "wrong-secret", "GET", "/open/v1/vehicles", nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误签名应为 401，实得 %d", i+1, w.Code)
		}
	}
	w = doOpen(t, o, kA.ID, secA, "GET", "/open/v1/vehicles", nil) // 正确签名也被锁（来源已锁）
	if w.Code != http.StatusLocked {
		t.Fatalf("锁定后应为 423，实得 %d: %s", w.Code, w.Body.String())
	}

	// 8. 撤销：撤销 B 后立即失效
	if err := o.Revoke(kB.ID, "tester"); err != nil {
		t.Fatal(err)
	}
	// 先解锁来源（锁定与撤销是两个维度）
	o.mu.Lock()
	o.locks = map[string]*lockEntry{}
	o.mu.Unlock()
	w = doOpen(t, o, kB.ID, secB, "GET", "/open/v1/vehicles", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("撤销后应为 401，实得 %d", w.Code)
	}

	// 9. 审计含拒绝与失败记录（可追溯）
	all := o.Audit(AuditFilter{Limit: 100})
	var sawDenied, sawAuthFail, sawLocked bool
	for _, e := range all {
		switch e.Result {
		case "denied":
			sawDenied = true
		case "auth_failed":
			sawAuthFail = true
		case "locked":
			sawLocked = true
		}
	}
	if !sawDenied || !sawAuthFail || !sawLocked {
		t.Fatalf("审计缺结果类型: denied=%v auth_failed=%v locked=%v", sawDenied, sawAuthFail, sawLocked)
	}
	_ = st
}

// TestOpenAPIMetaAndUnknown：meta 自检与未知接口。
func TestOpenAPIMetaAndUnknown(t *testing.T) {
	o, _ := openFixture(t)
	k, sec, _ := o.CreateKey("meta", "", []string{"vehicle.read"}, []string{"*"}, nil, "tester")
	w := doOpen(t, o, k.ID, sec, "GET", "/open/v1/meta", nil)
	if w.Code != 200 {
		t.Fatalf("meta: want 200 got %d", w.Code)
	}
	w = doOpen(t, o, k.ID, sec, "GET", "/open/v1/nope", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("未知接口: want 404 got %d", w.Code)
	}
	// 未带任何签名头：401
	req := httptest.NewRequest("GET", "/open/v1/meta", nil)
	rec := httptest.NewRecorder()
	o.ServeOpen(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无签名: want 401 got %d", rec.Code)
	}
}

// TestOpenAPIIPAllowlist：来源 IP 白名单——固定一个或多个 IP（含 CIDR），名单外拒绝。
func TestOpenAPIIPAllowlist(t *testing.T) {
	o, _ := openFixture(t)
	k, sec, err := o.CreateKey("IP绑定", "", []string{"vehicle.read"}, []string{"*"},
		[]string{"203.0.113.7", "198.51.100.0/24"}, "tester")
	if err != nil {
		t.Fatal(err)
	}

	do := func(remoteAddr string) int {
		req := httptest.NewRequest("GET", "/open/v1/vehicles", nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set("X-API-Key", k.ID)
		signReq(req, sec, nil)
		w := httptest.NewRecorder()
		o.ServeOpen(w, req)
		return w.Code
	}

	if code := do("203.0.113.7:40000"); code != 200 {
		t.Fatalf("名单内 IP 应放行: got %d", code)
	}
	if code := do("198.51.100.88:40000"); code != 200 {
		t.Fatalf("CIDR 网段内 IP 应放行: got %d", code)
	}
	if code := do("192.0.2.9:40000"); code != http.StatusUnauthorized {
		t.Fatalf("名单外 IP 应拒绝: got %d", code)
	}

	// 名单收紧为空外的非法格式应报错
	if _, _, err := o.CreateKey("bad", "", nil, nil, []string{"not-an-ip"}, "tester"); err == nil {
		t.Fatal("非法 IP 应被拒绝")
	}

	// 通过 KeyPatch 增删名单立即生效
	if _, err := o.UpdateKey(k.ID, KeyPatch{IPs: &[]string{"192.0.2.9"}}, "tester"); err != nil {
		t.Fatal(err)
	}
	if code := do("203.0.113.7:40000"); code != http.StatusUnauthorized {
		t.Fatalf("名单变更后旧 IP 应被拒: got %d", code)
	}
	if code := do("192.0.2.9:40000"); code != 200 {
		t.Fatalf("名单内新 IP 应放行: got %d", code)
	}
	// 清空名单 = 不限制来源
	empty := []string{}
	if _, err := o.UpdateKey(k.ID, KeyPatch{IPs: &empty}, "tester"); err != nil {
		t.Fatal(err)
	}
	if code := do("198.51.100.1:40000"); code != 200 {
		t.Fatalf("名单清空后应不限来源: got %d", code)
	}
}

func TestOpenAPIKeyExpiryIsRequiredAndEnforced(t *testing.T) {
	o, _ := openFixture(t)
	k, secret, err := o.CreateKey("expiry", "", []string{"vehicle.read"}, []string{"*"}, nil, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if k.ExpiresNS <= time.Now().UnixNano() {
		t.Fatalf("default expiry is not in the future: %d", k.ExpiresNS)
	}
	if remaining := time.Until(time.Unix(0, k.ExpiresNS)); remaining < 89*24*time.Hour || remaining > 90*24*time.Hour {
		t.Fatalf("default expiry=%s, want approximately 90 days", remaining)
	}

	requested := time.Now().Add(24 * time.Hour)
	k2, _, err := o.CreateKeyWithExpiry("explicit-expiry", "", []string{"vehicle.read"}, []string{"*"}, nil, "tester", &requested)
	if err != nil {
		t.Fatal(err)
	}
	if got := time.Until(time.Unix(0, k2.ExpiresNS)); got < 23*time.Hour || got > 24*time.Hour {
		t.Fatalf("explicit expiry=%s, want approximately 24 hours", got)
	}
	tooFar := time.Now().Add(maxAPIKeyLifetime + time.Hour)
	if _, _, err := o.CreateKeyWithExpiry("too-far", "", []string{"vehicle.read"}, []string{"*"}, nil, "tester", &tooFar); err == nil {
		t.Fatal("expiry beyond the maximum lifetime was accepted")
	}

	// Simulate the passage of time by changing only the in-memory projection;
	// this test does not weaken the production persistence path.
	o.mu.Lock()
	o.keys[k.ID].ExpiresNS = time.Now().Add(-time.Second).UnixNano()
	o.mu.Unlock()
	w := doOpen(t, o, k.ID, secret, http.MethodGet, "/open/v1/vehicles", nil)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"EXPIRED"`) {
		t.Fatalf("expired key was not rejected with EXPIRED: status=%d body=%s", w.Code, w.Body.String())
	}
	entries := o.Audit(AuditFilter{Limit: 10, Result: "expired"})
	if len(entries) == 0 {
		t.Fatal("expired request was not recorded in the audit ring")
	}
}

func TestOpenAPIKeyExpiryInputForms(t *testing.T) {
	o, _ := openFixture(t)
	if _, present, err := o.parseAPIKeyExpiry(map[string]any{}); present || err != nil {
		t.Fatalf("missing expiry should use the default: present=%v err=%v", present, err)
	}
	if exp, present, err := o.parseAPIKeyExpiry(map[string]any{"expires_in_s": float64(3600)}); !present || err != nil || exp == nil {
		t.Fatalf("valid duration was rejected: present=%v exp=%v err=%v", present, exp, err)
	}
	if _, _, err := o.parseAPIKeyExpiry(map[string]any{"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339), "expires_in_s": float64(3600)}); err == nil {
		t.Fatal("both expiry forms were accepted")
	}
	if _, _, err := o.parseAPIKeyExpiry(map[string]any{"expires_in_s": float64(0)}); err == nil {
		t.Fatal("zero duration was accepted")
	}
}
