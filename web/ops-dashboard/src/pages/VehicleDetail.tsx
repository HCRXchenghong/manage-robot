import type { FleetState } from "../api";
import type { VehicleSnap } from "../types";
import Sparkline from "../components/Sparkline";
import CollapsePanel from "../components/CollapsePanel";
import VideoPanel from "../components/VideoPanel";
import VehicleTable, { vehicleStatusOf } from "../components/VehicleTable";

const MODE_LABEL: Record<string, string> = {
  autonomous: "自动驾驶",
  remote_control: "远程遥控",
  minimum_risk: "最小风险",
  stopped: "已停车",
};

const STATUS_LABEL: Record<string, string> = {
  driving: "行驶",
  idle: "空闲",
  offline: "离线",
  alert: "告警",
};

interface Props {
  fleet: FleetState;
  vehicle: VehicleSnap | null;
  onSelect: (id: string, goDetail?: boolean) => void;
  compact?: boolean;
}

export default function VehicleDetail({ fleet, vehicle, onSelect, compact }: Props) {
  if (!vehicle) {
    return (
      <CollapsePanel id="ov-detail" title="车辆详情">
        <VehicleTable snap={fleet.snap} onSelect={onSelect} />
      </CollapsePanel>
    );
  }
  const v = vehicle;
  const caps = Object.entries(v.capabilities || {});
  return (
    <CollapsePanel
      id="ov-detail"
      title={"车辆详情 · " + v.vehicle_id}
      hint={STATUS_LABEL[vehicleStatusOf(v)] + " · " + (MODE_LABEL[v.mode] || v.mode || "-")}
    >
        <div className="kv">
          <span className="k">在线</span><span>{v.online ? "是" : "否（心跳龄 " + v.last_heartbeat_age_s.toFixed(1) + "s）"}</span>
          <span className="k">模式</span><span>{MODE_LABEL[v.mode] || v.mode || "-"}</span>
          <span className="k">车速</span><span className="mono">{v.speed_mps.toFixed(2)} m/s（{((v.speed_mps * 3.6).toFixed(1))} km/h）</span>
          <span className="k">电量</span><span className="mono">{Math.round(v.soc * 100)}% · {v.voltage.toFixed(1)} V</span>
          <span className="k">挡位 / 转向</span><span className="mono">{v.gear || "-"} · {v.steer_rad.toFixed(3)} rad</span>
          <span className="k">位姿</span><span className="mono">x={v.pose.x.toFixed(1)} y={v.pose.y.toFixed(1)} yaw={v.pose.yaw.toFixed(2)}</span>
        </div>
        <div className="muted" style={{ fontSize: 11, margin: "6px 0 4px" }}>车速历史（{(v.speed_history || []).length}/150 点）</div>
        <Sparkline data={v.speed_history || []} height={compact ? 70 : 110} />
        {caps.length > 0 && (
          <>
            <div className="muted" style={{ fontSize: 11, margin: "10px 0 6px" }}>能力声明（register retained）</div>
            <div className="cap-list">
              {caps.map(([k, val]) => (
                <div className="cap-item" key={k}>
                  <div className="cap-key">{k}</div>
                  <div>{val}</div>
                </div>
              ))}
            </div>
          </>
        )}
      {!compact && (
        <div className="panel" style={{ marginTop: 12 }}>
          <VideoPanel vehicle={v} height={200} />
        </div>
      )}
    </CollapsePanel>
  );
}
