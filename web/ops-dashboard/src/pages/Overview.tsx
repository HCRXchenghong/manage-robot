// 总览大屏：顶部工具条（含 点云地图/GPS轨迹 切换）；下方整块地图，
// 卡片以悬浮层形式放在地图内部（左：车辆列表/告警与事件；右：详情/终端）。
// 浮层整列可收起成细竖条，翼内卡片也可单独收起；收起后地图完整露出；
// 地图右侧控制按钮列随右浮层开合联动移位。
import { useEffect, useRef, useState } from "react";
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

// 车辆搜索小框：替代旧「点击选中联动地图」，输入 ID 过滤，
// 回车或点结果即选中并联动地图；placeholder 显示当前选中车。
function VehicleSearch({
  vehicles,
  selectedId,
  onSelect,
}: {
  vehicles: VehicleSnap[];
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  const [q, setQ] = useState("");
  const [open, setOpen] = useState(false);
  const boxRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const onDoc = (e: MouseEvent) => {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, []);
  const kw = q.trim().toLowerCase();
  const matches = kw ? vehicles.filter((v) => v.vehicle_id.toLowerCase().includes(kw)) : vehicles;
  const pick = (id: string) => {
    onSelect(id);
    setQ("");
    setOpen(false);
  };
  return (
    <div className="veh-search" ref={boxRef}>
      <input
        value={q}
        placeholder={selectedId ? "搜索车辆 · 当前 " + selectedId : "搜索车辆"}
        onChange={(e) => {
          setQ(e.target.value);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && matches.length > 0) pick(matches[0].vehicle_id);
          if (e.key === "Escape") setOpen(false);
        }}
      />
      {open && matches.length > 0 && (
        <div className="veh-search-pop">
          {matches.slice(0, 8).map((v) => (
            <div
              key={v.vehicle_id}
              className={"veh-search-row" + (v.vehicle_id === selectedId ? " sel" : "")}
              onMouseDown={() => pick(v.vehicle_id)}
            >
              {v.vehicle_id}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export default function Overview({ fleet, selected, onSelect }: Props) {
  // 自动刷新在「设置 → 车辆与底盘」配置（默认开），保存后实时生效
  const [autoRefresh, setAutoRefresh] = useState<boolean>(() => {
    try {
      const v = window.localStorage.getItem("ov-auto-refresh");
      return v === null ? true : v === "1";
    } catch {
      return true;
    }
  });
  useEffect(() => {
    const onCfg = () => {
      try {
        const v = window.localStorage.getItem("ov-auto-refresh");
        setAutoRefresh(v === null ? true : v === "1");
      } catch {
        /* 忽略 */
      }
    };
    window.addEventListener("ra-cfg-changed", onCfg);
    return () => window.removeEventListener("ra-cfg-changed", onCfg);
  }, []);
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
    <SideCol id="left" side="left" label="车辆列表 / 告警事件">
      <CollapsePanel
        id="ov-vehicles"
        title="车辆列表"
        fixed
        right={
          <VehicleSearch
            vehicles={view.vehicles}
            selectedId={selected ? selected.vehicle_id : null}
            onSelect={(id) => onSelect(id, false)}
          />
        }
      >
        <VehicleTable snap={view} onSelect={onSelect} selectedId={selected ? selected.vehicle_id : null} compact />
      </CollapsePanel>
      <EventFeed events={view.events} compact fixed />
    </SideCol>
  );
  const rightOverlay = (
    <SideCol id="right" side="right" label="详情 / 终端" open={rightOpen} onOpenChange={setRightOpen}>
      <VehicleDetail fleet={fleet} vehicle={selected} onSelect={onSelect} compact />
      <TerminalPanel vehicle={selected} fixed />
    </SideCol>
  );

  return (
    <div className="overview">
      <div className="overview-toolbar">
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
