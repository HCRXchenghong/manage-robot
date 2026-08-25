// 总览顶部统计：地图内顶部一条磨砂悬浮条（无单独卡片边框），
// 五项均匀排开、细竖线分隔；整条可收起成小圆钮，展开/收起由父级受控并持久化。
import NavIcon from "./NavIcons";
import type { FleetSnap } from "../types";

interface Props {
  snap: FleetSnap;
  open: boolean;
  onOpenChange: (v: boolean) => void;
}

export default function StatCards({ snap, open, onOpenChange }: Props) {
  const total = snap.vehicles.length;
  const online = snap.vehicles.filter((v) => v.online).length;
  const driving = snap.vehicles.filter((v) => v.online && v.speed_mps > 0.1).length;
  const takeoverCount = snap.takeover.active ? 1 : 0;
  const alerts = snap.events.filter((e) => e.level === "critical" || e.level === "warn").length;
  const health = total === 0 ? 100 : Math.round((online / total) * 100);
  const healthLabel = health >= 99 ? "健康" : health >= 60 ? "一般" : "异常";

  if (!open) {
    return (
      <button className="stat-pill" onClick={() => onOpenChange(true)} title="展开统计">
        统计 {online}/{total} ▾
      </button>
    );
  }

  const items = [
    { icon: "truck", tint: "#4ade80", bg: "rgba(34,197,94,.14)", label: "在线车辆 / 总数", value: online + " / " + total, sub: "6 秒无遥测判离线" },
    { icon: "route", tint: "#22d3ee", bg: "rgba(34,211,238,.12)", label: "行驶中", value: String(driving), sub: "车速 > 0.1 m/s" },
    { icon: "joystick", tint: "#a78bfa", bg: "rgba(167,139,250,.14)", label: "接管人数", value: String(takeoverCount), sub: snap.takeover.active ? "驾驶员 " + (snap.takeover.driver || "?") : "当前无接管" },
    { icon: "bell", tint: "#f87171", bg: "rgba(248,113,113,.14)", label: "告警数（环内）", value: String(alerts), sub: "critical + warn" },
  ];

  return (
    <div className="stat-strip">
      {items.map((it) => (
        <div className="stat-item" key={it.label}>
          <span className="stat-ico" style={{ background: it.bg, color: it.tint }}>
            <NavIcon name={it.icon} />
          </span>
          <span className="stat-txt">
            <span className="stat-label">{it.label}</span>
            <span className="stat-value">{it.value}</span>
            <span className="stat-sub">{it.sub}</span>
          </span>
        </div>
      ))}
      <div className="stat-item">
        <span className="stat-ico" style={{ background: "rgba(52,211,153,.14)", color: "#34d399" }}>
          <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round">
            <path d="M8 1.8 3 3.6v4c0 3.2 2.1 5.6 5 6.6 2.9-1 5-3.4 5-6.6v-4L8 1.8Z" />
            <path d="m5.8 7.8 1.6 1.6 2.8-3" />
          </svg>
        </span>
        <span className="stat-txt">
          <span className="stat-label">链路健康度</span>
          <span className="stat-value">{health}%</span>
          <span className="stat-sub">{healthLabel}</span>
        </span>
      </div>
      <button className="stat-collapse" onClick={() => onOpenChange(false)} title="收起统计">▴</button>
    </div>
  );
}
