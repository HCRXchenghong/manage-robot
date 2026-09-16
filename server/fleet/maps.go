package main

// maps.go：地图中心后端（map-engine 阶段 2）。
// 职责：
//   - 地图仓库：每车多版本、追加式存储（原始数据永远只读，编辑/3D→2D 生成新版本）；
//   - 车端实时上报：地图代理每 15s 带 sha256 上传，内容没变则去重跳过；
//   - 一行命令转换：调用 map-engine/tools/map_convert.py 完成 3D(PCD/CSV)→2D(PNG/PGM/YAML)；
//   - 编辑保存：前端编辑页提交新 PNG + 操作记录，落成新版本。
// 存储布局：{dir}/{map_id}/v{N}/{file} + {dir}/index.json（进程重启可恢复）。

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	Version       int            `json:"version"`
	Files         []string       `json:"files"`
	Kind          string         `json:"kind"` // pcd|csv|png
	Note          string         `json:"note,omitempty"`
	Author        string         `json:"author,omitempty"`
	CreatedNS     int64          `json:"created_ns"`
	DerivedFrom   string         `json:"derived_from,omitempty"`
	ContentSHA256 string         `json:"content_sha256,omitempty"`
	ContentSize   int64          `json:"content_size,omitempty"`
	Meta          map[string]any `json:"meta,omitempty"`
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
	Uploader   string       `json:"uploader,omitempty"`
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
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	ms := &MapStore{dir: dir, pyBin: pyBin, script: script, maps: map[string]*MapEntry{}, hub: hub, st: st}
	if st != nil {
		st.setMapStore(ms)
	}
	if st != nil && st.db != nil {
		if err := ms.loadDB(); err != nil {
			return nil, fmt.Errorf("从 PostgreSQL 恢复地图: %w", err)
		}
		return ms, nil
	}
	// Volatile mode is explicitly non-production. It may read its isolated
	// diagnostic index, but persistent deployments never consult this file.
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

