// API 平台（精简版）：页面只留密钥表与调用审计；
// 建 Key（含一次性明文展示）与接口文档都收进弹窗。
import { useCallback, useEffect, useState } from "react";
import Modal from "../components/Modal";
import { createKey, fetchAudit, fetchKeys, revokeKey } from "../api";
import type { APIKey, AuditEntry } from "../types";

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
    replay: "重放拦截",
    rate_limited: "限流",
    bad_request: "请求错误",
    error: "服务错误",
  };
  return m[r] || r;
}

const SIGN_DOC = [
  "1. 请求头：X-API-Key（Key ID）、X-Timestamp（unix 秒）、X-Nonce（随机串）、X-Signature。",
  "2. 签名密钥 = hex(SHA256(secret))；签名 = hex( HMAC-SHA256( 签名密钥, 方法 + \"\\n\" + 路径 + \"\\n\" + 时间戳 + \"\\n\" + nonce + \"\\n\" + hex(SHA256(请求体)) ) )。",
  "3. 时间戳允许偏差 ±300 秒（可配置）；nonce 一次性，10 分钟内重复即拒绝（防重放）。",
  "4. 每 Key 每 10 秒默认最多 20 次调用（可配置），超出返回限流。",
  "5. 明文 secret 只在创建响应里出现一次，服务端只存 SHA-256 哈希，请立即保存。",
];

const LEVEL3 = [
  ["身份鉴别", "API Key 哈希存储 + 明文一次性发放；服务端不落明文"],
  ["访问控制", "Key 可随时撤销；接口面收敛为导航指令与只读查询"],
  ["安全审计", "每次调用（含失败原因）进审计环并落库留存"],
  ["抗重放", "时间戳窗口 + nonce 一次性校验"],
  ["数据完整性", "HMAC-SHA256 签名覆盖方法/路径/时间戳/nonce/请求体哈希"],
  ["通信保密", "生产经 nginx + TLS 入口；车云链路 mTLS"],
  ["资源限制", "每 Key 滑动窗口限流，防滥用"],
];

