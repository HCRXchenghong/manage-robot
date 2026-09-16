// Video wall intentionally shows no generated frames or fabricated recordings.
// A media-control integration will replace each unavailable slot with a signed
// WebRTC playback element after the vehicle media agent has registered it.
import { useEffect, useState } from "react";
import { fetchMe, useFleet } from "../api";
import type { Me } from "../types";
import { CAMS, CAM_LABEL, type CamId, VideoUnavailable } from "../components/VideoPanel";

type Channel = { vehicleID: string; camera: CamId };

export default function VideoWall() {
  const [me, setMe] = useState<Me | null>(null);
  const [grid, setGrid] = useState(2);
  const [channels, setChannels] = useState<(Channel | null)[]>([null, null, null, null]);
  const fleet = useFleet(me !== null);

  useEffect(() => { void fetchMe().then(setMe).catch(() => setMe(null)); }, []);
  useEffect(() => {
    setChannels((old) => {
      const next = old.slice(0, grid * grid);
      while (next.length < grid * grid) next.push(null);
      return next;
    });
  }, [grid]);

  if (!me) return <div className="vw-root"><div className="vw-guard">会话无效或已过期，请先登录。</div></div>;

  const add = (vehicleID: string, camera: CamId) => setChannels((old) => {
    const i = old.findIndex((channel) => channel === null);
    if (i < 0) return old;
    const next = old.slice();
    next[i] = { vehicleID, camera };
    return next;
  });

  return (
    <div className="vw-root">
      <header className="vw-head">
        <div className="vw-head-l"><span className="muted mono">{fleet.source === "live" ? "车队实时链路" : fleet.source === "degraded" ? "车队链路降级（使用最后验证快照）" : "车队链路不可用"}</span></div>
        <h1>视频监控中控台</h1>
        <div className="vw-head-r">
          <div className="vw-split">
            {[1, 2, 3, 4].map((n) => <button key={n} className={grid === n ? "on" : ""} onClick={() => setGrid(n)}>{n}×{n}</button>)}
          </div>
        </div>
      </header>
      <div className="vw-body">
        <aside className="vw-side">
          <div className="vw-side-title">车辆与机位</div>
          {fleet.snap.vehicles.length === 0 && <div className="muted">暂无已注册车辆。</div>}
          {fleet.snap.vehicles.map((vehicle) => (
            <div className="vw-veh" key={vehicle.vehicle_id}>
              <div className="mono">{vehicle.vehicle_id}</div>
              <div className="vw-cams">{CAMS.map((camera) => <button key={camera.id} onClick={() => add(vehicle.vehicle_id, camera.id)}>{camera.label}</button>)}</div>
            </div>
          ))}
        </aside>
        <main className="vw-main">
          <div className="vw-wall" style={{ gridTemplateColumns: "repeat(" + grid + ", minmax(0, 1fr))" }}>
            {channels.map((channel, index) => (
              <div className={"vw-cell" + (channel ? "" : " empty")} key={index}>
                {channel ? <>
                  <VideoUnavailable vehicleID={channel.vehicleID} camera={channel.camera} fill />
                  <div className="vw-cell-top"><span className="vw-cell-id mono">{channel.vehicleID} · {CAM_LABEL[channel.camera]}</span><span className="vw-cell-badge">未注册</span><button className="vw-cell-x" onClick={() => setChannels((old) => old.map((item, i) => i === index ? null : item))}>×</button></div>
                </> : <div className="vw-cell-empty">从左侧选择车辆机位</div>}
              </div>
            ))}
          </div>
          <div className="muted" style={{ marginTop: 12, fontSize: 12 }}>录像回放已禁用：当前未接入可信录像索引与签名播放服务。</div>
        </main>
      </div>
    </div>
  );
}
