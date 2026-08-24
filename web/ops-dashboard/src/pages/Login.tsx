// 登录页（按设计稿重做）：左侧品牌+三能力卡片，右侧 tab 两种登录方式。
// 账号密码两段式：先账密校验 → 通过后再出图形人机验证 → 验证通过才发会话。
// 手机号登录：手机号格式正确才允许点「获取验证码」；短信码本身即人机验证，通过直接进入。
import { useCallback, useEffect, useState } from "react";
import { fetchCaptcha, login, loginPhone, sendSmsCode, verifyLogin } from "../api";

const PHONE_RE = /^1[3-9]\d{9}$/;

const FEATURES = [
  {
    t: "任务调度",
    d: "任务分配与资源管理",
    ico: (
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <rect x="3" y="4" width="18" height="17" rx="2" />
        <line x1="16" y1="2" x2="16" y2="6" />
        <line x1="8" y1="2" x2="8" y2="6" />
        <line x1="3" y1="10" x2="21" y2="10" />
        <path d="M9 15.5l2 2 4-4" />
      </svg>
    ),
  },
  {
    t: "状态监控",
    d: "查看运行状态与数据",
    ico: (
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M22 12h-4l-3 9L9 3l-3 9H2" />
      </svg>
    ),
  },
  {
    t: "指令执行",
    d: "处理异常与执行指令",
    ico: (
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <polyline points="4 17 10 11 4 5" />
        <line x1="12" y1="19" x2="20" y2="19" />
      </svg>
    ),
  },
];

function fmtErr(e: unknown): string {
  return String(e).replace(/^Error: /, "");
}

