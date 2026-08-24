// 登录页：账号密码 + 图形验证码（人机验证），通过后进入后台。
// 等保三级配套：失败锁定、密码复杂度、会话 HttpOnly Cookie、全程审计（服务端）。
import { useCallback, useEffect, useState } from "react";
import { fetchCaptcha, login } from "../api";

export default function Login({ onLoggedIn }: { onLoggedIn: () => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [capId, setCapId] = useState("");
  const [capImg, setCapImg] = useState("");
  const [capAns, setCapAns] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const reloadCaptcha = useCallback(async () => {
    try {
      const c = await fetchCaptcha();
      setCapId(c.captcha_id);
      setCapImg(c.image);
      setCapAns("");
    } catch {
      setErr("验证码服务暂不可用，稍后重试");
    }
  }, []);

  useEffect(() => {
    void reloadCaptcha();
  }, [reloadCaptcha]);

  const doLogin = async () => {
    setErr("");
    if (!username || !password) {
      setErr("请输入账号和密码");
      return;
    }
    if (!capAns) {
      setErr("请输入人机验证码");
      return;
    }
    setBusy(true);
    try {
      await login(username, password, capId, capAns);
      onLoggedIn();
    } catch (e) {
      setErr(String(e).replace(/^Error: /, ""));
      void reloadCaptcha();
    }
    setBusy(false);
  };

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") void doLogin();
  };

  return (
    <div className="login-bg">
      <div className="panel login-card">
        <div className="login-brand">
          <div className="brand-logo big">机</div>
          <div>
            <div className="brand-name" style={{ fontSize: 17 }}>机器人集中管理调度平台</div>
            <div className="brand-sub">内部系统 · 授权使用 · 操作留痕</div>
          </div>
        </div>
        <div className="login-field">
          <label>账号</label>
          <input
            className="input" value={username} autoComplete="username"
            placeholder="请输入账号" onChange={(e) => setUsername(e.target.value)} onKeyDown={onKey}
          />
        </div>
        <div className="login-field">
          <label>密码</label>
          <input
            className="input" type="password" value={password} autoComplete="current-password"
            placeholder="请输入密码" onChange={(e) => setPassword(e.target.value)} onKeyDown={onKey}
          />
        </div>
        <div className="login-field">
          <label>人机验证</label>
          <div className="row">
            <input
              className="input" style={{ flex: 1 }} value={capAns} maxLength={4}
              placeholder="输入右图字符" onChange={(e) => setCapAns(e.target.value)} onKeyDown={onKey}
            />
            {capImg ? (
              <img src={capImg} alt="验证码" title="看不清？点击刷新" onClick={() => void reloadCaptcha()} style={{ height: 40, borderRadius: 6, cursor: "pointer", border: "1px solid var(--border)" }} />
            ) : (
              <div className="muted" style={{ fontSize: 11 }}>加载中…</div>
            )}
          </div>
        </div>
        {err && <div className="notice" style={{ borderColor: "#b91c1c", color: "#fecaca", background: "rgba(127,29,29,.18)" }}>{err}</div>}
        <button className="btn primary" style={{ width: "100%", padding: "10px 0", fontSize: 14 }} disabled={busy} onClick={() => void doLogin()}>
          {busy ? "验证中…" : "登 录"}
        </button>
        <div className="muted" style={{ fontSize: 11, marginTop: 12, lineHeight: 1.8 }}>
          安全提示：连续 5 次失败将锁定 30 分钟；会话 30 分钟无操作自动失效；
          所有登录与操作均被审计记录（等保三级）。
        </div>
        <div className="muted" style={{ fontSize: 11, marginTop: 8, borderTop: "1px dashed #22304f", paddingTop: 8 }}>
          演示账号：superadmin / Super@2026（超管）· boss_a / Boss@2026（分组管理员）· ops_a / Ops@2026（用户）
        </div>
      </div>
    </div>
  );
}
