// 循迹导航：任务列表 + 新建任务（地图选点 / 路点编辑 / 定时与停留）。
// 路点从车辆地图上点选（地图标定位置即路点），两个及以上点连成循迹路线；
// 支持定时到达（何时到某点）与到点停留（停多久），后端经 MQTT 发布 vehicle/{id}/nav。
import { useCallback, useEffect, useState } from "react";
import type { FleetState } from "../api";
import { cancelRoute, fetchRoutes, submitRoute } from "../api";
import Modal from "../components/Modal";
import RouteMapPicker from "../components/RouteMapPicker";
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
  if (s === "accepted") return "车端已接受";
  if (s === "running") return "执行中";
  if (s === "completed") return "已完成";
  if (s === "cancel_requested") return "取消请求中";
  if (s === "cancelled") return "已取消";
  if (s === "cancel_rejected") return "取消被拒";
  if (s === "rejected") return "车端拒绝";
  if (s === "failed") return "执行失败";
  return s;
}

function canCancel(s: string): boolean {
  return ["queued", "dispatched", "accepted", "running"].includes(s);
}

function toLocalInput(ns?: number): string {
  if (!ns) return "";
  const d = new Date(ns / 1e6);
  const p = (x: number) => (x < 10 ? "0" + x : String(x));
  return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate()) + "T" + p(d.getHours()) + ":" + p(d.getMinutes());
}

