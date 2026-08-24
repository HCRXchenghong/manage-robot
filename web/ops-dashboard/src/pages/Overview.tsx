// 总览大屏：顶部统计+工具条；下方整块 2D/3D 地图，五张卡片以悬浮层
// 形式叠在地图内部左右两侧（左：车辆列表/告警与事件；右：详情/接管/终端）。
// 左右浮层整列可收起成细竖条，翼内卡片也可单独收起；收起后地图完整露出。
import { useRef, useState } from "react";
import type { FleetState } from "../api";
import type { FleetSnap, VehicleSnap } from "../types";
import StatCards from "../components/StatCards";
import CollapsePanel from "../components/CollapsePanel";
import SideCol from "../components/SideCol";
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
      <div className="overview-main">
        <div className="panel overview-map">
          <LidarView
            snap={view}
            selectedId={selected ? selected.vehicle_id : null}
            onSelect={(id) => onSelect(id, false)}
            overlayLeft={
              <SideCol id="left" label="车辆列表 / 告警事件">
                <CollapsePanel id="ov-vehicles" title="车辆列表" hint="点击选中联动地图">
                  <VehicleTable snap={view} onSelect={onSelect} selectedId={selected ? selected.vehicle_id : null} compact />
                </CollapsePanel>
                <EventFeed events={view.events} compact />
              </SideCol>
            }
            overlayRight={
              <SideCol id="right" label="详情 / 接管 / 终端">
                <VehicleDetail fleet={fleet} vehicle={selected} onSelect={onSelect} compact />
                <TakeoverPanel snap={view} />
                <TerminalPanel vehicle={selected} />
              </SideCol>
            }
          />
        </div>
      </div>
    </div>
  );
}
