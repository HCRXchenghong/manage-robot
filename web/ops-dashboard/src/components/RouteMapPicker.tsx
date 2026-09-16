// 循迹导航地图选点：在车辆地图上点选路点（地图标定位置即路点）。
// 优先用 3D 点云地图（俯视投影，真实米坐标），无 3D 时用 2D 栅格（PNG，按分辨率标定换算米）。
// 左键点击=加点；滚轮缩放；按住拖动=平移；已选路点连线显示，可点列表联动高亮。
import { useCallback, useEffect, useRef, useState } from "react";
import { fetchMapPoints, fetchMaps, mapFileURL, mapHas3D, mapLatestPng } from "../api";
import type { MapEntry, NavPoint } from "../types";

interface Props {
  vehicleId: string;
  pts: NavPoint[];
  selected: number;
  onPick: (x: number, y: number) => void;
  onSelect: (i: number) => void;
}

const round2 = (n: number) => Math.round(n * 100) / 100;

export default function RouteMapPicker({ vehicleId, pts, selected, onPick, onSelect }: Props) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [maps, setMaps] = useState<MapEntry[]>([]);
  const [mapId, setMapId] = useState("");
  const [mode, setMode] = useState<"3d" | "2d" | "none">("none");
  const [status, setStatus] = useState("加载地图中…");
  // 3D 俯视投影点（世界米坐标，x 右 / y 下）
  const cloudRef = useRef<Float32Array | null>(null);
  // 2D 栅格
  const imgRef = useRef<HTMLImageElement | null>(null);
  const [mpp, setMpp] = useState(0.05); // 米/像素（2D 标定）
  // 视图变换：屏幕 = 中心 + (世界 - 原点) * scale
  const viewRef = useRef({ scale: 8, ox: 0, oy: 0 });
  const dragRef = useRef<{ x: number; y: number; moved: boolean } | null>(null);
  const ptsRef = useRef(pts);
  ptsRef.current = pts;
  const selRef = useRef(selected);
  selRef.current = selected;
  const mppRef = useRef(mpp);
  mppRef.current = mpp;
  const modeRef = useRef(mode);
  modeRef.current = mode;

  // 该车地图列表
  useEffect(() => {
    let alive = true;
    setStatus("加载地图中…");
    void fetchMaps()
      .then((all) => {
        if (!alive) return;
        const mine = all.filter((m) => m.vehicle_id === vehicleId);
        setMaps(mine);
        if (mine.length > 0) setMapId(mine[0].id);
        else {
          setMode("none");
          setStatus("该车暂无地图（可先在「地图中心」上传点云/栅格地图）");
        }
      })
      .catch(() => alive && setStatus("地图加载失败"));
    return () => {
      alive = false;
    };
  }, [vehicleId]);

  // 加载所选地图
  useEffect(() => {
    if (!mapId) return;
    let alive = true;
    const m = maps.find((x) => x.id === mapId);
    if (!m) return;
    setStatus("加载地图中…");
    cloudRef.current = null;
    imgRef.current = null;

    const finish3D = (xs: Float32Array) => {
      if (!alive) return;
      cloudRef.current = xs;
      setMode("3d");
      setStatus("");
      fitView();
      render();
    };

    if (mapHas3D(m)) {
      void fetchMapPoints(m.id, undefined, 300000)
        .then((pc) => {
          if (!alive) return;
          const n = Math.floor(pc.positions.length / 3);
          if (!n) {
            setStatus("点云为空");
            return;
          }
          // 俯视：世界 (x, y)，抽稀以提速
          const stride = Math.max(1, Math.floor(n / 150000));
          const xs = new Float32Array(Math.ceil(n / stride) * 2);
          let k = 0;
          for (let i = 0; i < n; i += stride) {
            xs[k * 2] = pc.positions[i * 3];
            xs[k * 2 + 1] = pc.positions[i * 3 + 1];
            k++;
          }
          finish3D(xs.slice(0, k * 2));
        })
        .catch(() => alive && setStatus("点云加载失败"));
      return () => {
        alive = false;
      };
    }

    const png = mapLatestPng(m);
    if (png) {
      const img = new Image();
      img.onload = () => {
        if (!alive) return;
        imgRef.current = img;
        setMode("2d");
        setStatus("");
        // 让整图适配画布
        const wrap = wrapRef.current;
        const w = wrap ? wrap.clientWidth : 600;
        const h = wrap ? wrap.clientHeight : 400;
        const s = Math.min(w / (img.width * mppRef.current), h / (img.height * mppRef.current)) * 0.9;
        viewRef.current = { scale: Math.max(0.01, s), ox: 0, oy: 0 };
        render();
      };
      img.src = mapFileURL(m.id, png.name, png.version);
      return () => {
        alive = false;
      };
    }
    setMode("none");
    setStatus("该地图无 3D 点云也无 2D 栅格，无法选点");
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mapId, maps]);

  const world2screen = (x: number, y: number, cv: HTMLCanvasElement): [number, number] => {
    const v = viewRef.current;
    return [cv.width / 2 + (x - v.ox) * v.scale, cv.height / 2 + (y - v.oy) * v.scale];
  };
  const screen2world = (px: number, py: number, cv: HTMLCanvasElement): [number, number] => {
    const v = viewRef.current;
    return [(px - cv.width / 2) / v.scale + v.ox, (py - cv.height / 2) / v.scale + v.oy];
  };

  const fitView = useCallback(() => {
    const xs = cloudRef.current;
    const wrap = wrapRef.current;
    if (!xs || !wrap || xs.length < 2) return;
    let xmin = Infinity, xmax = -Infinity, ymin = Infinity, ymax = -Infinity;
    for (let i = 0; i < xs.length; i += 2) {
      if (xs[i] < xmin) xmin = xs[i];
      if (xs[i] > xmax) xmax = xs[i];
      if (xs[i + 1] < ymin) ymin = xs[i + 1];
      if (xs[i + 1] > ymax) ymax = xs[i + 1];
    }
    const w = Math.max(1, wrap.clientWidth);
    const h = Math.max(1, wrap.clientHeight);
    const spanX = Math.max(1e-6, xmax - xmin);
    const spanY = Math.max(1e-6, ymax - ymin);
    viewRef.current = {
      scale: Math.min(w / spanX, h / spanY) * 0.92,
      ox: (xmin + xmax) / 2,
      oy: (ymin + ymax) / 2,
    };
  }, []);

  const render = useCallback(() => {
    const cv = canvasRef.current;
    if (!cv) return;
    const wrap = wrapRef.current;
    if (wrap) {
      cv.width = Math.max(1, wrap.clientWidth);
      cv.height = Math.max(1, wrap.clientHeight);
    }
    const ctx = cv.getContext("2d");
    if (!ctx) return;
    ctx.fillStyle = "#05080f";
    ctx.fillRect(0, 0, cv.width, cv.height);

    if (modeRef.current === "3d" && cloudRef.current) {
      const xs = cloudRef.current;
      const s = viewRef.current.scale;
      const ps = Math.max(1, Math.min(3, s / 12)); // 点像素大小随缩放自适应
      ctx.fillStyle = "rgba(88,166,220,.85)";
      for (let i = 0; i < xs.length; i += 2) {
        const [sx, sy] = world2screen(xs[i], xs[i + 1], cv);
        if (sx < -4 || sy < -4 || sx > cv.width + 4 || sy > cv.height + 4) continue;
        ctx.fillRect(sx, sy, ps, ps);
      }
    } else if (modeRef.current === "2d" && imgRef.current) {
      const img = imgRef.current;
      const wm = img.width * mppRef.current;
      const hm = img.height * mppRef.current;
      const [lx, ty] = world2screen(-wm / 2, -hm / 2, cv);
      const s = viewRef.current.scale;
      ctx.imageSmoothingEnabled = false;
      ctx.drawImage(img, lx, ty, wm * s, hm * s);
    }

    // 路点连线 + 标记
    const list = ptsRef.current;
    if (list.length > 0) {
      ctx.strokeStyle = "rgba(56,189,248,.9)";
      ctx.lineWidth = 2;
      ctx.beginPath();
      list.forEach((p, i) => {
        const [sx, sy] = world2screen(p.x, p.y, cv);
        if (i === 0) ctx.moveTo(sx, sy);
        else ctx.lineTo(sx, sy);
      });
      ctx.stroke();
      list.forEach((p, i) => {
        const [sx, sy] = world2screen(p.x, p.y, cv);
        const on = i === selRef.current;
        ctx.beginPath();
        ctx.arc(sx, sy, on ? 8 : 6, 0, Math.PI * 2);
        ctx.fillStyle = i === 0 ? "#22c55e" : i === list.length - 1 ? "#f59e0b" : on ? "#38bdf8" : "#0ea5e9";
        ctx.fill();
        ctx.strokeStyle = "#05080f";
        ctx.lineWidth = 1.5;
        ctx.stroke();
        ctx.fillStyle = "#fff";
        ctx.font = "10px sans-serif";
        ctx.textAlign = "center";
        ctx.fillText(String(i + 1), sx, sy + 3.5);
      });
    }

    // 比例尺
    const v = viewRef.current;
    const barM = niceLen(80 / v.scale);
    const barPx = barM * v.scale;
    ctx.strokeStyle = "#7dd3fc";
    ctx.lineWidth = 2;
    ctx.beginPath();
    ctx.moveTo(12, cv.height - 14);
    ctx.lineTo(12 + barPx, cv.height - 14);
    ctx.stroke();
    ctx.fillStyle = "#7dd3fc";
    ctx.font = "11px sans-serif";
    ctx.textAlign = "left";
    ctx.fillText(fmtLen(barM), 12, cv.height - 20);
  }, []);

  // pts / selected 变化重绘
  useEffect(() => {
    render();
  }, [pts, selected, render]);

  // 画布尺寸变化重绘
  useEffect(() => {
    const wrap = wrapRef.current;
    if (!wrap) return;
    const ro = new ResizeObserver(() => render());
    ro.observe(wrap);
    return () => ro.disconnect();
  }, [render]);

  const onMouseDown = (e: React.MouseEvent) => {
    if (e.button !== 0) return;
    dragRef.current = { x: e.clientX, y: e.clientY, moved: false };
  };
  const onMouseMove = (e: React.MouseEvent) => {
    const d = dragRef.current;
    if (!d) return;
    const dx = e.clientX - d.x;
    const dy = e.clientY - d.y;
    if (Math.abs(dx) + Math.abs(dy) > 4) d.moved = true;
    if (d.moved) {
      const v = viewRef.current;
      v.ox -= dx / v.scale;
      v.oy -= dy / v.scale;
      d.x = e.clientX;
      d.y = e.clientY;
      render();
    }
  };
  const onMouseUp = (e: React.MouseEvent) => {
    const d = dragRef.current;
    dragRef.current = null;
    if (!d || d.moved) return;
    const cv = canvasRef.current;
    if (!cv || modeRef.current === "none") return;
    const rect = cv.getBoundingClientRect();
    // 先看是否点到已有路点（选中）
    const px = e.clientX - rect.left;
    const py = e.clientY - rect.top;
    for (let i = 0; i < ptsRef.current.length; i++) {
      const [sx, sy] = world2screen(ptsRef.current[i].x, ptsRef.current[i].y, cv);
      if (Math.hypot(sx - px, sy - py) < 9) {
        onSelect(i);
        return;
      }
    }
    const [wx, wy] = screen2world(px, py, cv);
    onPick(round2(wx), round2(wy));
  };
  // 滚轮缩放：必须用原生非 passive 监听（React 合成 wheel 事件是 passive 的，
  // preventDefault 无效，触控板捏合会被浏览器拿去缩放整个页面）。
  useEffect(() => {
    const cv = canvasRef.current;
    if (!cv) return;
    const handler = (e: WheelEvent) => {
      e.preventDefault();
      const rect = cv.getBoundingClientRect();
      const px = e.clientX - rect.left;
      const py = e.clientY - rect.top;
      const [wx, wy] = screen2world(px, py, cv);
      const v = viewRef.current;
      const f = e.deltaY > 0 ? 0.85 : 1.18;
      v.scale = Math.min(4000, Math.max(0.005, v.scale * f));
      // 保持鼠标下的世界点不动
      v.ox = wx - (px - cv.width / 2) / v.scale;
      v.oy = wy - (py - cv.height / 2) / v.scale;
      render();
    };
    cv.addEventListener("wheel", handler, { passive: false });
    return () => cv.removeEventListener("wheel", handler);
  }, [render]);

  return (
    <div style={{ display: "flex", flexDirection: "column", minHeight: 0, flex: 1 }}>
      <div className="rmp-bar">
        {maps.length > 1 && (
          <select className="input pcv-sel" value={mapId} onChange={(e) => setMapId(e.target.value)}>
            {maps.map((m) => (
              <option key={m.id} value={m.id}>{m.name}</option>
            ))}
          </select>
        )}
        {maps.length === 1 && <span className="muted" style={{ fontSize: 12 }}>{maps[0].name}</span>}
        <span className="muted" style={{ fontSize: 11.5 }}>
          {mode === "3d" ? "3D 点云俯视 · 真实米坐标" : mode === "2d" ? "2D 栅格 · 按分辨率标定" : ""}
        </span>
        {mode === "2d" && (
          <label className="muted" style={{ fontSize: 11.5, display: "inline-flex", alignItems: "center", gap: 4 }}>
            分辨率(米/像素)
            <input
              className="input" style={{ width: 74 }} type="number" step={0.01} value={mpp}
              onChange={(e) => setMpp(parseFloat(e.target.value) || 0.05)}
            />
          </label>
        )}
        <span className="spacer" style={{ flex: 1 }} />
        <button className="btn small" onClick={() => { fitView(); render(); }}>适配</button>
        <span className="muted" style={{ fontSize: 11 }}>点击加点 · 拖动平移 · 滚轮缩放</span>
      </div>
      <div ref={wrapRef} style={{ flex: 1, minHeight: 220, position: "relative", borderRadius: 8, overflow: "hidden", border: "1px solid var(--border)" }}>
        <canvas
          ref={canvasRef}
          style={{ position: "absolute", inset: 0, width: "100%", height: "100%", cursor: "crosshair" }}
          onMouseDown={onMouseDown}
          onMouseMove={onMouseMove}
          onMouseUp={onMouseUp}
          onMouseLeave={() => (dragRef.current = null)}
        />
        {status && (
          <div style={{ position: "absolute", inset: 0, display: "flex", alignItems: "center", justifyContent: "center", color: "var(--text-dim)", fontSize: 12, pointerEvents: "none" }}>
            {status}
          </div>
        )}
      </div>
    </div>
  );
}

function niceLen(m: number): number {
  const pow = Math.pow(10, Math.floor(Math.log10(Math.max(1e-9, m))));
  const d = m / pow;
  return (d >= 5 ? 5 : d >= 2 ? 2 : 1) * pow;
}
function fmtLen(m: number): string {
  return m >= 1 ? m + " m" : Math.round(m * 100) + " cm";
}
