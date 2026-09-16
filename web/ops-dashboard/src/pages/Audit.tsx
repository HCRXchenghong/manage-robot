// 审计中心（统一页，等保三级「安全审计」）：
//  - 车辆事件：车辆与平台的链路（注册/上下线/链路丢失）与车辆故障等异常；
//  - 系统事件：平台操作日志（登录/账号/接管/地图/任务等）；
//  - API 调用审计：开放 API 成功与失败全记录。
// 事件类标签共享：分级统计概览 + 级别/车辆/关键词/日期筛选 + 分组视图 + 分页 + 导出 + 行详情 + 自动刷新 + 超管清除。
import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import type { EventRow, EventStats } from "../api";
import { auditExportURL, clearEvents, fetchAuditPage, fetchEventStats, fetchEvents, fetchGroupNames, fetchKeys } from "../api";
import Modal from "../components/Modal";
import type { AuditEntry, APIKey, Me } from "../types";

interface Props {
  me: Me;
}

type Tab = "veh" | "sys" | "api";

const PAGE_SIZE = 20;

function readParams(): URLSearchParams {
  const h = window.location.hash.replace(/^#\/?/, "");
  const qi = h.indexOf("?");
  return new URLSearchParams(qi >= 0 ? h.slice(qi + 1) : "");
}

function fmtTime(tsNs: number): string {
  const d = new Date(tsNs / 1e6);
  const p = (n: number) => (n < 10 ? "0" + n : String(n));
  return (
    p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " +
    p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds())
  );
}

function fmtFull(tsNs: number): string {
  const d = new Date(tsNs / 1e6);
  const p = (n: number) => (n < 10 ? "0" + n : String(n));
  return (
    d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " +
    p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds())
  );
}

const RESULT_LABEL: Record<string, string> = {
  ok: "成功",
  auth_failed: "鉴权失败",
  denied: "权限拒绝",
  locked: "账号锁定",
  replay: "重放攻击",
  rate_limited: "触发限流",
  bad_request: "请求非法",
  expired: "API Key 已过期",
  error: "服务错误",
};

const TAB_DESC: Record<Tab, string> = {
  veh: "车辆与平台的链路（注册 / 上下线 / 链路丢失）与车辆故障、状态异常",
  sys: "平台操作全程留痕：登录 / 账号 / 接管 / 设备 / 地图 / 任务 / 开放 API",
  api: "开放 API 成功与失败全记录（含失败原因 / 调用备注 / 来源 / trace）",
};

