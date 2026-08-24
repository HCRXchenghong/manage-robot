package main

// maps.go：地图中心后端（map-engine 阶段 2）。
// 职责：
//   - 地图仓库：每车多版本、追加式存储（原始数据永远只读，编辑/3D→2D 生成新版本）；
//   - 车端实时上报：vehicle_side 每 15s 带 sha256 上传，内容没变则去重跳过；
//   - 一行命令转换：调用 deploy/demo/map_convert.py 完成 3D(PCD/CSV)→2D(PNG/PGM/YAML)；
//   - 编辑保存：前端编辑页提交新 PNG + 操作记录，落成新版本。
// 存储布局：{dir}/{map_id}/v{N}/{file} + {dir}/index.json（进程重启可恢复）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type MapVersion struct {
	Version     int            `json:"version"`
	Files       []string       `json:"files"`
	Kind        string         `json:"kind"` // pcd|csv|png
	Note        string         `json:"note,omitempty"`
	Author      string         `json:"author,omitempty"`
	CreatedNS   int64          `json:"created_ns"`
	DerivedFrom string         `json:"derived_from,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

type MapEntry struct {
	ID         string       `json:"id"`
	VehicleID  string       `json:"vehicle_id"`
	Name       string       `json:"name"`
	Kind       string       `json:"kind"` // 3d_pcd|3d_csv|2d_png
	Points     int          `json:"points,omitempty"`
	SHA256     string       `json:"sha256"`
	Size       int64        `json:"size"`
	Source     string       `json:"source"` // vehicle_push|manual|derived
	CreatedNS  int64        `json:"created_ns"`
	UpdatedNS  int64        `json:"updated_ns"`
	Versions   []MapVersion `json:"versions"`
	LatestKind string       `json:"latest_kind,omitempty"`
	Has2D      bool         `json:"has_2d"`
}

type MapStore struct {
	mu     sync.Mutex
	dir    string
	pyBin  string
	script string
	maps   map[string]*MapEntry
	hub    *Hub
	st     *State
}

var mapIDRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func NewMapStore(dir, pyBin, script string, hub *Hub, st *State) (*MapStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ms := &MapStore{dir: dir, pyBin: pyBin, script: script, maps: map[string]*MapEntry{}, hub: hub, st: st}
	if b, err := os.ReadFile(filepath.Join(dir, "index.json")); err == nil {
		var all []*MapEntry
		if err := json.Unmarshal(b, &all); err == nil {
			for _, m := range all {
				ms.maps[m.ID] = m
			}
		}
	}
	return ms, nil
}

func (ms *MapStore) persistLocked() {
	all := make([]*MapEntry, 0, len(ms.maps))
	for _, m := range ms.maps {
		all = append(all, m)
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(ms.dir, "index.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		_ = os.Rename(tmp, filepath.Join(ms.dir, "index.json"))
	}
}

func (ms *MapStore) notify() {
	if ms.hub == nil {
		return
	}
	if b, err := json.Marshal(map[string]any{"type": "maps", "data": map[string]any{"updated_ns": time.Now().UnixNano()}}); err == nil {
		ms.hub.Broadcast(b)
	}
}

func slugify(s string) string {
	s = mapIDRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "map"
	}
	return s
}

func kindOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pcd":
		return "3d_pcd"
	case ".csv":
		return "3d_csv"
	case ".png", ".pgm":
		return "2d_png"
	}
	return "unknown"
}

// Register 写入一张地图（车端上报/手动导入共用）；同车同名同 sha 去重。
// 返回（条目, 是否有新内容）。
func (ms *MapStore) Register(vehicleID, name, source, author string, data []byte) (*MapEntry, bool, error) {
	if vehicleID == "" || name == "" {
		return nil, false, fmt.Errorf("vehicle_id 与 name 必填")
	}
	if len(data) == 0 {
		return nil, false, fmt.Errorf("文件为空")
	}
	kind := kindOf(name)
	if kind == "unknown" {
		return nil, false, fmt.Errorf("不支持的地图格式：%s（仅 .pcd/.csv/.png/.pgm）", filepath.Ext(name))
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	id := slugify(vehicleID + "-" + strings.TrimSuffix(name, filepath.Ext(name)))
	now := time.Now().UnixNano()

	ms.mu.Lock()
	defer ms.mu.Unlock()
	if m, ok := ms.maps[id]; ok && m.SHA256 == sha {
		return m, false, nil // 内容没变：去重，不算新版本
	}
	m, ok := ms.maps[id]
	if !ok {
		m = &MapEntry{ID: id, VehicleID: vehicleID, Name: name, Kind: kind, CreatedNS: now}
		ms.maps[id] = m
	}
	ver := len(m.Versions) + 1
	vdir := filepath.Join(ms.dir, id, fmt.Sprintf("v%d", ver))
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(filepath.Join(vdir, name), data, 0o644); err != nil {
		return nil, false, err
	}
	note := "原始数据"
	if source == "vehicle_push" {
		note = "车端实时上报"
	}
	m.Versions = append(m.Versions, MapVersion{
		Version: ver, Files: []string{name}, Kind: strings.TrimPrefix(kind, "3d_"),
		Note: note, Author: author, CreatedNS: now,
	})
	m.SHA256 = sha
	m.Size = int64(len(data))
	m.Source = source
	m.UpdatedNS = now
	m.LatestKind = kind
	ms.persistLocked()
	if ms.st != nil {
		ms.st.pushEvent("info", vehicleID, fmt.Sprintf("地图更新：%s（v%d，%s，%d 字节）", name, ver, note, len(data)))
	}
	ms.notify()
	return m, true, nil
}

// addDerivedLocked 以新版本形式追加派生产物（3D→2D / 编辑结果），原始版本不动。
// 调用方需持锁；files 为 name->bytes。
func (ms *MapStore) addDerivedLocked(m *MapEntry, kind, note, author string, meta map[string]any, files map[string][]byte) (*MapVersion, error) {
	ver := len(m.Versions) + 1
	vdir := filepath.Join(ms.dir, m.ID, fmt.Sprintf("v%d", ver))
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(vdir, filepath.Base(name)), b, 0o644); err != nil {
			return nil, err
		}
		names = append(names, filepath.Base(name))
	}
	sort.Strings(names)
	v := MapVersion{
		Version: ver, Files: names, Kind: kind, Note: note, Author: author,
		CreatedNS: time.Now().UnixNano(), DerivedFrom: fmt.Sprintf("v%d", ver-1), Meta: meta,
	}
	m.Versions = append(m.Versions, v)
	m.UpdatedNS = v.CreatedNS
	if kind == "png" {
		m.Has2D = true
	}
	ms.persistLocked()
	ms.notify()
	return &v, nil
}

// Convert 对地图最新版本执行一行命令 3D→2D（map_convert.py），产物落新版本。
func (ms *MapStore) Convert(id string) (*MapVersion, error) {
	ms.mu.Lock()
	m, ok := ms.maps[id]
	if !ok {
		ms.mu.Unlock()
		return nil, fmt.Errorf("地图不存在：%s", id)
	}
	if len(m.Versions) == 0 {
		ms.mu.Unlock()
		return nil, fmt.Errorf("地图无版本")
	}
	last := m.Versions[len(m.Versions)-1]
	srcName := last.Files[0]
	vdir := filepath.Join(ms.dir, id, fmt.Sprintf("v%d", last.Version))
	src := filepath.Join(vdir, srcName)
	outPNG := filepath.Join(os.TempDir(), fmt.Sprintf("ra-conv-%s-%d.png", id, time.Now().UnixNano()%100000))
	cmd := exec.Command(ms.pyBin, ms.script, src, outPNG)
	out, err := cmd.CombinedOutput()
	if err != nil {
		ms.mu.Unlock()
		return nil, fmt.Errorf("转换失败：%v：%s", err, string(out))
	}
	files := map[string][]byte{}
	for _, ext := range []string{".png", ".pgm", ".yaml"} {
		p := strings.TrimSuffix(outPNG, ".png") + ext
		if b, err := os.ReadFile(p); err == nil {
			// 产物用地图原名命名（不用临时文件名），下载/预览更直观
			clean := strings.TrimSuffix(m.Name, filepath.Ext(m.Name)) + ext
			files[clean] = b
			_ = os.Remove(p)
		}
	}
	if len(files) == 0 {
		ms.mu.Unlock()
		return nil, fmt.Errorf("转换无产物")
	}
	v, err := ms.addDerivedLocked(m, "png", "3D→2D（map_convert 一行命令）", "fleet-hub",
		map[string]any{"tool": "map_convert.py", "log": strings.TrimSpace(string(out))}, files)
	ms.mu.Unlock()
	if err == nil && ms.st != nil {
		ms.st.pushEvent("info", m.VehicleID, fmt.Sprintf("地图 %s 已完成 3D→2D（v%d）", m.Name, v.Version))
	}
	return v, err
}

// SaveEdit 保存编辑页提交的新 2D 版本（原始只读，编辑落成 v+1）。
func (ms *MapStore) SaveEdit(id, author string, png []byte, ops any) (*MapVersion, error) {
	if len(png) == 0 {
		return nil, fmt.Errorf("编辑内容为空")
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	m, ok := ms.maps[id]
	if !ok {
		return nil, fmt.Errorf("地图不存在：%s", id)
	}
	return ms.addDerivedLocked(m, "png", "人工编辑（套索擦除/恢复）", author,
		map[string]any{"ops": ops}, map[string][]byte{m.Name + ".edit.png": png})
}

// List 全量列表（最近更新在前）。
func (ms *MapStore) List() []*MapEntry {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	out := make([]*MapEntry, 0, len(ms.maps))
	for _, m := range ms.maps {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedNS > out[j].UpdatedNS })
	return out
}

// File 读取指定地图/版本/文件名的字节（名字必须在该版本清单内，防路径穿越）。
func (ms *MapStore) File(id, name string, version int) ([]byte, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	m, ok := ms.maps[id]
	if !ok {
		return nil, fmt.Errorf("地图不存在")
	}
	if version == 0 && len(m.Versions) > 0 {
		version = m.Versions[len(m.Versions)-1].Version
	}
	for _, v := range m.Versions {
		if v.Version != version {
			continue
		}
		for _, f := range v.Files {
			if f == filepath.Base(name) {
				return os.ReadFile(filepath.Join(ms.dir, id, fmt.Sprintf("v%d", version), f))
			}
		}
	}
	return nil, fmt.Errorf("文件不存在：%s@v%d", name, version)
}