export default function NavRoutePage({ fleet }: { fleet: FleetState }) {
  const [editorOpen, setEditorOpen] = useState(false);
  const [vid, setVid] = useState("");
  const [name, setName] = useState("");
  const [pts, setPts] = useState<NavPoint[]>([]);
  const [sel, setSel] = useState(-1);
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

  const addPt = (x: number, y: number) => {
    setPts((prev) => {
      const np = [...prev, { name: "P" + (prev.length + 1), x, y }];
      setSel(np.length - 1);
      return np;
    });
  };

  const setPt = (i: number, k: keyof NavPoint, val: string) => {
    setPts((prev) =>
      prev.map((p, j) => {
        if (j !== i) return p;
        const np = { ...p };
        if (k === "name") np.name = val;
        if (k === "x") np.x = parseFloat(val) || 0;
        if (k === "y") np.y = parseFloat(val) || 0;
        if (k === "dwell_s") {
          const d = parseFloat(val);
          if (!d || d <= 0) delete np.dwell_s;
          else np.dwell_s = d;
        }
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
    setSel((s) => (s === i ? i + dir : s === i + dir ? i : s));
  };

  const removePt = (i: number) => {
    setPts((prev) => prev.filter((_, j) => j !== i));
    setSel(-1);
  };

  const openEditor = () => {
    setName("");
    setPts([]);
    setSel(-1);
    setEditorOpen(true);
  };

  const doSubmit = async () => {
    setMsg("");
    if (!vid) {
      setMsg("请选择已登记车辆。");
      return;
    }
    if (pts.length < 2) {
      setMsg("至少需要两个路点（在地图上点选起点与终点）");
      return;
    }
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
            在车辆地图上点选标定位置作为路点，两个及以上点连成循迹路线；支持定时到达与到点停留。任务经云端转发到车端导航栈。
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
                <td>
                  {rt.points.length}
                  {rt.points.some((p) => p.dwell_s) && <span className="badge info" style={{ marginLeft: 6 }}>含停留</span>}
                  {rt.points.some((p) => p.at_ns) && <span className="badge info" style={{ marginLeft: 6 }}>定时</span>}
                </td>
                <td>{rt.origin === "open_api" ? "开放 API" : "控制台"}</td>
                <td className="mono">{fmtTime(rt.created_ns)}</td>
                <td>{statusLabel(rt.status)}</td>
                <td>
                  {canCancel(rt.status) && (
                    <button className="btn small danger" onClick={() => void doCancel(rt.id)}>取消</button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {editorOpen && (
        <Modal title="新建循迹任务（地图选点）" onClose={() => setEditorOpen(false)} width="min(1100px, 97vw)">
          <div className="row" style={{ gap: 8, marginBottom: 10 }}>
            <span className="muted" style={{ width: 60 }}>车辆</span>
            <select className="input" style={{ width: 200 }} value={vid} onChange={(e) => { setVid(e.target.value); setPts([]); setSel(-1); }}>
              {fleet.snap.vehicles.map((v) => (
                <option key={v.vehicle_id} value={v.vehicle_id}>{v.vehicle_id}</option>
              ))}
            </select>
            <span className="muted" style={{ width: 60, marginLeft: 12 }}>任务名</span>
            <input className="input" style={{ flex: 1 }} value={name} placeholder="留空自动生成（起点→终点）" onChange={(e) => setName(e.target.value)} />
          </div>

          <div style={{ display: "flex", gap: 12, minHeight: 420 }}>
            {/* 左：地图选点 */}
            <RouteMapPicker vehicleId={vid} pts={pts} selected={sel} onPick={addPt} onSelect={setSel} />

            {/* 右：路点列表（顺序 = 行驶顺序） */}
            <div style={{ flex: "0 0 350px", display: "flex", flexDirection: "column", minHeight: 0 }}>
              <div className="panel-title" style={{ marginBottom: 6 }}>
                路点（{pts.length}）<span className="hint">地图上点击添加，可手动微调坐标</span>
              </div>
              <div className="scroll" style={{ flex: 1, minHeight: 0, display: "flex", flexDirection: "column", gap: 8 }}>
                {pts.length === 0 && (
                  <div className="muted" style={{ fontSize: 12, padding: 8 }}>
                    在左侧地图上点击添加路点：第一个点为起点，最后一个点为目的地（终点）。
                  </div>
                )}
                {pts.map((p, i) => (
                  <div
                    key={i}
                    onClick={() => setSel(i)}
                    style={{
                      border: "1px solid " + (sel === i ? "var(--accent)" : "var(--border)"),
                      borderRadius: 8, padding: "7px 9px", cursor: "pointer",
                      background: sel === i ? "rgba(37,99,235,.10)" : "rgba(10,18,34,.6)",
                    }}
                  >
                    <div className="row" style={{ gap: 6, alignItems: "center" }}>
                      <b style={{ fontSize: 12, color: i === 0 ? "#22c55e" : i === pts.length - 1 ? "#f59e0b" : "var(--text)" }}>
                        {i + 1}
                      </b>
                      <input className="input" style={{ width: 70 }} value={p.name} onChange={(e) => setPt(i, "name", e.target.value)} />
                      <span className="mono muted" style={{ fontSize: 11 }}>
                        ({p.x}, {p.y})
                      </span>
                      <span style={{ flex: 1 }} />
                      <button className="btn small ghost" title="上移" onClick={(e) => { e.stopPropagation(); move(i, -1); }}>↑</button>
                      <button className="btn small ghost" title="下移" onClick={(e) => { e.stopPropagation(); move(i, 1); }}>↓</button>
                      <button className="btn small ghost" title="删除" onClick={(e) => { e.stopPropagation(); removePt(i); }}>✕</button>
                    </div>
                    <div className="row" style={{ gap: 6, alignItems: "center", marginTop: 6, flexWrap: "wrap" }}>
                      <label className="muted" style={{ fontSize: 11, display: "inline-flex", alignItems: "center", gap: 4 }}>
                        X
                        <input className="input" style={{ width: 62 }} value={p.x} onChange={(e) => setPt(i, "x", e.target.value)} />
                      </label>
                      <label className="muted" style={{ fontSize: 11, display: "inline-flex", alignItems: "center", gap: 4 }}>
                        Y
                        <input className="input" style={{ width: 62 }} value={p.y} onChange={(e) => setPt(i, "y", e.target.value)} />
                      </label>
                      <label className="muted" style={{ fontSize: 11, display: "inline-flex", alignItems: "center", gap: 4 }} title="到点后停留多少秒（0=不停）">
                        停留(秒)
                        <input
                          className="input" style={{ width: 56 }} type="number" min={0} step={1}
                          value={p.dwell_s ?? ""} placeholder="0"
                          onChange={(e) => setPt(i, "dwell_s", e.target.value)}
                        />
                      </label>
                    </div>
                    <div className="row" style={{ gap: 6, alignItems: "center", marginTop: 6 }}>
                      <label className="muted" style={{ fontSize: 11, display: "inline-flex", alignItems: "center", gap: 4 }} title="定时到达：计划何时到达该点（留空=立即顺序执行）">
                        定时到达
                        <input
                          className="input" type="datetime-local" style={{ width: 190 }}
                          value={toLocalInput(p.at_ns)}
                          onChange={(e) => setPt(i, "at_ns", e.target.value)}
                        />
                      </label>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </div>

          <div className="btn-row mt" style={{ justifyContent: "space-between" }}>
            <button className="btn" disabled={pts.length === 0} onClick={() => { setPts([]); setSel(-1); }}>
              清空路点
            </button>
            <span className="row" style={{ gap: 8 }}>
              <button className="btn" onClick={() => setEditorOpen(false)}>取消</button>
              <button className="btn primary" disabled={pts.length < 2} onClick={() => void doSubmit()}>
                下发循迹任务{pts.length < 2 ? "（至少 2 个点）" : ""}
              </button>
            </span>
          </div>
          <div className="muted mt" style={{ fontSize: 11 }}>
            3D 点云地图为真实米坐标；2D 栅格按分辨率标定换算。定时到达=计划何时到该点；停留=到点后停多少秒再走下一点。
          </div>
        </Modal>
      )}
    </div>
  );
}
