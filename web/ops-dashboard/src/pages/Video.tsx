// 视频监控页：车辆列表（可搜索）+ 右上角「监控中控台」（新标签页打开多路视频墙）。
// 点击车辆 → 进入该车的视频监控配置页（#/videoconf）。
import { useState } from "react";
import type { FleetState } from "../api";
import { navTo } from "../App";
import type { VehicleSnap } from "../types";

interface Props {
  fleet: FleetState;
  vehicle: VehicleSnap | null;
}

export default function Video({ fleet }: Props) {
  const [q, setQ] = useState("");
  const kw = q.trim().toLowerCase();
  const vehicles = fleet.snap.vehicles.filter((v) => !kw || v.vehicle_id.toLowerCase().includes(kw));

  return (
    <div className="panel" style={{ height: "100%", display: "flex", flexDirection: "column" }}>
      <div className="panel-title">
        <span>视频监控（{fleet.snap.vehicles.length} 车）</span>
        <span className="hint">点击车辆进入视频配置与标定</span>
        <span className="spacer" />
        <input
          className="input"
          style={{ width: 200, marginRight: 8 }}
          placeholder="搜索车辆 ID…"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <button className="btn small primary" onClick={() => window.open("#/videowall", "_blank")}>
          监控中控台
        </button>
      </div>

      <div className="vd-list">
        {vehicles.map((v) => (
          <button
            key={v.vehicle_id}
            className="vd-row"
            onClick={() => navTo("videoconf", { id: v.vehicle_id })}
          >
            <i className={"dot" + (v.online ? " on" : "")} />
            <span className="vd-row-id mono">{v.vehicle_id}</span>
            <span className={"badge " + (v.online ? "ok" : "dim")}>{v.online ? "在线" : "离线"}</span>
            <span className="badge info">{v.mode === "autonomous" ? "自动驾驶" : v.mode || "-"}</span>
            <span className="muted mono" style={{ fontSize: 11.5 }}>
              车速 {v.speed_mps.toFixed(2)} m/s · 电量 {Math.round(v.soc * 100)}%
            </span>
            <span className="spacer" />
            <span className="vd-row-go">视频配置 →</span>
          </button>
        ))}
        {vehicles.length === 0 && (
          <div className="muted" style={{ padding: 20, textAlign: "center" }}>
            {fleet.snap.vehicles.length === 0 ? "暂无车辆" : "无匹配车辆"}
          </div>
        )}
      </div>
    </div>
  );
}
