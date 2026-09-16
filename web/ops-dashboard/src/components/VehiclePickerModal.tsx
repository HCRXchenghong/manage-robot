// VehiclePickerModal：点击设备「使用」后弹出的可接管车辆列表。
// 规则：一个账号一次只能接管一辆车（前端禁用 + 后端 409 双保险）；
// 已被其他账号接管或离线的车辆不可选。
import { useCallback, useEffect, useState } from "react";
import Modal from "./Modal";
import type { FleetState } from "../api";
import { takeoverActiveList, takeoverMine, takeoverReleaseMine, takeoverTake } from "../api";
import type { ActiveTakeover, DeviceInfo } from "../types";
import { vehicleStatusOf } from "./VehicleTable";
import { DEVICE_TYPE_LABEL } from "../peripherals";

const STATUS_LABEL: Record<string, string> = {
  driving: "行驶中",
  idle: "空闲",
  offline: "离线",
  alert: "告警",
};
const STATUS_BADGE: Record<string, string> = {
  driving: "ok",
  idle: "info",
  offline: "dim",
  alert: "warn",
};

interface Props {
  fleet: FleetState;
  device: DeviceInfo;
  onClose: () => void;
  onTaken: (vehicleId: string) => void;
}

export default function VehiclePickerModal({ fleet, device, onClose, onTaken }: Props) {
  const [taken, setTaken] = useState<ActiveTakeover[]>([]);
  const [mine, setMine] = useState<ActiveTakeover | null>(null);
  const [busy, setBusy] = useState("");
  const [msg, setMsg] = useState("");

  const reload = useCallback(() => {
    void takeoverActiveList().then(setTaken);
    void takeoverMine().then(setMine);
  }, []);

  useEffect(() => {
    reload();
    const t = window.setInterval(reload, 5000); // 列表 5s 刷新，避免他人刚接管又被重复申请
    return () => window.clearInterval(t);
  }, [reload]);

  const doTake = async (vid: string) => {
    setBusy(vid);
    setMsg("");
    const resp = await takeoverTake(vid, device.id);
    setBusy("");
    if (resp && resp.ok && resp.takeover) {
      onTaken(vid);
      return;
    }
    setMsg(String(resp?.error || "接管失败，请重试"));
    reload();
  };

  const doRelease = async () => {
    setBusy("__release__");
    setMsg("");
    try {
      await takeoverReleaseMine();
    } catch (e) {
      setMsg("交还失败：" + String(e));
    }
    setBusy("");
    reload();
  };

  const takenOf = (vid: string) => taken.find((t) => t.vehicle_id === vid) || null;

  return (
    <Modal title={"选择接管车辆 · 控制设备：" + device.name} onClose={onClose} width="min(860px, 94vw)">
      <div className="vpm-tip">
        <span className="badge info">{DEVICE_TYPE_LABEL[device.type] || device.type}</span>
        <span className="muted">
          规则：一个账号一次只能接管一辆车；离线的、已被其他账号接管的车辆不可选。
        </span>
      </div>
      {mine && (
        <div className="vpm-mine">
          <span>
            你当前已接管 <b className="mono">{mine.vehicle_id}</b>（租约 {mine.lease_id}）
          </span>
          <span className="spacer" />
          <button className="btn small primary" onClick={() => onTaken(mine.vehicle_id)}>
            继续驾驶
          </button>
          <button className="btn small danger" disabled={busy === "__release__"} onClick={() => void doRelease()}>
            交还控制权
          </button>
        </div>
      )}
      <table className="tbl vpm-table">
        <thead>
          <tr>
            <th>车辆</th>
            <th>分组</th>
            <th>状态</th>
            <th>模式</th>
            <th>电量</th>
            <th>接管占用</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          {fleet.snap.vehicles.map((v) => {
            const st = vehicleStatusOf(v);
            const tk = takenOf(v.vehicle_id);
            const byMe = mine != null && mine.vehicle_id === v.vehicle_id;
            const disabled =
              busy !== "" || !v.online || byMe === false && (mine != null || (tk != null && tk.driver !== ""));
            return (
              <tr key={v.vehicle_id}>
                <td className="mono">{v.vehicle_id}</td>
                <td>{v.group || "-"}</td>
                <td><span className={"badge " + (STATUS_BADGE[st] || "dim")}>{STATUS_LABEL[st] || st}</span></td>
                <td>{v.mode === "autonomous" ? "自动驾驶" : v.mode || "-"}</td>
                <td className="mono">{Math.round(v.soc * 100)}%</td>
                <td>
                  {byMe ? (
                    <span className="badge ok">你已接管</span>
                  ) : tk ? (
                    <span className="badge warn">{tk.driver} 接管中</span>
                  ) : (
                    <span className="badge dim">空闲</span>
                  )}
                </td>
                <td>
                  {byMe ? (
                    <button className="btn small primary" onClick={() => onTaken(v.vehicle_id)}>
                      继续驾驶
                    </button>
                  ) : (
                    <button
                      className="btn small primary"
                      disabled={disabled}
                      title={
                        !v.online
                          ? "车辆离线"
                          : tk
                          ? "已被 " + tk.driver + " 接管"
                          : mine
                          ? "你已接管其他车辆，请先交还"
                          : ""
                      }
                      onClick={() => void doTake(v.vehicle_id)}
                    >
                      {busy === v.vehicle_id ? "接管中…" : "接管"}
                    </button>
                  )}
                </td>
              </tr>
            );
          })}
          {fleet.snap.vehicles.length === 0 && (
            <tr>
              <td colSpan={7} className="muted" style={{ textAlign: "center", padding: 18 }}>
                当前分组暂无可见车辆
              </td>
            </tr>
          )}
        </tbody>
      </table>
      {msg && <div className="vpm-err">{msg}</div>}
    </Modal>
  );
}

