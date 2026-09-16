// API 平台（开放接口管理）：
//  - Key 管理：创建（备注 + 功能白名单 + 车辆白名单，未勾选一律不允许）/编辑/撤销
//  - 调用审计：成功与失败全记录，含调用备注、涉及车辆、来源与失败原因，可按 Key/结果过滤
//  - 接口文档：全部开放接口 + 所需权限 + 签名算法 + 等保三级对照
import { useCallback, useEffect, useMemo, useState } from "react";
import Modal from "../components/Modal";
import {
  createKey, fetchFleetSnap, fetchKeys, fetchScopes, revokeKey, updateKey,
} from "../api";
import type { APIKey, OpenScope, VehicleSnap } from "../types";

function fmtTime(ns?: number): string {
  if (!ns) return "-";
  const d = new Date(ns / 1e6);
  const p = (x: number) => (x < 10 ? "0" + x : String(x));
  return (
    p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " +
    p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds())
  );
}

function resultLabel(r: string): string {
  const m: Record<string, string> = {
    ok: "成功",
    auth_failed: "鉴权失败",
    denied: "权限拒绝",
    locked: "失败锁定",
    replay: "重放拦截",
    rate_limited: "限流",
    bad_request: "请求错误",
    error: "服务错误",
    expired: "已过期",
  };
  return m[r] || r;
}

const RESULT_OPTIONS = ["ok", "auth_failed", "denied", "locked", "replay", "rate_limited", "bad_request", "error", "expired"];

function Tag({ children, tone }: { children: React.ReactNode; tone?: "read" | "write" | "dim" }) {
  const color = tone === "write" ? "#f59e0b" : tone === "dim" ? "#6b7280" : "#34d399";
  return (
    <span style={{
      display: "inline-block", padding: "1px 7px", margin: "1px 3px 1px 0", borderRadius: 9,
      fontSize: 11, border: "1px solid " + color + "55", color: color, whiteSpace: "nowrap",
    }}>{children}</span>
  );
}

const SIGN_DOC = [
  "1. 请求头：X-API-Key（Key ID）、X-Timestamp（unix 秒）、X-Nonce（随机串）、X-Signature；可附 X-Remark 记录调用备注。",
  "2. 签名密钥 = hex(SHA256(secret))；签名 = hex( HMAC-SHA256( 签名密钥, 方法 + \"\\n\" + 路径 + \"\\n\" + 时间戳 + \"\\n\" + nonce + \"\\n\" + hex(SHA256(请求体)) ) )。",
  "3. 时间戳允许偏差 ±300 秒（可配置）；nonce 一次性，10 分钟内重复即拒绝（防重放）。",
  "4. 每 Key 每 10 秒默认最多 20 次调用（可配置），超出返回限流。",
  "5. 窗口内鉴权失败超阈值（默认 15 分钟内 5 次）来源将被临时锁定（默认 5 分钟，可配置）。",
  "6. 明文 secret 只在创建响应里出现一次，服务端只存 SHA-256 哈希，请立即保存。",
];

const LEVEL3: [string, string][] = [
  ["身份鉴别", "API Key 哈希存储 + 明文一次性发放；鉴权失败超阈值临时锁定（次数/窗口/时长可配置）"],
  ["访问控制", "每 Key 功能白名单（勾选制，默认拒绝）；未勾选的功能一律不允许；Key 可随时撤销"],
  ["数据隔离", "每 Key 车辆白名单：只能查询/调度授权车辆，跨车访问 403；查询类接口同样过滤"],
  ["职责分离", "Key 的创建/修改/撤销与审计查询仅限管理员角色，操作人随审计记录在案"],
  ["安全审计", "每次调用（含失败原因、调用备注、涉及车辆、来源、trace）进审计环并落库留存"],
  ["抗重放", "时间戳窗口 + nonce 一次性校验"],
  ["数据完整性", "HMAC-SHA256 签名覆盖方法/路径/时间戳/nonce/请求体哈希"],
  ["通信保密", "生产经 nginx + TLS 入口；车云链路 mTLS"],
  ["资源限制", "每 Key 滑动窗口限流，防滥用"],
];

