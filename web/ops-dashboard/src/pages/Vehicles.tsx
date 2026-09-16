import { useEffect, useState } from "react";
import type { FleetState } from "../api";
import { fetchGroupNames, registerVehicle, uploadVehicleModel } from "../api";
import Modal from "../components/Modal";
import VehicleTable from "../components/VehicleTable";
import { VideoConfPanel } from "../components/VideoConf";
import type { Me } from "../types";

interface Props {
  fleet: FleetState;
  me: Me;
  onSelect: (id: string, goDetail?: boolean) => void;
}

const CHASSIS_OPTIONS = ["四轮四转", "阿克曼", "差速AGV"];

export default function Vehicles({ fleet, me, onSelect }: Props) {
  const isAdmin = me.role !== "user";
  const [regOpen, setRegOpen] = useState(false);
  const [cfgId, setCfgId] = useState("");
  const [cfgTab, setCfgTab] = useState<"twin" | "video">("twin");
  const [groups, setGroups] = useState<{ id: string; name: string }[]>([]);
  const [fid, setFid] = useState("");
  const [fvin, setFvin] = useState("");
  const [fgw, setFgw] = useState("");
  const [fchassis, setFchassis] = useState(CHASSIS_OPTIONS[1]);
  const [fgroup, setFgroup] = useState("");
  const [fsync, setFsync] = useState(true);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const [modelFile, setModelFile] = useState<File | null>(null);

  useEffect(() => {
    if (regOpen && me.role === "super") {
      void fetchGroupNames()
        .then((r) => setGroups(r.groups))
        .catch(() => setGroups([]));
    }
  }, [regOpen, me.role]);

  const myGroups = me.role === "super" ? groups : me.groups.map((g) => ({ id: g, name: g }));

  const submitReg = async () => {
    setBusy(true);
    setMsg("");
    try {
      await registerVehicle({
        id: fid.trim(),
        vin: fvin.trim(),
        gateway_id: fgw.trim(),
        chassis: fchassis,
        group: fgroup,
        sync_pointcloud: fsync,
      });
      setRegOpen(false);
      setFid("");
      setFvin("");
    } catch (e) {
      setMsg("注册失败：" + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const submitModel = async () => {
    if (!modelFile) return;
    setBusy(true);
    setMsg("");
    try {
      await uploadVehicleModel(cfgId, modelFile);
      setCfgId("");
      setModelFile(null);
    } catch (e) {
      setMsg("上传失败：" + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="panel" style={{ height: "100%", display: "flex", flexDirection: "column" }}>
      <div className="panel-title">
        <span>车辆列表</span>
        <span className="hint">点击行 → 新标签页打开数字孪生详情</span>
        <span className="spacer" />
        {isAdmin && (
          <button
            className="btn small primary"
            onClick={() => {
              setMsg("");
              setRegOpen(true);
            }}
          >
            注册车辆
          </button>
        )}
      </div>
      <VehicleTable
        snap={fleet.snap}
        onSelect={onSelect}
        onConfig={
          isAdmin
            ? (id) => {
                setModelFile(null);
                setMsg("");
                setCfgTab("twin");
                setCfgId(id);
              }
            : undefined
        }
      />

      {regOpen && (
        <Modal title="注册车辆" onClose={() => setRegOpen(false)} width="min(560px, 94vw)">
          <div className="form-grid">
            <label>
              车辆 ID *
              <input className="input" value={fid} onChange={(e) => setFid(e.target.value)} placeholder="如 vehicle-001" />
            </label>
            <label>
              VIN
              <input className="input" value={fvin} onChange={(e) => setFvin(e.target.value)} />
            </label>
            <label>
              网关 ID
              <input className="input" value={fgw} onChange={(e) => setFgw(e.target.value)} placeholder="gw-local-001" />
            </label>
            <label>
              底盘类型 *
              <select className="input" value={fchassis} onChange={(e) => setFchassis(e.target.value)}>
                {CHASSIS_OPTIONS.map((c) => (
                  <option key={c}>{c}</option>
                ))}
              </select>
            </label>
            <label>
              归属分组
              <select className="input" value={fgroup} onChange={(e) => setFgroup(e.target.value)}>
                <option value="">不挂分组</option>
                {myGroups.map((g) => (
                  <option key={g.id} value={g.id}>
                    {g.name}
                  </option>
                ))}
              </select>
            </label>
            <label className="check">
              <input type="checkbox" checked={fsync} onChange={(e) => setFsync(e.target.checked)} />
              启用点云图同步（车端建图后自动上报地图中心）
            </label>
          </div>
          {msg && <div className="msg err">{msg}</div>}
          <div className="btn-row" style={{ justifyContent: "flex-end", marginTop: 12 }}>
            <button className="btn small" onClick={() => setRegOpen(false)}>
              取消
            </button>
            <button className="btn small primary" disabled={busy || !fid.trim()} onClick={() => void submitReg()}>
              注册
            </button>
          </div>
        </Modal>
      )}

      {cfgId && (
        <Modal title={"车辆配置 · " + cfgId} onClose={() => setCfgId("")} width="min(860px, 94vw)">
          <div className="vc-tabs" style={{ marginBottom: 12 }}>
            <button className={"vc-tab" + (cfgTab === "twin" ? " on" : "")} onClick={() => setCfgTab("twin")}>孪生模型</button>
            <button className={"vc-tab" + (cfgTab === "video" ? " on" : "")} onClick={() => setCfgTab("video")}>视频配置与标定</button>
          </div>
          {cfgTab === "twin" && (
            <>
              <div className="muted" style={{ marginBottom: 10 }}>
                上传该车辆的真实 GLB 模型；未上传时数字孪生页明确显示不可用，不使用平台默认模型。
              </div>
              <input
                type="file"
                accept=".glb"
                onChange={(e) => setModelFile(e.target.files && e.target.files[0] ? e.target.files[0] : null)}
              />
              {msg && <div className="msg err">{msg}</div>}
              <div className="btn-row" style={{ justifyContent: "flex-end", marginTop: 12 }}>
                <button className="btn small" onClick={() => setCfgId("")}>
                  取消
                </button>
                <button className="btn small primary" disabled={busy || !modelFile} onClick={() => void submitModel()}>
                  上传
                </button>
              </div>
            </>
          )}
          {cfgTab === "video" && (
            <VideoConfPanel vehicle={fleet.snap.vehicles.find((v) => v.vehicle_id === cfgId) || null} />
          )}
        </Modal>
      )}
    </div>
  );
}
