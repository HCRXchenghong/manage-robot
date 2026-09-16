import CollapsePanel from "./CollapsePanel";
import { VideoUnavailable } from "./VideoPanel";
import type { VehicleSnap } from "../types";

// 紧凑视频监控：只有车端媒体代理注册真实流后才能显示画面。
interface Props {
  vehicle: VehicleSnap | null;
  onExpand?: () => void;
  single?: boolean;
  height?: number;
  fill?: boolean;
}

export default function MiniVideos({ vehicle, onExpand, single, height = 140, fill = false }: Props) {
  const vid = vehicle?.vehicle_id || "未选择车辆";
  return (
    <CollapsePanel
      id="ov-videos"
      fixed
      title={"视频监控 · " + vid}
      hint={single ? "等待真实媒体流" : "等待真实媒体流"}
      onTitleClick={onExpand}
    >
      <div
        className="mini-videos"
        onDoubleClick={(e) => {
          e.stopPropagation();
          onExpand?.();
        }}
        title="双击弹窗放大"
      >
        <div className="mini-video" style={fill ? { display: "flex" } : undefined}>
          <VideoUnavailable vehicleID={vehicle?.vehicle_id} camera="front" height={height} fill={fill} />
          <span className="video-meta">前向机位</span>
        </div>
        {!single && (
          <div className="mini-video">
            <VideoUnavailable vehicleID={vehicle?.vehicle_id} camera="top" height={height} />
            <span className="video-meta">俯视机位</span>
          </div>
        )}
      </div>
    </CollapsePanel>
  );
}
