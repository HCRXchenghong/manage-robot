import type { FleetState } from "../api";
import type { VehicleSnap } from "../types";
import TerminalPanel from "../components/TerminalPanel";

interface Props {
  fleet: FleetState;
  vehicle: VehicleSnap | null;
}

export default function Terminal({ fleet, vehicle }: Props) {
  return (
    <div style={{ height: "100%", display: "flex", flexDirection: "column" }}>
      <TerminalPanel vehicle={vehicle} />
      <div className="muted mt" style={{ fontSize: 11 }}>
        阶段 1：hub 代理的模拟回显（/ws/terminal）；阶段 2 接入 workspace-agent 真实 PTY（令牌门禁不变）。
      </div>
    </div>
  );
}
