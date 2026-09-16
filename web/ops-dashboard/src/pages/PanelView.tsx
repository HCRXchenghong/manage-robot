// PanelView：独立面板页 —— 「在新标签页打开」的落地页（#/panel/{kind}?vehicle=xxx）。
// 鉴权（等保三级）：页内二次校验会话（fetchMe），未登录只显示登录引导；
// 车辆可见性由 /api/fleet 的服务端分组过滤兜底，越权访问显示无权。
import { useEffect, useState } from "react";
import { fetchMe, useFleet } from "../api";
import type { Me } from "../types";
import { useGamepads } from "../peripherals";
import VideoPanel from "../components/VideoPanel";
import TwinGps from "../components/TwinGps";
import TwinRadar from "../components/TwinRadar";
import TerminalPanel from "../components/TerminalPanel";
import "./drive.css";

type Kind = "video" | "gps" | "pointcloud" | "terminal";

const TITLE: Record<Kind, string> = {
  video: "视频画面",
  gps: "GPS 定位",
  pointcloud: "激光点云",
  terminal: "远程终端",
};

function PanelBody({ kind, vehicleId, enabled }: { kind: Kind; vehicleId: string; enabled: boolean }) {
  const fleet = useFleet(enabled);
  const v = fleet.snap.vehicles.find((x) => x.vehicle_id === vehicleId) || null;
  if (!v) {
    return (
      <div className="pn-auth">
        <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 8 }}>车辆不存在或无权访问</div>
        <div className="muted">{vehicleId || "（URL 未带车辆 ID）"}</div>
      </div>
    );
  }
  switch (kind) {
    case "video":
      return <VideoPanel vehicle={v} fill />;
    case "gps":
      return <TwinGps vehicle={v} fill />;
    case "pointcloud":
      return <TwinRadar vehicle={v} fill />;
    case "terminal":
      return <TerminalPanel vehicle={v} fixed />;
  }
}

export default function PanelView({ kind, vehicleId }: { kind: string; vehicleId: string }) {
  const [me, setMe] = useState<Me | null>(null);
  const [checking, setChecking] = useState(true);
  useEffect(() => {
    void fetchMe().then((m) => {
      setMe(m);
      setChecking(false);
    });
  }, []);

  const pads = useGamepads();
  const k = (["video", "gps", "pointcloud", "terminal"] as Kind[]).includes(kind as Kind)
    ? (kind as Kind)
    : null;

  const close = () => {
    window.close(); // 脚本打开的标签页可直接关闭
    window.setTimeout(() => {
      window.location.hash = "#/drive";
    }, 150);
  };

  if (checking) {
    return (
      <div className="pn-root">
        <div className="pn-auth"><div className="muted">正在校验会话…</div></div>
      </div>
    );
  }
  if (!me) {
    return (
      <div className="pn-root">
        <div className="pn-auth">
          <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 8 }}>会话无效或已过期</div>
          <div className="muted">
            该面板页需要登录会话才能查看（等保三级身份鉴别）。请先在主平台登录，再重新打开本标签页。
          </div>
          <a className="btn small primary" style={{ marginTop: 12, display: "inline-block" }} href="/">
            去登录
          </a>
        </div>
      </div>
    );
  }
  if (!k) {
    return (
      <div className="pn-root">
        <div className="pn-auth">
          <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 8 }}>未知面板类型</div>
          <div className="muted mono">{kind || "（空）"}</div>
          <button className="btn small" style={{ marginTop: 12 }} onClick={close}>关闭</button>
        </div>
      </div>
    );
  }

  return (
    <div className="pn-root">
      <header className="pn-head">
        <button className="btn small" onClick={close}>✕ 关闭</button>
        <h1>{TITLE[k]} · {vehicleId}</h1>
        <span className="badge info">{me.display_name || me.username}</span>
        <span className="spacer" style={{ flex: 1 }} />
        <span className="dv-pill on">键盘 就绪</span>
        {pads.map((p) => (
          <span className="dv-pill on" key={p.index} title={p.id}>
            {(p.isWheel ? "USB 方向盘" : "USB 遥控器/手柄") + " #" + p.index + " 在线"}
          </span>
        ))}
      </header>
      <div className="pn-body">
          <PanelBody kind={k} vehicleId={vehicleId} enabled={me !== null} />
      </div>
    </div>
  );
}
