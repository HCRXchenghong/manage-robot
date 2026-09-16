package main

// groups.go：分组（= 独立项目平台）。超管把普通管理员/用户拉进分组，
// 车辆也归属分组；普通管理员与用户只能看到/调度自己分组的数据。

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Group struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MaxAdmins int    `json:"max_admins"` // 默认 5，超管可调
	MaxUsers  int    `json:"max_users"`  // 默认 10，超管可调
	CreatedNS int64  `json:"created_ns"`
}

type GroupStore struct {
	mu     sync.Mutex
	groups map[string]*Group
	seq    int
	db     *sql.DB
}

func NewGroupStore(dbs ...*sql.DB) *GroupStore {
	var db *sql.DB
	if len(dbs) > 0 {
		db = dbs[0]
	}
	gs := &GroupStore{groups: map[string]*Group{}, seq: 0, db: db}
	if db == nil {
		return gs
	}
	rows, err := db.Query("SELECT id, name, max_admins, max_users, (EXTRACT(EPOCH FROM created_at) * 1000000000)::bigint FROM org_groups")
	if err != nil {
		return gs
	}
	defer rows.Close()
	for rows.Next() {
		g := &Group{}
		if err := rows.Scan(&g.ID, &g.Name, &g.MaxAdmins, &g.MaxUsers, &g.CreatedNS); err != nil {
			continue
		}
		gs.groups[g.ID] = g
		var n int
		if _, err := fmt.Sscanf(g.ID, "g-%d", &n); err == nil && n > gs.seq {
			gs.seq = n
		}
	}
	return gs
}

func (gs *GroupStore) Create(name string) (*Group, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("分组名称必填")
	}
	gs.mu.Lock()
	defer gs.mu.Unlock()
	for _, g := range gs.groups {
		if g.Name == name {
			return nil, fmt.Errorf("分组已存在：%s", name)
		}
	}
	gs.seq++
	id := fmt.Sprintf("g-%d", gs.seq) // 稳定序号：重启后顺序不变，车端/网关可固定引用
	g := &Group{ID: id, Name: name, MaxAdmins: 5, MaxUsers: 10, CreatedNS: time.Now().UnixNano()}
	if gs.db != nil {
		if _, err := gs.db.Exec("INSERT INTO org_groups(id, name, max_admins, max_users, created_at) VALUES ($1,$2,$3,$4,$5)", g.ID, g.Name, g.MaxAdmins, g.MaxUsers, time.Unix(0, g.CreatedNS)); err != nil {
			gs.seq--
			return nil, err
		}
	}
	gs.groups[id] = g
	return g, nil
}

func (gs *GroupStore) Get(id string) (*Group, bool) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	g, ok := gs.groups[id]
	return g, ok
}

func (gs *GroupStore) List() []*Group {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	out := make([]*Group, 0, len(gs.groups))
	for _, g := range gs.groups {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedNS < out[j].CreatedNS })
	return out
}

func (gs *GroupStore) Update(id, name string, maxAdmins, maxUsers int) (*Group, error) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	g, ok := gs.groups[id]
	if !ok {
		return nil, fmt.Errorf("分组不存在")
	}
	if name != "" {
		for _, o := range gs.groups {
			if o.ID != id && o.Name == name {
				return nil, fmt.Errorf("分组名重复：%s", name)
			}
		}
		g.Name = name
	}
	if maxAdmins > 0 {
		g.MaxAdmins = maxAdmins
	}
	if maxUsers > 0 {
		g.MaxUsers = maxUsers
	}
	if gs.db != nil {
		if _, err := gs.db.Exec("UPDATE org_groups SET name=$2, max_admins=$3, max_users=$4 WHERE id=$1", g.ID, g.Name, g.MaxAdmins, g.MaxUsers); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func (gs *GroupStore) Delete(id string) error {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	if _, ok := gs.groups[id]; !ok {
		return fmt.Errorf("分组不存在")
	}
	if gs.db != nil {
		if _, err := gs.db.Exec("DELETE FROM org_groups WHERE id=$1", id); err != nil {
			return err
		}
	}
	delete(gs.groups, id)
	return nil
}