// loadDB restores the metadata and immutable version manifest from PostgreSQL.
// The local filesystem is only an object backend; it cannot create a map entry
// or a version that is absent from these authoritative tables.
func (ms *MapStore) loadDB() error {
	if ms == nil || ms.st == nil || ms.st.db == nil {
		return nil
	}
	rows, err := ms.st.db.Query(`SELECT id, vehicle_id, name, kind, sha256, size, source, COALESCE(uploader,''),
		(EXTRACT(EPOCH FROM created_at) * 1000000000)::bigint,
		(EXTRACT(EPOCH FROM updated_at) * 1000000000)::bigint
		FROM maps ORDER BY updated_at DESC, id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		m := &MapEntry{}
		if err := rows.Scan(&m.ID, &m.VehicleID, &m.Name, &m.Kind, &m.SHA256, &m.Size, &m.Source, &m.Uploader, &m.CreatedNS, &m.UpdatedNS); err != nil {
			rows.Close()
			return err
		}
		ms.maps[m.ID] = m
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	versions, err := ms.st.db.Query(`SELECT map_id, version, kind, note, author,
		COALESCE(derived_from,''),
		(EXTRACT(EPOCH FROM created_at) * 1000000000)::bigint,
		files::text, metadata::text, content_sha256, content_size
		FROM map_versions ORDER BY map_id, version`)
	if err != nil {
		return err
	}
	defer versions.Close()
	for versions.Next() {
		var mapID, filesJSON, metadataJSON string
		var v MapVersion
		if err := versions.Scan(&mapID, &v.Version, &v.Kind, &v.Note, &v.Author, &v.DerivedFrom,
			&v.CreatedNS, &filesJSON, &metadataJSON, &v.ContentSHA256, &v.ContentSize); err != nil {
			return err
		}
		m := ms.maps[mapID]
		if m == nil {
			return fmt.Errorf("map_versions 引用了不存在的地图 %s", mapID)
		}
		if err := json.Unmarshal([]byte(filesJSON), &v.Files); err != nil {
			return fmt.Errorf("地图 %s v%d 文件清单无效: %w", mapID, v.Version, err)
		}
		if metadataJSON != "" {
			if err := json.Unmarshal([]byte(metadataJSON), &v.Meta); err != nil {
				return fmt.Errorf("地图 %s v%d 元数据无效: %w", mapID, v.Version, err)
			}
		}
		m.Versions = append(m.Versions, v)
		if v.Kind == "png" {
			m.Has2D = true
		}
	}
	if err := versions.Err(); err != nil {
		return err
	}
	for _, m := range ms.maps {
		if len(m.Versions) > 0 {
			m.LatestKind = m.Versions[len(m.Versions)-1].Kind
		}
	}
	return nil
}

func (ms *MapStore) persistLocked() {
	if ms != nil && ms.st != nil && ms.st.db != nil {
		return
	}
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

type mapObjectManifest struct {
	Name        string
	ObjectKey   string
	SHA256      string
	Size        int64
	ContentType string
}

func cloneMapEntry(m *MapEntry) *MapEntry {
	if m == nil {
		return nil
	}
	cp := *m
	cp.Versions = make([]MapVersion, len(m.Versions))
	for i, v := range m.Versions {
		cp.Versions[i] = v
		cp.Versions[i].Files = append([]string(nil), v.Files...)
		if v.Meta != nil {
			cp.Versions[i].Meta = make(map[string]any, len(v.Meta))
			for k, value := range v.Meta {
				cp.Versions[i].Meta[k] = value
			}
		}
	}
	return &cp
}

// persistVersion commits one immutable version and its object manifest. The
// map row is locked so two Fleet instances cannot allocate the same version.
// Files are staged by the caller before this transaction; a failed commit is
// never reflected in memory and the caller removes the staged directory.
func (ms *MapStore) persistVersion(m *MapEntry, v *MapVersion, objects []mapObjectManifest) error {
	if ms == nil || ms.st == nil || ms.st.db == nil || m == nil || v == nil {
		return nil
	}
	filesJSON, err := json.Marshal(v.Files)
	if err != nil {
		return err
	}
	metadataJSON, err := json.Marshal(v.Meta)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := ms.st.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var current int
	var created time.Time
	err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT max(version) FROM map_versions WHERE map_id=$1),0), created_at
		FROM maps WHERE id=$1 FOR UPDATE`, m.ID).Scan(&current, &created)
	if err == sql.ErrNoRows {
		if m.CreatedNS <= 0 {
			m.CreatedNS = time.Now().UnixNano()
		}
		created = time.Unix(0, m.CreatedNS)
		if _, err := tx.ExecContext(ctx, `INSERT INTO maps(id, vehicle_id, name, kind, sha256, size, source, created_at, updated_at, uploader)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now(),$9)`, m.ID, m.VehicleID, m.Name, m.Kind, m.SHA256, m.Size, m.Source, created, m.Uploader); err != nil {
			return err
		}
		current = 0
	} else if err != nil {
		return err
	}
	if current+1 != v.Version {
		return fmt.Errorf("地图 %s 版本并发冲突：数据库最新 v%d，客户端准备 v%d", m.ID, current, v.Version)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO map_versions(map_id, version, kind, note, author, derived_from, created_at, files, metadata, content_sha256, content_size)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8::jsonb,$9::jsonb,$10,$11)`,
		m.ID, v.Version, v.Kind, v.Note, v.Author, v.DerivedFrom, time.Unix(0, v.CreatedNS), string(filesJSON), string(metadataJSON), v.ContentSHA256, v.ContentSize); err != nil {
		return err
	}
	for _, object := range objects {
		if _, err := tx.ExecContext(ctx, `INSERT INTO map_objects(map_id, version, file_name, object_key, sha256, size_bytes, content_type)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, m.ID, v.Version, object.Name, object.ObjectKey, object.SHA256, object.Size, object.ContentType); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE maps SET name=$2, kind=$3, sha256=$4, size=$5, source=$6, updated_at=$7, uploader=$8 WHERE id=$1`,
		m.ID, m.Name, m.Kind, m.SHA256, m.Size, m.Source, time.Unix(0, m.UpdatedNS), m.Uploader); err != nil {
		return err
	}
	return tx.Commit()
}

func fileManifest(mapID string, version int, name string, data []byte) mapObjectManifest {
	sum := sha256.Sum256(data)
	return mapObjectManifest{
		Name: name, ObjectKey: filepath.ToSlash(filepath.Join(mapID, fmt.Sprintf("v%d", version), name)),
		SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), ContentType: contentTypeOf(name),
	}
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
	if strings.ContainsAny(name, `/\\`) || filepath.Base(name) != name || name == "." || name == ".." {
		return nil, false, fmt.Errorf("文件名不得包含路径")
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
		return cloneMapEntry(m), false, nil // 内容没变：去重，不算新版本
	}
	m, ok := ms.maps[id]
	if !ok {
		m = &MapEntry{ID: id, VehicleID: vehicleID, Name: name, Kind: kind, CreatedNS: now}
	}
	ver := len(m.Versions) + 1
	vdir := filepath.Join(ms.dir, id, fmt.Sprintf("v%d", ver))
	if err := os.MkdirAll(vdir, 0o750); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(filepath.Join(vdir, name), data, 0o640); err != nil {
		_ = os.RemoveAll(vdir)
		return nil, false, err
	}
	note := "原始数据"
	if source == "vehicle_push" {
		note = "车端实时上报"
	}
	v := MapVersion{Version: ver, Files: []string{name}, Kind: strings.TrimPrefix(kind, "3d_"),
		Note: note, Author: author, CreatedNS: now, ContentSHA256: sha, ContentSize: int64(len(data))}
	candidate := cloneMapEntry(m)
	candidate.Versions = append(candidate.Versions, v)
	candidate.SHA256 = sha
	candidate.Size = int64(len(data))
	candidate.Source = source
	candidate.Kind = kind
	candidate.Uploader = author
	candidate.UpdatedNS = now
	candidate.LatestKind = kind
	if err := ms.persistVersion(candidate, &v, []mapObjectManifest{fileManifest(id, ver, name, data)}); err != nil {
		_ = os.RemoveAll(vdir)
		return nil, false, fmt.Errorf("地图元数据事务失败: %w", err)
	}
	ms.maps[id] = candidate
	ms.persistLocked()
	if ms.st != nil {
		ms.st.pushEvent("info", vehicleID, fmt.Sprintf("地图更新：%s（v%d，%s，%d 字节）", name, ver, note, len(data)), "sys")
	}
	ms.notify()
	return cloneMapEntry(candidate), true, nil
}

// addDerivedLocked 以新版本形式追加派生产物（3D→2D / 编辑结果），原始版本不动。
// 调用方需持锁；files 为 name->bytes。
func (ms *MapStore) addDerivedLocked(m *MapEntry, kind, note, author string, meta map[string]any, files map[string][]byte) (*MapVersion, error) {
	if m == nil || len(files) == 0 {
		return nil, fmt.Errorf("派生版本没有文件")
	}
	ver := len(m.Versions) + 1
	vdir := filepath.Join(ms.dir, m.ID, fmt.Sprintf("v%d", ver))
	if err := os.MkdirAll(vdir, 0o750); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	objects := make([]mapObjectManifest, 0, len(files))
	for name, b := range files {
		cleanName := filepath.Base(name)
		if cleanName == "." || cleanName == ".." || strings.ContainsAny(name, `/\\`) {
			_ = os.RemoveAll(vdir)
			return nil, fmt.Errorf("派生文件名不得包含路径")
		}
		if len(b) == 0 {
			_ = os.RemoveAll(vdir)
			return nil, fmt.Errorf("派生文件 %s 为空", cleanName)
		}
		if err := os.WriteFile(filepath.Join(vdir, cleanName), b, 0o640); err != nil {
			_ = os.RemoveAll(vdir)
			return nil, err
		}
		names = append(names, cleanName)
		objects = append(objects, fileManifest(m.ID, ver, cleanName, b))
	}
	sort.Strings(names)
	sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })
	v := MapVersion{
		Version: ver, Files: names, Kind: kind, Note: note, Author: author,
		CreatedNS: time.Now().UnixNano(), DerivedFrom: fmt.Sprintf("v%d", ver-1), Meta: meta,
	}
	var aggregate []byte
	for _, object := range objects {
		aggregate = append(aggregate, []byte(object.SHA256)...)
	}
	sum := sha256.Sum256(aggregate)
	v.ContentSHA256 = hex.EncodeToString(sum[:])
	for _, object := range objects {
		v.ContentSize += object.Size
	}
	candidate := cloneMapEntry(m)
	candidate.Versions = append(candidate.Versions, v)
	candidate.UpdatedNS = v.CreatedNS
	candidate.LatestKind = kind
	candidate.Uploader = author
	if kind == "png" {
		candidate.Has2D = true
	}
	if err := ms.persistVersion(candidate, &v, objects); err != nil {
		_ = os.RemoveAll(vdir)
		return nil, fmt.Errorf("派生地图元数据事务失败: %w", err)
	}
	ms.maps[m.ID] = candidate
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
		ms.st.pushEvent("info", m.VehicleID, fmt.Sprintf("地图 %s 已完成 3D→2D（v%d）", m.Name, v.Version), "sys")
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
		out = append(out, cloneMapEntry(m))
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

// Get 取地图元数据副本（不存在返回 nil），供路由做可见性判断。
func (ms *MapStore) Get(id string) *MapEntry {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	m, ok := ms.maps[id]
	if !ok {
		return nil
	}
	return cloneMapEntry(m)
}

// Delete 删除整张地图（含全部版本与磁盘文件）。车上原件不受影响。
func (ms *MapStore) Delete(id string) error {
	ms.mu.Lock()
	m, ok := ms.maps[id]
	if !ok {
		ms.mu.Unlock()
		return fmt.Errorf("地图不存在：%s", id)
	}
	name, veh, nver := m.Name, m.VehicleID, len(m.Versions)
	if ms.st != nil && ms.st.db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := ms.st.db.ExecContext(ctx, "DELETE FROM maps WHERE id=$1", id)
		cancel()
		if err != nil {
			ms.mu.Unlock()
			return fmt.Errorf("地图权威记录删除失败: %w", err)
		}
	}
	delete(ms.maps, id)
	ms.persistLocked()
	ms.mu.Unlock()
	if err := os.RemoveAll(filepath.Join(ms.dir, id)); err != nil {
		return fmt.Errorf("地图内容清理失败（权威记录已删除）: %w", err)
	}
	if ms.st != nil {
		ms.st.pushEvent("warn", veh, fmt.Sprintf("地图已删除：%s（含 %d 个版本，车上原件不动）", name, nver), "sys")
	}
	ms.notify()
	return nil
}

// MapPoints：3D 预览响应，点云降采样后的扁平数组（与 /api/pointcloud 同形状）。
type MapPoints struct {
	MapID       string    `json:"map_id"`
	Version     int       `json:"version"`
	Kind        string    `json:"kind"` // pcd|csv
	Name        string    `json:"name"`
	Count       int       `json:"count"`
	Total       int       `json:"total"`
	Sampled     bool      `json:"sampled"`
	Positions   []float64 `json:"positions"`
	Intensities []float64 `json:"intensities"`
}

// Points 取 3D 预览点云：优先指定版本，否则自新到旧找第一个 .pcd/.csv 版本。
// max 为返回点数上限（<=0 用默认 60 万，上限 300 万），等距降采样，坐标保留 2 位小数。
func (ms *MapStore) Points(id string, version, max int) (*MapPoints, error) {
	if max <= 0 {
		max = 600000
	}
	if max > 3000000 {
		max = 3000000
	}
	ms.mu.Lock()
	m, ok := ms.maps[id]
	if !ok {
		ms.mu.Unlock()
		return nil, fmt.Errorf("地图不存在：%s", id)
	}
	is3D := func(name string) bool {
		ext := strings.ToLower(filepath.Ext(name))
		return ext == ".pcd" || ext == ".csv"
	}
	var pick *MapVersion
	var pickFile string
	if version > 0 {
		for i := range m.Versions {
			v := &m.Versions[i]
			if v.Version != version {
				continue
			}
			for _, f := range v.Files {
				if is3D(f) {
					pick, pickFile = v, f
				}
			}
			break
		}
	} else {
		for i := len(m.Versions) - 1; i >= 0; i-- {
			v := &m.Versions[i]
			for _, f := range v.Files {
				if is3D(f) {
					pick, pickFile = v, f
					break
				}
			}
			if pick != nil {
				break
			}
		}
	}
	if pick == nil {
		ms.mu.Unlock()
		if version > 0 {
			return nil, fmt.Errorf("v%d 没有 3D 点云文件（.pcd/.csv）", version)
		}
		return nil, fmt.Errorf("该地图没有 3D 点云版本（.pcd/.csv）")
	}
	ver := pick.Version
	src := filepath.Join(ms.dir, id, fmt.Sprintf("v%d", ver), pickFile)
	ms.mu.Unlock()

	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("读取点云失败：%v", err)
	}
	var pos, inten []float64
	if strings.EqualFold(filepath.Ext(pickFile), ".pcd") {
		pos, inten, err = parsePCD(data)
	} else {
		pos, inten, err = parseCSVPoints(data)
	}
	if err != nil {
		return nil, err
	}
	total := len(pos) / 3
	if total < 3 {
		return nil, fmt.Errorf("有效点太少（%d），无法预览", total)
	}
	stride := (total + max - 1) / max
	if stride < 1 {
		stride = 1
	}
	hasI := len(inten) == total
	out := &MapPoints{
		MapID: id, Version: ver,
		Kind:      strings.TrimPrefix(strings.ToLower(filepath.Ext(pickFile)), "."),
		Name:      pickFile,
		Total:     total,
		Sampled:   stride > 1,
		Positions: make([]float64, 0, (total/stride+1)*3),
	}
	if hasI {
		out.Intensities = make([]float64, 0, total/stride+1)
	}
	for i := 0; i < total; i += stride {
		out.Positions = append(out.Positions, round2(pos[i*3]), round2(pos[i*3+1]), round2(pos[i*3+2]))
		if hasI {
			out.Intensities = append(out.Intensities, round2(inten[i]))
		}
	}
	out.Count = len(out.Positions) / 3
	return out, nil
}

// LatestPointsForVehicle returns the newest parseable 3D map belonging to one
// vehicle. A missing upload is an error; callers must surface it rather than
// replacing it with a generated scene.
func (ms *MapStore) LatestPointsForVehicle(vehicleID string, max int) (*MapPoints, error) {
	if vehicleID == "" {
		return nil, fmt.Errorf("vehicle_id 必填")
	}
	ms.mu.Lock()
	candidates := make([]struct {
		id      string
		updated int64
	}, 0)
	for _, m := range ms.maps {
		if m.VehicleID == vehicleID {
			candidates = append(candidates, struct {
				id      string
				updated int64
			}{id: m.ID, updated: m.UpdatedNS})
		}
	}
	ms.mu.Unlock()
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].updated > candidates[j].updated })
	if len(candidates) == 0 {
		return nil, fmt.Errorf("车辆 %s 尚未上传真实 3D 地图", vehicleID)
	}
	var lastErr error
	for _, candidate := range candidates {
		points, err := ms.Points(candidate.id, 0, max)
		if err == nil {
			return points, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("车辆 %s 没有可解析的真实 3D 地图: %w", vehicleID, lastErr)
}
