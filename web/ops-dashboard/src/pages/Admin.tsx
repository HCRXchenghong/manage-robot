// 组织与成员管理。
//   超管视角：分组（独立项目平台）+ 成员 + 配额，全部可见可调。
//   管理员视角：只看到「成员管理」——不出现任何「分组」字样，
//   让甲方觉得这就是独属于他们自己的平台。
import { useCallback, useEffect, useState } from "react";
import {
  createGroup, createUser, deleteGroup, deleteUser, fetchGroups, fetchUsers,
  resetUserPassword, setUserGroups, updateGroup,
} from "../api";
import type { GroupInfo, Me, UserInfo } from "../types";

const roleLabel = (r: string) =>
  r === "super" ? "超级管理员" : r === "group_admin" ? "管理员" : "用户";

export default function AdminPage({ me }: { me: Me }) {
  const [groups, setGroups] = useState<GroupInfo[]>([]);
  const [users, setUsers] = useState<UserInfo[]>([]);
  const [msg, setMsg] = useState("");
  const [newGroup, setNewGroup] = useState("");
  const [form, setForm] = useState({ username: "", display_name: "", password: "", role: "user", groups: [] as string[] });
  const [editing, setEditing] = useState("");
  const [editPwd, setEditPwd] = useState("");

  const isSuper = me.role === "super";

  const reload = useCallback(async () => {
    try {
      setGroups(await fetchGroups());
      setUsers(await fetchUsers());
    } catch (e) {
      setMsg("加载失败：" + String(e));
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  const wrap = async (fn: () => Promise<void>, okMsg: string) => {
    try {
      await fn();
      setMsg(okMsg);
    } catch (e) {
      setMsg("失败：" + String(e).replace(/^Error: /, ""));
    }
    void reload();
  };

  const toggleFormGroup = (gid: string, list: string[], set: (v: string[]) => void) => {
    set(list.includes(gid) ? list.filter((x) => x !== gid) : [...list, gid]);
  };

  const saveQuota = (g: GroupInfo) => {
    void wrap(
      () => updateGroup(g.id, { max_admins: g.max_admins, max_users: g.max_users }),
      "分组「" + g.name + "」配额已更新",
    );
  };

  return (
    <div style={{ padding: 16, overflow: "auto", height: "100%" }}>
      <div style={{ fontSize: 16, fontWeight: 700 }}>{isSuper ? "组织管理" : "成员管理"}</div>
      <div className="muted" style={{ marginBottom: 12 }}>
        {isSuper
          ? "分组 = 一个独立的项目平台：车辆、地图、任务都按分组隔离。超级管理员全局 ≤10 人；每分组默认最多 5 名管理员、10 名用户（可调）。"
          : "管理平台成员：创建账号、重置密码、删除账号。"}
      </div>
      {msg && <div className="notice">{msg}</div>}

      {isSuper && (
        <div className="panel" style={{ padding: 14, marginBottom: 12 }}>
          <div className="panel-title">分组管理</div>
          <div className="btn-row" style={{ marginBottom: 10 }}>
            <input className="input" style={{ width: 240 }} placeholder="新分组名称（如：XX 园区项目）" value={newGroup} onChange={(e) => setNewGroup(e.target.value)} />
            <button className="btn primary" onClick={() => void wrap(async () => { await createGroup(newGroup); setNewGroup(""); }, "分组已创建")}>
              创建分组
            </button>
          </div>
          <table className="table">
            <thead>
              <tr>
                <th>分组 ID</th><th>名称</th><th>管理员（在用/配额）</th><th>用户（在用/配额）</th>
                <th>配额调整</th><th></th>
              </tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <tr key={g.id}>
                  <td className="mono">{g.id}</td>
                  <td>{g.name}</td>
                  <td className="mono">{g.admins ?? 0} / {g.max_admins}</td>
                  <td className="mono">{g.users ?? 0} / {g.max_users}</td>
                  <td>
                    <span className="row" style={{ gap: 4 }}>
                      <input className="input" style={{ width: 56 }} type="number" value={g.max_admins}
                        onChange={(e) => setGroups((prev) => prev.map((x) => x.id === g.id ? { ...x, max_admins: parseInt(e.target.value || "0", 10) } : x))} />
                      <input className="input" style={{ width: 56 }} type="number" value={g.max_users}
                        onChange={(e) => setGroups((prev) => prev.map((x) => x.id === g.id ? { ...x, max_users: parseInt(e.target.value || "0", 10) } : x))} />
                      <button className="btn small" onClick={() => saveQuota(g)}>保存</button>
                    </span>
                  </td>
                  <td>
                    <button className="btn small danger" onClick={() => void wrap(() => deleteGroup(g.id), "分组已删除")}>删除</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="panel" style={{ padding: 14, marginBottom: 12 }}>
        <div className="panel-title">新建账号</div>
        <div className="row" style={{ gap: 8, flexWrap: "wrap", marginBottom: 8 }}>
          <input className="input" style={{ width: 140 }} placeholder="登录用户名" value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} />
          <input className="input" style={{ width: 140 }} placeholder="姓名" value={form.display_name} onChange={(e) => setForm({ ...form, display_name: e.target.value })} />
          <input className="input" style={{ width: 180 }} type="password" placeholder="初始密码（≥8位含3类字符）" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
          <select className="input" value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}>
            {isSuper && <option value="super">超级管理员</option>}
            <option value="group_admin">管理员</option>
            <option value="user">用户</option>
          </select>
        </div>
        {isSuper && form.role !== "super" && (
          <div className="row" style={{ gap: 6, flexWrap: "wrap", marginBottom: 8 }}>
            <span className="muted" style={{ fontSize: 12 }}>所属分组：</span>
            {groups.map((g) => (
              <label key={g.id} className="chip" style={{ cursor: "pointer" }}>
                <input
                  type="checkbox"
                  checked={form.groups.includes(g.id)}
                  onChange={() => toggleFormGroup(g.id, form.groups, (v) => setForm({ ...form, groups: v }))}
                />
                {" "}{g.name}
              </label>
            ))}
          </div>
        )}
        {!isSuper && (
          <div className="muted" style={{ fontSize: 12, marginBottom: 8 }}>新账号创建后即可登录本平台。</div>
        )}
        <button
          className="btn primary"
          onClick={() => void wrap(async () => {
            await createUser(form);
            setForm({ username: "", display_name: "", password: "", role: "user", groups: [] });
          }, "账号已创建")}
        >
          创建账号
        </button>
      </div>

      <div className="panel" style={{ padding: 14 }}>
        <div className="panel-title">成员列表 <span className="hint">（{isSuper ? "全部分组" : "本平台"}）</span></div>
        <table className="table">
          <thead>
            <tr>
              <th>用户名</th><th>姓名</th><th>角色</th>
              {isSuper && <th>分组</th>}
              <th></th>
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <>
                <tr key={u.username}>
                  <td className="mono">{u.username}</td>
                  <td>{u.display_name}</td>
                  <td>
                    <span className={"badge" + (u.role === "super" ? " kind-2d_png" : u.role === "group_admin" ? " kind-3d_pcd" : "")}>
                      {roleLabel(u.role)}
                    </span>
                  </td>
                  {isSuper && (
                    <td className="muted">{u.role === "super" ? "—" : (u.groups || []).map((g) => groups.find((x) => x.id === g)?.name || g).join("、")}</td>
                  )}
                  <td>
                    {u.username !== me.username && u.role !== "super" && (
                      <span className="row" style={{ gap: 4 }}>
                        <button className="btn small" onClick={() => { setEditing(editing === u.username ? "" : u.username); setEditPwd(""); }}>
                          {editing === u.username ? "收起" : "管理"}
                        </button>
                        <button className="btn small danger" onClick={() => void wrap(() => deleteUser(u.username), "已删除 " + u.username)}>
                          删除
                        </button>
                      </span>
                    )}
                  </td>
                </tr>
                {editing === u.username && (
                  <tr key={u.username + "-edit"}>
                    <td colSpan={isSuper ? 5 : 4} style={{ background: "var(--bg-soft)" }}>
                      {isSuper && (
                        <div className="row" style={{ gap: 6, flexWrap: "wrap", marginBottom: 8 }}>
                          <span className="muted" style={{ fontSize: 12 }}>调整分组：</span>
                          {groups.map((g) => (
                            <label key={g.id} className="chip" style={{ cursor: "pointer" }}>
                              <input
                                type="checkbox"
                                checked={(u.groups || []).includes(g.id)}
                                onChange={() => void wrap(() => setUserGroups(u.username,
                                  (u.groups || []).includes(g.id) ? (u.groups || []).filter((x) => x !== g.id) : [...(u.groups || []), g.id]), "分组已更新")}
                              />
                              {" "}{g.name}
                            </label>
                          ))}
                        </div>
                      )}
                      <div className="row" style={{ gap: 6 }}>
                        <input className="input" style={{ width: 240 }} type="password" placeholder="重置为新密码（≥8位含3类字符）" value={editPwd} onChange={(e) => setEditPwd(e.target.value)} />
                        <button className="btn small" onClick={() => void wrap(() => resetUserPassword(u.username, editPwd), "密码已重置，该用户会话已强制下线")}>
                          重置密码
                        </button>
                      </div>
                    </td>
                  </tr>
                )}
              </>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
