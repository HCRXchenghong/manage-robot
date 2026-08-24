import type { FleetState } from "../api";
import type { VehicleSnap } from "../types";
import TakeoverPanel from "../components/TakeoverPanel";
import VideoWall from "../components/VideoWall";
import TianMap from "../components/TianMap";
import ChassisPanel from "../components/ChassisPanel";
import LidarView from "../components/LidarView";
import "../cockpit.css";

interface Props {
  fleet: FleetState;
  vehicle: VehicleSnap | null;
  onSelect: (id: string, goDetail?: boolean) => void;
}

export default function Drive({ fleet, vehicle, onSelect }: Props) {
  return (
    <div className="cockpit">
      <div className="panel ck-video">
        <VideoWall vehicle={vehicle} />
      </div>
      <div className="panel ck-right">
        <div className="panel-title">
          <span>控制车辆</span>
          <select
            className="input"
            value={vehicle ? vehicle.vehicle_id : ""}
            onChange={(e) => onSelect(e.target.value, false)}
          >
            {fleet.snap.vehicles.map((v) => (
              <option key={v.vehicle_id} value={v.vehicle_id}>{v.vehicle_id}</option>
            ))}
          </select>
        </div>
        <TakeoverPanel snap={fleet.snap} big />
        <div className="panel-title"><span>底盘状态</span></div>
        <ChassisPanel vehicle={vehicle} />
        <div className="muted" style={{ lineHeight: 1.7, fontSize: 11, marginTop: 8 }}>
          接管流程：申请接管（租约 + fencing）→ 租约内遥控指令才被车端仲裁器接受 →
          续租顶替旧令牌 → 交还即收回 → 紧急停车为 3 秒短租约 + 零速。
        </div>
      </div>
      <div className="panel ck-lidar">
        <LidarView
          snap={fleet.snap}
          selectedId={vehicle ? vehicle.vehicle_id : null}
          onSelect={(id) => onSelect(id, false)}
        />
      </div>
      <div className="panel ck-map">
        <TianMap vehicle={vehicle} />
      </div>
    </div>
  );
}
