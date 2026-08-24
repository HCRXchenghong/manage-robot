import { useEffect, useRef, useState } from "react";
import type { VehicleSnap } from "../types";
import { fetchPointCloud } from "../api";
import { bevColor, buildBev, type BevGrid } from "./bev";

// 视频墙：1/2/4 画面布局；机位含前/后/左/右 + 融合鸟瞰(BEV) + 360°环视。
// 阶段 1 为模拟画面（canvas 动效）；阶段 2 换成 media-control 的 WebRTC 真流，
// 组件接口不变（只把 <canvas> 换成 <video>）。

type SrcId = "front" | "rear" | "left" | "right" | "bev" | "pano";
const LABEL: Record<SrcId, string> = {
  front: "前向",
  rear: "后向",
  left: "左侧",
  right: "右侧",
  bev: "融合鸟瞰 BEV",
  pano: "360° 环视",
};

let bevCache: BevGrid | null = null;

function drawCam(ctx: CanvasRenderingContext2D, w: number, h: number, t: number, src: SrcId) {
  const hue = src === "front" ? 210 : src === "rear" ? 280 : src === "left" ? 150 : 30;
  const horizon = h * 0.45;
  const sky = ctx.createLinearGradient(0, 0, 0, horizon);
  sky.addColorStop(0, "hsl(" + hue + ",60%,12%)");
  sky.addColorStop(1, "hsl(" + hue + ",55%,26%)");
  ctx.fillStyle = sky;
  ctx.fillRect(0, 0, w, horizon);
  const g2 = ctx.createLinearGradient(0, horizon, 0, h);
  g2.addColorStop(0, "#1b2c22");
  g2.addColorStop(1, "#0d1512");
  ctx.fillStyle = g2;
  ctx.fillRect(0, horizon, w, h - horizon);
  ctx.strokeStyle = "rgba(220,230,247,0.5)";
  ctx.lineWidth = 2;
  for (let i = -3; i <= 3; i++) {
    ctx.beginPath();
    ctx.moveTo(w / 2 + i * w * 0.16, horizon);
    ctx.lineTo(w / 2 + i * w * 0.9, h);
    ctx.stroke();
  }
  ctx.strokeStyle = "rgba(56,189,248,0.8)";
  for (let k = 0; k < 6; k++) {
    const p = ((t * 4 + k * 90) % (h - horizon)) / (h - horizon);
    const y = horizon + p * (h - horizon);
    ctx.lineWidth = 2 + p * 5;
    ctx.beginPath();
    ctx.moveTo(w / 2, y);
    ctx.lineTo(w / 2, y + 12 + p * 26);
    ctx.stroke();
  }
  const bx = w / 2 + Math.sin(t / 60 + hue) * w * 0.12;
  ctx.fillStyle = "rgba(234,179,8,0.65)";
  ctx.fillRect(bx - 14, horizon - 16, 28, 16);
}

function drawPano(ctx: CanvasRenderingContext2D, w: number, h: number, t: number) {
  const g = ctx.createLinearGradient(0, 0, w, 0);
  g.addColorStop(0, "#122036");
  g.addColorStop(0.5, "#1a2f4a");
  g.addColorStop(1, "#122036");
  ctx.fillStyle = g;
  ctx.fillRect(0, 0, w, h);
  ctx.strokeStyle = "rgba(159,211,255,0.25)";
  ctx.lineWidth = 1;
  for (let x = 0; x < w; x += 40) {
    ctx.beginPath();
    ctx.moveTo(x, 0);
    ctx.lineTo(x, h);
    ctx.stroke();
  }
  const sx = (t * 3) % w;
  ctx.fillStyle = "rgba(56,189,248,0.5)";
  ctx.fillRect(sx - 2, 0, 4, h);
  ctx.fillStyle = "rgba(234,179,8,0.6)";
  for (let k = 0; k < 4; k++) {
    const x = (k * 0.25 + 0.1) * w + Math.sin(t / 50 + k) * 10;
    ctx.fillRect(x, h * 0.55, 26, 14);
  }
  ctx.fillStyle = "rgba(159,211,255,0.9)";
  ctx.font = "11px monospace";
  ctx.fillText("360° 环视拼接（模拟）", 8, h - 8);
}

