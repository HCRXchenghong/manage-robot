import { useCallback, useEffect, useState } from "react";
import type { FleetState } from "../api";
import {
  emergencyStop,
  fetchDevices,
  takeoverActiveList,
  takeoverMine,
  takeoverReleaseMine,
  takeoverTake,
} from "../api";
import type { ActiveTakeover, DeviceInfo, VehicleSnap } from "../types";
import CollapsePanel from "../components/CollapsePanel";
import Modal from "../components/Modal";
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

// 远程接管快捷弹窗：接账号级接管新架构（绑定设备 → takeoverTake），
// 已绑定设备可一键接管本车并跳驾驶页；未绑定则引导去「远程接管」页绑定。
function TakeoverQuickModal({ vehicleId, onClose }: { vehicleId: string; onClose: () => void }) {
  const [devices, setDevices] = useState<DeviceInfo[]>([]);
  const [mine, setMine] = useState<ActiveTakeover | null>(null);
  const [active, setActive] = useState<ActiveTakeover[]>([]);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  const reload = useCallback(() => {
    void fetchDevices().then(setDevices).catch(() => setDevices([]));
    void takeoverMine().then(setMine);
    void takeoverActiveList().then(setActive);
  }, []);
  useEffect(() => {
    reload();
    const t = window.setInterval(reload, 5000);
    return () => window.clearInterval(t);
  }, [reload]);

  const occ = active.find((a) => a.vehicle_id === vehicleId) || null;
  const byMe = mine != null && mine.vehicle_id === vehicleId;
  const device = devices[0] || null;

  const doTake = async () => {
    if (!device) return;
    setBusy(true);
    setMsg("");
    const r = await takeoverTake(vehicleId, device.id);
    setBusy(false);
    if (r && r.ok) {
      onClose();
      window.location.hash = "#/drive";
      return;
    }
    setMsg(String(r?.error || "接管失败，请重试"));
    reload();
  };

  const doRelease = async () => {
    setBusy(true);
    await takeoverReleaseMine().catch(() => undefined);
    setBusy(false);
    reload();
  };

  return (
    <Modal title={"远程接管 · " + vehicleId} onClose={onClose} width="min(520px, 92vw)">
      <div className="kv-list">
        <div className="kv">
          <span>接管占用</span>
          <b>{byMe ? "你已接管" : occ ? (occ.driver || "他人") + " 接管中" : "空闲"}</b>
        </div>
        <div className="kv">
          <span>控制设备</span>
          <b>{device ? device.name + "（" + device.type + "）" : "未绑定"}</b>
        </div>
        {mine && (
          <div className="kv">
            <span>你的租约</span>
            <b className="mono">{mine.vehicle_id} · {mine.lease_id}</b>
          </div>
        )}
      </div>
      <div className="btn-row mt">
        {byMe ? (
          <>
            <button
              className="btn small primary"
              onClick={() => {
                onClose();
                window.location.hash = "#/drive";
              }}
            >
              继续驾驶
            </button>
            <button className="btn small danger" disabled={busy} onClick={() => void doRelease()}>
              交还控制权
            </button>
          </>
        ) : device ? (
          <button
            className="btn small primary"
            disabled={busy || !!occ || (mine != null && mine.vehicle_id !== vehicleId)}
            onClick={() => void doTake()}
          >
            {busy ? "接管中…" : "接管该车"}
          </button>
        ) : (
          <button
            className="btn small primary"
            onClick={() => {
              onClose();
              window.location.hash = "#/drive";
            }}
          >
            去绑定控制设备
          </button>
        )}
      </div>
      {msg && <div className="msg err">{msg}</div>}
      <div className="muted mt" style={{ fontSize: 11 }}>
        一个账号一次只能接管一辆车；离线或已被他人接管的车辆不可选。
      </div>
    </Modal>
  );
}

// 弹窗大仪表盘：状态徽章 + 急停/远程接管按钮 + 车速/电量表盘 + 大挡位 + 踏板 + 温湿度条 + 加速度曲线 + 实时信号表
export function VehicleDashboard({ v }: { v: VehicleSnap }) {
  const [armEstop, setArmEstop] = useState(false);
  const [msg, setMsg] = useState("");
  const [tkOpen, setTkOpen] = useState(false);

  // 急停两步确认：第一次点亮 3 秒窗口，第二次真正下发（防误触）
  const onEstop = () => {
    if (!armEstop) {
      setArmEstop(true);
      window.setTimeout(() => setArmEstop(false), 3000);
      return;
    }
    setArmEstop(false);
    emergencyStop()
      .then((r) => setMsg("紧急停车 " + (r.ok ? "已下发并被接受" : "失败：" + String(r.error || r.ack || ""))))
      .catch((e) => setMsg("紧急停车失败：" + String(e)));
  };

  return (
    <div className="dash">
      <div className="dash-chips">
        <span className={"chip " + (v.online ? "ok" : "err")}>{v.online ? "在线" : "离线"}</span>
        <span className="chip">{MODE_LABEL[v.mode] || v.mode || "-"}</span>
        <span className="chip mono">位姿 x={v.pose.x.toFixed(1)} y={v.pose.y.toFixed(1)} yaw={v.pose.yaw.toFixed(2)}</span>
        <span className="dash-actions">
          <button className={"btn small danger" + (armEstop ? " estop-arm" : "")} onClick={onEstop}>
            {armEstop ? "再次点击确认急停！" : "急停"}
          </button>
          <button className="btn small primary" onClick={() => setTkOpen(true)}>远程接管</button>
        </span>
      </div>
      {msg && <div className="muted" style={{ fontSize: 11, marginTop: -6 }}>{msg}</div>}
      <div className="dash-gauges">
        <DialGauge value={v.speed_mps * 3.6} max={60} unit="km/h" label="车速" color="#38bdf8" sub={v.speed_mps.toFixed(2) + " m/s"} />
        <DialGauge value={v.soc * 100} max={100} unit="%" label="电量" color="#22c55e" sub={v.voltage.toFixed(1) + " V"} />
        <div className="gear-big">
          <div className="gear-big-val">{v.gear || "D"}</div>
          <div className="gear-big-label">挡位</div>
        </div>
        <PedalBars throttle={v.throttle_pct || 0} brake={v.brake_pct || 0} />
        <div className="dash-bars">
          <BarGauge label="机内温度" value={v.cabin_temp_c ?? 26} min={0} max={50} unit="°C" color="#f59e0b" />
          <BarGauge label="机内湿度" value={v.cabin_humidity_pct ?? 45} min={0} max={100} unit="%" color="#38bdf8" digits={0} />
        </div>
      </div>
      <AccelChart hist={v.accel_history || []} />
      <SignalTable v={v} showSource />
      {tkOpen && <TakeoverQuickModal vehicleId={v.vehicle_id} onClose={() => setTkOpen(false)} />}
    </div>
  );
}