const ENDPOINTS: [string, string, string, string][] = [
  ["GET", "/open/v1/meta", "任意有效 Key", "自检：服务器时间（对时）+ 本 Key 权限画像"],
  ["GET", "/open/v1/vehicles", "vehicle.read", "车辆列表（仅白名单车辆：在线/模式/速度/电量/定位）"],
  ["GET", "/open/v1/vehicles/{id}", "vehicle.read", "单车详情（基本资料 + 能力声明）"],
  ["GET", "/open/v1/vehicles/{id}/telemetry", "telemetry.read", "单车遥测全量（实时值 + 速度/油门/刹车历史 + GPS）"],
  ["GET", "/open/v1/vehicles/{id}/alarms", "alarm.read", "单车告警与事件（?level= 过滤，默认 50 条）"],
  ["GET", "/open/v1/nav/status?vehicle_id=", "task.read", "某车最近一条循迹任务状态"],
  ["GET", "/open/v1/nav/routes", "task.read", "任务列表（仅白名单车辆；?vehicle_id= 收窄）"],
  ["POST", "/open/v1/nav/command", "task.write", "下发循迹：某车从 A 点到 B 点（可带途经点 + 调用备注）"],
  ["POST", "/open/v1/nav/cancel", "task.write", "取消该车当前任务（或指定任务）"],
  ["POST", "/open/v1/control/emergency-stop", "control.write", "紧急停车（高危，须单独授予）"],
];

