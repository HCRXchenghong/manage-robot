package main

// auth.go：登录与账号体系（等保三级身份鉴别）。
//   账号密码 + 图形验证码（人机验证）→ 会话 Cookie（HttpOnly）
//   策略：密码复杂度、连续失败锁定（5 次锁 30 分钟）、会话闲置 30 分钟/绝对 8 小时、
//        登录全程审计、明文密码不落任何日志。
//   角色：super（超管，全局≤10）/ group_admin（普通管理员，每分组配额）/ user（用户，每分组配额）。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math/big"
	mrand "math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxSuperAdmins   = 10
	lockAfterFails   = 5
	lockDuration     = 30 * time.Minute
	captchaTTL       = 5 * time.Minute
	captchaMaxTries  = 3
	smsTTL           = 5 * time.Minute
	smsMaxTries      = 5
	smsCooldown      = 60 * time.Second
	smsDailyMax      = 10
	sessionIdleTTL   = 30 * time.Minute
	sessionAbsTTL    = 8 * time.Hour
	passwordLifetime = 180 * 24 * time.Hour // 等保：密码最长使用期限
	csrfCookieName   = "ra_csrf"
)

// ---------- 图形验证码 ----------

// font5x7 每字 7 行、每行 5 位（手写点阵，无外部依赖）。
// 字符集刻意去掉 0/O/1/I/L 等易混淆字符。
var font5x7 = map[rune][7]string{
	'2': {"01110", "10001", "00001", "00110", "01000", "10000", "11111"},
	'3': {"11110", "00001", "00001", "01110", "00001", "00001", "11110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "11110", "00001", "00001", "10001", "01110"},
	'6': {"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00010", "01100"},
	'A': {"01110", "10001", "10001", "11111", "10001", "10001", "10001"},
	'B': {"11110", "10001", "10001", "11110", "10001", "10001", "11110"},
	'C': {"01110", "10001", "10000", "10000", "10000", "10001", "01110"},
	'D': {"11100", "10010", "10001", "10001", "10001", "10010", "11100"},
	'E': {"11111", "10000", "10000", "11110", "10000", "10000", "11111"},
	'F': {"11111", "10000", "10000", "11110", "10000", "10000", "10000"},
	'G': {"01110", "10001", "10000", "10111", "10001", "10001", "01111"},
	'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'J': {"00111", "00010", "00010", "00010", "00010", "10010", "01100"},
	'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'N': {"10001", "11001", "10101", "10011", "10001", "10001", "10001"},
	'P': {"11110", "10001", "10001", "11110", "10000", "10000", "10000"},
	'Q': {"01110", "10001", "10001", "10001", "10101", "10010", "01101"},
	'R': {"11110", "10001", "10001", "11110", "10100", "10010", "10001"},
	'S': {"01111", "10000", "10000", "01110", "00001", "00001", "11110"},
	'T': {"11111", "00100", "00100", "00100", "00100", "00100", "00100"},
	'U': {"10001", "10001", "10001", "10001", "10001", "10001", "01110"},
	'V': {"10001", "10001", "10001", "10001", "01010", "01010", "00100"},
	'W': {"10001", "10001", "10001", "10101", "10101", "11011", "10001"},
	'X': {"10001", "01010", "00100", "00100", "00100", "01010", "10001"},
	'Y': {"10001", "10001", "01010", "00100", "00100", "00100", "00100"},
	'Z': {"11111", "00001", "00010", "00100", "01000", "10000", "11111"},
}

const captchaAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

type captcha struct {
	answer string
	exp    time.Time
	tries  int
}

// smsEntry 短信验证码状态：5 分钟有效、错 5 次作废、60 秒重发冷却、每日上限。
type smsEntry struct {
	code     string
	exp      time.Time
	tries    int
	lastSend time.Time
	dayKey   string
	dayCount int
}

// validCNPhone 大陆手机号格式：1 开头、第二位 3-9、共 11 位数字。
func validCNPhone(p string) bool {
	if len(p) != 11 || p[0] != '1' || p[1] < '3' || p[1] > '9' {
		return false
	}
	for i := 2; i < 11; i++ {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
	}
	return true
}

func maskPhone(p string) string { return p[:3] + "****" + p[7:] }

// renderCaptcha 画一张 132x48 的干扰图：4 个字符、随机抖动、噪线与噪点。
func renderCaptcha(text string) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 132, 48))
	bg := color.RGBA{14, 23, 48, 255}
	for y := 0; y < 48; y++ {
		for x := 0; x < 132; x++ {
			img.Set(x, y, bg)
		}
	}
	rng := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	// 噪线
	for i := 0; i < 5; i++ {
		c := color.RGBA{uint8(40 + rng.Intn(60)), uint8(60 + rng.Intn(80)), uint8(120 + rng.Intn(80)), 255}
		x0, y0 := rng.Intn(132), rng.Intn(48)
		x1, y1 := rng.Intn(132), rng.Intn(48)
		steps := 60
		for s := 0; s <= steps; s++ {
			x := x0 + (x1-x0)*s/steps
			y := y0 + (y1-y0)*s/steps
			img.Set(x, y, c)
			if x+1 < 132 {
				img.Set(x+1, y, c)
			}
		}
	}
	// 字符
	for ci, ch := range text {
		g, ok := font5x7[ch]
		if !ok {
			continue
		}
		ox := 8 + ci*30 + rng.Intn(5)
		oy := 6 + rng.Intn(6)
		c := color.RGBA{uint8(190 + rng.Intn(65)), uint8(210 + rng.Intn(45)), 255, 255}
		for r := 0; r < 7; r++ {
			for col := 0; col < 5; col++ {
				if g[r][col] == '1' {
					for dy := 0; dy < 3; dy++ {
						for dx := 0; dx < 3; dx++ {
							img.Set(ox+col*3+dx, oy+r*3+dy, c)
						}
					}
				}
			}
		}
	}
	// 噪点
	for i := 0; i < 140; i++ {
		img.Set(rng.Intn(132), rng.Intn(48), color.RGBA{uint8(100 + rng.Intn(100)), uint8(120 + rng.Intn(100)), 255, 255})
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// ---------- 用户与会话 ----------

type User struct {
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Role        string   `json:"role"` // super|group_admin|user
	Groups      []string `json:"groups"`
	Phone       string   `json:"phone"`
	PassHash    string   `json:"-"`
	Salt        string   `json:"-"`
	CreatedNS   int64    `json:"created_ns"`
	PassSetNS   int64    `json:"pass_set_ns"`
	FailCount   int      `json:"-"`
	LockedUntil int64    `json:"-"`
}

type Session struct {
	Token     string   `json:"-"`
	Username  string   `json:"username"`
	Role      string   `json:"role"`
	Groups    []string `json:"groups"`
	CreatedNS int64    `json:"created_ns"`
	lastUse   time.Time
}

type AuthStore struct {
	mu       sync.Mutex
	users    map[string]*User
	sessions map[string]*Session
	captchas map[string]*captcha
	preauths map[string]*preauth
	sms      map[string]*smsEntry
	groups   *GroupStore
	st       *State
	open     *OpenAPI
	db       *sql.DB
}

// preauth 账密已通过、等待人机验证的中间态（5 分钟、一次性）。
type preauth struct {
	username string
	exp      time.Time
}

func NewAuthStore(groups *GroupStore, st *State, open *OpenAPI) *AuthStore {
	var db *sql.DB
	if st != nil {
		db = st.db
	}
	a := &AuthStore{users: map[string]*User{}, sessions: map[string]*Session{}, captchas: map[string]*captcha{}, preauths: map[string]*preauth{}, sms: map[string]*smsEntry{}, groups: groups, st: st, open: open, db: db}
	a.loadUsers()
	a.loadSessions()
	go a.cleanupLoop()
	return a
}

// loadUsers hydrates the in-process authorization cache from the production
// database. A service restart must never recreate accounts from source code.
func (a *AuthStore) loadUsers() {
	if a.db == nil {
		return
	}
	rows, err := a.db.Query(`SELECT username, display_name, role, group_ids::text,
		COALESCE(phone, ''), pass_hash, salt,
		(EXTRACT(EPOCH FROM created_at) * 1000000000)::bigint,
		(EXTRACT(EPOCH FROM pass_set_at) * 1000000000)::bigint FROM auth_users`)
	if err != nil {
		log.Printf("[auth] 加载用户失败: %v", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		u := &User{}
		var groupsJSON string
		if err := rows.Scan(&u.Username, &u.DisplayName, &u.Role, &groupsJSON, &u.Phone, &u.PassHash, &u.Salt, &u.CreatedNS, &u.PassSetNS); err != nil {
			log.Printf("[auth] 读取用户失败: %v", err)
			continue
		}
		if err := json.Unmarshal([]byte(groupsJSON), &u.Groups); err != nil {
			log.Printf("[auth] 用户 %s 的分组数据无效: %v", u.Username, err)
			continue
		}
		a.users[u.Username] = u
	}
}

// persistUserLocked synchronously commits an identity before the caller
// reports success. Caller must hold a.mu.
func (a *AuthStore) persistUserLocked(u *User) error {
	if a.db == nil {
		return nil
	}
	groupsJSON, err := json.Marshal(u.Groups)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`INSERT INTO auth_users(username, display_name, role, group_ids, phone, pass_hash, salt, created_at, pass_set_at)
		VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9)
		ON CONFLICT (username) DO UPDATE SET display_name=$2, role=$3, group_ids=$4::jsonb, phone=$5, pass_hash=$6, salt=$7, pass_set_at=$9`,
		u.Username, u.DisplayName, u.Role, string(groupsJSON), nullable(u.Phone), u.PassHash, u.Salt, time.Unix(0, u.CreatedNS), time.Unix(0, u.PassSetNS))
	return err
}

// ---------- PostgreSQL 会话：跨重启/多实例恢复，绝不落盘明文 token ----------

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte("robot-agent/session/v1:" + token))
	return hex.EncodeToString(sum[:])
}

