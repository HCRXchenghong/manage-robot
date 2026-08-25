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
import MiniVideos from "../components/MiniVideos";
import Modal from "../components/Modal";
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
  // 交互三件套：点列表/搜索 → 地图居中到该车（focusNonce 驱动点云/GPS）；
  // 右键车辆（列表行或地图标记）→ 小菜单[定位到车/进入详情]；
  // 单击地图车辆点 → 小窗确认是否进详情页。
  const [focusNonce, setFocusNonce] = useState(0);
  const [ctxMenu, setCtxMenu] = useState<{ id: string; x: number; y: number } | null>(null);
  const [confirmDetail, setConfirmDetail] = useState<{ id: string; x: number; y: number } | null>(null);
  // 远程终端 / 告警与事件 点击标题弹窗放大
  const [bigTerminal, setBigTerminal] = useState(false);
  const [bigEvents, setBigEvents] = useState(false);
  const frozenRef = useRef<FleetSnap | null>(null);

  // 自动刷新关闭时冻结画面（仍接收数据，只是不渲染新值）
  if (autoRefresh) frozenRef.current = null;
  else if (!frozenRef.current) frozenRef.current = fleet.snap;
  const view = frozenRef.current || fleet.snap;

  // 左侧列表/搜索/右键「定位到车」：选中 + 让地图居中到该车
  const selectAndCenter = (id: string, goDetail?: boolean) => {
    setCtxMenu(null);
    setConfirmDetail(null);
    onSelect(id, goDetail);
    if (!goDetail) setFocusNonce((n) => n + 1);
  };
  // 地图车辆点单击：选中 + 弹小窗确认是否进详情
  const markerClick = (id: string, x: number, y: number) => {
    onSelect(id, false);
    setCtxMenu(null);
    setConfirmDetail({ id, x, y });
  };
  // 右键车辆：弹小菜单
  const openCtx = (id: string, x: number, y: number) => {
    setConfirmDetail(null);
    setCtxMenu({ id, x, y });
  };

  // 左右浮层两种地图视图共用
  const leftOverlay = (
    <SideCol id="left" side="left" label="车辆列表 / 终端 / 告警">
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
        <VehicleTable
          snap={view}
          onSelect={selectAndCenter}
          onContext={openCtx}
          selectedId={selected ? selected.vehicle_id : null}
          compact
        />
      </CollapsePanel>
      <div className="ov-split">
        <TerminalPanel vehicle={selected} fixed onExpand={() => setBigTerminal(true)} />
        <EventFeed events={view.events} compact fixed onExpand={() => setBigEvents(true)} />
      </div>
    </SideCol>
  );
  const rightOverlay = (
    <SideCol id="right" side="right" label="详情 / 视频" open={rightOpen} onOpenChange={setRightOpen}>
      <VehicleDetail fleet={fleet} vehicle={selected} onSelect={onSelect} compact />
      <MiniVideos vehicle={selected} />
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
              onSelect={markerClick}
              onContext={openCtx}
              focusNonce={focusNonce}
              toolsRight={rightOpen ? 318 : 52}
              overlayLeft={leftOverlay}
              overlayRight={rightOverlay}
            />
          ) : (
            <GpsMap
              snap={view}
              selectedId={selected ? selected.vehicle_id : null}
              onSelect={markerClick}
              onContext={openCtx}
              focusNonce={focusNonce}
              toolsRight={rightOpen ? 318 : 52}
              overlayLeft={leftOverlay}
              overlayRight={rightOverlay}
            />
          )}
        </div>
      </div>
      {bigTerminal && (
        <Modal
          title={"远程终端 · " + (selected ? selected.vehicle_id : "sim-veh-001")}
          onClose={() => setBigTerminal(false)}
          width="min(960px, 94vw)"
        >
          <div className="big-modal-body" style={{ height: "min(560px, 70vh)" }}>
            <TerminalPanel vehicle={selected} fixed />
          </div>
        </Modal>
      )}
      {bigEvents && (
        <Modal title="告警与事件" onClose={() => setBigEvents(false)} width="min(860px, 94vw)">
          <div className="big-modal-body" style={{ height: "min(520px, 70vh)" }}>
            <EventFeed events={view.events} fixed />
          </div>
        </Modal>
      )}
      {ctxMenu && (
        <>
          <div
            className="pop-mask"
            onClick={() => setCtxMenu(null)}
            onContextMenu={(e) => {
              e.preventDefault();
              setCtxMenu(null);
            }}
          />
          <div
            className="ctx-menu"
            style={{
              left: Math.min(ctxMenu.x, window.innerWidth - 150),
              top: Math.min(ctxMenu.y, window.innerHeight - 110),
            }}
          >
            <button onClick={() => selectAndCenter(ctxMenu.id, false)}>定位到车</button>
            <button
              onClick={() => {
                setCtxMenu(null);
                onSelect(ctxMenu.id, true);
              }}
            >
              进入详情
            </button>
          </div>
        </>
      )}
      {confirmDetail && (
        <>
          <div className="pop-mask" onClick={() => setConfirmDetail(null)} />
          <div
            className="ctx-menu"
            style={{
              left: Math.min(confirmDetail.x, window.innerWidth - 200),
              top: Math.min(confirmDetail.y, window.innerHeight - 130),
            }}
          >
            <div className="ctx-title">进入车辆详情页？</div>
            <div className="ctx-sub">{confirmDetail.id}</div>
            <div className="ctx-row">
              <button
                className="primary"
                onClick={() => {
                  setConfirmDetail(null);
                  onSelect(confirmDetail.id, true);
                }}
              >
                确定
              </button>
              <button onClick={() => setConfirmDetail(null)}>取消</button>
            </div>
          </div>
        </>
      )}
    </div>
  );
}
