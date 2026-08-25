// 告警与事件（成熟版）：服务端分页 + 关键词 + 级别 + 车辆 + 日期范围 + 分组视图，
// 按日期导出 CSV，超管二次确认清除，权限隔离（非超管仅本分组），保留 30 天。
// 筛选状态写入 hash —— 刷新不退回到未筛选状态。
import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import type { EventRow } from "../api";
import { clearEvents, fetchEvents, fetchGroupNames } from "../api";
import Modal from "../components/Modal";
import type { Me } from "../types";

interface Props {
  me: Me;
}

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

export default function Alerts({ me }: Props) {
  const p0 = useMemo(readParams, []);
  const [level, setLevel] = useState(p0.get("level") || "all");
  const [veh, setVeh] = useState(p0.get("vehicle") || "");
  const [q, setQ] = useState(p0.get("q") || "");
  const [from, setFrom] = useState(p0.get("from") || "");
  const [to, setTo] = useState(p0.get("to") || "");
  const [groupBy, setGroupBy] = useState(p0.get("groupby") || "none");
  const [page, setPage] = useState(parseInt(p0.get("page") || "1", 10) || 1);
  const [data, setData] = useState<{ items: EventRow[]; total: number }>({ items: [], total: 0 });
  const [busy, setBusy] = useState(false);

  const [exportOpen, setExportOpen] = useState(false);
  const [exportDate, setExportDate] = useState(new Date().toISOString().slice(0, 10));

  const [clearOpen, setClearOpen] = useState(false);
  const [clearId, setClearId] = useState<number | null>(null);
  const [cu, setCu] = useState(me.username);
  const [cp, setCp] = useState("");
  const [cfrom, setCfrom] = useState("");
  const [cto, setCto] = useState("");
  const [msg, setMsg] = useState("");
  const [groupNames, setGroupNames] = useState<Record<string, string>>({});

  // 筛选状态写回 hash（replaceState 不产生历史记录）——刷新保持筛选
  useEffect(() => {
    const p = new URLSearchParams();
    if (level !== "all") p.set("level", level);
    if (veh) p.set("vehicle", veh);
    if (q) p.set("q", q);
    if (from) p.set("from", from);
    if (to) p.set("to", to);
    if (groupBy !== "none") p.set("groupby", groupBy);
    if (page > 1) p.set("page", String(page));
    const qs = p.toString();
    window.history.replaceState(null, "", "#/alerts" + (qs ? "?" + qs : ""));
  }, [level, veh, q, from, to, groupBy, page]);

  const load = useCallback(async () => {
    setBusy(true);
    try {
      const r = await fetchEvents({ page, page_size: PAGE_SIZE, level, vehicle: veh || "all", q, from, to });
      setData({ items: r.items, total: r.total });
    } catch {
      /* 后端未就绪时保留旧数据 */
    }
    setBusy(false);
  }, [page, level, veh, q, from, to]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    setPage(1);
  }, [level, veh, q, from, to, groupBy]);

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
    if (groupBy === "none") return null;
    const m = new Map<string, EventRow[]>();
    for (const e of data.items) {
      const key =
        groupBy === "level"
          ? e.level
          : groupBy === "vehicle"
            ? e.vehicle_id || "平台级"
            : fmtTime(e.ts_ns).slice(0, 11);
      const arr = m.get(key) || [];
      arr.push(e);
      m.set(key, arr);
    }
    return [...m.entries()];
  }, [data.items, groupBy]);

  const doExport = () => {
    window.open("/api/events/export?date=" + encodeURIComponent(exportDate), "_blank");
    setExportOpen(false);
  };

  const doClear = async () => {
    setMsg("");
    try {
      const body: Record<string, unknown> = { username: cu, password: cp };
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
    <tr key={e.id} className={"evt-row evt-" + e.level.toLowerCase()}>
      <td className="mono" style={{ whiteSpace: "nowrap" }}>{fmtTime(e.ts_ns)}</td>
      <td><span className={"badge " + (e.level === "critical" ? "err" : e.level === "warn" ? "warn" : "info")}>{e.level}</span></td>
      <td className="mono">{e.vehicle_id || "平台"}</td>
      <td>{e.text}</td>
      <td className="dim">{e.group_id ? groupNames[e.group_id] || e.group_id : "—"}</td>
      {me.role === "super" && (
        <td style={{ textAlign: "right" }}>
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
          <div style={{ fontSize: 16, fontWeight: 700 }}>告警与事件</div>
          <div className="muted">
            {me.role === "super" ? "超级管理员：可见全部分组与平台级日志" : "仅显示本分组日志"} · 日志保留 30 天，到期自动清理
          </div>
        </div>
        <div className="btn-row">
          <button className="btn small" onClick={() => setExportOpen(true)}>导出日志</button>
          {me.role === "super" && (
            <button className="btn small danger" onClick={() => { setClearId(null); setMsg(""); setClearOpen(true); }}>
              清除日志
            </button>
          )}
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
        <input className="input" style={{ width: 180 }} placeholder="车辆 ID" value={veh} onChange={(e) => setVeh(e.target.value)} />
        <input className="input" style={{ flex: 1, minWidth: 140 }} placeholder="关键词搜索…" value={q} onChange={(e) => setQ(e.target.value)} />
        <input className="input" type="date" value={from} onChange={(e) => setFrom(e.target.value)} title="起始日期" />
        <span className="muted">~</span>
        <input className="input" type="date" value={to} onChange={(e) => setTo(e.target.value)} title="结束日期" />
        <select className="input" value={groupBy} onChange={(e) => setGroupBy(e.target.value)} title="分组视图">
          <option value="none">不分组</option>
          <option value="level">按级别分组</option>
          <option value="vehicle">按车辆分组</option>
          <option value="day">按天分组</option>
        </select>
      </div>

      <div className="scroll" style={{ flex: 1, minHeight: 0 }}>
        <table className="table">
          <thead>
            <tr>
              <th>时间</th>
              <th>级别</th>
              <th>车辆</th>
              <th>内容</th>
              <th>分组</th>
              {me.role === "super" && <th style={{ textAlign: "right" }}>操作</th>}
            </tr>
          </thead>
          <tbody>
            {groupsView
              ? groupsView.map(([g, rows]) => (
                  <GroupBlock key={g} name={g} rows={rows} render={renderRow} />
                ))
              : data.items.map(renderRow)}
            {data.items.length === 0 && (
              <tr><td colSpan={6} className="muted" style={{ padding: 16 }}>暂无日志（当前筛选条件下）</td></tr>
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

      {exportOpen && (
        <Modal title="导出日志（按日期）" onClose={() => setExportOpen(false)} width="min(440px, 94vw)">
          <div className="muted" style={{ marginBottom: 10 }}>仅导出所选日期当天的日志（CSV，含 BOM，Excel 直接打开）。</div>
          <input className="input" type="date" value={exportDate} onChange={(e) => setExportDate(e.target.value)} />
          <div className="btn-row" style={{ justifyContent: "flex-end", marginTop: 12 }}>
            <button className="btn small" onClick={() => setExportOpen(false)}>取消</button>
            <button className="btn small primary" onClick={doExport}>导出</button>
          </div>
        </Modal>
      )}

      {clearOpen && me.role === "super" && (
        <Modal title={clearId != null ? "删除单条日志" : "清除日志（超管确认）"} onClose={() => setClearOpen(false)} width="min(480px, 94vw)">
          <div className="muted" style={{ marginBottom: 10 }}>
            等保三级：清除操作需超级管理员账号密码二次确认，并写入审计。
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

function GroupBlock({ name, rows, render }: { name: string; rows: EventRow[]; render: (e: EventRow) => ReactNode }) {
  return (
    <>
      <tr className="group-head">
        <td colSpan={6}>{name}（{rows.length} 条）</td>
      </tr>
      {rows.map(render)}
    </>
  );
}