// loadSessions 清理过期数据库记录。会话本身按请求懒加载：数据库只保存
// token 摘要，服务重启后可用浏览器携带的原 token 通过摘要查回，不需要
// 也不能从数据库反推出 token 明文。
func (a *AuthStore) loadSessions() {
	if a.db == nil {
		return
	}
	if _, err := a.db.Exec(`DELETE FROM auth_sessions
		WHERE revoked_at IS NOT NULL
		   OR last_use_at <= now() - interval '30 minutes'
		   OR created_at <= now() - interval '8 hours'`); err != nil {
		log.Printf("[auth] 清理过期会话失败: %v", err)
	}
}

func (a *AuthStore) restoreSession(token string) (*Session, error) {
	if a.db == nil || token == "" {
		return nil, nil
	}
	var s Session
	var groupsJSON string
	var createdNS, lastUseNS int64
	err := a.db.QueryRow(`SELECT username, role, group_ids::text,
		(EXTRACT(EPOCH FROM created_at) * 1000000000)::bigint,
		(EXTRACT(EPOCH FROM last_use_at) * 1000000000)::bigint
		FROM auth_sessions
		WHERE token_hash=$1 AND revoked_at IS NULL
		  AND last_use_at > now() - interval '30 minutes'
		  AND created_at > now() - interval '8 hours'`, sessionTokenHash(token)).Scan(
		&s.Username, &s.Role, &groupsJSON, &createdNS, &lastUseNS)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(groupsJSON), &s.Groups); err != nil {
		return nil, fmt.Errorf("会话分组数据无效: %w", err)
	}
	s.Token, s.CreatedNS, s.lastUse = token, createdNS, time.Unix(0, lastUseNS)
	return &s, nil
}

