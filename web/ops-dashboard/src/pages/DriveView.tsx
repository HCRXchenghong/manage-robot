// DriveView：远程驾驶接管页 v2 —— 参照驾驶舱布局：
//  左上 实时驾驶画面（6 机位 HUD，右键新标签页）
//  右上 运行状态（档位/车速/电量/心跳/四轮转向/温湿度）+ 车辆状态（3D 车辆模型 + 子系统，占满剩余高度）
//  底部：GPS 定位 / 激光点云 / 任务卡（左半油门刹车曲线 + 右半任务字段，无标题行）/ 远程终端
//  接管租约：打开本页即每 3s 心跳续租；交还即收回
import { useEffect, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent } from "react";
import {
  fetchDevices,
  fetchMe,
  fetchRoutes,
  takeoverKeepalive,
  takeoverMine,
  takeoverReleaseMine,
  useFleet,
} from "../api";
import type { ActiveTakeover, DeviceInfo, Me, NavRoute, VehicleSnap } from "../types";
import { useGamepads } from "../peripherals";
import EstopButton from "../components/EstopControl";
import { openIfPlain } from "../components/Modal";
import VideoPanel from "../components/VideoPanel";
import TwinGps from "../components/TwinGps";
import TwinRadar from "../components/TwinRadar";
import TerminalPanel from "../components/TerminalPanel";
import TwinModel from "../components/TwinModel";
import DualLine from "../components/DualLine";
import { DialGauge } from "../components/DashGauges";
import "./drive.css";

type Kind = "video" | "gps" | "pointcloud" | "terminal";
const KIND_LABEL: Record<Kind, string> = {
  video: "视频画面",
  gps: "GPS 定位",
  pointcloud: "激光点云",
  terminal: "远程终端",
};

const SYS_LIST = ["电机控制器", "电池管理系统", "驱动系统", "定位系统", "通信模块", "自动驾驶域"];
const ROUTE_STATUS: Record<string, string> = {
  queued: "排队中", dispatched: "已下发", accepted: "车端已接受", running: "执行中",
  completed: "已完成", cancel_requested: "取消请求中", cancelled: "已取消",
  cancel_rejected: "取消被拒", rejected: "车端拒绝", failed: "执行失败",
};
const WHEELS = ["左前", "右前", "左后", "右后"];
const BATT_CAPACITY_KWH = 30.7; // 电池标称总容量（kWh），用于 SOC → 剩余度电换算

function pad2(n: number) {
  return String(n).padStart(2, "0");
}
function fmtDT(ns: number) {
  if (!ns) return "-";
  const d = new Date(ns / 1e6);
  return d.getFullYear() + "-" + pad2(d.getMonth() + 1) + "-" + pad2(d.getDate()) + " " + pad2(d.getHours()) + ":" + pad2(d.getMinutes()) + ":" + pad2(d.getSeconds());
}
function fmtDur(ms: number) {
  const s = Math.max(0, Math.floor(ms / 1000));
  return pad2(Math.floor(s / 3600)) + ":" + pad2(Math.floor((s % 3600) / 60)) + ":" + pad2(s % 60);
}
function routeKm(r: NavRoute) {
  let m = 0;
  for (let i = 1; i < r.points.length; i++) {
    m += Math.hypot(r.points[i].x - r.points[i - 1].x, r.points[i].y - r.points[i - 1].y);
  }
  return m / 1000;
}

