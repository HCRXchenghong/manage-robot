import CollapsePanel from "./CollapsePanel";
import { SimCamCanvas } from "./VideoPanel";
import type { VehicleSnap } from "../types";

// 总览右列紧凑双视频监控：前向 + 俯视两路机位并排（模拟画面，阶段 2 换 WebRTC）
interface Props {
  vehicle: VehicleSnap | null;
  onExpand?: () => void;
}

export default function MiniVideos({ vehicle, onExpand }: Props) {
  const vid = vehicle ? vehicle.vehicle_id : "sim-veh-001";
  return (
    <CollapsePanel id="ov-videos" fixed title={"视频监控 · " + vid} hint="前向 / 俯视" onTitleClick={onExpand}>
      <div className="mini-videos" onClick={onExpand}>
        <div className="mini-video">
          <SimCamCanvas camera="front" height={140} />
          <span className="video-meta">前向机位</span>
        </div>
        <div className="mini-video">
          <SimCamCanvas camera="top" height={140} />
          <span className="video-meta">俯视机位</span>
        </div>
      </div>
    </CollapsePanel>
  );
}
