// 地图中心：每个车一张卡片；车端每 15s 实时上报（服务端版本化存储），
// 支持手动导入、一行命令 3D→2D、进入独立编辑页。轮询 3s 保持近实时。
import { useCallback, useEffect, useRef, useState } from "react";
import type { FleetState } from "../api";
import { convertMap, fetchMaps, mapFileURL, uploadMap } from "../api";
import Modal from "../components/Modal";
import type { MapEntry } from "../types";

interface Props {
  fleet: FleetState;
  onEdit: (mapId: string) => void;
}

function fmtSize(n: number): string {
  if (n >= 1048576) return (n / 1048576).toFixed(1) + " MB";
  if (n >= 1024) return (n / 1024).toFixed(1) + " KB";
  return n + " B";
}

function fmtTime(ns: number): string {
  if (!ns) return "-";
  const d = new Date(ns / 1e6);
  const p = (x: number) => (x < 10 ? "0" + x : String(x));
  return (
    p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " +
    p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds())
  );
}

function kindLabel(k: string): string {
  if (k === "3d_pcd") return "3D · PCD";
  if (k === "3d_csv") return "3D · CSV";
  if (k === "2d_png") return "2D · 栅格";
  return k;
}

function sourceLabel(s: string): string {
  if (s === "vehicle_push") return "车端实时上报";
  if (s === "manual") return "手动导入";
  return s;
}

