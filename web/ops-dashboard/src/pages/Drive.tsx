import type { FleetState } from "../api";
import type { VehicleSnap } from "../types";
import TakeoverPanel from "../components/TakeoverPanel";
import VideoPanel from "../components/VideoPanel";

interface Props {
  fleet: FleetState;
  vehicle: VehicleSnap | null;
  onSelect: (id: string, goDetail?: boolean) => void;
}

export default function Drive({ fleet, vehicle, onSelect }: Props) {
  return (
    <div style={{ display: "grid", gridTemplateColumns: "1fr 1.3fr", gap: 12, height: "100%" }}>
      <div style={{ display: "flex", flexDirection: "column", gap: 12, minHeight: 0 }}>
        <div className="panel">
          <TakeoverPanel snap={fleet.snap} big />
        </div>
        <div className="panel">
          <div className="panel-title"><span>接管流程说明</span></div>
          <div className="muted" style={{ lineHeight: 1.8, fontSize: 12 }}>
            1. 申请接管：control-authority 签发租约 + fencing token，经 Gateway 推车端；<br />
            2. 租约有效期内，遥控指令才会被车端仲裁器接受；<br />
            3. 续租：重发申请顶掉旧租约（fencing 单调 +1，旧令牌自动失效）；<br />
            4. 交还：下发空租约，车端立即收回权限；<br />
            5. 紧急停车：签发 3 秒短租约 + 零速指令，车端看门狗收敛至最小风险并停稳。
          </div>
        </div>
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 12, minHeight: 0 }}>
        <div className="panel">
          <div className="panel-title">
            <span>当前车辆</span>
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
          {vehicle ? (
            <div className="kv">
              <span className="k">状态</span><span>{vehicle.online ? "在线" : "离线"}</span>
              <span className="k">模式</span><span>{vehicle.mode || "-"}</span>
              <span className="k">车速</span><span className="mono">{vehicle.speed_mps.toFixed(2)} m/s</span>
              <span className="k">电量</span><span className="mono">{Math.round(vehicle.soc * 100)}%</span>
            </div>
          ) : (
            <div className="muted">暂无车辆</div>
          )}
        </div>
        <div className="panel" style={{ flex: 1 }}>
          <VideoPanel vehicle={vehicle} height={260} />
        </div>
      </div>
    </div>
  );
}
