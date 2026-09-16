// VideoPanel renders no generated picture. Media becomes visible only after a
// vehicle media-agent registers a real, authenticated stream.
import type { CSSProperties } from "react";
import type { VehicleSnap } from "../types";

export type CamId = "front" | "rear" | "left" | "right" | "surround" | "top";
export const CAMS: { id: CamId; label: string }[] = [
  { id: "front", label: "前向" }, { id: "rear", label: "后向" },
  { id: "left", label: "左视" }, { id: "right", label: "右视" },
  { id: "surround", label: "环视" }, { id: "top", label: "鸟瞰" },
];
export const CAM_LABEL: Record<CamId, string> = {
  front: "前向", rear: "后向", left: "左视", right: "右视", surround: "环视", top: "鸟瞰",
};

export function VideoUnavailable({
  vehicleID,
  camera = "front",
  fill = false,
  height = 220,
}: {
  vehicleID?: string;
  camera?: CamId;
  fill?: boolean;
  height?: number;
}) {
  const style: CSSProperties = fill
    ? { flex: 1, minHeight: 0, width: "100%" }
    : { height, width: "100%" };
  return (
    <div className="video-box" style={{ ...style, display: "grid", placeItems: "center", textAlign: "center", padding: 16 }}>
      <div>
        <div style={{ fontWeight: 650, marginBottom: 6 }}>真实视频源未注册</div>
        <div className="muted" style={{ fontSize: 12, lineHeight: 1.6 }}>
          {vehicleID ? vehicleID + " · " + CAM_LABEL[camera] + "机位" : CAM_LABEL[camera] + "机位"}<br />
          请由车端 media-agent 通过受控信令注册 WebRTC/SFU 流后查看。
        </div>
      </div>
    </div>
  );
}

interface Props {
  vehicle: VehicleSnap | null;
  height?: number;
  fill?: boolean;
  hud?: boolean;
}

export default function VideoPanel({ vehicle, height = 220, fill = false, hud = false }: Props) {
  const vehicleID = vehicle?.vehicle_id;
  return (
    <div className={hud ? "vp-hud" : ""} style={{ display: "flex", flexDirection: "column", minHeight: 0, flex: 1 }}>
      <div className="panel-title">
        <span>{hud ? "实时驾驶视频" : "视频监控"}{vehicleID ? " · " + vehicleID : ""}</span>
        <span className="hint">媒体控制面未注册真实流</span>
      </div>
      <VideoUnavailable vehicleID={vehicleID} height={height} fill={fill || hud} />
    </div>
  );
}
