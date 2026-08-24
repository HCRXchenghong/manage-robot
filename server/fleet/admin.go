package main

// admin.go：组织管理 API（分组 + 成员），以及登录相关 HTTP 路由。
// 权限矩阵：
//   super        管所有分组与所有人；可调配额；超管总数 ≤ 10
//   group_admin  只能管自己分组内的人（建号/改分组/重置密码/删号），不可碰超管
//   user         无管理面

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// ---------- 登录路由 ----------

func registerAuthRoutes(mux *http.ServeMux, svc *Services) {
	mux.HandleFunc("GET /api/captcha", func(w http.ResponseWriter, r *http.Request) {
		id, img := svc.auth.NewCaptcha()
		writeJSON(w, http.StatusOK, map[string]string{"captcha_id": id, "image": img})
	})
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		u, _ := body["username"].(string)
		p, _ := body["password"].(string)
		token, note, err := svc.auth.LoginPassword(u, p, r.RemoteAddr)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "note": note, "preauth": token})
	})
	// 第二段：人机验证通过才发会话 Cookie。
	mux.HandleFunc("POST /api/auth/verify", func(w http.ResponseWriter, r *http.Request) {
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		pre, _ := body["preauth"].(string)
		cid, _ := body["captcha_id"].(string)
		cans, _ := body["captcha_answer"].(string)
		sess, err := svc.auth.FinishLogin(pre, cid, cans, r.RemoteAddr)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: "ra_session", Value: sess.Token, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"me": map[string]any{"username": sess.Username, "role": sess.Role, "groups": sess.Groups},
		})
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		svc.auth.Logout(r)
		http.SetCookie(w, &http.Cookie{Name: "ra_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
		if sess != nil && svc.auth.st != nil {
			svc.auth.st.pushEvent("info", "", "用户登出："+sess.Username)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		display := ""
		if u := svc.auth.userNamed(sess.Username); u != nil {
			display = u.DisplayName
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"username": sess.Username, "role": sess.Role, "groups": sess.Groups, "display_name": display,
		})
	})
	// 修改密码（等保：改密后其余会话全部失效）
	mux.HandleFunc("POST /api/auth/password", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		old, _ := body["old"].(string)
		nw, _ := body["new"].(string)
		if err := svc.auth.ChangePassword(sess, old, nw, r); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
}

// ---------- 组织管理 ----------