function drawBev(ctx: CanvasRenderingContext2D, w: number, h: number, bev: BevGrid | null) {
  ctx.fillStyle = "#060b16";
  ctx.fillRect(0, 0, w, h);
  if (!bev) {
    ctx.fillStyle = "#9fd3ff";
    ctx.font = "12px monospace";
    ctx.fillText("BEV 融合鸟瞰加载中…", 10, 20);
    return;
  }
  const s = Math.min(w / bev.nx, h / bev.ny);
  const ox = (w - bev.nx * s) / 2;
  const oy = (h - bev.ny * s) / 2;
  const span = Math.max(1e-6, bev.z1 - bev.z0);
  for (let iy = 0; iy < bev.ny; iy++) {
    for (let ix = 0; ix < bev.nx; ix++) {
      const i2 = iy * bev.nx + ix;
      if (!bev.occ[i2]) continue;
      const tt = Math.min(1, Math.max(0, (bev.maxz[i2] - bev.z0) / span));
      const c = bevColor(tt);
      ctx.fillStyle = "rgb(" + Math.round(c[0] * 255) + "," + Math.round(c[1] * 255) + "," + Math.round(c[2] * 255) + ")";
      ctx.fillRect(ox + ix * s, oy + iy * s, Math.max(1, s), Math.max(1, s));
    }
  }
  ctx.fillStyle = "rgba(159,211,255,0.9)";
  ctx.font = "11px monospace";
  ctx.fillText("融合鸟瞰（点云 BEV 投影）", 8, h - 8);
}

function VideoCard({ src, vid }: { src: SrcId; vid: string }) {
  const ref = useRef<HTMLCanvasElement | null>(null);
  useEffect(() => {
    const cv = ref.current;
    if (!cv) return;
    const ctx = cv.getContext("2d");
    if (!ctx) return;
    let raf = 0;
    let t = 0;
    let bev: BevGrid | null = src === "bev" ? bevCache : null;
    if (src === "bev" && !bev) {
      fetchPointCloud()
        .then((c) => {
          bevCache = buildBev(c.static.positions, 0.2);
          bev = bevCache;
        })
        .catch(() => {});
    }
    const draw = () => {
      t += 1;
      const w = cv.width;
      const h = cv.height;
      if (src === "bev") drawBev(ctx, w, h, bev);
      else if (src === "pano") drawPano(ctx, w, h, t);
      else drawCam(ctx, w, h, t, src);
      raf = requestAnimationFrame(draw);
    };
    raf = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(raf);
  }, [src]);
  return (
    <div className="vw-card">
      <canvas ref={ref} width={480} height={270} />
      <span className="vw-label">{vid} · {LABEL[src]}</span>
      <span className="vw-live">LIVE</span>
    </div>
  );
}

export default function VideoWall({ vehicle }: { vehicle: VehicleSnap | null }) {
  const [layout, setLayout] = useState<1 | 2 | 4>(4);
  const [focus, setFocus] = useState<SrcId>("front");
  const vid = vehicle ? vehicle.vehicle_id : "sim-veh-001";
  const cards: SrcId[] =
    layout === 1 ? [focus] : layout === 2 ? ["front", "bev"] : ["front", "bev", "pano", "rear"];
  return (
    <div style={{ display: "flex", flexDirection: "column", minHeight: 0, flex: 1 }}>
      <div className="panel-title">
        <span>视频监控 · {vid}</span>
        <span className="hint">阶段 1 模拟画面 · 含融合鸟瞰 / 360° 环视</span>
        <span className="spacer" />
        <div className="tabs">
          {([1, 2, 4] as const).map((n) => (
            <button key={n} className={"tab" + (layout === n ? " active" : "")} onClick={() => setLayout(n)}>
              {n} 画面
            </button>
          ))}
        </div>
      </div>
      {layout === 1 && (
        <div className="tabs" style={{ marginBottom: 6 }}>
          {(Object.keys(LABEL) as SrcId[]).map((s) => (
            <button key={s} className={"tab" + (focus === s ? " active" : "")} onClick={() => setFocus(s)}>
              {LABEL[s]}
            </button>
          ))}
        </div>
      )}
      <div className={"video-wall l" + layout}>
        {cards.map((s) => (
          <VideoCard key={s} src={s} vid={vid} />
        ))}
      </div>
    </div>
  );
}

