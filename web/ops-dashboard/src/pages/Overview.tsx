// 总览大屏：顶部工具条（含 点云地图/GPS轨迹 切换）；下方整块地图，
// 卡片以悬浮层形式放在地图内部（左：车辆列表/告警与事件；右：详情/终端）。
// 浮层整列可收起成细竖条，翼内卡片也可单独收起；收起后地图完整露出；
// 地图右侧控制按钮列随右浮层开合联动移位。
import { useRef, useState } from "react";
import type { FleetState } from "../api";
import type { FleetSnap, VehicleSnap } from "../types";
import CollapsePanel from "../components/CollapsePanel";
import SideCol from "../components/SideCol";
import LidarView from "../components/LidarView";
import GpsMap from "../components/GpsMap";
import VehicleTable from "../components/VehicleTable";
import EventFeed from "../components/EventFeed";
import TerminalPanel from "../components/TerminalPanel";
import VehicleDetail from "./VehicleDetail";

interface Props {
  fleet: FleetState;
  selected: VehicleSnap | null;
  onSelect: (id: string, goDetail?: boolean) => void;
}

export default function Overview({ fleet, selected, onSelect }: Props) {
  const [autoRefresh, setAutoRefresh] = useState(true);
  // 右浮层开合（联动控制按钮列位置）
  const [rightOpen, setRightOpen] = useState<boolean>(() => {
    try {
      const v = window.localStorage.getItem("ov-col-right");
      return v === null ? true : v === "1";
    } catch {
      return true;
    }
  });
  // 地图视图：点云地图 / GPS 轨迹
  const [mapKind, setMapKind] = useState<"lidar" | "gps">("lidar");
  const frozenRef = useRef<FleetSnap | null>(null);

  // 自动刷新关闭时冻结画面（仍接收数据，只是不渲染新值）
  if (autoRefresh) frozenRef.current = null;
  else if (!frozenRef.current) frozenRef.current = fleet.snap;
  const view = frozenRef.current || fleet.snap;

  // 左右浮层两种地图视图共用
  const leftOverlay = (
    <SideCol id="left" label="车辆列表 / 告警事件">
      <CollapsePanel id="ov-vehicles" title="车辆列表" hint="点击选中联动地图">
        <VehicleTable snap={view} onSelect={onSelect} selectedId={selected ? selected.vehicle_id : null} compact />
      </CollapsePanel>
      <EventFeed events={view.events} compact />
    </SideCol>
  );
  const rightOverlay = (
    <SideCol id="right" label="详情 / 终端" open={rightOpen} onOpenChange={setRightOpen}>
      <VehicleDetail fleet={fleet} vehicle={selected} onSelect={onSelect} compact />
      <TerminalPanel vehicle={selected} />
    </SideCol>
  );

  return (
    <div className="overview">
      <div className="overview-toolbar">
        <button className={"btn small" + (autoRefresh ? " primary" : "")} onClick={() => setAutoRefresh((a) => !a)}>
          自动刷新 {autoRefresh ? "开" : "关"}
        </button>
        <button className="btn small" onClick={fleet.refresh}>手动刷新</button>
        <div className="tabs">
          <button className={"tab" + (mapKind === "lidar" ? " active" : "")} onClick={() => setMapKind("lidar")}>
            点云地图
          </button>
          <button className={"tab" + (mapKind === "gps" ? " active" : "")} onClick={() => setMapKind("gps")}>
            GPS 轨迹
          </button>
        </div>
        <span className="spacer" />
        <span className="muted" style={{ fontSize: 11 }}>
          数据源：{fleet.source === "live" ? "fleet-hub 实时" : "离线演示（mock）"} · 1Hz 推送
        </span>
      </div>
      <div className="overview-main">
        <div className="panel overview-map">
          {mapKind === "lidar" ? (
            <LidarView
              snap={view}
              selectedId={selected ? selected.vehicle_id : null}
              onSelect={(id) => onSelect(id, false)}
              toolsRight={rightOpen ? 318 : 52}
              overlayLeft={leftOverlay}
              overlayRight={rightOverlay}
            />
          ) : (
            <GpsMap
              snap={view}
              selectedId={selected ? selected.vehicle_id : null}
              onSelect={(id) => onSelect(id, false)}
              overlayLeft={leftOverlay}
              overlayRight={rightOverlay}
            />
          )}
        </div>
      </div>
    </div>
  );
}