func intersects(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func registerAdminRoutes(mux *http.ServeMux, svc *Services) {
	// 分组列表：超管全部；普通管理员只看自己所在；用户同。
	mux.HandleFunc("GET /api/admin/groups", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
			return
		}
		// 普通管理员不感知「分组」概念：返回空列表，前端自然不显示。
		if sess.Role == "group_admin" {
			writeJSON(w, http.StatusOK, map[string]any{"groups": []any{}})
			return
		}
		all := svc.groups.List()
		counts := svc.auth.groupCounts()
		type row struct {
			*Group
			Admins int `json:"admins"`
			Users  int `json:"users"`
		}
		out := []row{}
		for _, g := range all {
			if sess.Role != "super" && !contains(sess.Groups, g.ID) {
				continue
			}
			c := counts[g.ID]
			out = append(out, row{Group: g, Admins: c[0], Users: c[1]})
		}
		writeJSON(w, http.StatusOK, map[string]any{"groups": out})
	})

	mux.HandleFunc("POST /api/admin/groups", func(w http.ResponseWriter, r *http.Request) {
		if requireRole(w, r, "super") == nil {
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		name, _ := body["name"].(string)
		g, err := svc.groups.Create(name)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": g})
	})

	// 更新分组（改名/配额）——仅超管
	mux.HandleFunc("POST /api/admin/groups/{id}", func(w http.ResponseWriter, r *http.Request) {
		if requireRole(w, r, "super") == nil {
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		name, _ := body["name"].(string)
		g, err := svc.groups.Update(r.PathValue("id"), name, intOf(body["max_admins"]), intOf(body["max_users"]))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group": g})
	})

	mux.HandleFunc("POST /api/admin/groups/{id}/delete", func(w http.ResponseWriter, r *http.Request) {
		if requireRole(w, r, "super") == nil {
			return
		}
		id := r.PathValue("id")
		if svc.auth.groupHasMembers(id) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "分组内还有成员，先把人移出"})
			return
		}
		if svc.auth.groupHasVehicles(id) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "分组内还有车辆，先处理车辆归属"})
			return
		}
		if err := svc.groups.Delete(id); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// 成员列表：超管全部；普通管理员仅自己分组内
	mux.HandleFunc("GET /api/admin/users", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil || sess.Role == "user" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "权限不足"})
			return
		}
		users := svc.auth.ListUsers()
		out := users[:0]
		for _, u := range users {
			if sess.Role == "super" || intersects(sess.Groups, u.Groups) {
				out = append(out, u)
			}
		}
		// 普通管理员看不到分组字段（甲方视角：这就是他的平台成员列表）。
		if sess.Role == "group_admin" {
			for _, u := range out {
				u.Groups = nil
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"users": out})
	})

	// 建号（带配额校验）
	mux.HandleFunc("POST /api/admin/users", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil || sess.Role == "user" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "权限不足"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		username, _ := body["username"].(string)
		display, _ := body["display_name"].(string)
		pwd, _ := body["password"].(string)
		role, _ := body["role"].(string)
		groups := strSliceOf(body["groups"])
		if sess.Role == "group_admin" {
			if role == "super" {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "普通管理员不能创建超级管理员"})
				return
			}
			// 普通管理员建号默认进自己的分组（前端不展示分组选择）。
			if len(groups) == 0 {
				groups = sess.Groups
			} else if !intersects(sess.Groups, groups) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "权限不足"})
				return
			}
		}
		u, err := svc.auth.CreateUser(username, display, pwd, role, groups)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if svc.auth.st != nil {
			svc.auth.st.pushEvent("info", "", "新建账号："+u.Username+"（"+role+"）by "+sess.Username)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u})
	})

	// 改分组（拉入/移出）
	mux.HandleFunc("POST /api/admin/users/{name}/groups", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil || sess.Role == "user" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "权限不足"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		groups := strSliceOf(body["groups"])
		u, err := svc.auth.SetGroups(sess, r.PathValue("name"), groups)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u})
	})

	// 重置密码
	mux.HandleFunc("POST /api/admin/users/{name}/reset", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil || sess.Role == "user" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "权限不足"})
			return
		}
		body := readJSONBody(w, r)
		if body == nil {
			return
		}
		pwd, _ := body["password"].(string)
		if err := svc.auth.ResetPassword(sess, r.PathValue("name"), pwd, r); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	// 删号
	mux.HandleFunc("POST /api/admin/users/{name}/delete", func(w http.ResponseWriter, r *http.Request) {
		sess := sessOf(r)
		if sess == nil || sess.Role == "user" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "权限不足"})
			return
		}
		if err := svc.auth.DeleteUser(sess, r.PathValue("name")); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if svc.auth.st != nil {
			svc.auth.st.pushEvent("warn", "", "删除账号："+r.PathValue("name")+" by "+sess.Username)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
}

// ---------- 小工具 ----------

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func strSliceOf(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ListUsers 按用户名排序输出（不含敏感字段，PassHash/Salt 带 json:"-"）。
func (a *AuthStore) ListUsers() []*User {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*User, 0, len(a.users))
	for _, u := range a.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

func (a *AuthStore) userNamed(name string) *User {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.users[name]
}

// groupCounts 每分组的 [管理员数, 用户数]。
func (a *AuthStore) groupCounts() map[string][2]int {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string][2]int{}
	for _, u := range a.users {
		for _, g := range u.Groups {
			c := out[g]
			if u.Role == "group_admin" {
				c[0]++
			} else if u.Role == "user" {
				c[1]++
			}
			out[g] = c
		}
	}
	return out
}

