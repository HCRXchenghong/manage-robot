// 修改密码弹窗（等保：复杂度校验在前端提示一遍，服务端为权威；改密后其余会话失效）。
import { useState } from "react";
import { changePassword } from "../api";

export default function PasswordModal({ onClose }: { onClose: () => void }) {
  const [oldPwd, setOldPwd] = useState("");
  const [newPwd, setNewPwd] = useState("");
  const [confirm, setConfirm] = useState("");
  const [msg, setMsg] = useState("");
  const [ok, setOk] = useState(false);

  const doSave = async () => {
    setMsg("");
    if (newPwd !== confirm) {
      setMsg("两次输入的新密码不一致");
      return;
    }
    if (newPwd.length < 8) {
      setMsg("新密码至少 8 位");
      return;
    }
    const kinds = [/[A-Z]/, /[a-z]/, /[0-9]/, /[^A-Za-z0-9]/].filter((re) => re.test(newPwd)).length;
    if (kinds < 3) {
      setMsg("需含大写/小写/数字/符号中至少 3 类");
      return;
    }
    try {
      await changePassword(oldPwd, newPwd);
      setOk(true);
      setMsg("修改成功，其他设备的会话已失效");
    } catch (e) {
      setMsg("修改失败：" + String(e).replace(/^Error: /, ""));
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" style={{ width: 380 }} onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <span style={{ fontWeight: 700 }}>修改密码</span>
          <button className="btn small ghost" onClick={onClose}>✕</button>
        </div>
        <div style={{ padding: 16, display: "flex", flexDirection: "column", gap: 10 }}>
          <input className="input" type="password" placeholder="原密码" value={oldPwd} onChange={(e) => setOldPwd(e.target.value)} />
          <input className="input" type="password" placeholder="新密码（≥8位，含大写/小写/数字/符号中至少3类）" value={newPwd} onChange={(e) => setNewPwd(e.target.value)} />
          <input className="input" type="password" placeholder="再次输入新密码" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          {msg && <div className="muted" style={{ fontSize: 12, color: ok ? "var(--green)" : "#fecaca" }}>{msg}</div>}
          <div className="btn-row">
            <button className="btn primary" disabled={ok} onClick={() => void doSave()}>确认修改</button>
            <button className="btn" onClick={onClose}>关闭</button>
          </div>
        </div>
      </div>
    </div>
  );
}
