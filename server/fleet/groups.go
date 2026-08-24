package main

// groups.go：分组（= 独立项目平台）。超管把普通管理员/用户拉进分组，
// 车辆也归属分组；普通管理员与用户只能看到/调度自己分组的数据。

import (
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
}

func NewGroupStore() *GroupStore {
	g := &GroupStore{groups: map[string]*Group{}, seq: 0}
	g.Create("默认分组")
	g.Create("示范项目平台")
	return g
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
	return g, nil
}

func (gs *GroupStore) Delete(id string) error {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	if _, ok := gs.groups[id]; !ok {
		return fmt.Errorf("分组不存在")
	}
	delete(gs.groups, id)
	return nil
}
