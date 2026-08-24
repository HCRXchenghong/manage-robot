// 循迹导航（精简版）：页面只留任务列表；新建任务（路点编辑/定时）收进弹窗。
// 后端经 MQTT 发布 vehicle/{id}/nav 给车端，任务全程可在任务表里跟踪/取消。
import { useCallback, useEffect, useState } from "react";
import type { FleetState } from "../api";
import { cancelRoute, fetchRoutes, submitRoute } from "../api";
import Modal from "../components/Modal";
import type { NavPoint, NavRoute } from "../types";

function fmtTime(ns?: number): string {
  if (!ns) return "-";
  const d = new Date(ns / 1e6);
  const p = (x: number) => (x < 10 ? "0" + x : String(x));
  return (
    d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " +
    p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds())
  );
}

function statusLabel(s: string): string {
  if (s === "dispatched") return "已下发";
  if (s === "queued") return "排队中（待链路）";
  if (s === "cancelled") return "已取消";
  return s;
}

export default function NavRoutePage({ fleet }: { fleet: FleetState }) {
  const [editorOpen, setEditorOpen] = useState(false);
  const [vid, setVid] = useState("sim-veh-001");
  const [name, setName] = useState("");
  const [pts, setPts] = useState<NavPoint[]>([
    { name: "起点", x: 0, y: 0 },
    { name: "终点", x: 20, y: 5 },
  ]);
  const [routes, setRoutes] = useState<NavRoute[]>([]);
  const [msg, setMsg] = useState("");

  const reload = useCallback(async () => {
    try {
      setRoutes(await fetchRoutes());
    } catch {
      /* 后端未就绪 */
    }
  }, []);

  useEffect(() => {
    void reload();
    const t = window.setInterval(() => void reload(), 5000);
    return () => window.clearInterval(t);
  }, [reload]);

  useEffect(() => {
    const v = fleet.snap.vehicles[0];
    if (v && !fleet.snap.vehicles.some((x) => x.vehicle_id === vid)) setVid(v.vehicle_id);
  }, [fleet.snap.vehicles, vid]);

  const setPt = (i: number, k: keyof NavPoint, val: string) => {
    setPts((prev) =>
      prev.map((p, j) => {
        if (j !== i) return p;
        const np = { ...p };
        if (k === "name") np.name = val;
        if (k === "x") np.x = parseFloat(val) || 0;
        if (k === "y") np.y = parseFloat(val) || 0;
        if (k === "at_ns") {
          if (!val) delete np.at_ns;
          else np.at_ns = new Date(val).getTime() * 1e6;
        }
        return np;
      }),
    );
  };

  const move = (i: number, dir: number) => {
    setPts((prev) => {
      const j = i + dir;
      if (j < 0 || j >= prev.length) return prev;
      const arr = prev.slice();
      const tmp = arr[i];
      arr[i] = arr[j];
      arr[j] = tmp;
      return arr;
    });
  };

  const openEditor = () => {
    setName("");
    setPts([
      { name: "起点", x: 0, y: 0 },
      { name: "终点", x: 20, y: 5 },
    ]);
    setEditorOpen(true);
  };

  const doSubmit = async () => {
    setMsg("");
    try {
      const rt = await submitRoute(vid, name, pts);
      setMsg("任务 " + rt.id + " " + statusLabel(rt.status));
      setEditorOpen(false);
    } catch (e) {
      setMsg("下发失败：" + String(e));
    }
    void reload();
  };

  const doCancel = async (id: string) => {
    if (!window.confirm("取消该循迹任务？")) return;
    try {
      await cancelRoute(id);
    } catch (e) {
      setMsg("取消失败：" + String(e));
    }
    void reload();
  };

  return (
    <div style={{ padding: 16, overflow: "auto", height: "100%" }}>
      <div className="row" style={{ justifyContent: "space-between", alignItems: "flex-start", marginBottom: 12 }}>
        <div>
          <div style={{ fontSize: 16, fontWeight: 700 }}>循迹导航</div>
          <div className="muted">
            设置多个路点（可定时）并下发给车辆：从哪个点出发、经过哪些点、到哪个点结束。任务经云端转发到车端导航栈。
          </div>
        </div>
        <button className="btn primary" onClick={openEditor}>新建循迹任务</button>
      </div>
      {msg && <div className="notice">{msg}</div>}

      <div className="panel" style={{ padding: 14 }}>
        <div className="panel-title">任务列表 <span className="hint">（含开放 API 下发的任务）</span></div>
        <table className="table">
          <thead>
            <tr><th>任务</th><th>车辆</th><th>路点</th><th>来源</th><th>创建时间</th><th>状态</th><th></th></tr>
          </thead>
          <tbody>
            {routes.length === 0 && (
              <tr><td colSpan={7} className="muted" style={{ textAlign: "center", padding: 18 }}>暂无任务，点右上角「新建循迹任务」</td></tr>
            )}
            {routes.map((rt) => (
              <tr key={rt.id}>
                <td>
                  <div>{rt.name}</div>
                  <div className="muted mono" style={{ fontSize: 11 }}>{rt.id}</div>
                </td>
                <td>{rt.vehicle_id}</td>
                <td>{rt.points.length}</td>
                <td>{rt.origin === "open_api" ? "开放 API" : "控制台"}</td>
                <td className="mono">{fmtTime(rt.created_ns)}</td>
                <td>{statusLabel(rt.status)}</td>
                <td>
                  {rt.status !== "cancelled" && (
                    <button className="btn small danger" onClick={() => void doCancel(rt.id)}>取消</button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {editorOpen && (
        <Modal title="新建循迹任务" onClose={() => setEditorOpen(false)} width="min(760px, 94vw)">
          <div className="row" style={{ gap: 8, marginBottom: 10 }}>
            <span className="muted" style={{ width: 60 }}>车辆</span>
            <select className="input" style={{ width: 200 }} value={vid} onChange={(e) => setVid(e.target.value)}>
              {fleet.snap.vehicles.map((v) => (
                <option key={v.vehicle_id} value={v.vehicle_id}>{v.vehicle_id}</option>
              ))}
            </select>
            <span className="muted" style={{ width: 60, marginLeft: 12 }}>任务名</span>
            <input className="input" style={{ flex: 1 }} value={name} placeholder="留空自动生成（起点→终点）" onChange={(e) => setName(e.target.value)} />
          </div>
          <table className="table">
            <thead>
              <tr>
                <th>#</th><th>点名</th><th>X (m)</th><th>Y (m)</th><th>定时到达（可选）</th><th></th>
              </tr>
            </thead>
            <tbody>
              {pts.map((p, i) => (
                <tr key={i}>
                  <td>
                    <span className="row" style={{ gap: 2 }}>
                      <button className="btn small ghost" onClick={() => move(i, -1)}>↑</button>
                      <button className="btn small ghost" onClick={() => move(i, 1)}>↓</button>
                    </span>
                  </td>
                  <td><input className="input" style={{ width: 64 }} value={p.name} onChange={(e) => setPt(i, "name", e.target.value)} /></td>
                  <td><input className="input" style={{ width: 64 }} value={p.x} onChange={(e) => setPt(i, "x", e.target.value)} /></td>
                  <td><input className="input" style={{ width: 64 }} value={p.y} onChange={(e) => setPt(i, "y", e.target.value)} /></td>
                  <td>
                    <input
                      className="input" type="datetime-local"
                      defaultValue={p.at_ns ? new Date(p.at_ns / 1e6).toISOString().slice(0, 16) : ""}
                      onChange={(e) => setPt(i, "at_ns", e.target.value)}
                    />
                  </td>
                  <td>
                    <button className="btn small ghost" disabled={pts.length <= 2} onClick={() => setPts((prev) => prev.filter((_, j) => j !== i))}>
                      删除
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="btn-row mt" style={{ justifyContent: "space-between" }}>
            <button className="btn" onClick={() => setPts((prev) => [...prev, { name: "P" + (prev.length + 1), x: 0, y: 0 }])}>
              + 加路点
            </button>
            <span className="row" style={{ gap: 8 }}>
              <button className="btn" onClick={() => setEditorOpen(false)}>取消</button>
              <button className="btn primary" onClick={() => void doSubmit()}>下发循迹任务</button>
            </span>
          </div>
          <div className="muted mt" style={{ fontSize: 11 }}>
            坐标为车辆本地地图坐标系（米）；定时到达留空 = 立即顺序执行。
          </div>
        </Modal>
      )}
    </div>
  );
}
