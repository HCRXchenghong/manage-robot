// 远程终端页：顶部设置远程车辆（分组下拉 + 状态徽标），下方终端面板；
// 支持新标签页打开独立车端终端状态页；真正 PTY 仅在车端 Agent 注册后开放。
import { useEffect, useMemo, useState } from "react";
import type { FleetState } from "../api";
import type { VehicleSnap } from "../types";
import TerminalPanel from "../components/TerminalPanel";

interface Props {
  fleet: FleetState;
  vehicle: VehicleSnap | null;
}

export default function Terminal({ fleet, vehicle }: Props) {
  const vehicles = fleet.snap.vehicles;
  const [vid, setVid] = useState<string>(() => (vehicle ? vehicle.vehicle_id : ""));

  // 车辆列表就绪后回填默认选中：App 传入车辆 > 第一辆车
  useEffect(() => {
    if (!vid && vehicles.length > 0) {
      setVid(vehicle?.vehicle_id || vehicles[0].vehicle_id);
    }
  }, [vid, vehicle, vehicles]);

  const cur = vehicles.find((v) => v.vehicle_id === vid) || null;
  const groups = useMemo(
    () => Array.from(new Set(vehicles.map((v) => v.group))),
    [vehicles],
  );

  const openTermWin = () => {
    if (cur) window.open("#/termwin/" + encodeURIComponent(cur.vehicle_id), "_blank");
  };

  return (
    <div className="terminal-page">
      <div className="panel">
        <div className="panel-title">
          <span>
            远程车辆设置
            <span className="hint"> 选择要接入终端的车端设备</span>
          </span>
          <span className="btn-row">
            <button className="btn small" onClick={openTermWin} disabled={!cur} title="在新浏览器标签页打开该车的独立终端状态页">
              ⧉ 新标签页打开车端终端
            </button>
          </span>
        </div>
        <div className="term-setup-row">
          <label className="muted" htmlFor="term-vid">远程车辆</label>
          <select id="term-vid" className="btn small" value={vid} onChange={(e) => setVid(e.target.value)}>
            {vehicles.length === 0 && <option value="">（暂无车辆）</option>}
            {groups.map((g) => (
              <optgroup key={g} label={"分组 · " + g}>
                {vehicles
                  .filter((v) => v.group === g)
                  .map((v) => (
                    <option key={v.vehicle_id} value={v.vehicle_id}>
                      {v.vehicle_id + (v.online ? "" : "（离线）")}
                    </option>
                  ))}
              </optgroup>
            ))}
          </select>
          {cur ? (
            <>
              <span className={"badge " + (cur.online ? "ok" : "dim")}>{cur.online ? "在线" : "离线"}</span>
              <span className="badge info">{cur.mode === "autonomous" ? "自动驾驶" : cur.mode || "-"}</span>
              <span className="badge dim">{cur.group}</span>
              <span className="badge dim">{cur.chassis || "底盘未登记"}</span>
            </>
          ) : (
            <span className="muted">等待车辆数据…</span>
          )}
        </div>
      </div>
      <div className="term-main">
        <TerminalPanel vehicle={cur} fixed />
      </div>
      <div className="muted mt" style={{ fontSize: 11 }}>
        真终端仅接受由车端 workspace-agent 通过 Gateway 注册的、带短期令牌的反向会话；未注册时平台不会提供虚构回显。
      </div>
    </div>
  );
}
