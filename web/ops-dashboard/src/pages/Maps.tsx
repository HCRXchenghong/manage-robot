// 地图中心：每个车一张卡片；车端每 15s 实时上报（服务端版本化存储），
// 支持手动导入、一行命令 3D→2D、进入独立编辑页。轮询 3s 保持近实时。
// 三维/二维统一：每张卡片同时提供 3D 点云查看（PCD/CSV，服务端解析）与
// 2D 栅格查看（PNG），以及删除（含全部版本，车上原件不动）。
import { useCallback, useEffect, useRef, useState } from "react";
import type { FleetState } from "../api";
import {
  approveMapPublication, convertMap, deleteMap, fetchGroupNames, fetchMapPublications, fetchMaps,
  mapFileURL, mapHas3D, mapLatestPng, requestMapPublication, rollbackMapPublication, uploadMap,
} from "../api";
import Modal from "../components/Modal";
import PcdViewer from "../components/PcdViewer";
import type { MapEntry, MapPublication, Me } from "../types";

interface Props {
  fleet: FleetState;
  me: Me;
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

function publicationStateLabel(state: string): string {
  const labels: Record<string, string> = {
    requested: "待审批", approved: "已审批·待下行", dispatched: "Broker 已接收·待车端",
    confirmed: "车端已确认", active: "车端已生效", superseded: "已被新版本替代",
    rolled_back: "已回滚", rejected: "车端拒绝",
  };
  return labels[state] || state;
}

export default function Maps({ fleet, me, onEdit }: Props) {
  const [maps, setMaps] = useState<MapEntry[]>([]);
  const [publications, setPublications] = useState<MapPublication[]>([]);
  const [groupNames, setGroupNames] = useState<Record<string, string>>({});
  const [uploadVid, setUploadVid] = useState("");
  const [busy, setBusy] = useState("");
  const [delBusy, setDelBusy] = useState("");
  const [msg, setMsg] = useState("");
  const [importOpen, setImportOpen] = useState(false);
  const [view3d, setView3d] = useState<MapEntry | null>(null);
  const [view2d, setView2d] = useState<MapEntry | null>(null);
  const [confirmDel, setConfirmDel] = useState<MapEntry | null>(null);
  const [publishMap, setPublishMap] = useState<MapEntry | null>(null);
  const [publishVersion, setPublishVersion] = useState(0);
  const [publishFrame, setPublishFrame] = useState("");
  const fileRef = useRef<HTMLInputElement>(null);

  const reload = useCallback(async () => {
    try { setMaps(await fetchMaps()); } catch { /* 后端未就绪时保持空列表 */ }
    try { setPublications(await fetchMapPublications()); } catch { setPublications([]); }
  }, []);

  useEffect(() => {
    void reload();
    const t = window.setInterval(() => void reload(), 3000); // 车端 15s 上报，3s 轮询即近实时
    return () => window.clearInterval(t);
  }, [reload]);

  useEffect(() => {
    void fetchGroupNames()
      .then((r) => {
        const m: Record<string, string> = {};
        for (const g of r.groups) m[g.id] = g.name;
        setGroupNames(m);
      })
      .catch(() => setGroupNames({}));
  }, []);

  useEffect(() => {
    const v = fleet.snap.vehicles[0];
    if (v && !fleet.snap.vehicles.some((x) => x.vehicle_id === uploadVid)) {
      setUploadVid(v.vehicle_id);
    }
  }, [fleet.snap.vehicles, uploadVid]);

  const doUpload = async (f: File | null) => {
    if (!f) return;
    if (!uploadVid) {
      setMsg("请先选择已登记车辆，再上传真实地图。");
      return;
    }
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

  const doDelete = async (m: MapEntry) => {
    setDelBusy(m.id);
    try {
      await deleteMap(m.id);
      setMsg("已删除地图：" + m.name + "（服务器各版本文件已清除，车上原件不动）");
      setConfirmDel(null);
    } catch (e) {
      setMsg("删除失败：" + String(e));
    }
    setDelBusy("");
    void reload();
  };

  const openPublish = (m: MapEntry) => {
    setPublishMap(m);
    setPublishVersion(m.versions.length ? m.versions[m.versions.length - 1].version : 0);
    setPublishFrame("");
  };

  const doPublish = async () => {
    if (!publishMap || !publishFrame.trim()) {
      setMsg("必须填写真实坐标系；未知坐标系的地图不得进入车端发布流程。");
      return;
    }
    setBusy("publish-" + publishMap.id);
    try {
      await requestMapPublication(publishMap.id, publishMap.vehicle_id, publishVersion, publishFrame.trim());
      setMsg("已创建地图发布审批，审批和真实车端 APPLIED ACK 完成前不会生效。");
      setPublishMap(null);
      await reload();
    } catch (e) { setMsg("创建发布失败：" + String(e)); }
    setBusy("");
  };

  const doApprove = async (publication: MapPublication) => {
    setBusy(publication.id);
    try {
      await approveMapPublication(publication.id);
      setMsg("发布已审批并进入可靠下行队列；Broker 接收不等于车端生效。");
      await reload();
    } catch (e) { setMsg("审批失败：" + String(e)); }
    setBusy("");
  };

  const doRollback = async (publication: MapPublication) => {
    setBusy(publication.id);
    try {
      await rollbackMapPublication(publication.id);
      setMsg("已创建回滚审批；必须审批并收到目标版本的真实 APPLIED ACK。");
      await reload();
    } catch (e) { setMsg("创建回滚失败：" + String(e)); }
    setBusy("");
  };

  return (
    <div style={{ padding: 16, overflow: "auto", height: "100%" }}>
      <div className="row" style={{ justifyContent: "space-between", marginBottom: 12 }}>
        <div>
          <div style={{ fontSize: 16, fontWeight: 700 }}>地图中心</div>
          <div className="muted">
            每车一张卡片：车端地图实时上报到云端（服务器版本化存储 + 车上原件不动）；
            三维（.pcd/.csv 点云）/二维（.png/.pgm 栅格）统一查看，一行命令 3D→2D，编辑进独立页面
            {me.role === "super" ? " · 超管可见全部分组地图" : " · 仅显示本分组地图"}
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
          <div className="muted">
            车端启动后会自动上报（默认 /tmp/ra-ndt-map.csv），也可以右上角手动导入
            .pcd / .csv（3D 点云）或 .png / .pgm（2D 栅格）
          </div>
        </div>
      ) : (
        <div className="map-cards">
          {maps.map((m) => {
            const latest = m.versions.length ? m.versions[m.versions.length - 1] : null;
            const png = mapLatestPng(m);
            const has3d = mapHas3D(m);
            const vehGroup =
              fleet.snap.vehicles.find((v) => v.vehicle_id === m.vehicle_id)?.group || "";
            const groupName = vehGroup ? groupNames[vehGroup] || vehGroup : "未挂分组";
            const uploader =
              latest && latest.author ? latest.author : m.source === "vehicle_push" ? m.vehicle_id : "平台上传";
            return (
              <div className="panel map-card" key={m.id}>
                <div className="row" style={{ justifyContent: "space-between" }}>
                  <div>
                    <div style={{ fontWeight: 700 }}>{m.name}</div>
                    <div className="muted">{m.vehicle_id}</div>
                    <div className="map-attrib">
                      <span className="badge info">{groupName}</span>
                      <span className="badge dim">
                        {m.source === "vehicle_push" ? "车辆同步 · " + m.vehicle_id : "上传 · " + uploader}
                      </span>
                    </div>
                  </div>
                  <span className={"badge kind-" + (m.latest_kind || m.kind)}>{kindLabel(m.latest_kind || m.kind)}</span>
                </div>
                {png ? (
                  <img
                    className="map-thumb clickable"
                    src={mapFileURL(m.id, png.name, png.version)}
                    alt="地图缩略图"
                    title="点击查看 2D 栅格"
                    onClick={() => setView2d(m)}
                  />
                ) : has3d ? (
                  <div
                    className="map-thumb placeholder clickable"
                    title="点击查看 3D 点云"
                    onClick={() => setView3d(m)}
                  >
                    3D 点云地图（{(m.latest_kind || m.kind) === "3d_csv" ? "CSV" : "PCD"}）· 点击查看三维
                  </div>
                ) : (
                  <div className="map-thumb placeholder">无 3D 点云</div>
                )}
                <div className="map-meta">
                  <span>{sourceLabel(m.source)}</span>
                  <span>{m.versions.length} 个版本</span>
                  <span>{fmtSize(m.size)}</span>
                  <span>更新 {fmtTime(m.updated_ns)}</span>
                </div>
                <div className="btn-row">
                  <button
                    className="btn small"
                    disabled={!has3d}
                    title={has3d ? "三维点云查看（.pcd/.csv）" : "该地图没有 3D 点云版本"}
                    onClick={() => setView3d(m)}
                  >3D</button>
                  <button
                    className="btn small"
                    disabled={!png}
                    title={png ? "二维栅格查看（.png）" : "先执行 3D→2D 生成 2D 栅格"}
                    onClick={() => setView2d(m)}
                  >2D</button>
                  <button
                    className="btn small primary"
                    disabled={!m.has_2d}
                    title={m.has_2d ? "进入独立编辑页" : "先执行 3D→2D 生成可编辑的 2D 地图"}
                    onClick={() => onEdit(m.id)}
                  >编辑</button>
                  <button
                    className="btn small"
                    disabled={busy === m.id || (m.latest_kind || m.kind) === "2d_png" || !has3d}
                    onClick={() => void doConvert(m.id)}
                  >{busy === m.id ? "转换中…" : "3D→2D"}</button>
                  <button className="btn small primary" onClick={() => openPublish(m)}>申请发布</button>
                  <button
                    className="btn small danger"
                    disabled={delBusy === m.id}
                    title="删除地图（含全部版本，车上原件不动）"
                    onClick={() => setConfirmDel(m)}
                  >删除</button>
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

      <div className="panel" style={{ marginTop: 14, padding: 14 }}>
        <div style={{ fontWeight: 700, marginBottom: 6 }}>地图发布记录</div>
        <div className="muted" style={{ fontSize: 12, marginBottom: 10 }}>
          状态来自 PostgreSQL 发布记录和车端 ACK；“Broker 已接收”不代表车辆已安装。
        </div>
        {publications.length === 0 ? <div className="muted">暂无发布记录。</div> : (
          <div style={{ display: "grid", gap: 8 }}>
            {publications.map((p) => (
              <div key={p.id} className="ver-row" style={{ alignItems: "center", flexWrap: "wrap" }}>
                <span className="mono">{p.vehicle_id} · v{p.version}</span>
                <span className="badge info">{publicationStateLabel(p.state)}</span>
                <span className="muted">{p.action === "rollback" ? "回滚" : "发布"} · {p.requested_by}</span>
                <span className="muted mono" style={{ marginLeft: "auto" }}>{fmtTime(p.updated_ns)}</span>
                {p.state === "requested" && p.requested_by !== me.username && (
                  <button className="btn small" disabled={busy === p.id} onClick={() => void doApprove(p)}>审批</button>
                )}
                {p.state === "active" && (
                  <button className="btn small danger" disabled={busy === p.id} onClick={() => void doRollback(p)}>申请回滚</button>
                )}
                {p.vehicle_ack_detail && <span className="muted" style={{ width: "100%" }}>车端：{p.vehicle_ack_detail}</span>}
              </div>
            ))}
          </div>
        )}
      </div>

      {publishMap && (
        <Modal title={publishMap.name + " · 申请车端发布"} onClose={() => setPublishMap(null)} width="min(500px, 92vw)">
          <div className="muted" style={{ lineHeight: 1.7, marginBottom: 12 }}>
            目标车辆：{publishMap.vehicle_id}。发布只提交不可变版本元数据，车辆必须通过能力准入并返回真实 ACK。
          </div>
          <label className="muted">版本</label>
          <select className="input" value={publishVersion} onChange={(e) => setPublishVersion(Number(e.target.value))}>
            {publishMap.versions.slice().reverse().map((v) => <option key={v.version} value={v.version}>v{v.version} · {v.kind}</option>)}
          </select>
          <label className="muted" style={{ display: "block", marginTop: 10 }}>坐标系（必填）</label>
          <input className="input" value={publishFrame} onChange={(e) => setPublishFrame(e.target.value)} placeholder="填写车端已声明的坐标系，例如 map" />
          <div className="row" style={{ justifyContent: "flex-end", marginTop: 14 }}>
            <button className="btn" onClick={() => setPublishMap(null)}>取消</button>
            <button className="btn primary" disabled={busy === "publish-" + publishMap.id} onClick={() => void doPublish()}>
              {busy === "publish-" + publishMap.id ? "提交中…" : "提交审批"}
            </button>
          </div>
        </Modal>
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
            支持 ROS1/ROS2/Autoware/Apollo 的 .pcd / .csv 三维点云与 .png / .pgm 二维栅格；内容相同自动去重。
          </div>
        </Modal>
      )}

      {view3d && (
        <Modal
          title={view3d.name + " · 3D 点云（三维）"}
          onClose={() => setView3d(null)}
          width="min(960px, 96vw)"
        >
          <PcdViewer mapId={view3d.id} height="62vh" />
        </Modal>
      )}

      {view2d && (() => {
        const png = mapLatestPng(view2d);
        if (!png) return null;
        return (
          <Modal
            title={view2d.name + " · 2D 栅格（v" + png.version + "）"}
            onClose={() => setView2d(null)}
            width="min(860px, 96vw)"
          >
            <img
              src={mapFileURL(view2d.id, png.name, png.version)}
              alt={view2d.name + " 2D 栅格"}
              style={{ width: "100%", background: "#fff", borderRadius: 6, imageRendering: "pixelated" }}
            />
            <div className="muted" style={{ fontSize: 11, marginTop: 6 }}>
              黑=占据，白=自由（map_server 口径）；下载：
              <a href={mapFileURL(view2d.id, png.name, png.version)} target="_blank" rel="noreferrer" style={{ marginLeft: 6 }}>
                {png.name}
              </a>
            </div>
          </Modal>
        );
      })()}

      {confirmDel && (
        <Modal title="删除地图" onClose={() => setConfirmDel(null)} width="min(440px, 92vw)">
          <div style={{ fontSize: 13, lineHeight: 1.7 }}>
            确定删除地图 <b>{confirmDel.name}</b>（{confirmDel.vehicle_id}）？
            <div className="muted" style={{ marginTop: 8, fontSize: 12 }}>
              将删除服务器上该地图的全部 {confirmDel.versions.length} 个版本与文件，操作不可恢复；
              车端原件不受影响，车端下次上报会重新建档。
            </div>
          </div>
          <div className="row" style={{ justifyContent: "flex-end", marginTop: 14 }}>
            <button className="btn" onClick={() => setConfirmDel(null)}>取消</button>
            <button
              className="btn danger"
              disabled={delBusy === confirmDel.id}
              onClick={() => void doDelete(confirmDel)}
            >{delBusy === confirmDel.id ? "删除中…" : "确认删除"}</button>
          </div>
        </Modal>
      )}
    </div>
  );
}