export default function ApiPortal() {
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [scopes, setScopes] = useState<OpenScope[]>([]);
  const [vehicles, setVehicles] = useState<VehicleSnap[]>([]);
  const [createOpen, setCreateOpen] = useState(false);
  const [docsOpen, setDocsOpen] = useState(false);
  const [editKey, setEditKey] = useState<APIKey | null>(null);
  const [secret, setSecret] = useState<{ id: string; secret: string } | null>(null);
  const [msg, setMsg] = useState("");
  // 表单（创建与编辑共用语义）
  const [name, setName] = useState("");
  const [remark, setRemark] = useState("");
  const [selScopes, setSelScopes] = useState<string[]>([]);
  const [allVehicles, setAllVehicles] = useState(false);
  const [selVehicles, setSelVehicles] = useState<string[]>([]);
  const [ipsText, setIpsText] = useState("");
  const [expiresDays, setExpiresDays] = useState("90");

  const reload = useCallback(async () => {
    try {
      const [ks, sc, fl] = await Promise.all([
        fetchKeys(),
        fetchScopes().catch(() => [] as OpenScope[]),
        fetchFleetSnap().catch(() => null),
      ]);
      setKeys(ks); setScopes(sc);
      if (fl) setVehicles(fl.vehicles || []);
    } catch {
      /* 后端未就绪 */
    }
  }, []);

  useEffect(() => {
    void reload();
    const t = window.setInterval(() => void reload(), 5000);
    return () => window.clearInterval(t);
  }, [reload]);

  const scopeName = useMemo(() => {
    const m: Record<string, string> = {};
    for (const s of scopes) m[s.id] = s.name;
    return m;
  }, [scopes]);

  const resetForm = () => {
    setName(""); setRemark(""); setSelScopes([]); setAllVehicles(false); setSelVehicles([]); setIpsText(""); setExpiresDays("90");
  };

  const parseExpiryDays = (): number | null => {
    const days = Number(expiresDays);
    if (!Number.isInteger(days) || days < 1 || days > 365) {
      setMsg("API Key 有效期必须是 1 到 365 天的整数");
      return null;
    }
    return days;
  };

  // 解析来源 IP 输入（逗号/空格/换行分隔；支持单个 IP 与 CIDR）。
  const parseIps = (): string[] | null => {
    const items = ipsText.split(/[,，;；\s]+/).map((s) => s.trim()).filter(Boolean);
    for (const s of items) {
      const ip = s.includes("/") ? s.split("/")[0] : s;
      const v4 = ip.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/);
      const okV4 = v4 != null && v4.slice(1).every((p) => Number(p) <= 255);
      const okV6 = ip.includes(":");
      const okMask = !s.includes("/") || /^\d+$/.test(s.split("/")[1]);
      if ((!okV4 && !okV6) || !okMask) {
        setMsg("来源 IP 格式不合法：" + s + "（支持单个 IP 或 CIDR 网段）");
        return null;
      }
    }
    return items;
  };

  const toggleScope = (id: string) =>
    setSelScopes((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]));
  const toggleVehicle = (id: string) =>
    setSelVehicles((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]));

  const doCreate = async () => {
    if (selScopes.length === 0) { setMsg("请至少勾选一个功能权限（未勾选的功能将不允许调用）"); return; }
    if (!allVehicles && selVehicles.length === 0) { setMsg("请选择允许访问的车辆，或勾选「全部车辆」"); return; }
    const ips = parseIps();
    if (ips === null) return;
    const days = parseExpiryDays();
    if (days === null) return;
    try {
      const r = await createKey({
        name: name || "open-api", remark: remark,
        scopes: selScopes, vehicles: allVehicles ? ["*"] : selVehicles,
        ips: ips, expires_in_s: days * 86400,
      });
      setSecret({ id: r.key.id, secret: r.secret });
      resetForm(); setMsg("");
    } catch (e) {
      setMsg("创建失败：" + String(e));
    }
    void reload();
  };

  const openEdit = (k: APIKey) => {
    setName(k.name); setRemark(k.remark || "");
    setSelScopes(k.scopes || []);
    setAllVehicles((k.vehicles || []).includes("*"));
    setSelVehicles((k.vehicles || []).filter((v) => v !== "*"));
    setIpsText((k.ips || []).join(", "));
    setExpiresDays(String(Math.max(1, Math.min(365, Math.ceil((k.expires_ns - Date.now() * 1e6) / (86400 * 1e9))))));
    setEditKey(k); setMsg("");
  };

  const doUpdate = async () => {
    if (!editKey) return;
    if (selScopes.length === 0) { setMsg("请至少勾选一个功能权限（未勾选的功能将不允许调用）"); return; }
    if (!allVehicles && selVehicles.length === 0) { setMsg("请选择允许访问的车辆，或勾选「全部车辆」"); return; }
    const ips = parseIps();
    if (ips === null) return;
    const days = parseExpiryDays();
    if (days === null) return;
    try {
      await updateKey(editKey.id, {
        name: name, remark: remark,
        scopes: selScopes, vehicles: allVehicles ? ["*"] : selVehicles,
        ips: ips, expires_in_s: days * 86400,
      });
      setEditKey(null); setMsg("");
    } catch (e) {
      setMsg("保存失败：" + String(e));
    }
    void reload();
  };

  const doRevoke = async (id: string) => {
    if (!window.confirm("撤销该 Key？撤销后对方平台立即无法调用。")) return;
    try {
      await revokeKey(id);
    } catch (e) {
      setMsg("撤销失败：" + String(e));
    }
    void reload();
  };

  const scopePicker = (
    <div>
      <div className="muted" style={{ marginBottom: 6 }}>功能权限（勾选制：<b>未勾选的功能将不允许调用</b>）</div>
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 6 }}>
        {scopes.map((s) => (
          <label key={s.id} style={{
            display: "flex", gap: 8, alignItems: "flex-start", padding: "7px 9px", borderRadius: 8,
            border: "1px solid " + (selScopes.includes(s.id) ? "#34d39966" : "#2b2f3a"),
            background: selScopes.includes(s.id) ? "#34d3990f" : "transparent", cursor: "pointer",
          }}>
            <input type="checkbox" checked={selScopes.includes(s.id)} onChange={() => toggleScope(s.id)} style={{ marginTop: 2 }} />
            <span>
              <span style={{ fontSize: 13 }}>{s.name}</span>
              <Tag tone={s.kind === "write" ? "write" : "read"}>{s.kind === "write" ? "写" : "读"}</Tag>
              <div className="muted" style={{ fontSize: 11 }}>{s.desc} <span className="mono">{s.id}</span></div>
            </span>
          </label>
        ))}
        {scopes.length === 0 && <div className="muted">目录加载中…（后端 /api/openscopes）</div>}
      </div>
    </div>
  );

  const vehiclePicker = (
    <div style={{ marginTop: 12 }}>
      <div className="muted" style={{ marginBottom: 6 }}>
        车辆范围（车端与数据隔离：<b>只能查询/调度勾选的车辆</b>）
      </div>
      <label style={{ display: "flex", gap: 6, alignItems: "center", marginBottom: 6, cursor: "pointer" }}>
        <input type="checkbox" checked={allVehicles} onChange={(e) => setAllVehicles(e.target.checked)} />
        <span style={{ fontSize: 13 }}>全部车辆（<span className="mono">*</span>）</span>
      </label>
      {!allVehicles && (
        <div style={{ display: "flex", flexWrap: "wrap", gap: 6 }}>
          {vehicles.map((v) => (
            <label key={v.vehicle_id} style={{
              display: "flex", gap: 6, alignItems: "center", padding: "5px 9px", borderRadius: 8, cursor: "pointer",
              border: "1px solid " + (selVehicles.includes(v.vehicle_id) ? "#34d39966" : "#2b2f3a"),
              background: selVehicles.includes(v.vehicle_id) ? "#34d3990f" : "transparent",
            }}>
              <input type="checkbox" checked={selVehicles.includes(v.vehicle_id)} onChange={() => toggleVehicle(v.vehicle_id)} />
              <span className="mono" style={{ fontSize: 12 }}>{v.vehicle_id}</span>
              <span style={{ fontSize: 11 }} className={v.online ? "" : "muted"}>{v.online ? "在线" : "离线"}</span>
            </label>
          ))}
          {vehicles.length === 0 && <div className="muted">暂无车辆（车辆上线后自动出现）</div>}
        </div>
      )}
    </div>
  );

  const expiryPicker = (
    <div style={{ marginTop: 12 }}>
      <div className="muted" style={{ marginBottom: 6 }}>
        有效期（必填，永久 Key 禁止）：服务端最多 365 天，到期后立即拒绝调用并记录“已过期”审计
      </div>
      <div className="btn-row">
        <input className="input" type="number" min={1} max={365} step={1} style={{ width: 120 }}
          value={expiresDays} onChange={(e) => setExpiresDays(e.target.value)} />
        <span className="muted">天（以服务端收到请求的时间为准）</span>
      </div>
    </div>
  );

  return (
    <div style={{ padding: 16, overflow: "auto", height: "100%" }}>
      <div className="row" style={{ justifyContent: "space-between", alignItems: "flex-start", marginBottom: 12 }}>
        <div>
          <div style={{ fontSize: 16, fontWeight: 700 }}>API 平台（对外开放接口）</div>
          <div className="muted">
            第三方平台凭 API Key + HMAC 签名调用；按 Key 细分功能权限与车辆范围（未勾选一律不允许），
            每次调用（含备注与失败原因）全程审计，按等保三级要求落实。
          </div>
        </div>
        <div className="btn-row">
          <button className="btn primary" onClick={() => { setSecret(null); resetForm(); setCreateOpen(true); }}>创建 API Key</button>
          <button className="btn" onClick={() => setDocsOpen(true)}>接口文档</button>
        </div>
      </div>
      {msg && <div className="notice">{msg}</div>}

      <div className="panel" style={{ padding: 14, marginBottom: 12 }}>
        <div className="panel-title">
          密钥与权限管理 <span className="hint">（功能白名单 + 车辆白名单：未勾选的功能/车辆一律不允许调用）</span>
        </div>
        <table className="table">
          <thead>
            <tr><th>名称</th><th>Key ID</th><th>功能权限</th><th>车辆范围</th><th>来源 IP</th><th>创建</th><th>到期</th><th>最近使用</th><th>状态</th><th></th></tr>
          </thead>
          <tbody>
            {keys.length === 0 && (
              <tr><td colSpan={10} className="muted" style={{ textAlign: "center", padding: 16 }}>还没有 Key，点右上角「创建 API Key」</td></tr>
            )}
            {keys.map((k) => (
              <tr key={k.id}>
                <td>
                  <div>{k.name}</div>
                  {k.remark && <div className="muted" style={{ fontSize: 11 }}>{k.remark}</div>}
                </td>
                <td>
                  <div className="mono">{k.id}</div>
                  <div className="mono muted" style={{ fontSize: 11 }}>{k.prefix}…</div>
                </td>
                <td style={{ maxWidth: 220 }}>
                  {(k.scopes || []).length === 0 && <Tag tone="dim">未开通任何功能</Tag>}
                  {(k.scopes || []).map((s) => <Tag key={s} tone={s.endsWith(".write") ? "write" : "read"}>{scopeName[s] || s}</Tag>)}
                </td>
                <td style={{ maxWidth: 180 }}>
                  {(k.vehicles || []).includes("*")
                    ? <Tag>全部车辆</Tag>
                    : (k.vehicles || []).length === 0
                      ? <Tag tone="dim">无车辆权限</Tag>
                      : (k.vehicles || []).map((v) => <Tag key={v}>{v}</Tag>)}
                </td>
                <td style={{ maxWidth: 150 }}>
                  {(k.ips || []).length === 0
                    ? <Tag tone="dim">不限来源</Tag>
                    : (k.ips || []).map((ip) => <Tag key={ip}>{ip}</Tag>)}
                </td>
                <td className="mono">{fmtTime(k.created_ns)}</td>
                <td className="mono">{fmtTime(k.expires_ns)}</td>
                <td className="mono">{fmtTime(k.last_used_ns)}</td>
                <td>{k.revoked_ns ? "已撤销" : k.expires_ns && k.expires_ns <= Date.now() * 1e6 ? "已过期" : "有效"}</td>
                <td>
                  {!k.revoked_ns && (
                    <div className="btn-row">
                      <button className="btn small" onClick={() => openEdit(k)}>编辑</button>
                      <button className="btn small danger" onClick={() => void doRevoke(k.id)}>撤销</button>
                    </div>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {createOpen && (
        <Modal title="创建 API Key" onClose={() => setCreateOpen(false)} width="min(680px, 94vw)">
          {!secret ? (
            <>
              <div className="muted" style={{ marginBottom: 10 }}>
                给调用方平台起名并写备注（便于审计识别来源），然后勾选功能权限与车辆范围——
                <b>未勾选的功能与车辆一律不允许访问</b>。
              </div>
              <div className="btn-row" style={{ marginBottom: 10 }}>
                <input className="input" style={{ width: 220 }} placeholder="Key 名称（如：调度平台A）" value={name} onChange={(e) => setName(e.target.value)} />
                <input className="input" style={{ flex: 1, minWidth: 200 }} placeholder="备注（如：三期巡检调度对接，联系人张三）" value={remark} onChange={(e) => setRemark(e.target.value)} />
              </div>
              {scopePicker}
      {vehiclePicker}
      {expiryPicker}
      <div style={{ marginTop: 12 }}>
        <div className="muted" style={{ marginBottom: 6 }}>
          来源 IP（可选）：只允许下列 IP 调用本 Key，<b>多个用逗号分隔，支持 CIDR 网段</b>；留空 = 不限制来源
        </div>
        <input className="input" style={{ width: "100%", boxSizing: "border-box" }}
          placeholder="如：203.0.113.7, 198.51.100.0/24" value={ipsText} onChange={(e) => setIpsText(e.target.value)} />
      </div>
      <div className="btn-row" style={{ justifyContent: "flex-end", marginTop: 14 }}>
        <button className="btn primary" onClick={() => void doCreate()}>创建</button>
      </div>
            </>
          ) : (
            <>
              <div className="notice" style={{ borderColor: "#b45309" }}>
                <div style={{ marginBottom: 6 }}>新 Key 的明文只出现这一次，请立即复制保存（之后服务端只保留哈希）：</div>
                <div className="mono" style={{ wordBreak: "break-all" }}>
                  Key ID: {secret.id}<br />Secret: {secret.secret}
                </div>
              </div>
              <div className="btn-row" style={{ justifyContent: "flex-end" }}>
                <button className="btn primary" onClick={() => setCreateOpen(false)}>我已保存</button>
              </div>
            </>
          )}
        </Modal>
      )}

      {editKey && (
        <Modal title={"编辑 API Key · " + editKey.id} onClose={() => setEditKey(null)} width="min(680px, 94vw)">
          <div className="muted" style={{ marginBottom: 10 }}>
            调整备注、功能权限与车辆范围后立即生效；缩小授权即收权（默认拒绝）。
          </div>
          <div className="btn-row" style={{ marginBottom: 10 }}>
            <input className="input" style={{ width: 220 }} placeholder="Key 名称" value={name} onChange={(e) => setName(e.target.value)} />
            <input className="input" style={{ flex: 1, minWidth: 200 }} placeholder="备注" value={remark} onChange={(e) => setRemark(e.target.value)} />
          </div>
          {scopePicker}
          {vehiclePicker}
          {expiryPicker}
          <div style={{ marginTop: 12 }}>
            <div className="muted" style={{ marginBottom: 6 }}>
              来源 IP（可选）：只允许下列 IP 调用；多个用逗号分隔，支持 CIDR 网段；留空 = 不限制来源
            </div>
            <input className="input" style={{ width: "100%", boxSizing: "border-box" }}
              placeholder="如：203.0.113.7, 198.51.100.0/24" value={ipsText} onChange={(e) => setIpsText(e.target.value)} />
          </div>
          <div className="btn-row" style={{ justifyContent: "flex-end", marginTop: 14 }}>
            <button className="btn" onClick={() => setEditKey(null)}>取消</button>
            <button className="btn primary" onClick={() => void doUpdate()}>保存</button>
          </div>
        </Modal>
      )}

      {docsOpen && (
        <Modal title="接口文档 · 权限、签名与等保对照" onClose={() => setDocsOpen(false)} width="min(920px, 94vw)">
          <div className="panel-title">接口一览（每个接口绑定所需功能权限，未勾选即 403）</div>
          <table className="table">
            <thead><tr><th>方法</th><th>路径</th><th>所需权限</th><th>用途</th></tr></thead>
            <tbody>
              {ENDPOINTS.map((e, i) => (
                <tr key={i}>
                  <td>{e[0]}</td>
                  <td className="mono">{e[1]}</td>
                  <td className="mono">{e[2]}</td>
                  <td className="muted">{e[3]}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="panel-title mt">权限与隔离（默认拒绝）</div>
          <ul style={{ margin: "6px 0", paddingLeft: 18, color: "var(--text-dim)", fontSize: 12, lineHeight: 1.9 }}>
            <li><b>功能白名单</b>：每个 Key 只允许调用创建/编辑时勾选的功能；未勾选的功能一律返回 403（权限拒绝）。</li>
            <li><b>车辆白名单（车端/数据隔离）</b>：Key 只能查询与调度勾选的车辆；跨车查询、下发、取消一律 403。<span className="mono">*</span> 表示全部车辆。</li>
            <li><b>调用备注</b>：任意请求可带 <span className="mono">X-Remark</span> 请求头（POST 也可放 <span className="mono">body.remark</span>，GET 可用 <span className="mono">?remark=</span>），原文随审计日志留存，便于追溯每次调用的用途。</li>
            <li><b>来源 IP 绑定</b>：可为 Key 固定一个或多个允许的来源 IP（支持 CIDR 网段）；名单外的来源一律拒绝，名单留空则不限制来源。</li>
            <li><b>高危能力</b>：紧急停车（<span className="mono">control.write</span>）需单独授予；每次触发记录 critical 事件与调用方。</li>
          </ul>
          <div className="panel-title mt">签名与防重放</div>
          <ul style={{ margin: "6px 0", paddingLeft: 18, color: "var(--text-dim)", fontSize: 12, lineHeight: 1.9 }}>
            {SIGN_DOC.map((s, i) => <li key={i}>{s}</li>)}
          </ul>
          <div className="panel-title mt">调用示例（bash：下发循迹任务并写调用备注）</div>
          <pre className="code-box">{[
            "ts=$(date +%s); nonce=$(openssl rand -hex 8)",
            "body='{\"vehicle_id\":\"<已登记车辆ID>\",\"from\":{\"name\":\"A\",\"x\":0,\"y\":0},\"to\":{\"name\":\"B\",\"x\":20,\"y\":5},\"remark\":\"夜班巡检第3轮\"}'",
            "body_sha=$(printf '%s' \"$body\" | openssl dgst -sha256 -hex | awk '{print $2}')",
            "sign_key=$(printf '%s' \"$SECRET\" | openssl dgst -sha256 -hex | awk '{print $2}')   # 签名密钥=SHA256(secret)",
            "sig=$(printf 'POST\\n/open/v1/nav/command\\n%s\\n%s\\n%s' \"$ts\" \"$nonce\" \"$body_sha\" \\",
            "      | openssl dgst -sha256 -hmac \"$sign_key\" -hex | awk '{print $2}')",
            "curl -sS -X POST https://平台域名/open/v1/nav/command \\",
            "  -H \"X-API-Key: $KEY_ID\" -H \"X-Timestamp: $ts\" -H \"X-Nonce: $nonce\" \\",
            "  -H \"X-Signature: $sig\" -H 'Content-Type: application/json' -d \"$body\"",
          ].join("\n")}</pre>
          <div className="panel-title mt">等保三级控制点对照</div>
          <table className="table">
            <thead><tr><th>控制点</th><th>落实方式</th></tr></thead>
            <tbody>
              {LEVEL3.map((r, i) => (
                <tr key={i}><td style={{ whiteSpace: "nowrap" }}>{r[0]}</td><td className="muted">{r[1]}</td></tr>
              ))}
            </tbody>
          </table>
        </Modal>
      )}
    </div>
  );
}