// persistSession 创建或刷新一条会话；调用方不得持有 a.mu。
func (a *AuthStore) persistSession(s *Session) error {
	if a.db == nil {
		return nil
	}
	groupsJSON, err := json.Marshal(s.Groups)
	if err != nil {
		return err
	}
	var result sql.Result
	result, err = a.db.Exec(`INSERT INTO auth_sessions(token_hash, username, role, group_ids, created_at, last_use_at)
		VALUES ($1,$2,$3,$4::jsonb,$5,$6)
		ON CONFLICT (token_hash) DO UPDATE SET
		  username=$2, role=$3, group_ids=$4::jsonb, last_use_at=$6
		WHERE auth_sessions.revoked_at IS NULL`,
		sessionTokenHash(s.Token), s.Username, s.Role, string(groupsJSON),
		time.Unix(0, s.CreatedNS), s.lastUse)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return fmt.Errorf("会话已被撤销")
	}
	return nil
}

func (a *AuthStore) revokeSession(token string) error {
	if a.db == nil || token == "" {
		return nil
	}
	_, err := a.db.Exec(`UPDATE auth_sessions SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL`, sessionTokenHash(token))
	return err
}

// auditSystem writes operator/session lifecycle evidence to the append-only
// platform audit stream. Session tokens and passwords are never included.
func (a *AuthStore) auditSystem(actor, action string, detail map[string]any) {
	if a == nil || a.db == nil || strings.TrimSpace(action) == "" {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	if _, err := a.db.Exec(`INSERT INTO audit_logs(actor, action, detail) VALUES ($1,$2,$3::jsonb)`,
		strings.TrimSpace(actor), action, mustJSON(detail)); err != nil {
		log.Printf("[auth] 平台审计写入失败: %v", err)
	}
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Token generation is a security boundary. Continuing with a predictable
		// token would be materially worse than failing the process closed.
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

const passwordKDFIterations = 310000

func hashPassword(salt, pwd string) string {
	derived := pbkdf2SHA256([]byte(pwd), []byte(salt), passwordKDFIterations, sha256.Size)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s", passwordKDFIterations, hex.EncodeToString(derived))
}

// pbkdf2SHA256 is local to keep the fleet binary's crypto dependency small.
// New passwords always use a salted, deliberately expensive KDF; legacy
// single-SHA256 records are accepted only for migration compatibility.
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	out := make([]byte, 0, keyLen)
	for block := uint32(1); len(out) < keyLen; block++ {
		h := hmac.New(sha256.New, password)
		_, _ = h.Write(salt)
		_, _ = h.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := h.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			h = hmac.New(sha256.New, password)
			_, _ = h.Write(u)
			u = h.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func verifyPassword(salt, pwd, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) == 3 && parts[0] == "pbkdf2-sha256" {
		iterations, err := strconv.Atoi(parts[1])
		if err != nil || iterations < 100000 || iterations > 1000000 {
			return false
		}
		want, err := hex.DecodeString(parts[2])
		if err != nil || len(want) != sha256.Size {
			return false
		}
		got := pbkdf2SHA256([]byte(pwd), []byte(salt), iterations, len(want))
		return subtle.ConstantTimeCompare(got, want) == 1
	}
	// Compatibility only for records created before the KDF migration. No new
	// record can be created in this format because hashPassword never emits it.
	h := sha256.Sum256([]byte(salt + ":" + pwd))
	return subtle.ConstantTimeCompare([]byte(hex.EncodeToString(h[:])), []byte(stored)) == 1
}

// PasswordOK 等保密码复杂度：≥8 位，且含 大写/小写/数字/符号 中至少 3 类。
func PasswordOK(pwd string) error {
	if len(pwd) < 8 {
		return fmt.Errorf("密码至少 8 位")
	}
	kinds := 0
	has := func(f func(rune) bool) bool {
		for _, r := range pwd {
			if f(r) {
				return true
			}
		}
		return false
	}
	if has(func(r rune) bool { return r >= 'A' && r <= 'Z' }) {
		kinds++
	}
	if has(func(r rune) bool { return r >= 'a' && r <= 'z' }) {
		kinds++
	}
	if has(func(r rune) bool { return r >= '0' && r <= '9' }) {
		kinds++
	}
	if has(func(r rune) bool { return (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') }) {
		kinds++
	}
	if kinds < 3 {
		return fmt.Errorf("需含大写/小写/数字/符号中至少 3 类")
	}
	return nil
}

// SeedUser 内置/迁移用建号（不走配额校验，调用方保证）。
func (a *AuthStore) SeedUser(username, displayName, pwd, role string, groups []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.users[username]; ok {
		return
	}
	salt := randToken(8)
	u := &User{
		Username: username, DisplayName: displayName, Role: role, Groups: groups,
		PassHash: hashPassword(salt, pwd), Salt: salt,
		CreatedNS: time.Now().UnixNano(), PassSetNS: time.Now().UnixNano(),
	}
	if err := a.persistUserLocked(u); err != nil {
		log.Printf("[auth] 初始化管理员持久化失败: %v", err)
		return
	}
	a.users[username] = u
}

// NewCaptcha 生成新验证码。
func (a *AuthStore) NewCaptcha() (id, imageB64 string) {
	text := ""
	max := big.NewInt(int64(len(captchaAlphabet)))
	for i := 0; i < 4; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}
		text += string(captchaAlphabet[n.Int64()])
	}
	id = randToken(8)
	a.mu.Lock()
	a.captchas[id] = &captcha{answer: text, exp: time.Now().Add(captchaTTL)}
	a.mu.Unlock()
	return id, "data:image/png;base64," + b64(renderCaptcha(text))
}

// checkCaptcha 校验并消费验证码；错 3 次或过期作废。
func (a *AuthStore) checkCaptcha(id, ans string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.captchas[id]
	if !ok {
		return fmt.Errorf("验证码已失效，请刷新")
	}
	if time.Now().After(c.exp) {
		delete(a.captchas, id)
		return fmt.Errorf("验证码已过期，请刷新")
	}
	if !strings.EqualFold(strings.TrimSpace(ans), c.answer) {
		c.tries++
		if c.tries >= captchaMaxTries {
			delete(a.captchas, id)
			return fmt.Errorf("验证码错误次数过多，请刷新后重试")
		}
		return fmt.Errorf("人机验证未通过：验证码错误")
	}
	delete(a.captchas, id)
	return nil
}

// LoginPassword 第一段：校验账密（含锁定策略），通过发预认证 token。
// 人机验证放到第二段（点登录后才出验证码），体验与安全兼顾。
func (a *AuthStore) LoginPassword(username, pwd, ip string) (string, string, error) {
	a.mu.Lock()
	u, ok := a.users[username]
	if !ok {
		a.mu.Unlock()
		a.auditLogin(username, ip, "auth_failed", "用户不存在")
		return "", "", fmt.Errorf("账号或密码错误")
	}
	if u.LockedUntil > time.Now().UnixNano() {
		left := time.Duration(u.LockedUntil-time.Now().UnixNano()) / time.Minute
		a.mu.Unlock()
		a.auditLogin(username, ip, "auth_failed", "账号锁定中")
		return "", "", fmt.Errorf("账号已锁定，请 %d 分钟后再试", left+1)
	}
	if !verifyPassword(u.Salt, pwd, u.PassHash) {
		u.FailCount++
		msg := "账号或密码错误"
		if u.FailCount >= lockAfterFails {
			u.LockedUntil = time.Now().Add(lockDuration).UnixNano()
			u.FailCount = 0
			msg = fmt.Sprintf("连续失败 %d 次，账号已锁定 30 分钟", lockAfterFails)
		}
		a.mu.Unlock()
		a.auditLogin(username, ip, "auth_failed", msg)
		return "", "", fmt.Errorf("%s", msg)
	}
	u.FailCount = 0
	u.LockedUntil = 0
	token := randToken(16)
	a.preauths[token] = &preauth{username: username, exp: time.Now().Add(captchaTTL)}
	passAge := time.Since(time.Unix(0, u.PassSetNS))
	expiring := passAge > passwordLifetime-14*24*time.Hour
	a.mu.Unlock()
	a.auditLogin(username, ip, "ok", "账密校验通过，进入人机验证")
	note := ""
	if expiring {
		note = "密码即将到期（180 天），请尽快修改"
	}
	return token, note, nil
}

// FinishLogin 第二段：人机验证通过 → 发会话。
func (a *AuthStore) FinishLogin(preToken, capID, capAns, ip string) (*Session, error) {
	if err := a.checkCaptcha(capID, capAns); err != nil {
		return nil, err
	}
	a.mu.Lock()
	p, ok := a.preauths[preToken]
	if !ok || time.Now().After(p.exp) {
		a.mu.Unlock()
		return nil, fmt.Errorf("登录状态已失效，请重新输入账号密码")
	}
	delete(a.preauths, preToken) // 一次性
	u, ok2 := a.users[p.username]
	if !ok2 {
		a.mu.Unlock()
		return nil, fmt.Errorf("账号不存在")
	}
	sess := &Session{
		Token: randToken(24), Username: u.Username, Role: u.Role,
		Groups:    append([]string(nil), u.Groups...),
		CreatedNS: time.Now().UnixNano(), lastUse: time.Now(),
	}
	a.mu.Unlock()
	if err := a.persistSession(sess); err != nil {
		return nil, fmt.Errorf("会话持久化失败: %w", err)
	}
	a.mu.Lock()
	a.sessions[sess.Token] = sess
	a.mu.Unlock()
	a.auditLogin(u.Username, ip, "ok", "人机验证通过，登录成功")
	if a.st != nil {
		a.st.pushEvent("info", "", fmt.Sprintf("用户登录：%s（%s）", u.DisplayName, u.Role), "sys")
	}
	return sess, nil
}

// SendSmsCode requires a configured out-of-band SMS provider. The platform
// deliberately does not generate or expose a substitute code.
func (a *AuthStore) SendSmsCode(phone string) (string, error) {
	if !validCNPhone(phone) {
		return "", fmt.Errorf("手机号格式不正确")
	}
	return "", fmt.Errorf("短信网关尚未配置")
}

// LoginPhone 手机号+短信验证码登录：短信码本身即人机验证，通过直接发会话。
func (a *AuthStore) LoginPhone(phone, code, ip string) (*Session, error) {
	if !validCNPhone(phone) {
		return nil, fmt.Errorf("手机号格式不正确")
	}
	a.mu.Lock()
	e, ok := a.sms[phone]
	if !ok || time.Now().After(e.exp) {
		a.mu.Unlock()
		return nil, fmt.Errorf("验证码已失效，请重新获取")
	}
	if e.code != code {
		e.tries++
		if e.tries >= smsMaxTries {
			delete(a.sms, phone)
			a.mu.Unlock()
			return nil, fmt.Errorf("验证码错误次数过多，请重新获取")
		}
		a.mu.Unlock()
		return nil, fmt.Errorf("短信验证码错误")
	}
	delete(a.sms, phone)
	var owner *User
	for _, u := range a.users {
		if u.Phone == phone {
			owner = u
			break
		}
	}
	if owner == nil {
		a.mu.Unlock()
		return nil, fmt.Errorf("该手机号未绑定本平台账号")
	}
	if owner.LockedUntil > time.Now().UnixNano() {
		a.mu.Unlock()
		return nil, fmt.Errorf("账号已锁定，请稍后再试")
	}
	sess := &Session{
		Token: randToken(24), Username: owner.Username, Role: owner.Role,
		Groups:    append([]string(nil), owner.Groups...),
		CreatedNS: time.Now().UnixNano(), lastUse: time.Now(),
	}
	a.mu.Unlock()
	if err := a.persistSession(sess); err != nil {
		return nil, fmt.Errorf("会话持久化失败: %w", err)
	}
	a.mu.Lock()
	a.sessions[sess.Token] = sess
	a.mu.Unlock()
	a.auditLogin(owner.Username, ip, "ok", "手机号+短信验证码登录成功")
	if a.st != nil {
		a.st.pushEvent("info", "", fmt.Sprintf("用户登录：%s（%s）", owner.DisplayName, owner.Role), "sys")
	}
	return sess, nil
}

// SetPhone 绑定/更换账号手机号（空串为解绑），拒绝重复绑定。
func (a *AuthStore) SetPhone(username, phone string) (*User, error) {
	if phone != "" && !validCNPhone(phone) {
		return nil, fmt.Errorf("手机号格式不正确")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	u, ok := a.users[username]
	if !ok {
		return nil, fmt.Errorf("账号不存在")
	}
	if phone != "" {
		for n, o := range a.users {
			if o.Phone == phone && n != username {
				return nil, fmt.Errorf("该手机号已绑定账号 %s", n)
			}
		}
	}
	old := u.Phone
	u.Phone = phone
	if err := a.persistUserLocked(u); err != nil {
		u.Phone = old
		return nil, fmt.Errorf("保存手机号失败: %w", err)
	}
	return u, nil
}

func (a *AuthStore) auditLogin(username, ip, result, detail string) {
	if a.open != nil {
		a.open.logAudit(AuditEntry{
			TsNS: time.Now().UnixNano(), KeyName: username, Method: "POST",
			Path: "/api/auth/login", Result: result, IP: ip, Detail: detail,
		})
	}
}

func cloneSession(s *Session) *Session {
	if s == nil {
		return nil
	}
	out := *s
	out.Groups = append([]string(nil), s.Groups...)
	return &out
}

// SessionByToken 取会话并滑动续期。返回副本，避免请求在锁外读取到被管理员并发修改的会话。
func (a *AuthStore) SessionByToken(token string) *Session {
	if token == "" {
		return nil
	}
	a.mu.Lock()
	s, ok := a.sessions[token]
	if ok {
		now := time.Now()
		if now.Sub(s.lastUse) > sessionIdleTTL || now.Sub(time.Unix(0, s.CreatedNS)) > sessionAbsTTL {
			username := s.Username
			delete(a.sessions, token)
			a.mu.Unlock()
			_ = a.revokeSession(token)
			a.auditSystem(username, "auth.session_expired", map[string]any{"reason": "idle_or_absolute_ttl"})
			return nil
		}
		s.lastUse = now
		out := cloneSession(s)
		a.mu.Unlock()
		if err := a.persistSession(out); err != nil {
			log.Printf("[auth] 更新会话失败: %v", err)
			return nil
		}
		return out
	}
	a.mu.Unlock()
	restored, err := a.restoreSession(token)
	if err != nil || restored == nil {
		if err != nil {
			log.Printf("[auth] 恢复会话失败: %v", err)
		}
		return nil
	}
	restored.lastUse = time.Now()
	if err := a.persistSession(restored); err != nil {
		log.Printf("[auth] 更新恢复会话失败: %v", err)
		return nil
	}
	a.mu.Lock()
	a.sessions[token] = restored
	a.mu.Unlock()
	return cloneSession(restored)
}

// SessionSnapshot returns the current, immutable authorization view for a long-lived
// connection without extending the session's idle timeout. WebSocket clients use it
// before every fan-out so logout, password reset, and group changes take effect there too.
func (a *AuthStore) SessionSnapshot(token string) *Session {
	a.mu.Lock()
	s, ok := a.sessions[token]
	if ok {
		now := time.Now()
		if now.Sub(s.lastUse) > sessionIdleTTL || now.Sub(time.Unix(0, s.CreatedNS)) > sessionAbsTTL {
			username := s.Username
			delete(a.sessions, token)
			a.mu.Unlock()
			_ = a.revokeSession(token)
			a.auditSystem(username, "auth.session_expired", map[string]any{"reason": "idle_or_absolute_ttl"})
			return nil
		}
		out := cloneSession(s)
		a.mu.Unlock()
		return out
	}
	a.mu.Unlock()
	restored, err := a.restoreSession(token)
	if err != nil || restored == nil {
		return nil
	}
	a.mu.Lock()
	a.sessions[token] = restored
	a.mu.Unlock()
	return cloneSession(restored)
}

// SessionFromRequest 从 Cookie 解会话。
func (a *AuthStore) SessionFromRequest(r *http.Request) *Session {
	c, err := r.Cookie("ra_session")
	if err != nil || c.Value == "" {
		return nil
	}
	return a.SessionByToken(c.Value)
}

func (a *AuthStore) Logout(r *http.Request) {
	c, err := r.Cookie("ra_session")
	if err != nil {
		return
	}
	a.mu.Lock()
	sess, _ := a.sessions[c.Value]
	username := ""
	if sess != nil {
		username = sess.Username
	}
	delete(a.sessions, c.Value)
	a.mu.Unlock()
	if err := a.revokeSession(c.Value); err != nil {
		log.Printf("[auth] 撤销会话失败: %v", err)
	}
	a.auditSystem(username, "auth.logout", map[string]any{"session_revoked": true})
}

func unsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}

func validCSRFRequest(r *http.Request) bool {
	if r == nil || !unsafeMethod(r.Method) || !strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	header := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
	if header == "" || len(header) != len(cookie.Value) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) == 1
}

// RefreshSessionGroups 用户分组被管理员调整后，同步其活跃会话。
func (a *AuthStore) RefreshSessions(username string, groups []string, role string) {
	a.mu.Lock()
	var changed []*Session
	for _, s := range a.sessions {
		if s.Username == username {
			s.Groups = append([]string(nil), groups...)
			s.Role = role
			changed = append(changed, cloneSession(s))
		}
	}
	a.mu.Unlock()
	for _, s := range changed {
		if err := a.persistSession(s); err != nil {
			log.Printf("[auth] 刷新会话权限失败: %v", err)
		}
	}
}

// KillSessions 改密后踢掉除当前会话外的全部会话（等保）。
func (a *AuthStore) KillSessions(username, exceptToken string) {
	a.killSessions(username, exceptToken, "", "auth.sessions_revoked")
}

func (a *AuthStore) killSessions(username, exceptToken, actor, action string) {
	a.mu.Lock()
	var revoked []string
	for t, s := range a.sessions {
		if s.Username == username && t != exceptToken {
			delete(a.sessions, t)
			revoked = append(revoked, t)
		}
	}
	a.mu.Unlock()
	for _, token := range revoked {
		if err := a.revokeSession(token); err != nil {
			log.Printf("[auth] 撤销旧会话失败: %v", err)
		}
	}
	if len(revoked) > 0 {
		a.auditSystem(actor, action, map[string]any{"target_username": username, "count": len(revoked)})
	}
}

func (a *AuthStore) cleanupLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		a.mu.Lock()
		for id, c := range a.captchas {
			if now.After(c.exp) {
				delete(a.captchas, id)
			}
		}
		for ph, s := range a.sms {
			if now.After(s.exp.Add(time.Hour)) {
				delete(a.sms, ph)
			}
		}
		for t2, s := range a.sessions {
			if now.Sub(s.lastUse) > sessionIdleTTL {
				delete(a.sessions, t2)
			}
		}
		a.mu.Unlock()
		if a.db != nil {
			if _, err := a.db.Exec(`DELETE FROM auth_sessions
				WHERE revoked_at IS NOT NULL
				   OR last_use_at <= now() - interval '30 minutes'
				   OR created_at <= now() - interval '8 hours'`); err != nil {
				log.Printf("[auth] 会话清理失败: %v", err)
			}
		}
	}
}