func (a *AuthStore) groupHasMembers(gid string) bool {
	for _, u := range a.ListUsers() {
		if contains(u.Groups, gid) {
			return true
		}
	}
	return false
}

func (a *AuthStore) groupHasVehicles(gid string) bool {
	if a.st == nil {
		return false
	}
	for _, v := range a.st.Snapshot().Vehicles {
		if v.Group == gid {
			return true
		}
	}
	return false
}

// CreateUser 建号（配额：超管≤10；每分组管理员/用户 ≤ 组配额）。
func (a *AuthStore) CreateUser(username, display, pwd, role string, groups []string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" || display == "" {
		return nil, errNew("用户名与姓名必填")
	}
	if role != "super" && role != "group_admin" && role != "user" {
		return nil, errNew("角色非法")
	}
	if err := PasswordOK(pwd); err != nil {
		return nil, err
	}
	if role != "super" && len(groups) == 0 {
		return nil, errNew("普通管理员/用户必须属于至少一个分组")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.users[username]; ok {
		return nil, errNew("用户名已存在")
	}
	if role == "super" {
		n := 0
		for _, u := range a.users {
			if u.Role == "super" {
				n++
			}
		}
		if n >= maxSuperAdmins {
			return nil, errNew("超级管理员最多 10 人")
		}
	} else {
		counts := map[string][2]int{}
		for _, u := range a.users {
			for _, g := range u.Groups {
				c := counts[g]
				if u.Role == "group_admin" {
					c[0]++
				} else if u.Role == "user" {
					c[1]++
				}
				counts[g] = c
			}
		}
		for _, gid := range groups {
			g, ok := a.groups.Get(gid)
			if !ok {
				return nil, errNew("分组不存在：" + gid)
			}
			c := counts[gid]
			if role == "group_admin" && c[0] >= g.MaxAdmins {
				return nil, errNew("分组「" + g.Name + "」管理员已达上限（" + itoa(g.MaxAdmins) + "）")
			}
			if role == "user" && c[1] >= g.MaxUsers {
				return nil, errNew("分组「" + g.Name + "」用户已达上限（" + itoa(g.MaxUsers) + "）")
			}
		}
	}
	salt := randToken(8)
	u := &User{
		Username: username, DisplayName: display, Role: role, Groups: groups,
		PassHash: hashPassword(salt, pwd), Salt: salt,
		CreatedNS: time.Now().UnixNano(), PassSetNS: time.Now().UnixNano(),
	}
	a.users[username] = u
	return u, nil
}

// SetGroups 调整用户分组（同样过配额；超管账号不可改）。
func (a *AuthStore) SetGroups(actor *Session, username string, groups []string) (*User, error) {
	a.mu.Lock()
	u, ok := a.users[username]
	if !ok {
		a.mu.Unlock()
		return nil, errNew("用户不存在")
	}
	if u.Role == "super" {
		a.mu.Unlock()
		if actor.Role != "super" {
			return nil, errNew("无权调整超级管理员")
		}
		return nil, errNew("超级管理员不属于分组")
	}
	if actor.Role != "super" && !intersects(actor.Groups, u.Groups) && !intersects(actor.Groups, groups) {
		a.mu.Unlock()
		return nil, errNew("只能调整自己分组内的成员")
	}
	a.mu.Unlock()

	// 复用建号时的配额校验：模拟该角色在这些分组中 +1
	tmp := &User{Username: u.Username + "#tmp", Role: u.Role, Groups: groups}
	if err := a.checkQuotaFor(tmp, u); err != nil {
		return nil, err
	}
	a.mu.Lock()
	u.Groups = groups
	role := u.Role
	a.mu.Unlock()
	a.RefreshSessions(username, groups, role)
	return u, nil
}

