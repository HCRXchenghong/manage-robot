import type { VehicleSnap } from "../types";

// 底盘状态面板：轮速 / 转向 / 电量 / 底盘类型（阿克曼 / 四轮四转 / 差速AGV）
const CHASSIS_LABEL: Record<string, string> = {
  ackermann: "阿克曼（前转后驱）",
  "4w4s": "四轮四转",
  diff_agv: "差速 AGV",
};

type WheelPos = "FL" | "FR" | "RL" | "RR";
const ORDER: WheelPos[] = ["FL", "FR", "RL", "RR"];
const IDX: Record<WheelPos, number> = { FL: 0, FR: 1, RL: 2, RR: 3 };

export default function ChassisPanel({ vehicle }: { vehicle: VehicleSnap | null }) {
  if (!vehicle) return <div className="muted">暂无车辆</div>;
  const type = (vehicle.capabilities && vehicle.capabilities.chassis_type) || "ackermann";
  const ws =
    vehicle.wheel_speeds && vehicle.wheel_speeds.length === 4
      ? vehicle.wheel_speeds
      : [vehicle.speed_mps, vehicle.speed_mps, vehicle.speed_mps, vehicle.speed_mps];
  const steer = vehicle.steer_rad || 0;
  const steerOf = (p: WheelPos) => {
    if (type === "4w4s") return steer;
    if (type === "ackermann") return p === "FL" || p === "FR" ? steer : 0;
    return 0; // 差速 AGV 无转向，靠左右轮速差
  };
  return (
    <div>
      <div className="kv">
        <span className="k">底盘类型</span>
        <span>{CHASSIS_LABEL[type] || type}</span>
        <span className="k">转向角</span>
        <span className="mono">{steer.toFixed(3)} rad（{(steer * 57.3).toFixed(1)}°）</span>
        <span className="k">电量</span>
        <span className="mono">{Math.round(vehicle.soc * 100)}% · {vehicle.voltage.toFixed(1)} V</span>
      </div>
      <div className="chassis-grid">
        {ORDER.map((p) => (
          <div key={p} className="wheel-card">
            <div
              className="wheel"
              style={{ transform: "rotate(" + (steerOf(p) * 57.3).toFixed(0) + "deg)" }}
            />
            <div className="mono" style={{ fontSize: 11 }}>
              {p} · {ws[IDX[p]].toFixed(2)} m/s
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