export default function Maps({ fleet, onEdit }: Props) {
  const [maps, setMaps] = useState<MapEntry[]>([]);
  const [uploadVid, setUploadVid] = useState("sim-veh-001");
  const [busy, setBusy] = useState("");
  const [msg, setMsg] = useState("");
  const [importOpen, setImportOpen] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const reload = useCallback(async () => {
    try {
      setMaps(await fetchMaps());
    } catch {
      /* 后端未就绪时保持空列表 */
    }
  }, []);

  useEffect(() => {
    void reload();
    const t = window.setInterval(() => void reload(), 3000); // 车端 15s 上报，3s 轮询即近实时
    return () => window.clearInterval(t);
  }, [reload]);

  useEffect(() => {
    const v = fleet.snap.vehicles[0];
    if (v && !fleet.snap.vehicles.some((x) => x.vehicle_id === uploadVid)) {
      setUploadVid(v.vehicle_id);
    }
  }, [fleet.snap.vehicles, uploadVid]);

  const doUpload = async (f: File | null) => {
    if (!f) return;
    setMsg("正在导入 " + f.name + " …");
    try {
      const r = await uploadMap(uploadVid, f, "manual");
      setMsg(r.changed ? "已导入：" + f.name : "内容未变化，已去重（不产生新版本）");
      setImportOpen(false);
    } catch (e) {
      setMsg("导入失败：" + String(e));
    }
    if (fileRef.current) fileRef.current.value = "";
    void reload();
  };

  const doConvert = async (id: string) => {
    setBusy(id);
    setMsg("");
    try {
      await convertMap(id);
      setMsg("3D→2D 完成，已生成新版本（PNG/PGM/YAML）");
    } catch (e) {
      setMsg("转换失败：" + String(e));
    }
    setBusy("");
    void reload();
  };

  return (
    <div style={{ padding: 16, overflow: "auto", height: "100%" }}>
      <div className="row" style={{ justifyContent: "space-between", marginBottom: 12 }}>
        <div>
          <div style={{ fontSize: 16, fontWeight: 700 }}>地图中心</div>
          <div className="muted">
            每车一张卡片：车端地图实时上报到云端（服务器版本化存储 + 车上原件不动），一行命令 3D→2D，编辑进独立页面
          </div>
        </div>
        <div className="btn-row">
          <button className="btn primary" onClick={() => setImportOpen(true)}>导入地图</button>
          <button className="btn" onClick={() => void reload()}>刷新</button>
        </div>
      </div>
      {msg && <div className="notice">{msg}</div>}
      {maps.length === 0 ? (
        <div className="panel" style={{ padding: 40, textAlign: "center" }}>
          <div style={{ fontSize: 14, marginBottom: 6 }}>还没有地图</div>
          <div className="muted">车端启动后会自动上报（默认 /tmp/ra-ndt-map.csv），也可以右上角手动导入 .pcd / .csv / .png</div>
        </div>
      ) : (
        <div className="map-cards">
          {maps.map((m) => {
            const latest = m.versions.length ? m.versions[m.versions.length - 1] : null;
            const pngFile = latest ? latest.files.find((f) => f.endsWith(".png")) : undefined;
            return (
              <div className="panel map-card" key={m.id}>
                <div className="row" style={{ justifyContent: "space-between" }}>
                  <div>
                    <div style={{ fontWeight: 700 }}>{m.name}</div>
                    <div className="muted">{m.vehicle_id}</div>
                  </div>
                  <span className={"badge kind-" + (m.latest_kind || m.kind)}>{kindLabel(m.latest_kind || m.kind)}</span>
                </div>
                {pngFile && latest ? (
                  <img className="map-thumb" src={mapFileURL(m.id, pngFile, latest.version)} alt="地图缩略图" />
                ) : (
                  <div className="map-thumb placeholder">3D 点云地图，点「3D→2D」生成栅格缩略图</div>
                )}
                <div className="map-meta">
                  <span>{sourceLabel(m.source)}</span>
                  <span>{m.versions.length} 个版本</span>
                  <span>{fmtSize(m.size)}</span>
                  <span>更新 {fmtTime(m.updated_ns)}</span>
                </div>
                <div className="btn-row">
                  <button
                    className="btn small primary"
                    disabled={!m.has_2d}
                    title={m.has_2d ? "进入独立编辑页" : "先执行 3D→2D 生成可编辑的 2D 地图"}
                    onClick={() => onEdit(m.id)}
                  >编辑</button>
                  <button
                    className="btn small"
                    disabled={busy === m.id || (m.latest_kind || m.kind) === "2d_png"}
                    onClick={() => void doConvert(m.id)}
                  >{busy === m.id ? "转换中…" : "3D→2D"}</button>
                  {pngFile && latest && (
                    <a className="btn small" href={mapFileURL(m.id, pngFile, latest.version)} target="_blank" rel="noreferrer">预览</a>
                  )}
                </div>
                <details className="map-versions">
                  <summary>版本历史（原始只读，编辑/转换都是新版本）</summary>
                  {m.versions.slice().reverse().map((v) => (
                    <div className="ver-row" key={v.version}>
                      <span className="mono">v{v.version}</span>
                      <span className="muted" style={{ flex: 1 }}>{v.note}</span>
                      <span className="muted mono">{fmtTime(v.created_ns)}</span>
                    </div>
                  ))}
                </details>
              </div>
            );
          })}
        </div>
      )}

      {importOpen && (
        <Modal title="导入地图" onClose={() => setImportOpen(false)} width="min(480px, 92vw)">
          <div className="row" style={{ marginBottom: 10 }}>
            <span className="muted" style={{ width: 60 }}>车辆</span>
            <select className="input" value={uploadVid} onChange={(e) => setUploadVid(e.target.value)}>
              {fleet.snap.vehicles.map((v) => (
                <option key={v.vehicle_id} value={v.vehicle_id}>{v.vehicle_id}</option>
              ))}
            </select>
          </div>
          <input
            ref={fileRef} type="file" accept=".pcd,.csv,.png,.pgm"
            onChange={(e) => void doUpload(e.target.files ? e.target.files[0] : null)}
          />
          <div className="muted mt" style={{ fontSize: 11 }}>
            支持 ROS1/ROS2/Autoware/Apollo 的 .pcd / .csv 点云与 .png / .pgm 栅格；内容相同自动去重。
          </div>
        </Modal>
      )}
    </div>
  );
}
