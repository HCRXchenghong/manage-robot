import { useMemo, useState } from "react";
import type { FleetSnap, VehicleSnap } from "../types";

export function vehicleStatusOf(v: VehicleSnap): "driving" | "idle" | "offline" | "alert" {
  if (!v.online) return "offline";
  if (v.mode === "minimum_risk") return "alert";
  if (v.speed_mps > 0.1) return "driving";
  return "idle";
}

const STATUS_LABEL: Record<string, { label: string; cls: string }> = {
  driving: { label: "行驶", cls: "ok" },
  idle: { label: "空闲", cls: "info" },
  offline: { label: "离线", cls: "dim" },
  alert: { label: "告警", cls: "err" },
};

const MODE_LABEL: Record<string, string> = {
  autonomous: "自动驾驶",
  remote_control: "远程遥控",
  minimum_risk: "最小风险",
  stopped: "已停车",
};

interface Props {
  snap: FleetSnap;
  onSelect?: (id: string, goDetail?: boolean) => void;
  onContext?: (id: string, x: number, y: number) => void;
  selectedId?: string | null;
  compact?: boolean;
  onConfig?: (id: string) => void;
}

export default function VehicleTable({ snap, onSelect, onContext, selectedId, compact, onConfig }: Props) {
  const [q, setQ] = useState("");
  const [tab, setTab] = useState<"all" | "online" | "offline" | "alert">("all");

  const rows = useMemo(() => {
    let list = snap.vehicles;
    if (tab === "online") list = list.filter((v) => v.online);
    if (tab === "offline") list = list.filter((v) => !v.online);
    if (tab === "alert") list = list.filter((v) => vehicleStatusOf(v) === "alert");
    if (q.trim()) list = list.filter((v) => v.vehicle_id.toLowerCase().includes(q.trim().toLowerCase()));
    return list;
  }, [snap.vehicles, q, tab]);

  return (
    <div style={{ display: "flex", flexDirection: "column", minHeight: 0, flex: 1 }}>
      {!compact && (
        <>
          <div className="row" style={{ marginBottom: 10 }}>
            <input className="input" placeholder="搜索车辆 ID…" value={q} onChange={(e) => setQ(e.target.value)} />
            <div className="tabs">
              {([["all", "全部"], ["online", "在线"], ["offline", "离线"], ["alert", "告警"]] as const).map(([id, label]) => (
                <button key={id} className={"tab" + (tab === id ? " active" : "")} onClick={() => setTab(id)}>
                  {label}
                </button>
              ))}
            </div>
          </div>
        </>
      )}
      <div className="scroll">
        <table className="table">
          <thead>
            <tr>
              <th>车辆 ID</th>
              <th>状态</th>
              {!compact && <th>模式</th>}
              {!compact && <th>底盘</th>}
              <th>车速 m/s</th>
              {!compact && <th>电量</th>}
              <th>心跳龄 s</th>
              {onConfig && <th style={{ textAlign: "right" }}>操作</th>}
            </tr>
          </thead>
          <tbody>
            {rows.map((v) => {
              const st = STATUS_LABEL[vehicleStatusOf(v)];
              return (
                <tr
                  key={v.vehicle_id}
                  className={"clickable" + (selectedId === v.vehicle_id ? " selected" : "")}
                  onClick={() => onSelect && onSelect(v.vehicle_id, !compact)}
                  onContextMenu={(e) => {
                    e.preventDefault();
                    if (onContext) onContext(v.vehicle_id, e.clientX, e.clientY);
                  }}
                >
                  <td>{v.vehicle_id}</td>
                  <td><span className={"badge " + st.cls}>{st.label}</span></td>
                  {!compact && <td>{MODE_LABEL[v.mode] || v.mode || "-"}</td>}
                  {!compact && <td>{v.chassis || "-"}</td>}
                  <td className="mono">{v.speed_mps.toFixed(2)}</td>
                  {!compact && <td className="mono">{Math.round(v.soc * 100)}%</td>}
                  <td className="mono">{v.last_heartbeat_age_s.toFixed(1)}</td>
                  {onConfig && (
                    <td style={{ textAlign: "right" }}>
                      <button
                        className="btn small"
                        onClick={(e) => {
                          e.stopPropagation();
                          onConfig(v.vehicle_id);
                        }}
                      >
                        配置
                      </button>
                    </td>
                  )}
                </tr>
              );
            })}
            {rows.length === 0 && (
              <tr><td colSpan={6} className="muted" style={{ padding: 12 }}>无匹配车辆</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