// checkQuotaFor 校验「把 move 从原分组挪到 tmp.Groups」后不超配额。
func (a *AuthStore) checkQuotaFor(tmp, move *User) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	counts := map[string][2]int{}
	for _, u := range a.users {
		if move != nil && u.Username == move.Username {
			continue
		}
		for _, g := range u.Groups {
			c := counts[g]
			if u.Role == "group_admin" {
				c[0]++
			} else if u.Role == "user" {
				c[1]++
			}
			counts[g] = c
		}
	}
	for _, gid := range tmp.Groups {
		g, ok := a.groups.Get(gid)
		if !ok {
			return errNew("分组不存在：" + gid)
		}
		c := counts[gid]
		if tmp.Role == "group_admin" && c[0] >= g.MaxAdmins {
			return errNew("分组「" + g.Name + "」管理员已达上限")
		}
		if tmp.Role == "user" && c[1] >= g.MaxUsers {
			return errNew("分组「" + g.Name + "」用户已达上限")
		}
	}
	return nil
}

func (a *AuthStore) ResetPassword(actor *Session, username, pwd string, r *http.Request) error {
	if err := PasswordOK(pwd); err != nil {
		return err
	}
	a.mu.Lock()
	u, ok := a.users[username]
	if !ok {
		a.mu.Unlock()
		return errNew("用户不存在")
	}
	if u.Role == "super" && actor.Role != "super" {
		a.mu.Unlock()
		return errNew("无权重置超级管理员密码")
	}
	if actor.Role != "super" && !intersects(actor.Groups, u.Groups) {
		a.mu.Unlock()
		return errNew("只能重置自己分组内成员的密码")
	}
	salt := randToken(8)
	u.Salt = salt
	u.PassHash = hashPassword(salt, pwd)
	u.PassSetNS = time.Now().UnixNano()
	u.FailCount = 0
	u.LockedUntil = 0
	a.mu.Unlock()
	a.KillSessions(username, "")
	return nil
}

func (a *AuthStore) DeleteUser(actor *Session, username string) error {
	a.mu.Lock()
	u, ok := a.users[username]
	if !ok {
		a.mu.Unlock()
		return errNew("用户不存在")
	}
	if u.Role == "super" && actor.Role != "super" {
		a.mu.Unlock()
		return errNew("无权删除超级管理员")
	}
	if actor.Role != "super" && !intersects(actor.Groups, u.Groups) {
		a.mu.Unlock()
		return errNew("只能删除自己分组内的成员")
	}
	nSuper := 0
	for _, x := range a.users {
		if x.Role == "super" {
			nSuper++
		}
	}
	if u.Role == "super" && nSuper <= 1 {
		a.mu.Unlock()
		return errNew("至少要保留一名超级管理员")
	}
	delete(a.users, username)
	a.mu.Unlock()
	a.KillSessions(username, "")
	return nil
}

// ChangePassword 本人改密（旧密码校验 + 复杂度 + 踢其他会话）。
func (a *AuthStore) ChangePassword(sess *Session, old, nw string, r *http.Request) error {
	if err := PasswordOK(nw); err != nil {
		return err
	}
	a.mu.Lock()
	u, ok := a.users[sess.Username]
	if !ok {
		a.mu.Unlock()
		return errNew("用户不存在")
	}
	if hashPassword(u.Salt, old) != u.PassHash {
		a.mu.Unlock()
		return errNew("原密码错误")
	}
	salt := randToken(8)
	u.Salt = salt
	u.PassHash = hashPassword(salt, nw)
	u.PassSetNS = time.Now().UnixNano()
	a.mu.Unlock()
	c, err := r.Cookie("ra_session")
	except := ""
	if err == nil {
		except = c.Value
	}
	a.KillSessions(sess.Username, except)
	if a.open != nil {
		a.open.logAudit(AuditEntry{TsNS: time.Now().UnixNano(), KeyName: sess.Username,
			Method: "POST", Path: "/api/auth/password", Result: "ok", IP: r.RemoteAddr, Detail: "修改密码"})
	}
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }

func errNew(s string) error { return errString(s) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
