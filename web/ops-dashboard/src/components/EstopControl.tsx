// EstopControl：急停按钮（大弹窗确认）。
// 浏览器不会本地伪造“已急停/已解除”状态；车辆的实际状态只能以车端遥测为准。
import { useState } from "react";
import Modal from "./Modal";
import { emergencyStop } from "../api";

export default function EstopButton({
  vehicleId,
  small,
  className,
}: {
  vehicleId: string;
  small?: boolean;
  className?: string;
}) {
  const [modal, setModal] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  const confirmEngage = async () => {
    setBusy(true);
    setMsg("");
    try {
      const r = await emergencyStop(vehicleId);
      if (r && r.ok) {
        setModal(false);
      } else {
        setMsg("急停下发失败：" + String((r && (r.error || r.ack)) || "未知错误"));
      }
    } catch (e) {
      setMsg("急停下发失败：" + String(e));
    }
    setBusy(false);
  };

  const sz = small ? " small" : "";
  return (
    <>
      <button
        className={(className || "btn danger") + sz}
        onClick={() => setModal(true)}
      >
        急停
      </button>
      {modal && (
        <Modal title={"紧急停车确认 · " + vehicleId} onClose={() => !busy && setModal(false)} width="min(720px, 94vw)">
          <div className="estop-modal-body">
            <div className="estop-modal-ic">!</div>
            <div className="estop-modal-txt">
              即将对车辆 <b className="mono">{vehicleId}</b> 下发<b>紧急停车</b>指令：
              车辆将立即进入最小风险状态并自主减速停稳。
              <br />
              成功与否将由车端回执和实时遥测确认；本页面不会本地模拟车辆状态。
            </div>
            {msg && <div className="msg err">{msg}</div>}
            <div className="btn-row mt" style={{ justifyContent: "flex-end" }}>
              <button className="btn" disabled={busy} onClick={() => setModal(false)}>取消</button>
              <button className="btn danger" disabled={busy} onClick={() => void confirmEngage()}>
                {busy ? "下发中…" : "确认急停"}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </>
  );
}