export default function Login({ onLoggedIn }: { onLoggedIn: () => void }) {
  const [tab, setTab] = useState<"phone" | "creds">("phone");
  const [stage, setStage] = useState<"form" | "captcha">("form");
  // 账号密码
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [preauth, setPreauth] = useState("");
  const [capId, setCapId] = useState("");
  const [capImg, setCapImg] = useState("");
  const [capAns, setCapAns] = useState("");
  // 手机号
  const [phone, setPhone] = useState("");
  const [smsCode, setSmsCode] = useState("");
  const [smsNote, setSmsNote] = useState("");
  const [cooldown, setCooldown] = useState(0);
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
    if (stage === "captcha") void reloadCaptcha();
  }, [stage, reloadCaptcha]);

  useEffect(() => {
    if (cooldown <= 0) return;
    const t = setTimeout(() => setCooldown((c) => c - 1), 1000);
    return () => clearTimeout(t);
  }, [cooldown]);

  const phoneOK = PHONE_RE.test(phone);

  const doSendSms = async () => {
    setErr("");
    setSmsNote("");
    try {
      const r = await sendSmsCode(phone);
      setSmsNote((r.note ? r.note + "：" : "") + (r.demo_code || ""));
      setCooldown(60);
    } catch (e) {
      setErr(fmtErr(e));
    }
  };

  const doPhoneLogin = async () => {
    setErr("");
    if (!phoneOK) {
      setErr("请输入正确的 11 位手机号");
      return;
    }
    if (!smsCode) {
      setErr("请输入短信验证码");
      return;
    }
    setBusy(true);
    try {
      await loginPhone(phone, smsCode);
      onLoggedIn();
    } catch (e) {
      setErr(fmtErr(e));
    }
    setBusy(false);
  };

  const doLogin = async () => {
    setErr("");
    if (!username || !password) {
      setErr("请输入账号和密码");
      return;
    }
    setBusy(true);
    try {
      const r = await login(username, password);
      setPreauth(r.preauth);
      setStage("captcha");
    } catch (e) {
      setErr(fmtErr(e));
    }
    setBusy(false);
  };

  const doVerify = async () => {
    setErr("");
    if (!capAns) {
      setErr("请输入验证码");
      return;
    }
    setBusy(true);
    try {
      await verifyLogin(preauth, capId, capAns);
      onLoggedIn();
    } catch (e) {
      setErr(fmtErr(e));
      void reloadCaptcha();
    }
    setBusy(false);
  };

  const onKey = (e: React.KeyboardEvent, fn: () => void) => {
    if (e.key === "Enter") fn();
  };

  return (
    <div className="login-bg">
      <div className="login-left">
        <h1 className="login-title">机器人集中管理调度平台</h1>
        <div className="login-title-bar" />
        <div className="login-features">
          {FEATURES.map((f) => (
            <div className="login-feature" key={f.t}>
              <div className="login-feature-ico">{f.ico}</div>
              <div className="login-feature-t">{f.t}</div>
              <div className="login-feature-d">{f.d}</div>
            </div>
          ))}
        </div>
      </div>
      <div className="login-right">
        <div className="login-tabs">
          <div
            className={"login-tab" + (tab === "phone" ? " active" : "")}
            onClick={() => { setTab("phone"); setErr(""); }}
          >
            手机号登录
          </div>
          <div
            className={"login-tab" + (tab === "creds" ? " active" : "")}
            onClick={() => { setTab("creds"); setErr(""); }}
          >
            账号密码
          </div>
        </div>
        {tab === "phone" ? (
          <>
            <div className="login-field">
              <label>手机号</label>
              <input
                className="login-input" value={phone} maxLength={11} autoComplete="tel"
                placeholder="请输入手机号"
                onChange={(e) => setPhone(e.target.value.replace(/\D/g, ""))}
                onKeyDown={(e) => onKey(e, () => void doPhoneLogin())}
              />
            </div>
            <div className="login-field">
              <label>验证码</label>
              <div className="login-sms-row">
                <input
                  className="login-input" style={{ flex: 1 }} value={smsCode} maxLength={6}
                  placeholder="请输入验证码"
                  onChange={(e) => setSmsCode(e.target.value.replace(/\D/g, ""))}
                  onKeyDown={(e) => onKey(e, () => void doPhoneLogin())}
                />
                <button
                  className="login-sms-btn" disabled={!phoneOK || cooldown > 0 || busy}
                  onClick={() => void doSendSms()}
                >
                  {cooldown > 0 ? cooldown + "s 后重发" : "获取验证码"}
                </button>
              </div>
              {smsNote && <div className="login-demo-note">{smsNote}</div>}
            </div>
            {err && <div className="login-err">{err}</div>}
            <button className="login-btn" disabled={busy} onClick={() => void doPhoneLogin()}>
              {busy ? "验证中…" : "登录"}
            </button>
          </>
        ) : stage === "form" ? (
          <>
            <div className="login-field">
              <label>账号</label>
              <input
                className="login-input" value={username} autoComplete="username"
                placeholder="请输入账号" onChange={(e) => setUsername(e.target.value)}
                onKeyDown={(e) => onKey(e, () => void doLogin())}
              />
            </div>
            <div className="login-field">
              <label>密码</label>
              <input
                className="login-input" type="password" value={password} autoComplete="current-password"
                placeholder="请输入密码" onChange={(e) => setPassword(e.target.value)}
                onKeyDown={(e) => onKey(e, () => void doLogin())}
              />
            </div>
            {err && <div className="login-err">{err}</div>}
            <button className="login-btn" disabled={busy} onClick={() => void doLogin()}>
              {busy ? "校验中…" : "登录"}
            </button>
          </>
        ) : (
          <>
            <div className="login-field">
              <label>人机验证</label>
              <div className="login-cap-row">
                <input
                  className="login-input" style={{ flex: 1 }} value={capAns} maxLength={4}
                  placeholder="请输入验证码" onChange={(e) => setCapAns(e.target.value)}
                  onKeyDown={(e) => onKey(e, () => void doVerify())}
                />
                {capImg ? (
                  <img
                    src={capImg} alt="验证码" title="看不清？点击刷新"
                    onClick={() => void reloadCaptcha()} className="login-cap-img"
                  />
                ) : (
                  <div className="login-cap-img" style={{ display: "flex", alignItems: "center", justifyContent: "center", color: "#5b6b82", fontSize: 12 }}>加载中</div>
                )}
              </div>
            </div>
            {err && <div className="login-err">{err}</div>}
            <button className="login-btn" disabled={busy} onClick={() => void doVerify()}>
              {busy ? "验证中…" : "验证并进入"}
            </button>
            <button className="login-back" onClick={() => { setStage("form"); setErr(""); }}>
              返回重新输入账号密码
            </button>
          </>
        )}
        <div className="login-foot">
          内部系统 · 授权使用 · 操作留痕。连续 5 次失败锁定 30 分钟，会话 30 分钟无操作自动失效。
        </div>
      </div>
    </div>
  );
}
