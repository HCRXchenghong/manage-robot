import { useEffect, useState } from "react";
import type { FleetSnap } from "../types";
import type { FleetSource } from "../api";

interface Props {
  snap: FleetSnap;
  source: FleetSource;
  wsConnected: boolean;
}

function pad(n: number): string {
  return n < 10 ? "0" + n : String(n);
}

export default function TopBar({ snap, source, wsConnected }: Props) {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const t = window.setInterval(() => setNow(new Date()), 1000);
    return () => window.clearInterval(t);
  }, []);

  const onlineCount = snap.vehicles.filter((v) => v.online).length;
  const mqttOk = source === "live";
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
        <span className="status-dot">
          <span className={"dot " + (mqttOk ? "ok" : "err")} />
          MQTT 服务 {mqttOk ? "正常" : "离线（演示数据）"}
        </span>
        <span className="status-dot">
          <span className={"dot " + (wsConnected ? "ok" : mqttOk ? "warn" : "err")} />
          实时推送 {wsConnected ? "已连接" : "未连接"}
        </span>
        <span className="status-dot">
          <span className={"dot " + (gwOk ? "ok" : "warn")} />
          网关心跳 {gwOk ? onlineCount + " 车在线" : "无在线车辆"}
        </span>
      </div>
      <div className="topbar-clock">{clock}</div>
    </header>
  );
}
