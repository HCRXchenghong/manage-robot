import CollapsePanel from "./CollapsePanel";
import { SimCamCanvas } from "./VideoPanel";
import type { VehicleSnap } from "../types";

// 紧凑视频监控：默认前向 + 俯视两路并排；single 时只显示前向单画面（孪生页用）。
// 模拟画面，阶段 2 换 WebRTC。
interface Props {
  vehicle: VehicleSnap | null;
  onExpand?: () => void;
  single?: boolean;
}

export default function MiniVideos({ vehicle, onExpand, single }: Props) {
  const vid = vehicle ? vehicle.vehicle_id : "sim-veh-001";
  return (
    <CollapsePanel
      id="ov-videos"
      fixed
      title={"视频监控 · " + vid}
      hint={single ? "单画面 · 弹窗可换机位" : "前向 / 俯视"}
      onTitleClick={onExpand}
    >
      <div className="mini-videos" onClick={onExpand}>
        <div className="mini-video">
          <SimCamCanvas camera="front" height={140} />
          <span className="video-meta">前向机位</span>
        </div>
        {!single && (
          <div className="mini-video">
            <SimCamCanvas camera="top" height={140} />
            <span className="video-meta">俯视机位</span>
          </div>
        )}
      </div>
    </CollapsePanel>
  );
}
