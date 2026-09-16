// 仪表盘式可视化：表盘(车速/电量) / 踏板竖条(油门/刹车) / 条形表(温湿度) /
// 加速度曲线 / 实时信号表。纯 SVG 实现，不引图表库。
import type { VehicleSnap } from "../types";

function polar(cx: number, cy: number, r: number, deg: number): [number, number] {
  const rad = (deg * Math.PI) / 180;
  return [cx + r * Math.cos(rad), cy + r * Math.sin(rad)];
}
function arcPath(cx: number, cy: number, r: number, d0: number, d1: number): string {
  const [x0, y0] = polar(cx, cy, r, d0);
  const [x1, y1] = polar(cx, cy, r, d1);
  const large = d1 - d0 > 180 ? 1 : 0;
  return (
    "M " + x0.toFixed(2) + " " + y0.toFixed(2) +
    " A " + r + " " + r + " 0 " + large + " 1 " + x1.toFixed(2) + " " + y1.toFixed(2)
  );
}

const A0 = 150;
const A1 = 390; // 240° 表盘，缺口朝下

// 汽车仪表盘式表盘：指针 + 刻度 + 数字读数
export function DialGauge({
  value,
  max,
  unit,
  label,
  digits = 1,
  color = "#38bdf8",
  sub,
  tight = false,
}: {
  value: number;
  max: number;
  unit: string;
  label: string;
  digits?: number;
  color?: string;
  sub?: string;
  tight?: boolean; // 裁掉表盘 viewBox 四周留白，同样容器下表盘显得更大
}) {
  const frac = Math.min(1, Math.max(0, value / max));
  const ang = A0 + (A1 - A0) * frac;
  const [nx, ny] = polar(50, 50, 28, ang);
  return (
    <div className="dial">
      <svg viewBox={tight ? "7 7 86 86" : "0 0 100 100"}>
        <path d={arcPath(50, 50, 40, A0, A1)} className="dial-track" />
        <path d={arcPath(50, 50, 40, A0, Math.max(A0 + 0.5, ang))} className="dial-fill" style={{ stroke: color }} />
        {Array.from({ length: 7 }, (_, i) => {
          const a = A0 + ((A1 - A0) * i) / 6;
          const [tx0, ty0] = polar(50, 50, 44, a);
          const [tx1, ty1] = polar(50, 50, 47, a);
          return <line key={i} x1={tx0} y1={ty0} x2={tx1} y2={ty1} className="dial-tick" />;
        })}
        <line x1={50} y1={50} x2={nx} y2={ny} className="dial-needle" />
        <circle cx={50} cy={50} r={3.2} className="dial-hub" />
      </svg>
      <div className="dial-read">
        <div className="dial-val mono" style={{ color }}>{value.toFixed(digits)}</div>
        <div className="dial-unit">{label ? unit + " · " + label : unit}</div>
        {sub ? <div className="dial-sub mono">{sub}</div> : null}
      </div>
    </div>
  );
}

// 踏板式竖条：油门绿 / 刹车红，底部填充 + 顶部百分比
export function PedalBars({ throttle, brake, small }: { throttle: number; brake: number; small?: boolean }) {
  const items = [
    { label: "油门", val: throttle, cls: "thr" },
    { label: "刹车", val: brake, cls: "brk" },
  ];
  return (
    <div className={"pedals" + (small ? " small" : "")}>
      {items.map((p) => (
        <div className="pedal" key={p.label}>
          <div className="pedal-bar">
            <div className={"pedal-fill " + p.cls} style={{ height: Math.min(100, Math.max(0, p.val)) + "%" }} />
            <span className="pedal-pct mono">{p.val.toFixed(0)}%</span>
          </div>
          <div className="pedal-label">{p.label}</div>
        </div>
      ))}
    </div>
  );
}

// 横向条形表（温度 / 湿度等）
export function BarGauge({
  label,
  value,
  min,
  max,
  unit,
  color,
  digits = 1,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  unit: string;
  color: string;
  digits?: number;
}) {
  const frac = Math.min(1, Math.max(0, (value - min) / (max - min)));
  return (
    <div className="barg">
      <div className="barg-head">
        <span>{label}</span>
        <span className="mono" style={{ color }}>{value.toFixed(digits)}{unit}</span>
      </div>
      <div className="barg-track">
        <div className="barg-fill" style={{ width: (frac * 100).toFixed(1) + "%", background: color }} />
      </div>
      <div className="barg-scale mono"><span>{min}</span><span>{max}</span></div>
    </div>
  );
}

