import { useRef, useState } from "react";
import type { FleetState } from "../api";
import type { FleetSnap, VehicleSnap } from "../types";
import StatCards from "../components/StatCards";
import CollapsePanel from "../components/CollapsePanel";
import LidarView from "../components/LidarView";
import VehicleTable from "../components/VehicleTable";
import EventFeed from "../components/EventFeed";
import TakeoverPanel from "../components/TakeoverPanel";
import TerminalPanel from "../components/TerminalPanel";
import VehicleDetail from "./VehicleDetail";

interface Props {
  fleet: FleetState;
  selected: VehicleSnap | null;
  onSelect: (id: string, goDetail?: boolean) => void;
}

export default function Overview({ fleet, selected, onSelect }: Props) {
  const [autoRefresh, setAutoRefresh] = useState(true);
  const frozenRef = useRef<FleetSnap | null>(null);

  // 自动刷新关闭时冻结画面（仍接收数据，只是不渲染新值）
  if (autoRefresh) frozenRef.current = null;
  else if (!frozenRef.current) frozenRef.current = fleet.snap;
  const view = frozenRef.current || fleet.snap;

  const fullscreen = () => {
    void document.documentElement.requestFullscreen();
  };

  return (
    <div className="overview">
      <div className="overview-toolbar">
        <StatCards snap={view} />
      </div>
      <div className="overview-toolbar">
        <button className={"btn small" + (autoRefresh ? " primary" : "")} onClick={() => setAutoRefresh((a) => !a)}>
          自动刷新 {autoRefresh ? "开" : "关"}
        </button>
        <button className="btn small" onClick={fleet.refresh}>手动刷新</button>
        <button className="btn small" onClick={fullscreen}>全屏投屏</button>
        <span className="spacer" />
        <span className="muted" style={{ fontSize: 11 }}>
          数据源：{fleet.source === "live" ? "fleet-hub 实时" : "离线演示（mock）"} · 1Hz 推送
        </span>
      </div>
      <div className="panel overview-map">
        <LidarView snap={view} selectedId={selected ? selected.vehicle_id : null} onSelect={(id) => onSelect(id, false)} />
      </div>
      <div className="overview-cols">
        <div className="col">
          <CollapsePanel id="ov-vehicles" title="车辆列表" hint="点击选中联动地图">
            <VehicleTable snap={view} onSelect={onSelect} selectedId={selected ? selected.vehicle_id : null} compact />
          </CollapsePanel>
          <EventFeed events={view.events} compact />
        </div>
        <div className="col">
          <VehicleDetail fleet={fleet} vehicle={selected} onSelect={onSelect} compact />
        </div>
        <div className="col">
          <TakeoverPanel snap={view} />
          <TerminalPanel vehicle={selected} />
        </div>
      </div>
    </div>
  );
}
