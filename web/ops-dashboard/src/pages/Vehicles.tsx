import type { FleetState } from "../api";
import VehicleTable from "../components/VehicleTable";

interface Props {
  fleet: FleetState;
  onSelect: (id: string, goDetail?: boolean) => void;
}

export default function Vehicles({ fleet, onSelect }: Props) {
  return (
    <div className="panel" style={{ height: "100%", display: "flex", flexDirection: "column" }}>
      <div className="panel-title">
        <span>车辆列表</span>
        <span className="hint">点击行 → 详情页 + 地图联动</span>
      </div>
      <VehicleTable snap={fleet.snap} onSelect={onSelect} />
    </div>
  );
}