// 加速度曲线（±2 m/s² 量程，零线虚线）
export function AccelChart({ hist, height = 96 }: { hist: number[]; height?: number }) {
  const W = 300;
  const H = 100;
  const range = 2;
  const pts = (hist && hist.length ? hist : [0]).slice(-150);
  const step = W / 149;
  const path = pts
    .map((v, i) => {
      const x = W - (pts.length - 1 - i) * step;
      const y = H / 2 - (Math.min(range, Math.max(-range, v)) / range) * (H / 2 - 6);
      return (i === 0 ? "M" : "L") + x.toFixed(1) + " " + y.toFixed(1);
    })
    .join(" ");
  const last = pts[pts.length - 1] || 0;
  return (
    <div className="accel-chart">
      <div className="chart-head">
        <b>加速度曲线</b>
        <span className="mono" style={{ color: last >= 0 ? "#22c55e" : "#ef4444" }}>
          {last.toFixed(2)} m/s²
        </span>
      </div>
      <svg viewBox={"0 0 " + W + " " + H} preserveAspectRatio="none" style={{ height }}>
        <line x1={0} y1={H / 2} x2={W} y2={H / 2} className="accel-zero" />
        <path d={path} className="accel-line" />
      </svg>
    </div>
  );
}

// 实时信号表（对齐 1.html 的信号表：信号/数值/单位[/来源字段]）。
// 来源字段默认隐藏，仅弹窗详情页（showSource）展示。
export function SignalTable({ v, showSource }: { v: VehicleSnap; showSource?: boolean }) {
  const rows: [string, string, string, string][] = [
    ["车速", (v.speed_mps * 3.6).toFixed(1), "km/h", "Vehicle.Chassis.Speed"],
    ["纵向加速度", (v.accel_mps2 ?? 0).toFixed(2), "m/s2", "Vehicle.Chassis.Accel.Longitudinal"],
    ["油门反馈", (v.throttle_pct || 0).toFixed(0), "%", "Vehicle.Chassis.Throttle.Pct"],
    ["制动反馈", (v.brake_pct || 0).toFixed(0), "%", "Vehicle.Chassis.Brake.Pct"],
    ["转向", v.steer_rad.toFixed(3), "rad", "Vehicle.Chassis.SteeringWheel.Angle"],
    ["挡位", v.gear || "-", "", "Vehicle.Powertrain.Transmission.CurrentGear"],
    ["电量", String(Math.round(v.soc * 100)), "%", "Vehicle.Powertrain.TractionBattery.StateOfCharge"],
    ["总电压", v.voltage.toFixed(1), "V", "Vehicle.Powertrain.TractionBattery.Voltage"],
    ["机内温度", (v.cabin_temp_c ?? 26).toFixed(1), "°C", "Vehicle.Cabin.Temperature.C"],
    ["机内湿度", (v.cabin_humidity_pct ?? 45).toFixed(0), "%", "Vehicle.Cabin.Humidity.Pct"],
    ["位姿 X / Y", v.pose.valid ? v.pose.x.toFixed(1) + " / " + v.pose.y.toFixed(1) : "未上报", "m", "Platform.Autonomy.Localization.Pose"],
    ["航向 yaw", v.pose.valid ? v.pose.yaw.toFixed(2) : "未上报", "rad", "Platform.Autonomy.Localization.Pose.Yaw"],
    ["GPS 纬 / 经", v.gps.lat.toFixed(5) + " / " + v.gps.lon.toFixed(5), "°", "Vehicle.GPS.*"],
    ["心跳龄", v.last_heartbeat_age_s.toFixed(1), "s", "Platform.Heartbeat"],
  ];
  return (
    <div className="sig-table-wrap">
      <table className="sig-table">
        <thead>
          <tr>
            <th>信号</th>
            <th style={{ textAlign: "right" }}>数值</th>
            <th>单位</th>
            {showSource && <th>来源字段</th>}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r[3] + r[0]}>
              <td>{r[0]}</td>
              <td className="mono" style={{ textAlign: "right" }}>{r[1]}</td>
              <td>{r[2]}</td>
              {showSource && <td className="dim">{r[3]}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