const HeartIcon = ({ size = 20 }: { size?: number }) => (
  <svg viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="#22c55e" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M19 14c1.5-1.5 3-3.2 3-5.5A5.5 5.5 0 0 0 16.5 3c-1.8 0-3 .5-4.5 2-1.5-1.5-2.7-2-4.5-2A5.5 5.5 0 0 0 2 8.5c0 2.3 1.5 4 3 5.5l7 7z" />
  </svg>
);
export default function DriveView({ vehicleId }: { vehicleId: string }) {
  const [me, setMe] = useState<Me | null>(null);
  const [checking, setChecking] = useState(true);
  const fleet = useFleet(me !== null);
  useEffect(() => {
    void fetchMe().then((m) => {
      setMe(m);
      setChecking(false);
    });
  }, []);

  const v = fleet.snap.vehicles.find((x) => x.vehicle_id === vehicleId) || null;

  const [ctx, setCtx] = useState<{ x: number; y: number; kind: Kind } | null>(null);
  const [big, setBig] = useState<"video" | "gps" | "radar">("video");
  const [mine, setMine] = useState<ActiveTakeover | null>(null);
  const [leaseLeft, setLeaseLeft] = useState(0);
  const [lost, setLost] = useState("");
  const [devices, setDevices] = useState<DeviceInfo[]>([]);
  const [routes, setRoutes] = useState<NavRoute[]>([]);
  const [selTask, setSelTask] = useState<string | null>(null);
  const pads = useGamepads();
  const mineRef = useRef<ActiveTakeover | null>(null);
  mineRef.current = mine;

  const back = () => {
    window.location.hash = "#/drive";
  };

  useEffect(() => {
    void takeoverMine().then(setMine);
    void fetchDevices().then(setDevices).catch(() => undefined);
    void fetchRoutes().then(setRoutes).catch(() => undefined);
  }, []);

  // 续租心跳
  useEffect(() => {
    if (!me || !vehicleId) return;
    let stopped = false;
    const tick = async () => {
      const cur = mineRef.current;
      if (!cur || cur.vehicle_id !== vehicleId) return;
      const resp = await takeoverKeepalive(vehicleId, cur.device_id);
      if (stopped) return;
      if (resp.ok && resp.takeover) {
        setMine(resp.takeover);
      } else if (resp.error && /未接管/.test(resp.error)) {
        setLost("接管租约已失效（可能已被交还或过期），请重新发起接管。");
      }
    };
    void tick();
    const t = window.setInterval(() => void tick(), 3000);
    return () => {
      stopped = true;
      window.clearInterval(t);
    };
  }, [me, vehicleId]);

  // 租约倒计时
  useEffect(() => {
    const t = window.setInterval(() => {
      const m = mineRef.current;
      if (!m) {
        setLeaseLeft(0);
        return;
      }
      setLeaseLeft(Math.max(0, m.until_ns / 1e6 - Date.now()) / 1000);
    }, 200);
    return () => window.clearInterval(t);
  }, []);

  const release = async () => {
    if (!window.confirm("确认交还控制权并返回设备绑定页？")) return;
    try {
      await takeoverReleaseMine();
    } catch {
      /* 会话已失效也照样返回 */
    }
    back();
  };

  const onCtxMenu = (kind: Kind) => (e: ReactMouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setCtx({ x: Math.min(e.clientX, window.innerWidth - 200), y: Math.min(e.clientY, window.innerHeight - 120), kind });
  };
  const openInNewTab = (kind: Kind) => {
    window.open("#/panel/" + kind + "?vehicle=" + encodeURIComponent(vehicleId), "_blank", "noopener");
  };

  const periChips = () => {
    const chips: { cls: string; text: string }[] = [];
    for (const d of devices) {
      chips.push({ cls: d.online ? "ok" : "dim", text: (d.online ? "受信控制代理已验证：" : "等待控制代理注册：") + d.name });
    }
    if (devices.length === 0) chips.push({ cls: "dim", text: "当前账号未绑定控制设备" });
    for (const p of pads) {
      chips.push({ cls: "dim", text: "浏览器检测到 " + (p.isWheel ? "USB 方向盘" : "USB 遥控器/手柄") + " #" + p.index + "（未作控制授权）" });
    }
    return chips;
  };

  if (checking) {
    return (
      <div className="dr-root">
        <div className="dr-auth"><div className="muted">正在校验会话…</div></div>
      </div>
    );
  }
  if (!me) {
    return (
      <div className="dr-root">
        <div className="dr-auth">
          <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 8 }}>会话无效或已过期</div>
          <div className="muted">远程驾驶页启用等保三级鉴权，请先在主平台登录后再打开本页。</div>
          <a className="btn small primary" style={{ marginTop: 12, display: "inline-block" }} href="/">返回登录</a>
        </div>
      </div>
    );
  }
  if (!v) {
    return (
      <div className="dr-root">
        <div className="dr-auth">
          <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 8 }}>车辆不存在或无权访问</div>
          <div className="muted">{vehicleId || "（URL 无车辆 ID）"}</div>
          <button className="btn small" style={{ marginTop: 12 }} onClick={back}>← 返回设备绑定页</button>
        </div>
      </div>
    );
  }

  const isMineHere = mine != null && mine.vehicle_id === vehicleId;
  const kmh = v.speed_mps * 3.6;
  const socPct = Math.round(v.soc * 100);
  const steerDeg = (v.steer_rad * 180) / Math.PI;
  const battColor = socPct > 50 ? "#22c55e" : socPct > 20 ? "#f59e0b" : "#ef4444";
  const myRoutes = routes.filter((r) => r.vehicle_id === vehicleId);
  const latest = myRoutes.slice().sort((a, b) => b.created_ns - a.created_ns)[0] || null;
  const activeCount = myRoutes.filter((r) => ["queued", "dispatched", "accepted", "running", "cancel_requested"].includes(r.status)).length;
  const shown = myRoutes.find((r) => r.id === selTask) || latest;

  return (
    <div className="dr-root">
      <header className="dr-head">
        <button className="btn small" onClick={back}>← 返回</button>
        <h1>远程驾驶接管 · {v.vehicle_id}<span className="dv-dash" /></h1>
        <span className={"badge " + (v.online ? "ok" : "dim")}>{v.online ? "在线" : "离线"}</span>
        <span className="badge info">{v.mode === "autonomous" ? "自动驾驶" : v.mode || "-"}</span>
        <span className="muted mono">车速 {v.speed_mps.toFixed(2)} m/s · 电量 {socPct}%</span>
        <span className="spacer" />
        {isMineHere && (
          <div className="dr-lease">
            <span className={"badge " + (leaseLeft < 2 ? "err" : "ok")}>接管中 · {mine!.driver}</span>
            <span className={"dr-count" + (leaseLeft < 2 ? " low" : "")}>租约 {leaseLeft.toFixed(1)}s</span>
            <button className="btn small" onClick={() => void release()}>交还控制权</button>
          </div>
        )}
        <EstopButton vehicleId={v.vehicle_id} className="btn danger dr-head-estop" />
      </header>

      <div className="dr-peri">
        <span className="dr-peri-title">外设设备状态</span>
        {periChips().map((c, i) => (
          <span key={i} className={"dv-pill " + c.cls}>{c.text}</span>
        ))}
        {!lost && !isMineHere && (
          <span className="dr-peri-warn">
            你当前未接管该车（直接打开本页无法下发控制指令）。
            <button className="btn small primary" onClick={back}>去绑定设备并接管</button>
          </span>
        )}
      </div>

      {lost && (
        <div className="dv-msg err" style={{ margin: "8px 10px 0" }}>
          {lost} <button className="btn small" onClick={back}>返回重新接管</button>
        </div>
      )}

      <div className="dr2-body">
        <section
          className="dr2-card dr2-video"
          onContextMenu={onCtxMenu(big === "gps" ? "gps" : big === "radar" ? "pointcloud" : "video")}
        >
          {big === "video" && <VideoPanel vehicle={v} hud />}
          {big === "gps" && <div className="dr2-b-body"><TwinGps vehicle={v} fill /></div>}
          {big === "radar" && <div className="dr2-b-body"><TwinRadar vehicle={v} fill /></div>}
        </section>

        <div className="dr2-right">
          <section className="dr2-card dr2-rstate">
            <div className="dr2-head"><b>运行状态</b></div>
            <div className="rt-split">
              <div className="rt-grid rt-grid-half">
                <div className="rt-tile">
                  <span className="rt-l">档位</span>
                  <b className="rt-v rt-gear">{v.gear || "N"}</b>
                </div>
                <div className="rt-tile">
                  <span className="rt-l">车速</span>
                  <DialGauge value={kmh} max={60} unit="km/h" label="" color="#38bdf8" tight />
                </div>
                <div className="rt-tile wide">
                  <span className="rt-l">四轮转向角度</span>
                  <div className="wh-grid rt-wh">
                    {WHEELS.map((w, i) => (
                      <div className="wh-tile" key={w}>
                        <span className="wh-l">{w}</span>
                        <b className="wh-v mono">{(i < 2 ? steerDeg : 0).toFixed(1)}°</b>
                      </div>
                    ))}
                  </div>
                </div>
              </div>
              <div className="rt-model">
                <TwinModel vehicleId={v.vehicle_id} powered={v.online} compact />
              </div>
            </div>
          </section>

          <section className="dr2-card dr2-vstate">
            <div className="dr2-head"><b>车辆状态</b></div>
            <div className="dr2-vs-body">
              <div className="vs-left">
                <div className="vs-cell vs-cell-batt">
                  <span className="rt-l">电量</span>
                  <div className="vs-batt-row">
                    <svg viewBox="0 0 26 64" className="rt-batt-svg-v">
                      <rect x="3" y="7" width="20" height="56" rx="4" fill="none" stroke="#334155" strokeWidth="2" />
                      <rect x="9" y="1" width="8" height="4" rx="1.5" fill="#334155" />
                      <rect x="6" y={10 + 50 * (1 - socPct / 100)} width="14" height={Math.max(1.5, (50 * socPct) / 100)} rx="2" fill={battColor} />
                    </svg>
                    <div className="vs-col">
                      <b className="vs-batt-v mono">{(v.voltage || 0).toFixed(1)}<i> V</i></b>
                      <b className="rt-v rt-batt-v" style={{ color: battColor }}>{socPct}<i>%</i></b>
                      <span className="rt-s mono">{(v.soc * BATT_CAPACITY_KWH).toFixed(1)} kWh</span>
                    </div>
                  </div>
                </div>
                <div className="vs-envcol">
                  <div className="vs-cell">
                    <span className="rt-l">机内温度 / 湿度</span>
                    <div className="vs-env-row">
                      <span className="rt-s mono vs-env-v">温度 {(v.cabin_temp_c ?? 26).toFixed(1)}°C</span>
                      <div className="dd-bar temp"><i style={{ width: Math.min(100, Math.max(2, ((v.cabin_temp_c ?? 26) / 50) * 100)) + "%" }} /></div>
                    </div>
                    <div className="vs-env-row">
                      <span className="rt-s mono vs-env-v">湿度 {(v.cabin_humidity_pct ?? 50).toFixed(0)}%</span>
                      <div className="dd-bar hum"><i style={{ width: Math.min(100, Math.max(2, v.cabin_humidity_pct ?? 50)) + "%" }} /></div>
                    </div>
                  </div>
                  <div className="vs-cell vs-cell-hb">
                    <span className="rt-l">心跳状态</span>
                    <div className="rt-hb">
                      <HeartIcon size={18} />
                      <b className="rt-v rt-hb-v">{v.last_heartbeat_age_s.toFixed(1)}<i>s</i></b>
                    </div>
                    <span className="rt-s" style={{ color: v.online ? "#22c55e" : "#ef4444" }}>{v.online ? "正常" : "异常"}</span>
                  </div>
                </div>
              </div>
              <ul className="dr2-sys">
                {SYS_LIST.map((s) => (
                  <li key={s}>
                    <i className={"dot" + (v.online ? " on" : "")} />
                    <span>{s}</span>
                    <b className={v.online ? "ok" : "err"}>{v.online ? "正常" : "异常"}</b>
                  </li>
               ))}
             </ul>
           </div>
         </section>
        </div>

       <div className="dr2-bottom">
          <div className="dr2-bl">
          {big !== "video" && (
            <section className="dr2-card dr2-gps dr2-swap" onDoubleClick={openIfPlain(() => setBig("video"))} onContextMenu={onCtxMenu("video")}>
              <div className="dr2-head"><b>实时驾驶画面</b><span className="spacer" /><span className="pd-chip">双击放大</span></div>
              <div className="dr2-b-body"><VideoPanel vehicle={v} /></div>
            </section>
          )}
          {big !== "gps" && (
            <section className="dr2-card dr2-gps dr2-swap" onDoubleClick={openIfPlain(() => setBig("gps"))} onContextMenu={onCtxMenu("gps")}>
              <div className="dr2-head"><b>GPS 定位</b><span className="spacer" /><span className="badge ok">定位正常</span></div>
              <div className="dr2-b-body"><TwinGps vehicle={v} fill /></div>
            </section>
          )}
          {big !== "radar" && (
            <section className="dr2-card dr2-radar dr2-swap" onDoubleClick={openIfPlain(() => setBig("radar"))} onContextMenu={onCtxMenu("pointcloud")}>
              <div className="dr2-head"><b>激光点云</b><span className="spacer" /><span className="badge ok">运行中</span></div>
              <div className="dr2-b-body"><TwinRadar vehicle={v} fill /></div>
            </section>
          )}
          </div>
          <div className="dr2-taskcol">
         <section className="dr2-card dr2-task">
            <div className="dr2-b-body dr2-task-cols">
              <div className="dr2-task-curve">
                <div className="dr2-vs-curve-head">
                  <b>油门 / 刹车曲线</b>
                  <span className="pd-chip">最近 5 分钟</span>
                </div>
                <DualLine a={v.throttle_history || []} b={v.brake_history || []} fill />
              </div>
              <div className="dr2-task-info">
                <div className="dr2-kv"><span>当前模式</span><b><span className="badge info">{v.mode === "autonomous" ? "自动驾驶" : v.mode || "-"}</span></b></div>
                <div className="dr2-kv"><span>任务状态</span><b><span className={"badge " + (activeCount ? "ok" : "dim")}>{activeCount ? "进行中" : "无任务"}</span></b></div>
                <div className="dr2-kv"><span>任务 ID</span><b className="mono">{shown ? shown.id : "-"}</b></div>
                <div className="dr2-kv"><span>开始时间</span><b className="mono">{shown ? fmtDT(shown.created_ns) : "-"}</b></div>
                <div className="dr2-kv"><span>运行时长</span><b className="mono">{shown ? fmtDur(Date.now() - shown.created_ns / 1e6) : "-"}</b></div>
                <div className="dr2-kv"><span>预计里程</span><b className="mono">{shown ? routeKm(shown).toFixed(1) + " km" : "-"}</b></div>
                <div className="ti-list">
                  {myRoutes.length === 0 && <span className="rt-s">暂无任务，可在「循迹导航」下发路线任务</span>}
                  {myRoutes.map((r) => (
                    <button
                      key={r.id}
                      className={"ti-row" + (shown && r.id === shown.id ? " on" : "")}
                      onClick={() => setSelTask(r.id)}
                    >
                      <span className="nm">{r.name || r.id}</span>
                      <span className={"badge " + (["accepted", "running", "completed"].includes(r.status) ? "ok" : ["cancelled", "rejected", "failed", "cancel_rejected"].includes(r.status) ? "dim" : "info")}>
                        {ROUTE_STATUS[r.status] || r.status}
                      </span>
                    </button>
                 ))}
               </div>
              </div>
            </div>
         </section>
          <section className="dr2-card dr2-term" onContextMenu={onCtxMenu("terminal")}>
            <div className="dr2-head"><b>远程终端</b><span className="spacer" /><span className={"badge " + (v.online ? "ok" : "dim")}>{v.online ? "已连接" : "未连接"}</span></div>
            <div className="dr2-b-body"><TerminalPanel vehicle={v} fixed /></div>
          </section>
          </div>
       </div>
      </div>

      {ctx && (
        <>
          <div
            style={{ position: "fixed", inset: 0, zIndex: 89 }}
            onClick={() => setCtx(null)}
            onContextMenu={(e) => {
              e.preventDefault();
              setCtx(null);
            }}
          />
          <div className="dr-ctx-menu" style={{ left: ctx.x, top: ctx.y }}>
            <button onClick={() => { openInNewTab(ctx.kind); setCtx(null); }}>
              在新标签页打开{KIND_LABEL[ctx.kind]}
            </button>
            <button onClick={() => setCtx(null)}>取消</button>
          </div>
        </>
      )}
    </div>
  );
}