// ---------- 鉴权中间件 ----------

type ctxKey string

const ctxSession ctxKey = "session"

func isPublicPath(p string) bool {
	switch p {
	case "/", "/login", "/index.html", "/favicon.ico", "/robots.txt", "/healthz", "/readyz", "/metrics", "/api/captcha", "/api/auth/csrf", "/api/auth/login", "/api/auth/verify", "/api/auth/sms/send", "/api/auth/login-phone":
		return true
	}
	return strings.HasPrefix(p, "/assets/")
}

// Middleware 全站鉴权：未登录挡在门外；/open/v1/* 是对外开放面，
// 走自带的 API Key + HMAC 鉴权（不依赖登录会话）。车端不复用本机环回绕过，
// 应经 MQTT mTLS 或独立的设备证书通道接入。
func (a *AuthStore) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// Volatile mode is for read-only local diagnosis. Authentication itself
		// may use an in-memory session so the operator can inspect the service,
		// but no business, identity, map, device, takeover, or safety write may
		// report success without PostgreSQL durability.
		if a.st != nil && a.st.db == nil && r.Method != http.MethodGet &&
			r.Method != http.MethodHead && r.Method != http.MethodOptions &&
			p != "/api/auth/login" && p != "/api/auth/verify" &&
			p != "/api/auth/logout" && p != "/api/captcha" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "当前为 volatile 诊断模式，PostgreSQL 未就绪；所有业务写操作已禁用",
				"code":  "PERSISTENCE_REQUIRED",
			})
			return
		}
		if !validCSRFRequest(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "缺少或无效的 CSRF 令牌",
				"code":  "CSRF_FAILED",
			})
			return
		}
		if isPublicPath(p) || strings.HasPrefix(p, "/open/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		sess := a.SessionFromRequest(r)
		if sess == nil {
			if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/open/") || strings.HasPrefix(p, "/ws/") {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录或会话已过期"})
				return
			}
			next.ServeHTTP(w, r) // 静态页面放行，登录态由前端判断
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxSession, sess))
		next.ServeHTTP(w, r)
	})
}

