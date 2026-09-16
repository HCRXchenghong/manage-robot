package main

// devices.go：远程接管「控制设备」绑定管理。
// 四类接管输入设备：
//   - console   自研一体机（链接绑定：填入设备接入地址/配对链接）
//   - rc        航模遥控器（经 USB 接入 PC，浏览器 Gamepad/HID 识别）
//   - keyboard  键盘控制（本机内置）
//   - wheel     罗技方向盘（USB HID）
// 绑定记录归属登录账号；全站鉴权中间件已保证 /api/* 必须携带会话，
// 查询/解绑再做账号归属校验（双重保障，等保三级访问控制）。

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DeviceConsole  = "console"  // 自研一体机
	DeviceRC       = "rc"       // 航模遥控器（USB）
	DeviceKeyboard = "keyboard" // 键盘
	DeviceWheel    = "wheel"    // 罗技方向盘
)

var deviceTypeNames = map[string]string{
	DeviceConsole:  "自研一体机",
	DeviceRC:       "航模遥控器",
	DeviceKeyboard: "键盘",
	DeviceWheel:    "罗技方向盘",
}

type Device struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Link      string `json:"link,omitempty"` // 一体机接入链接/配对标识
	SN        string `json:"sn,omitempty"`   // 一体机 SN 码
	Salt      string `json:"-"`              // 一体机密码盐（不对外）
	PassHash  string `json:"-"`              // 一体机密码哈希（不对外，等保：不落明文）
	Owner     string `json:"owner"`
	Online    bool   `json:"online"`
	CreatedNS int64  `json:"created_ns"`
}

type DeviceStore struct {
	mu      sync.Mutex
	devices map[string]*Device
	seq     int
	db      *sql.DB
}

func NewDeviceStore(dbs ...*sql.DB) *DeviceStore {
	var db *sql.DB
	if len(dbs) > 0 {
		db = dbs[0]
	}
	s := &DeviceStore{devices: map[string]*Device{}, db: db}
	s.load()
	return s
}

func (s *DeviceStore) List(owner string) []*Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []*Device{}
	for _, d := range s.devices {
		if d.Owner == owner {
			copy := *d
			out = append(out, &copy)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedNS < out[j].CreatedNS })
	return out
}

// Bind 绑定设备。一体机以「SN 码 + 设备密码」绑定：SN 全局唯一，密码只存哈希；
// 绑定只保存设备准入信息；只有受信控制代理建立认证会话后才能标记在线并获得控制资格。
// 同账号同类型同名设备不允许重复绑定。
func (s *DeviceStore) Bind(owner, typ, name, link, sn, password string) (*Device, error) {
	if _, ok := deviceTypeNames[typ]; !ok {
		return nil, fmt.Errorf("不支持的设备类型：%s", typ)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = deviceTypeNames[typ]
	}
	link = strings.TrimSpace(link)
	sn = strings.TrimSpace(sn)
	s.mu.Lock()
	defer s.mu.Unlock()
	if typ == DeviceConsole {
		if sn == "" || password == "" {
			return nil, fmt.Errorf("自研一体机绑定需同时提供 SN 码与设备密码")
		}
		if len(sn) < 6 || len(sn) > 32 {
			return nil, fmt.Errorf("SN 码长度应为 6-32 位")
		}
		for _, d := range s.devices {
			if d.Type == DeviceConsole && strings.EqualFold(d.SN, sn) {
				return nil, fmt.Errorf("SN %s 已被账号 %s 绑定", sn, d.Owner)
			}
		}
	}
	for _, d := range s.devices {
		if d.Owner == owner && d.Type == typ && d.Name == name {
			return nil, fmt.Errorf("已绑定同名同类设备「%s」，请勿重复绑定", name)
		}
	}
	s.seq++
	salt := ""
	passHash := ""
	if typ == DeviceConsole {
		salt = randToken(8)
		passHash = hashPassword(salt, password)
	}
	d := &Device{
		ID:       fmt.Sprintf("dev-%s", randToken(16)),
		Type:     typ,
		Name:     name,
		Link:     link,
		SN:       sn,
		Salt:     salt,
		PassHash: passHash,
		Owner:    owner,
		// 不能把“已绑定”伪装成“在线”。实际在线状态必须来自受信控制代理的认证心跳。
		Online:    false,
		CreatedNS: time.Now().UnixNano(),
	}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := s.db.ExecContext(ctx, `INSERT INTO control_devices
			(id, owner, type, name, link, serial_number, pass_salt, pass_hash, online, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,false,$9)`, d.ID, d.Owner, d.Type, d.Name,
			d.Link, d.SN, d.Salt, d.PassHash, time.Unix(0, d.CreatedNS)); err != nil {
			return nil, fmt.Errorf("控制设备持久化失败: %w", err)
		}
	}
	s.devices[d.ID] = d
	copy := *d
	return &copy, nil
}

func (s *DeviceStore) Get(owner, id string) (*Device, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok || d.Owner != owner {
		return nil, false
	}
	copy := *d
	return &copy, true
}

func (s *DeviceStore) Unbind(owner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok || d.Owner != owner {
		return fmt.Errorf("设备不存在或无权解绑")
	}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := s.db.ExecContext(ctx, "DELETE FROM control_devices WHERE id=$1 AND owner=$2", id, owner); err != nil {
			return fmt.Errorf("控制设备持久化删除失败: %w", err)
		}
	}
	delete(s.devices, id)
	return nil
}

func (s *DeviceStore) load() {
	if s == nil || s.db == nil {
		return
	}
	rows, err := s.db.Query(`SELECT id, owner, type, name, link, serial_number, pass_salt, pass_hash,
		online, (EXTRACT(EPOCH FROM created_at) * 1000000000)::bigint FROM control_devices`)
	if err != nil {
		log.Printf("[devices] 恢复控制设备失败: %v", err)
		return
	}
	defer rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		d := &Device{}
		if err := rows.Scan(&d.ID, &d.Owner, &d.Type, &d.Name, &d.Link, &d.SN, &d.Salt, &d.PassHash, &d.Online, &d.CreatedNS); err != nil {
			continue
		}
		// 进程重启后在线证明必须重新建立，不能从旧心跳恢复。
		d.Online = false
		s.devices[d.ID] = d
	}
}

// registerDeviceRoutes 设备绑定路由（会话由全局中间件校验，这里再校验账号归属）。
func registerDeviceRoutes(mux *http.ServeMux, svc *Services) {
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"devices": svc.devices.List(sess.Username)})
	})
	mux.HandleFunc("POST /api/devices", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		typ, _ := body["type"].(string)
		name, _ := body["name"].(string)
		link, _ := body["link"].(string)
		sn, _ := body["sn"].(string)
		password, _ := body["password"].(string)
		d, err := svc.devices.Bind(sess.Username, typ, name, link, sn, password)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if svc.st != nil {
			svc.st.pushEvent("info", "", fmt.Sprintf("设备绑定：%s 绑定%s「%s」", sess.Username, deviceTypeNames[typ], d.Name), "sys")
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": d})
	})
	mux.HandleFunc("POST /api/devices/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		if err := svc.devices.Unbind(sess.Username, r.PathValue("id")); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
}
