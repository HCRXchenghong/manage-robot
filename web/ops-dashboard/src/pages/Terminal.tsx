import type { FleetState } from "../api";
import type { VehicleSnap } from "../types";
import TerminalPanel from "../components/TerminalPanel";

interface Props {
  fleet: FleetState;
  vehicle: VehicleSnap | null;
}

export default function Terminal({ fleet, vehicle }: Props) {
  return (
    <div className="panel" style={{ height: "100%", display: "flex", flexDirection: "column" }}>
      <div className="panel-title">
        <span>远程终端</span>
        <select
          className="input"
          value={vehicle ? vehicle.vehicle_id : ""}
          onChange={() => undefined}
          disabled
        >
          {fleet.snap.vehicles.map((v) => (
            <option key={v.vehicle_id} value={v.vehicle_id}>{v.vehicle_id}</option>
          ))}
        </select>
      </div>
      <TerminalPanel vehicle={vehicle} />
      <div className="muted mt" style={{ fontSize: 11 }}>
        阶段 1：hub 代理的模拟回显（/ws/terminal）；阶段 2 接入 workspace-agent 真实 PTY（令牌门禁不变）。
      </div>
    </div>
  );
}