export default function Audit({ me }: Props) {
  const p0 = useMemo(readParams, []);
  const t0 = p0.get("tab");
  const [tab, setTab] = useState<Tab>(t0 === "sys" || t0 === "api" || t0 === "veh" ? t0 : "veh");

  // ---- 事件（车辆 / 系统共用） ----
  const [level, setLevel] = useState(p0.get("level") || "all");
  const [veh, setVeh] = useState(p0.get("vehicle") || "");
  const [q, setQ] = useState(p0.get("q") || "");
  const [from, setFrom] = useState(p0.get("from") || "");
  const [to, setTo] = useState(p0.get("to") || "");
  const [groupBy, setGroupBy] = useState(p0.get("groupby") || "none");
  const [page, setPage] = useState(parseInt(p0.get("page") || "1", 10) || 1);
  const [data, setData] = useState<{ items: EventRow[]; total: number }>({ items: [], total: 0 });
  const [stats, setStats] = useState<EventStats | null>(null);
  const [busy, setBusy] = useState(false);
  const [auto, setAuto] = useState(false);
  const [detail, setDetail] = useState<EventRow | null>(null);
  const [groupNames, setGroupNames] = useState<Record<string, string>>({});
  const [exportOpen, setExportOpen] = useState(false);
  const [exportDate, setExportDate] = useState(new Date().toISOString().slice(0, 10));

  // 清除（超管二次确认）
  const [clearOpen, setClearOpen] = useState(false);
  const [clearId, setClearId] = useState<number | null>(null);
  const [cu, setCu] = useState(me.username);
  const [cp, setCp] = useState("");
  const [cfrom, setCfrom] = useState("");
  const [cto, setCto] = useState("");
  const [msg, setMsg] = useState("");

  // ---- API 调用审计 ----
  const [audit, setAudit] = useState<AuditEntry[]>([]);
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [fKey, setFKey] = useState("");
  const [fResult, setFResult] = useState("");
  const [fSource, setFSource] = useState<"api" | "platform" | "all">("api");
  const [auditDetail, setAuditDetail] = useState<AuditEntry | null>(null);
  const [auditCursor, setAuditCursor] = useState("");
  const [auditHasMore, setAuditHasMore] = useState(false);
  const [auditBusy, setAuditBusy] = useState(false);

  // 筛选状态写回 hash（刷新保持）
  useEffect(() => {
    const p = new URLSearchParams();
    p.set("tab", tab);
    if (level !== "all") p.set("level", level);
    if (veh) p.set("vehicle", veh);
    if (q) p.set("q", q);
    if (from) p.set("from", from);
    if (to) p.set("to", to);
    if (groupBy !== "none") p.set("groupby", groupBy);
    if (page > 1) p.set("page", String(page));
    window.history.replaceState(null, "", "#/audit?" + p.toString());
  }, [tab, level, veh, q, from, to, groupBy, page]);

  const load = useCallback(async () => {
    if (tab === "api") return;
    setBusy(true);
    try {
      const [r, s] = await Promise.all([
        fetchEvents({ page, page_size: PAGE_SIZE, level, vehicle: veh || "all", q, from, to, kind: tab }),
        fetchEventStats(tab).catch(() => null),
      ]);
      setData({ items: r.items, total: r.total });
      if (s) setStats(s);
    } catch {
      /* 后端未就绪保留旧数据 */
    }
    setBusy(false);
  }, [tab, page, level, veh, q, from, to]);

  const loadAudit = useCallback(async () => {
    setAuditBusy(true);
    try {
      const [au, ks] = await Promise.all([
        fetchAuditPage({ limit: 100, source: fSource, key_id: fKey || undefined, result: fResult || undefined }),
        fetchKeys().catch(() => [] as APIKey[]),
      ]);
      setAudit(au.audit);
      setAuditCursor(au.next_cursor || "");
      setAuditHasMore(!!au.has_more);
      setKeys(ks);
    } catch {
      /* 忽略 */
    }
    setAuditBusy(false);
  }, [fKey, fResult, fSource]);

  const loadMoreAudit = useCallback(async () => {
    if (!auditHasMore || !auditCursor || auditBusy) return;
    setAuditBusy(true);
    try {
      const next = await fetchAuditPage({ limit: 100, source: fSource, key_id: fKey || undefined, result: fResult || undefined, cursor: auditCursor });
      setAudit((old) => old.concat(next.audit));
      setAuditCursor(next.next_cursor || "");
      setAuditHasMore(!!next.has_more);
    } catch {
      /* 保留已加载的审计证据 */
    }
    setAuditBusy(false);
  }, [auditBusy, auditCursor, auditHasMore, fKey, fResult, fSource]);

  useEffect(() => {
    if (tab === "api") void loadAudit();
    else void load();
  }, [tab, load, loadAudit]);

  useEffect(() => {
    setPage(1);
  }, [tab, level, veh, q, from, to, groupBy]);

  // 自动刷新（5s）
  useEffect(() => {
    if (!auto) return;
    const t = window.setInterval(() => {
      if (tab === "api") void loadAudit();
      else void load();
    }, 5000);
    return () => window.clearInterval(t);
  }, [auto, tab, load, loadAudit]);

  useEffect(() => {
    void fetchGroupNames()
      .then((r) => {
        const m: Record<string, string> = {};
        for (const g of r.groups) m[g.id] = g.name;
        setGroupNames(m);
      })
      .catch(() => setGroupNames({}));
  }, []);

  const totalPages = Math.max(1, Math.ceil(data.total / PAGE_SIZE));

  const groupsView = useMemo(() => {
    if (groupBy === "none" || tab === "api") return null;
    const m = new Map<string, EventRow[]>();
    for (const e of data.items) {
      const key =
        groupBy === "level"
          ? e.level
          : groupBy === "vehicle"
            ? e.vehicle_id || "平台级"
            : groupBy === "actor"
              ? actorOf(e.text)
              : fmtTime(e.ts_ns).slice(0, 11);
      const arr = m.get(key) || [];
      arr.push(e);
      m.set(key, arr);
    }
    return [...m.entries()];
  }, [data.items, groupBy, tab]);

  const doExport = () => {
    window.open("/api/events/export?date=" + encodeURIComponent(exportDate) + "&kind=" + tab, "_blank");
    setExportOpen(false);
  };

  const doClear = async () => {
    setMsg("");
    try {
      const body: Record<string, unknown> = { username: cu, password: cp, kind: tab };
      if (clearId != null) body.id = clearId;
      else {
        if (!cfrom || !cto) throw new Error("请选择日期段");
        body.from = cfrom;
        body.to = cto;
      }
      const r = await clearEvents(body);
      setMsg("已删除 " + String((r as { deleted?: number }).deleted ?? 0) + " 条");
      setCp("");
      void load();
    } catch (e) {
      setMsg("清除失败：" + (e as Error).message);
    }
  };

  const renderRow = (e: EventRow) => (
    <tr key={e.id} className={"evt-row evt-" + e.level.toLowerCase()} style={{ cursor: "pointer" }} onClick={() => setDetail(e)}>
      <td className="mono" style={{ whiteSpace: "nowrap" }}>{fmtTime(e.ts_ns)}</td>
      <td><span className={"badge " + (e.level === "critical" ? "err" : e.level === "warn" ? "warn" : "info")}>{e.level}</span></td>
      <td className="mono">{e.vehicle_id || "平台"}</td>
      <td>{e.text}</td>
      <td className="dim">{e.group_id ? groupNames[e.group_id] || e.group_id : "—"}</td>
      {me.role === "super" && (
        <td style={{ textAlign: "right" }} onClick={(ev) => ev.stopPropagation()}>
          <button
            className="btn small"
            onClick={() => {
              setClearId(e.id);
              setMsg("");
              setClearOpen(true);
            }}
          >
            删除
          </button>
        </td>
      )}
    </tr>
  );

  return (
    <div className="panel alerts2" style={{ height: "100%", display: "flex", flexDirection: "column" }}>
      <div className="alerts2-head">
        <div>
          <div style={{ fontSize: 16, fontWeight: 700 }}>审计中心</div>
          <div className="muted">
            等保三级 · 全程留痕可追溯 · 日志保留 30 天 · {me.role === "super" ? "超级管理员可见全部" : "仅本分组可见"}
          </div>
        </div>
        <div className="btn-row">
          <label className="au-auto" title="每 5 秒自动刷新">
            <input type="checkbox" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
            自动刷新
          </label>
          {tab !== "api" && (
            <>
              <button className="btn small" onClick={() => setExportOpen(true)}>导出日志</button>
              {me.role === "super" && (
                <button className="btn small danger" onClick={() => { setClearId(null); setMsg(""); setClearOpen(true); }}>
                  清除日志
                </button>
              )}
              <button className="btn small" onClick={() => void load()}>刷新</button>
            </>
          )}
          {tab === "api" && (
            <button className="btn small" onClick={() => void loadAudit()}>刷新</button>
          )}
        </div>
      </div>

      <div className="tabs" style={{ padding: "0 14px" }}>
        <button className={"tab" + (tab === "veh" ? " active" : "")} onClick={() => setTab("veh")}>车辆事件</button>
        <button className={"tab" + (tab === "sys" ? " active" : "")} onClick={() => setTab("sys")}>系统事件</button>
        <button className={"tab" + (tab === "api" ? " active" : "")} onClick={() => setTab("api")}>API 调用审计</button>
      </div>
      <div className="muted" style={{ padding: "6px 14px 0", fontSize: 11.5 }}>{TAB_DESC[tab]}</div>

      {tab !== "api" && (
        <>
          <div className="au-stats">
            <div className="au-stat">
              <span className="au-stat-n">{stats ? stats.total : "—"}</span>
              <span className="au-stat-l">累计记录</span>
            </div>
            <div className="au-stat">
              <span className="au-stat-n">{stats ? stats.today : "—"}</span>
              <span className="au-stat-l">今日新增</span>
            </div>
            <div className="au-stat crit">
              <span className="au-stat-n">{stats ? stats.critical : "—"}</span>
              <span className="au-stat-l">critical</span>
            </div>
            <div className="au-stat warn">
              <span className="au-stat-n">{stats ? stats.warn : "—"}</span>
              <span className="au-stat-l">warn</span>
            </div>
            <div className="au-stat info">
              <span className="au-stat-n">{stats ? stats.info : "—"}</span>
              <span className="au-stat-l">info</span>
            </div>
          </div>

          <div className="alerts2-toolbar">
            <div className="tabs">
              {([["all", "全部级别"], ["critical", "critical"], ["warn", "warn"], ["info", "info"]] as const).map(([id, label]) => (
                <button key={id} className={"tab" + (level === id ? " active" : "")} onClick={() => setLevel(id)}>
                  {label}
                </button>
              ))}
            </div>
            <input className="input" style={{ width: 150 }} placeholder="车辆 / 对象" value={veh} onChange={(e) => setVeh(e.target.value)} />
            <input className="input" style={{ flex: 1, minWidth: 140 }} placeholder="关键词搜索…" value={q} onChange={(e) => setQ(e.target.value)} />
            <input className="input" type="date" value={from} onChange={(e) => setFrom(e.target.value)} title="起始日期" />
            <span className="muted">~</span>
            <input className="input" type="date" value={to} onChange={(e) => setTo(e.target.value)} title="结束日期" />
            <select className="input" value={groupBy} onChange={(e) => setGroupBy(e.target.value)} title="分组视图">
              <option value="none">不分组</option>
              <option value="level">按级别</option>
              <option value="vehicle">按车辆</option>
              {tab === "sys" && <option value="actor">按操作人</option>}
              <option value="day">按天</option>
            </select>
          </div>

          <div className="scroll" style={{ flex: 1, minHeight: 0 }}>
            <table className="table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>级别</th>
                  <th>车辆 / 对象</th>
                  <th>内容</th>
                  <th>分组</th>
                  {me.role === "super" && <th style={{ textAlign: "right" }}>操作</th>}
                </tr>
              </thead>
              <tbody>
                {groupsView
                  ? groupsView.map(([g, rows]) => (
                      <AuGroup key={g} name={g} rows={rows} render={renderRow} cols={me.role === "super" ? 6 : 5} />
                    ))
                  : data.items.map(renderRow)}
                {data.items.length === 0 && (
                  <tr><td colSpan={me.role === "super" ? 6 : 5} className="muted" style={{ padding: 16 }}>暂无记录（当前筛选条件下）</td></tr>
                )}
              </tbody>
            </table>
          </div>

          <div className="alerts2-foot">
            <span className="muted">共 {data.total} 条 · 第 {page}/{totalPages} 页{busy ? " · 加载中…" : ""}</span>
            <span className="spacer" />
            <button className="btn small" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>上一页</button>
            <button className="btn small" disabled={page >= totalPages} onClick={() => setPage((p) => p + 1)}>下一页</button>
          </div>
        </>
      )}

      {tab === "api" && (
        <>
          <div className="alerts2-toolbar">
            <select className="input" style={{ width: 150 }} value={fSource} onChange={(e) => setFSource(e.target.value as "api" | "platform" | "all")}>
              <option value="api">开放 API</option>
              <option value="platform">平台操作</option>
              <option value="all">全部审计</option>
            </select>
            <select className="input" style={{ width: 180 }} value={fKey} onChange={(e) => setFKey(e.target.value)}>
              <option value="">全部 API Key</option>
              {keys.map((k) => (
                <option key={k.id} value={k.id}>{k.name || k.id}</option>
              ))}
            </select>
            <select className="input" style={{ width: 160 }} value={fResult} onChange={(e) => setFResult(e.target.value)}>
              <option value="">全部结果</option>
              {Object.entries(RESULT_LABEL).map(([v, l]) => (
                <option key={v} value={v}>{l}</option>
              ))}
            </select>
            <span className="muted">成功与失败全记录（含失败原因 / 调用备注 / 来源 / trace）</span>
            <span className="spacer" />
            <a className="btn small" href={auditExportURL({ source: fSource, key_id: fKey || undefined, result: fResult || undefined })}>导出 CSV</a>
          </div>

          <div className="scroll" style={{ flex: 1, minHeight: 0 }}>
            <table className="table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>Key</th>
                  <th>方法</th>
                  <th>路径</th>
                  <th>结果</th>
                  <th>车辆</th>
                  <th>来源 IP</th>
                </tr>
              </thead>
              <tbody>
                {audit.length === 0 && (
                  <tr><td colSpan={7} className="muted" style={{ textAlign: "center", padding: 16 }}>暂无调用审计记录</td></tr>
                )}
                {audit.map((a, i) => (
                  <tr key={i} className="evt-row" style={{ cursor: "pointer" }} onClick={() => setAuditDetail(a)}>
                    <td className="mono" style={{ whiteSpace: "nowrap" }}>{fmtTime(a.ts_ns)}</td>
                    <td>{a.source === "platform" ? (a.actor || "—") : (a.key_name || "—")}</td>
                    <td className="mono">{a.method}</td>
                    <td className="mono">{a.source === "platform" ? (a.action || a.path) : a.path}</td>
                    <td>
                      <span className={"badge " + (a.result === "ok" ? "ok" : "err")}>
                        {RESULT_LABEL[a.result] || a.result}
                      </span>
                    </td>
                    <td className="mono">{a.vehicle_id || "—"}</td>
                    <td className="mono dim">{a.ip || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="alerts2-foot">
            <span className="muted">已加载 {audit.length} 条{auditBusy ? " · 加载中…" : ""} · 点击行查看完整审计详情</span>
            <span className="spacer" />
            {auditHasMore && <button className="btn small" disabled={auditBusy} onClick={() => void loadMoreAudit()}>加载更多</button>}
          </div>
        </>
      )}

      {detail && (
        <Modal title={"审计详情 · #" + detail.id} onClose={() => setDetail(null)} width="min(560px, 94vw)">
          <div className="au-detail">
            <div><span>时间</span><b className="mono">{fmtFull(detail.ts_ns)}</b></div>
            <div><span>级别</span><b><span className={"badge " + (detail.level === "critical" ? "err" : detail.level === "warn" ? "warn" : "info")}>{detail.level}</span></b></div>
            <div><span>分类</span><b>{detail.kind === "veh" || tab === "veh" ? "车辆事件（链路/故障）" : "系统事件（平台操作）"}</b></div>
            <div><span>涉及车辆</span><b className="mono">{detail.vehicle_id || "平台级"}</b></div>
            <div><span>归属分组</span><b>{detail.group_id ? groupNames[detail.group_id] || detail.group_id : "平台级"}</b></div>
            {tab === "sys" && <div><span>操作人</span><b>{actorOf(detail.text)}</b></div>}
            <div><span>内容</span><b style={{ whiteSpace: "normal" }}>{detail.text}</b></div>
          </div>
        </Modal>
      )}

      {auditDetail && (
        <Modal title="调用审计详情" onClose={() => setAuditDetail(null)} width="min(560px, 94vw)">
          <div className="au-detail">
            <div><span>时间</span><b className="mono">{fmtFull(auditDetail.ts_ns)}</b></div>
            <div><span>API Key</span><b>{auditDetail.key_name || "—"}（{auditDetail.key_id || "—"}）</b></div>
            <div><span>请求</span><b className="mono">{auditDetail.method} {auditDetail.path}</b></div>
            <div><span>结果</span><b><span className={"badge " + (auditDetail.result === "ok" ? "ok" : "err")}>{RESULT_LABEL[auditDetail.result] || auditDetail.result}</span></b></div>
            <div><span>HTTP 状态</span><b className="mono">{auditDetail.http || "—"}</b></div>
            <div><span>涉及车辆</span><b className="mono">{auditDetail.vehicle_id || "—"}</b></div>
            <div><span>来源 IP</span><b className="mono">{auditDetail.ip || "—"}</b></div>
            <div><span>Trace ID</span><b className="mono">{auditDetail.trace_id || "—"}</b></div>
            {auditDetail.remark && <div><span>调用备注</span><b style={{ whiteSpace: "normal" }}>{auditDetail.remark}</b></div>}
          </div>
        </Modal>
      )}

      {exportOpen && (
        <Modal title="导出日志（按日期）" onClose={() => setExportOpen(false)} width="min(440px, 94vw)">
          <div className="muted" style={{ marginBottom: 10 }}>
            仅导出所选日期当天的{tab === "veh" ? "车辆事件" : "系统事件"}（CSV，含 BOM，Excel 直接打开）。
          </div>
          <input className="input" type="date" value={exportDate} onChange={(e) => setExportDate(e.target.value)} />
          <div className="btn-row" style={{ justifyContent: "flex-end", marginTop: 12 }}>
            <button className="btn small" onClick={() => setExportOpen(false)}>取消</button>
            <button className="btn small primary" onClick={doExport}>导出</button>
          </div>
        </Modal>
      )}

      {clearOpen && me.role === "super" && tab !== "api" && (
        <Modal title={clearId != null ? "删除单条日志" : "清除日志（超管确认）"} onClose={() => setClearOpen(false)} width="min(480px, 94vw)">
          <div className="muted" style={{ marginBottom: 10 }}>
            等保三级：清除操作需超级管理员账号密码二次确认，并写入审计。仅清除当前标签（{tab === "veh" ? "车辆事件" : "系统事件"}）分类下的日志。
            {clearId == null && " 请选择要清除的日期段。"}
          </div>
          <div className="form-grid">
            <label>超管账号<input className="input" value={cu} onChange={(e) => setCu(e.target.value)} /></label>
            <label>超管密码<input className="input" type="password" value={cp} onChange={(e) => setCp(e.target.value)} /></label>
            {clearId == null && (
              <>
                <label>起始日期<input className="input" type="date" value={cfrom} onChange={(e) => setCfrom(e.target.value)} /></label>
                <label>结束日期<input className="input" type="date" value={cto} onChange={(e) => setCto(e.target.value)} /></label>
              </>
            )}
          </div>
          {msg && <div className={"msg " + (msg.startsWith("已删除") ? "ok" : "err")}>{msg}</div>}
          <div className="btn-row" style={{ justifyContent: "flex-end", marginTop: 12 }}>
            <button className="btn small" onClick={() => setClearOpen(false)}>取消</button>
            <button className="btn small danger" disabled={!cp} onClick={() => void doClear()}>确认清除</button>
          </div>
        </Modal>
      )}
    </div>
  );
}

// 从事件文案里提取操作人（"用户登录：X" / "… by X" / "X 绑定…" 等）。
function actorOf(text: string): string {
  const m =
    text.match(/用户登录：([^（(]+)/) ||
    text.match(/用户登出：(.+)$/) ||
    text.match(/by\s+(\S+)$/) ||
    text.match(/^([^：:]+)\s+绑定/) ||
    text.match(/远程接管：(\S+)\s+接管/) ||
    text.match(/接管开始：(\S+)/);
  return m ? m[1].trim() : "系统";
}

function AuGroup({ name, rows, render, cols }: { name: string; rows: EventRow[]; render: (e: EventRow) => ReactNode; cols: number }) {
  return (
    <>
      <tr className="group-head">
        <td colSpan={cols}>{name}（{rows.length} 条）</td>
      </tr>
      {rows.map(render)}
    </>
  );
}