export default function ApiPortal() {
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [audit, setAudit] = useState<AuditEntry[]>([]);
  const [createOpen, setCreateOpen] = useState(false);
  const [docsOpen, setDocsOpen] = useState(false);
  const [newName, setNewName] = useState("");
  const [secret, setSecret] = useState<{ id: string; secret: string } | null>(null);
  const [msg, setMsg] = useState("");

  const reload = useCallback(async () => {
    try {
      setKeys(await fetchKeys());
      setAudit(await fetchAudit(100));
    } catch {
      /* 后端未就绪 */
    }
  }, []);

  useEffect(() => {
    void reload();
    const t = window.setInterval(() => void reload(), 5000);
    return () => window.clearInterval(t);
  }, [reload]);

  const doCreate = async () => {
    try {
      const r = await createKey(newName || "open-api");
      setSecret({ id: r.key.id, secret: r.secret });
      setNewName("");
    } catch (e) {
      setMsg("创建失败：" + String(e));
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

  return (
    <div style={{ padding: 16, overflow: "auto", height: "100%" }}>
      <div className="row" style={{ justifyContent: "space-between", alignItems: "flex-start", marginBottom: 12 }}>
        <div>
          <div style={{ fontSize: 16, fontWeight: 700 }}>API 平台（对外开放接口）</div>
          <div className="muted">
            别的平台凭 API Key + HMAC 签名，调用「某车从 A 点到 B 点」与取消；全链路按等保三级要求落实鉴权、防重放、限流与审计。
          </div>
        </div>
        <div className="btn-row">
          <button className="btn primary" onClick={() => { setSecret(null); setNewName(""); setCreateOpen(true); }}>创建 API Key</button>
          <button className="btn" onClick={() => setDocsOpen(true)}>接口文档</button>
        </div>
      </div>
      {msg && <div className="notice">{msg}</div>}

      <div className="panel" style={{ padding: 14, marginBottom: 12 }}>
        <div className="panel-title">密钥管理</div>
        <table className="table">
          <thead>
            <tr><th>名称</th><th>Key ID</th><th>前缀</th><th>创建</th><th>最近使用</th><th>状态</th><th></th></tr>
          </thead>
          <tbody>
            {keys.length === 0 && (
              <tr><td colSpan={7} className="muted" style={{ textAlign: "center", padding: 16 }}>还没有 Key，点右上角「创建 API Key」</td></tr>
            )}
            {keys.map((k) => (
              <tr key={k.id}>
                <td>{k.name}</td>
                <td className="mono">{k.id}</td>
                <td className="mono">{k.prefix}…</td>
                <td className="mono">{fmtTime(k.created_ns)}</td>
                <td className="mono">{fmtTime(k.last_used_ns)}</td>
                <td>{k.revoked_ns ? "已撤销" : "有效"}</td>
                <td>
                  {!k.revoked_ns && (
                    <button className="btn small danger" onClick={() => void doRevoke(k.id)}>撤销</button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="panel" style={{ padding: 14 }}>
        <div className="panel-title">调用审计 <span className="hint">（成功与失败都记录，含失败原因与来源 IP）</span></div>
        <table className="table">
          <thead>
            <tr><th>时间</th><th>Key</th><th>请求</th><th>结果</th><th>来源</th><th>说明</th></tr>
          </thead>
          <tbody>
            {audit.length === 0 && (
              <tr><td colSpan={6} className="muted" style={{ textAlign: "center", padding: 16 }}>暂无审计记录</td></tr>
            )}
            {audit.map((a, i) => (
              <tr key={i}>
                <td className="mono">{fmtTime(a.ts_ns)}</td>
                <td>{a.key_name || "-"}</td>
                <td className="mono">{a.method} {a.path}</td>
                <td>{resultLabel(a.result)}</td>
                <td className="mono">{a.ip || "-"}</td>
                <td className="muted">{a.detail || a.trace_id || "-"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {createOpen && (
        <Modal title="创建 API Key" onClose={() => setCreateOpen(false)} width="min(560px, 92vw)">
          {!secret ? (
            <>
              <div className="muted" style={{ marginBottom: 10 }}>
                给调用方平台起个名字，便于审计时识别来源。
              </div>
              <div className="btn-row">
                <input className="input" style={{ width: 240 }} placeholder="Key 名称（如：调度平台A）" value={newName} onChange={(e) => setNewName(e.target.value)} />
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

      {docsOpen && (
        <Modal title="接口文档 · 签名与等保对照" onClose={() => setDocsOpen(false)} width="min(880px, 94vw)">
          <div className="panel-title">接口一览</div>
          <table className="table">
            <thead><tr><th>方法</th><th>路径</th><th>用途</th></tr></thead>
            <tbody>
              <tr><td>POST</td><td className="mono">/open/v1/nav/command</td><td>下发循迹：某车从 A 点到 B 点（可带途经点）</td></tr>
              <tr><td>POST</td><td className="mono">/open/v1/nav/cancel</td><td>取消该车当前任务（或指定任务）</td></tr>
              <tr><td>GET</td><td className="mono">/open/v1/nav/status?vehicle_id=</td><td>查询该车最近任务状态</td></tr>
              <tr><td>GET</td><td className="mono">/open/v1/vehicles</td><td>查询车队在线状态快照</td></tr>
            </tbody>
          </table>
          <div className="panel-title mt">签名与防重放</div>
          <ul style={{ margin: "6px 0", paddingLeft: 18, color: "var(--text-dim)", fontSize: 12, lineHeight: 1.9 }}>
            {SIGN_DOC.map((s, i) => <li key={i}>{s}</li>)}
          </ul>
          <div className="panel-title mt">调用示例（bash）</div>
          <pre className="code-box">{[
            "ts=$(date +%s); nonce=$(openssl rand -hex 8)",
            "body='{\"vehicle_id\":\"sim-veh-001\",\"from\":{\"name\":\"A\",\"x\":0,\"y\":0},\"to\":{\"name\":\"B\",\"x\":20,\"y\":5}}'",
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
