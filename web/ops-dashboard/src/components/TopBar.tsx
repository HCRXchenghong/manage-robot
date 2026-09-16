import { useEffect, useState } from "react";
import type { FleetSnap } from "../types";
import { fetchJSON, type FleetSource } from "../api";

interface Props {
  snap: FleetSnap;
  source: FleetSource;
  wsConnected: boolean;
  lastVerifiedAt: number | null;
  lastLiveAt: number | null;
}

function pad(n: number): string {
  return n < 10 ? "0" + n : String(n);
}

export default function TopBar({ snap, source, wsConnected, lastVerifiedAt, lastLiveAt }: Props) {
	const [now, setNow] = useState(() => new Date());
	const [runtime, setRuntime] = useState<{ database_ready: boolean; mqtt_ready: boolean } | null>(null);
  useEffect(() => {
    const t = window.setInterval(() => setNow(new Date()), 1000);
    return () => window.clearInterval(t);
	}, []);
	useEffect(() => {
		let stopped = false;
		const load = () => void fetchJSON<{ database_ready: boolean; mqtt_ready: boolean }>("/api/runtime")
			.then((next) => { if (!stopped) setRuntime(next); })
			.catch(() => { if (!stopped) setRuntime(null); });
		load();
		const timer = window.setInterval(load, 5000);
		return () => { stopped = true; window.clearInterval(timer); };
	}, []);

  const onlineCount = snap.vehicles.filter((v) => v.online).length;
	const dbOk = runtime?.database_ready === true;
	const mqttOk = runtime?.mqtt_ready === true;
  const fleetStatus = source === "live" ? "实时" : source === "degraded" ? "降级缓存" : "未获取快照";
  const fleetDot = source === "live" ? "ok" : source === "degraded" ? "warn" : "err";
  const ageSeconds = lastVerifiedAt == null ? null : Math.max(0, Math.floor((now.getTime() - lastVerifiedAt) / 1000));
  const freshness = ageSeconds == null ? "尚未同步" : ageSeconds + "s 前验证";
  const liveFreshness = lastLiveAt == null ? "未收到状态帧" : Math.max(0, Math.floor((now.getTime() - lastLiveAt) / 1000)) + "s 前";
	const gwOk = source === "live" && onlineCount > 0;
  const clock =
    now.getFullYear() + "-" + pad(now.getMonth() + 1) + "-" + pad(now.getDate()) +
    " " + pad(now.getHours()) + ":" + pad(now.getMinutes()) + ":" + pad(now.getSeconds());

  return (
    <header className="topbar">
      <div className="brand">
        <div>
          <div className="brand-name" style={{ fontSize: 14, letterSpacing: 0.5 }}>机器人集中管理调度平台</div>
          <div className="brand-sub">车队数字孪生 · 集中调度 · 远程接管</div>
        </div>
      </div>
      <div className="topbar-status">
		<span className="status-dot"><span className={"dot " + (dbOk ? "ok" : "err")} />数据库 {dbOk ? "正常" : "未就绪"}</span>
		<span className="status-dot"><span className={"dot " + (mqttOk ? "ok" : "err")} />MQTT 服务 {mqttOk ? "正常" : "未连接"}</span>
        <span className="status-dot">
          <span className={"dot " + (wsConnected ? "ok" : mqttOk ? "warn" : "err")} />
          实时推送 {wsConnected ? "已连接 · " + liveFreshness : "重连中"}
        </span>
        <span className="status-dot" title={freshness}>
          <span className={"dot " + fleetDot} />
          车队快照 {fleetStatus} · {freshness}
        </span>
        <span className="status-dot">
          <span className={"dot " + (gwOk ? "ok" : source === "unavailable" ? "err" : "warn")} />
          网关心跳 {onlineCount > 0 ? onlineCount + " 车在线" + (source === "degraded" ? "（缓存）" : "") : "无在线车辆"}
        </span>
      </div>
      <div className="topbar-clock">{clock}</div>
    </header>
  );
}
