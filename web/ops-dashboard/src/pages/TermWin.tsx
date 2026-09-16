// 独立车端终端页：从「远程终端」页一键新标签页打开（#/termwin/:id），无侧栏/顶栏；
// 车端 Agent 注册后才可建立终端会话；会话鉴权与数字孪生页一致（先登录后使用）。
import { useEffect, useState } from "react";
import { fetchMe, useFleet } from "../api";
import TerminalPanel from "../components/TerminalPanel";
import type { Me } from "../types";

export default function TermWin({ vehicleId }: { vehicleId: string }) {
  const [me, setMe] = useState<Me | null>(null);
  const [checking, setChecking] = useState(true);
  const fleet = useFleet(me !== null);
  useEffect(() => {
    void fetchMe().then((m) => {
      setMe(m);
      setChecking(false);
    });
  }, []);

  const v = fleet.snap.vehicles.find((x) => x.vehicle_id === vehicleId) || null;

  const back = () => {
    if (window.history.length > 1) window.history.back();
    else window.location.hash = "#/terminal";
  };

  if (checking) {
    return (
      <div className="twin-root">
        <div className="muted">正在校验会话…</div>
      </div>
    );
  }
  if (!me) {
    return (
      <div className="twin-root">
        <div className="twin-auth">
          <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 8 }}>会话无效或已过期</div>
          <div className="muted">车端终端页启用等保三级鉴权，请先在主平台登录后再打开本页。</div>
          <a className="btn small primary" style={{ marginTop: 12, display: "inline-block" }} href="/">
            返回登录
          </a>
        </div>
      </div>
    );
  }
  if (!vehicleId) {
    return (
      <div className="twin-root">
        <div className="twin-auth">
          <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 8 }}>URL 缺少车辆 ID</div>
          <div className="muted">请从「远程终端」页选择车辆后点「新标签页打开车端终端」。</div>
          <button className="btn small" style={{ marginTop: 12 }} onClick={() => (window.location.hash = "#/terminal")}>
            ← 去远程终端页
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="twin-root">
      <header className="twin-head">
        <button className="btn small" onClick={back}>← 返回</button>
        <h1>车端终端 · {vehicleId}</h1>
        {v && <span className={"badge " + (v.online ? "ok" : "dim")}>{v.online ? "在线" : "离线"}</span>}
        {v && <span className="badge info">{v.mode === "autonomous" ? "自动驾驶" : v.mode || "-"}</span>}
        {v && <span className="badge dim">{v.group}</span>}
        <span className="spacer" />
        <button className="btn small" onClick={() => (window.location.hash = "#/terminal")}>
          去远程终端页
        </button>
      </header>
      <div className="termwin-body">
        <TerminalPanel vehicle={v} vehicleId={vehicleId} fixed panelId="termwin-terminal" />
      </div>
    </div>
  );
}