func sessOf(r *http.Request) *Session {
	s, _ := r.Context().Value(ctxSession).(*Session)
	return s
}

func requireRole(w http.ResponseWriter, r *http.Request, roles ...string) *Session {
	s := sessOf(r)
	if s == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
		return nil
	}
	for _, rr := range roles {
		if s.Role == rr {
			return s
		}
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "权限不足"})
	return nil
}

func b64(b []byte) string {
	const tbl = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var sb strings.Builder
	for i := 0; i < len(b); i += 3 {
		var n uint32
		rem := len(b) - i
		switch {
		case rem >= 3:
			n = uint32(b[i])<<16 | uint32(b[i+1])<<8 | uint32(b[i+2])
			sb.WriteByte(tbl[n>>18&63])
			sb.WriteByte(tbl[n>>12&63])
			sb.WriteByte(tbl[n>>6&63])
			sb.WriteByte(tbl[n&63])
		case rem == 2:
			n = uint32(b[i])<<16 | uint32(b[i+1])<<8
			sb.WriteByte(tbl[n>>18&63])
			sb.WriteByte(tbl[n>>12&63])
			sb.WriteByte(tbl[n>>6&63])
			sb.WriteByte('=')
		case rem == 1:
			n = uint32(b[i]) << 16
			sb.WriteByte(tbl[n>>18&63])
			sb.WriteByte(tbl[n>>12&63])
			sb.WriteString("==")
		}
	}
	return sb.String()
}

// CheckCreds 校验账密（敏感操作二次确认用，如清除日志）：不建会话、不触发锁定计数。
func (a *AuthStore) CheckCreds(username, pwd string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	u, ok := a.users[username]
	if !ok {
		return "", false
	}
	if !verifyPassword(u.Salt, pwd, u.PassHash) {
		return "", false
	}
	return u.Role, true
}
