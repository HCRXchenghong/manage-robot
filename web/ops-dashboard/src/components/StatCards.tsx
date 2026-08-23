import type { FleetSnap } from "../types";

export default function StatCards({ snap }: { snap: FleetSnap }) {
  const total = snap.vehicles.length;
  const online = snap.vehicles.filter((v) => v.online).length;
  const driving = snap.vehicles.filter((v) => v.online && v.speed_mps > 0.1).length;
  const takeoverCount = snap.takeover.active ? 1 : 0;
  const alerts = snap.events.filter((e) => e.level === "critical" || e.level === "warn").length;
  const health = total === 0 ? 100 : Math.round((online / total) * 100);
  const healthLabel = health >= 99 ? "健康" : health >= 60 ? "一般" : "异常";

  return (
    <div className="stat-cards">
      <div className="stat-card">
        <div className="stat-label">在线车辆 / 总数</div>
        <div className="stat-value">{online} / {total}</div>
        <div className="stat-sub">6 秒无遥测判离线</div>
      </div>
      <div className="stat-card">
        <div className="stat-label">行驶中</div>
        <div className="stat-value">{driving}</div>
        <div className="stat-sub">车速 &gt; 0.1 m/s</div>
      </div>
      <div className="stat-card">
        <div className="stat-label">接管人数</div>
        <div className="stat-value">{takeoverCount}</div>
        <div className="stat-sub">{snap.takeover.active ? "驾驶员 " + (snap.takeover.driver || "?") : "当前无接管"}</div>
      </div>
      <div className="stat-card">
        <div className="stat-label">告警数（环内）</div>
        <div className="stat-value">{alerts}</div>
        <div className="stat-sub">critical + warn</div>
      </div>
      <div className="stat-card">
        <div className="stat-label">链路健康度</div>
        <div className="stat-value">{health}%</div>
        <div className="stat-sub">{healthLabel}</div>
      </div>
    </div>
  );
}
