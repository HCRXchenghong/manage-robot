import type { FleetState } from "../api";
import type { VehicleSnap } from "../types";
import CollapsePanel from "../components/CollapsePanel";
import VideoPanel from "../components/VideoPanel";
import VehicleTable, { vehicleStatusOf } from "../components/VehicleTable";
import { AccelChart, BarGauge, DialGauge, PedalBars, SignalTable } from "../components/DashGauges";

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
  onExpand?: () => void;
}

export default function VehicleDetail({ fleet, vehicle, onSelect, compact, onExpand }: Props) {
  if (!vehicle) {
    return (
      <CollapsePanel id="ov-detail" title="车辆详情" fixed={compact}>
        <VehicleTable snap={fleet.snap} onSelect={onSelect} />
      </CollapsePanel>
    );
  }
  const v = vehicle;
  return (
    <CollapsePanel
      id="ov-detail"
      fixed={compact}
      onTitleClick={onExpand}
      title={"车辆详情 · " + v.vehicle_id}
      hint={STATUS_LABEL[vehicleStatusOf(v)] + " · " + (MODE_LABEL[v.mode] || v.mode || "-")}
    >
        <div className={"compact-gauges" + (compact ? "" : " dash-gauges")}>
          <DialGauge value={v.speed_mps * 3.6} max={60} unit="km/h" label="车速" color="#38bdf8" sub={v.speed_mps.toFixed(2) + " m/s"} />
          <DialGauge value={v.soc * 100} max={100} unit="%" label="电量" color="#22c55e" sub={v.voltage.toFixed(1) + " V"} />
          {!compact && (
            <>
              <PedalBars throttle={v.throttle_pct || 0} brake={v.brake_pct || 0} />
              <div className="dash-bars">
                <BarGauge label="机内温度" value={v.cabin_temp_c ?? 26} min={0} max={50} unit="°C" color="#f59e0b" />
                <BarGauge label="机内湿度" value={v.cabin_humidity_pct ?? 45} min={0} max={100} unit="%" color="#38bdf8" digits={0} />
              </div>
            </>
          )}
        </div>
        {compact && <PedalBars throttle={v.throttle_pct || 0} brake={v.brake_pct || 0} small />}
        <div className="kv-list">
          <div className="kv"><span>在线</span><b>{v.online ? "是" : "否（心跳龄 " + v.last_heartbeat_age_s.toFixed(1) + "s）"}</b></div>
          <div className="kv"><span>模式</span><b>{MODE_LABEL[v.mode] || v.mode || "-"}</b></div>
          <div className="kv"><span>挡位 / 转向</span><b className="mono">{v.gear || "-"} · {v.steer_rad.toFixed(3)} rad</b></div>
          <div className="kv"><span>位姿</span><b className="mono">x={v.pose.x.toFixed(1)} y={v.pose.y.toFixed(1)} yaw={v.pose.yaw.toFixed(2)}</b></div>
        </div>
      {!compact && (
        <div className="panel" style={{ marginTop: 12 }}>
          <VideoPanel vehicle={v} height={200} />
        </div>
      )}
    </CollapsePanel>
  );
}

// 弹窗大仪表盘：状态徽章 + 车速/电量表盘 + 踏板 + 温湿度条 + 加速度曲线 + 实时信号表
export function VehicleDashboard({ v }: { v: VehicleSnap }) {
  return (
    <div className="dash">
      <div className="dash-chips">
        <span className={"chip " + (v.online ? "ok" : "err")}>{v.online ? "在线" : "离线"}</span>
        <span className="chip">{MODE_LABEL[v.mode] || v.mode || "-"}</span>
        <span className="chip mono">挡位 {v.gear || "-"} · 转向 {v.steer_rad.toFixed(3)} rad</span>
        <span className="chip mono">位姿 x={v.pose.x.toFixed(1)} y={v.pose.y.toFixed(1)} yaw={v.pose.yaw.toFixed(2)}</span>
      </div>
      <div className="dash-gauges">
        <DialGauge value={v.speed_mps * 3.6} max={60} unit="km/h" label="车速" color="#38bdf8" sub={v.speed_mps.toFixed(2) + " m/s"} />
        <DialGauge value={v.soc * 100} max={100} unit="%" label="电量" color="#22c55e" sub={v.voltage.toFixed(1) + " V"} />
        <PedalBars throttle={v.throttle_pct || 0} brake={v.brake_pct || 0} />
        <div className="dash-bars">
          <BarGauge label="机内温度" value={v.cabin_temp_c ?? 26} min={0} max={50} unit="°C" color="#f59e0b" />
          <BarGauge label="机内湿度" value={v.cabin_humidity_pct ?? 45} min={0} max={100} unit="%" color="#38bdf8" digits={0} />
        </div>
      </div>
      <AccelChart hist={v.accel_history || []} />
      <SignalTable v={v} showSource />
    </div>
  );
}
