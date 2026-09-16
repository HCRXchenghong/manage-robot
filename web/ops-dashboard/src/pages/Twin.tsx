// 数字孪生车辆详情页：独立页面（无侧栏/顶栏），新标签页打开，等保三级会话鉴权。
// 布局对齐桌面「数字孪生」参考图：左 数据面板 / 中 GLB 模型 / 右 点云+GPS+视频+终端，全部接真实接口。
import { useEffect, useState } from "react";
import { fetchMe, useFleet } from "../api";
import { BarGauge, DialGauge, SignalTable } from "../components/DashGauges";
import MiniVideos from "../components/MiniVideos";
import Modal, { openIfPlain } from "../components/Modal";
import TerminalPanel from "../components/TerminalPanel";
import TwinGps from "../components/TwinGps";
import TwinModel from "../components/TwinModel";
import TwinRadar from "../components/TwinRadar";
import VideoPanel from "../components/VideoPanel";
import EstopButton from "../components/EstopControl";
import { VehicleDashboard, TakeoverQuickModal } from "./VehicleDetail";
import type { Me } from "../types";

export default function Twin({ vehicleId }: { vehicleId: string }) {
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

  const [bigDash, setBigDash] = useState(false);
  const [bigRadar, setBigRadar] = useState(false);
  const [bigGps, setBigGps] = useState(false);
  const [bigVideo, setBigVideo] = useState(false);
  const [bigTerm, setBigTerm] = useState(false);
  const [tkOpen, setTkOpen] = useState(false);
  // 双击任意非交互区域弹窗放大（与总览大屏的 ov-click 体验一致）
  // 守卫统一在 Modal.tsx 的 openIfPlain：弹窗/浮层内或已有弹窗时不会误触发放大

  const back = () => {
    if (window.history.length > 1) window.history.back();
    else window.location.hash = "#/vehicles";
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
          <div className="muted">数字孪生页启用等保三级鉴权，请先在主平台登录后再打开本页。</div>
          <a className="btn small primary" style={{ marginTop: 12, display: "inline-block" }} href="/">
            返回登录
          </a>
        </div>
      </div>
    );
  }
  if (!v) {
    return (
      <div className="twin-root">
        <div className="twin-auth">
          <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 8 }}>车辆不存在或无权访问</div>
          <div className="muted">{vehicleId || "（URL 无车辆 ID）"}</div>
          <button className="btn small" style={{ marginTop: 12 }} onClick={back}>
            ← 返回
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="twin-root">
      <header className="twin-head">
        <button className="btn small" onClick={back}>
          ← 返回
        </button>
        <h1>数字孪生 · {v.vehicle_id}</h1>
        <span className={"badge " + (v.online ? "ok" : "dim")}>{v.online ? "在线" : "离线"}</span>
        <span className="badge info">{v.mode === "autonomous" ? "自动驾驶" : v.mode || "-"}</span>
        <span className="badge dim">{v.chassis || "底盘未登记"}</span>
        <span className="spacer" />
        <span className="muted mono">
          车速 {v.speed_mps.toFixed(2)} m/s · 电量 {Math.round(v.soc * 100)}%
        </span>
        <button className="btn primary twin-act" onClick={() => setTkOpen(true)}>接管</button>
        <EstopButton vehicleId={v.vehicle_id} className="btn danger twin-act" />
      </header>
      <div className="twin-body">
        <aside className="twin-col twin-left">
          <div className="ov-click" onDoubleClick={openIfPlain(() => setBigDash(true))}>
          <div className="twin-card">
            <div className="twin-card-t">机内环境</div>
            <BarGauge label="机内温度" value={v.cabin_temp_c ?? 26} min={0} max={50} unit="°C" color="#f59e0b" />
            <BarGauge label="机内湿度" value={v.cabin_humidity_pct ?? 45} min={0} max={100} unit="%" color="#38bdf8" digits={0} />
          </div>
          <div className="twin-card">
            <div className="twin-card-t">行驶仪表</div>
            <div className="twin-dials">
            <DialGauge value={v.speed_mps * 3.6} max={60} unit="km/h" label="车速" color="#38bdf8" sub={v.speed_mps.toFixed(2) + " m/s"} />
            <DialGauge value={v.soc * 100} max={100} unit="%" label="电量" color="#22c55e" sub={v.voltage.toFixed(1) + " V"} />
            </div>
          </div>
          <div className="twin-card twin-sig">
            <div className="twin-card-t">实时信号表</div>
            <SignalTable v={v} />
          </div>
          </div>
          <div className="twin-term-left" onDoubleClick={openIfPlain(() => setBigTerm(true))}>
            <TerminalPanel vehicle={v} fixed autoConnect panelId="twin-left-term" onExpand={() => setBigTerm(true)} />
          </div>
        </aside>
        <main className="twin-center">
          <TwinModel vehicleId={v.vehicle_id} powered={v.online} />
        </main>
        <aside className="twin-col twin-right">
          <div className="ov-click twin-radar" onDoubleClick={openIfPlain(() => setBigRadar(true))}>
            <div className="twin-card">
              <TwinRadar vehicle={v} fill />
            </div>
          </div>
          <div className="ov-click twin-gps" onDoubleClick={openIfPlain(() => setBigGps(true))}>
            <div className="twin-card">
              <TwinGps vehicle={v} fill />
            </div>
          </div>
          <div className="ov-click twin-video" onDoubleClick={openIfPlain(() => setBigVideo(true))}>
            <MiniVideos vehicle={v} single fill onExpand={() => setBigVideo(true)} />
          </div>
        </aside>
      </div>
      {bigDash && (
        <Modal title={"车辆详情 · " + v.vehicle_id} onClose={() => setBigDash(false)} width="min(980px, 94vw)">
          <VehicleDashboard v={v} />
        </Modal>
      )}
      {bigRadar && (
        <Modal title={"激光点云 · " + v.vehicle_id} onClose={() => setBigRadar(false)} width="min(760px, 92vw)">
          <div className="big-modal-body" style={{ height: "min(540px, 70vh)" }}>
            <TwinRadar vehicle={v} fill />
          </div>
        </Modal>
      )}
      {bigGps && (
        <Modal title={"GPS定位 · " + v.vehicle_id} onClose={() => setBigGps(false)} width="min(760px, 92vw)">
          <div className="big-modal-body" style={{ height: "min(480px, 70vh)" }}>
            <TwinGps vehicle={v} fill />
          </div>
        </Modal>
      )}
      {bigVideo && (
        <Modal title={"视频监控 · " + v.vehicle_id} onClose={() => setBigVideo(false)} width="min(860px, 94vw)">
          <div className="big-modal-body" style={{ height: "min(540px, 70vh)" }}>
            <VideoPanel vehicle={v} fill />
          </div>
        </Modal>
      )}
      {bigTerm && (
        <Modal title={"远程终端 · " + v.vehicle_id} onClose={() => setBigTerm(false)} width="min(960px, 94vw)">
          <div className="big-modal-body" style={{ height: "min(560px, 70vh)" }}>
            <TerminalPanel vehicle={v} fixed />
          </div>
        </Modal>
      )}
      {tkOpen && <TakeoverQuickModal vehicleId={v.vehicle_id} onClose={() => setTkOpen(false)} />}
    </div>
  );
}
