import type { FleetState } from "../api";
import type { VehicleSnap } from "../types";
import VideoPanel from "../components/VideoPanel";

interface Props {
  fleet: FleetState;
  vehicle: VehicleSnap | null;
}

export default function Video({ fleet }: Props) {
  const vehicles = fleet.snap.vehicles;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
      <div className="panel-title" style={{ marginBottom: 0 }}>
        <span>视频监控（{vehicles.length} 路）</span>
        <span className="hint">阶段 1 模拟画面 · 阶段 2 接 media-control WebRTC 双路合并流</span>
      </div>
      <div className="video-grid">
        {vehicles.map((v) => (
          <div className="panel" key={v.vehicle_id}>
            <VideoPanel vehicle={v} height={220} />
          </div>
        ))}
        {vehicles.length === 0 && <div className="panel muted">暂无车辆</div>}
      </div>
    </div>
  );
}
