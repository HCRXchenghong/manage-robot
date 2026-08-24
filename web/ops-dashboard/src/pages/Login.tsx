// 登录页（参考设计稿重做）：左侧品牌+能力介绍，右侧登录表单。
// 两段式：先账密校验 → 通过后再出图形人机验证 → 验证通过才发会话。
import { useCallback, useEffect, useState } from "react";
import { fetchCaptcha, login, verifyLogin } from "../api";

const FEATURES = [
  { t: "任务调度", d: "任务分配与资源管理" },
  { t: "状态监控", d: "查看运行状态与数据" },
  { t: "指令执行", d: "处理异常与执行指令" },
];

export default function Login({ onLoggedIn }: { onLoggedIn: () => void }) {
  const [stage, setStage] = useState<"creds" | "captcha">("creds");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [preauth, setPreauth] = useState("");
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
    if (stage === "captcha") void reloadCaptcha();
  }, [stage, reloadCaptcha]);

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
      setErr(String(e).replace(/^Error: /, "").replace(/^HTTP \d+:? /, ""));
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
      setErr(String(e).replace(/^Error: /, "").replace(/^HTTP \d+:? /, ""));
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
              <div className="login-feature-t">{f.t}</div>
              <div className="login-feature-d">{f.d}</div>
            </div>
          ))}
        </div>
      </div>
      <div className="login-right">
        <div className="login-tabs">
          <div className="login-tab active">账号密码</div>
        </div>
        {stage === "creds" ? (
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
            <button className="login-back" onClick={() => { setStage("creds"); setErr(""); }}>
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
