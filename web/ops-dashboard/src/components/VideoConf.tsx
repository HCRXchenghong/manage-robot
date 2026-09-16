// Media configuration is disabled until media-control exposes authenticated,
// persistent camera and recording APIs. Local-only settings would incorrectly
// imply that a vehicle has been configured.
import type { VehicleSnap } from "../types";
import VideoPanel from "./VideoPanel";

export function VideoConfPanel({ vehicle }: { vehicle: VehicleSnap | null }) {
  return (
    <div className="vc-root">
      <VideoPanel vehicle={vehicle} height={220} />
      <div className="vc-msg" style={{ marginTop: 12 }}>
        媒体配置不可用：请先部署 media-control、车端 media-agent 与受认证的 WebRTC 信令，再启用拼接、标定、录制和回放。
      </div>
    </div>
  );
}

export default function VideoConf({ vehicle, back }: { vehicle: VehicleSnap | null; back: () => void }) {
  return (
    <div className="panel" style={{ height: "100%", display: "flex", flexDirection: "column", overflow: "auto" }}>
      <div className="panel-title">
        <span>视频监控配置{vehicle ? " · " + vehicle.vehicle_id : ""}</span>
        <span className="hint">真实媒体控制面未注册</span>
        <span className="spacer" />
        <button className="btn small" onClick={back}>← 返回视频监控</button>
      </div>
      <VideoConfPanel vehicle={vehicle} />
    </div>
  );
}
