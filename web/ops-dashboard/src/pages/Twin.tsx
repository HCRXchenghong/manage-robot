// 数字孪生车辆详情页：独立页面（无侧栏/顶栏），新标签页打开，等保三级会话鉴权。
// 布局借鉴桌面「数字孪生」项目：左数据面板 / 中 GLB 模型 / 右视频+终端，全部接真实接口。
import { useEffect, useState } from "react";
import { fetchMe, useFleet } from "../api";
import { BarGauge, DialGauge, SignalTable } from "../components/DashGauges";
import MiniVideos from "../components/MiniVideos";
import TerminalPanel from "../components/TerminalPanel";
import TwinModel from "../components/TwinModel";
import type { Me } from "../types";

export default function Twin({ vehicleId }: { vehicleId: string }) {
  const fleet = useFleet();
  const [me, setMe] = useState<Me | null>(null);
  const [checking, setChecking] = useState(true);
  useEffect(() => {
    void fetchMe().then((m) => {
      setMe(m);
      setChecking(false);
    });
  }, []);

  const v = fleet.snap.vehicles.find((x) => x.vehicle_id === vehicleId) || null;

  const back = () => {
    if (window.history.length > 1) window.history.back();
    else window.location.hash = "#/vehicles";
  };

  if (checking) {
    return (
      <div className="twin-root">
        <div className="muted">正在校验会话…</div>
      </div>
    );
  }
  if (!me) {
    return (
      <div className="twin-root">
        <div className="twin-auth">
          <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 8 }}>会话无效或已过期</div>
          <div className="muted">数字孪生页启用等保三级鉴权，请先在主平台登录后再打开本页。</div>
          <a className="btn small primary" style={{ marginTop: 12, display: "inline-block" }} href="/">
            返回登录
          </a>
        </div>
      </div>
    );
  }
  if (!v) {
    return (
      <div className="twin-root">
        <div className="twin-auth">
          <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 8 }}>车辆不存在或无权访问</div>
          <div className="muted">{vehicleId || "（URL 无车辆 ID）"}</div>
          <button className="btn small" style={{ marginTop: 12 }} onClick={back}>
            ← 返回
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="twin-root">
      <header className="twin-head">
        <button className="btn small" onClick={back}>
          ← 返回
        </button>
        <h1>数字孪生 · {v.vehicle_id}</h1>
        <span className={"badge " + (v.online ? "ok" : "dim")}>{v.online ? "在线" : "离线"}</span>
        <span className="badge info">{v.mode === "autonomous" ? "自动驾驶" : v.mode || "-"}</span>
        <span className="badge dim">{v.chassis || "底盘未登记"}</span>
        <span className="spacer" />
        <span className="muted mono">
          车速 {v.speed_mps.toFixed(2)} m/s · 电量 {Math.round(v.soc * 100)}%
        </span>
      </header>
      <div className="twin-body">
        <aside className="twin-col twin-left">
          <div className="twin-card">
            <div className="twin-card-t">机内环境</div>
            <BarGauge label="机内温度" value={v.cabin_temp_c ?? 26} min={0} max={50} unit="°C" color="#f59e0b" />
            <BarGauge label="机内湿度" value={v.cabin_humidity_pct ?? 45} min={0} max={100} unit="%" color="#38bdf8" digits={0} />
          </div>
          <div className="twin-card twin-dials">
            <DialGauge value={v.speed_mps * 3.6} max={60} unit="km/h" label="车速" color="#38bdf8" sub={v.speed_mps.toFixed(2) + " m/s"} />
            <DialGauge value={v.soc * 100} max={100} unit="%" label="电量" color="#22c55e" sub={v.voltage.toFixed(1) + " V"} />
          </div>
          <div className="twin-card">
            <div className="twin-card-t">设备状态</div>
            <div className="kv">
              <span>挡位 / 转向</span>
              <b className="mono">{v.gear || "-"} · {v.steer_rad.toFixed(3)} rad</b>
            </div>
            <div className="kv">
              <span>位姿</span>
              <b className="mono">x={v.pose.x.toFixed(1)} y={v.pose.y.toFixed(1)} yaw={v.pose.yaw.toFixed(2)}</b>
            </div>
            <div className="kv">
              <span>GPS</span>
              <b className="mono">{v.gps.fix ? v.gps.lat.toFixed(5) + ", " + v.gps.lon.toFixed(5) : "无定位"}</b>
            </div>
            <div className="kv">
              <span>心跳龄</span>
              <b className="mono">{v.last_heartbeat_age_s.toFixed(1)} s</b>
            </div>
          </div>
          <div className="twin-card twin-sig">
            <div className="twin-card-t">实时信号表</div>
            <SignalTable v={v} />
          </div>
        </aside>
        <main className="twin-center">
          <TwinModel vehicleId={v.vehicle_id} powered={v.online} />
        </main>
        <aside className="twin-col twin-right">
          <div className="twin-card">
            <MiniVideos vehicle={v} />
          </div>
          <div className="twin-card twin-term">
            <TerminalPanel vehicle={v} fixed />
          </div>
        </aside>
      </div>
    </div>
  );
}
