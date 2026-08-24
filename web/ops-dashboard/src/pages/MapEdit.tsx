// 地图编辑页（从「地图中心」点编辑进入，独立页面）。
// 做法对齐 SUSTechPOINTS/CVAT 的 2D 标注交互：多边形套索（点选加点、双击闭合），
// 擦除=把圈内格子涂成自由（白），恢复=圈内还原原图；原始数据只读，
// 保存时把最终 2D 图 + 操作记录提交 /api/maps/{id}/edit 落成新版本。
import { useCallback, useEffect, useRef, useState } from "react";
import { fetchMaps, mapFileURL, saveMapEdit } from "../api";
import type { MapEntry, MapVersion } from "../types";

interface Props {
  mapId: string;
  onBack: () => void;
}

type Tool = "erase" | "restore" | "pan";

interface Op {
  tool: "erase" | "restore";
  poly: [number, number][]; // 图像坐标
}

export default function MapEdit({ mapId, onBack }: Props) {
  const [entry, setEntry] = useState<MapEntry | null>(null);
  const [ver, setVer] = useState<MapVersion | null>(null);
  const [pngName, setPngName] = useState("");
  const [tool, setTool] = useState<Tool>("erase");
  const [ops, setOps] = useState<Op[]>([]);
  const [redo, setRedo] = useState<Op[]>([]);
  const [draft, setDraft] = useState<[number, number][] | null>(null);
  const [status, setStatus] = useState("加载中…");
  const [tick, setTick] = useState(0); // 视图变化触发重绘

  const canvasRef = useRef<HTMLCanvasElement>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const imgRef = useRef<HTMLImageElement | null>(null);
  const viewRef = useRef({ scale: 1, ox: 20, oy: 20 });
  const dragRef = useRef<{ x: number; y: number } | null>(null);
  const toolRef = useRef<Tool>(tool);
  toolRef.current = tool;

  // 载入地图条目 + 最新 PNG 版本
  useEffect(() => {
    let stop = false;
    void (async () => {
      try {
        const maps = await fetchMaps();
        const m = maps.find((x) => x.id === mapId);
        if (!m) {
          setStatus("地图不存在");
          return;
        }
        if (stop) return;
        setEntry(m);
        for (let i = m.versions.length - 1; i >= 0; i--) {
          const v = m.versions[i];
          const png = v.files.find((f) => f.endsWith(".png"));
          if (png) {
            setVer(v);
            setPngName(png);
            const img = new Image();
            img.onload = () => {
              imgRef.current = img;
              // 自适应缩放铺满画布
              const wrap = wrapRef.current;
              if (wrap) {
                const s = Math.min(
                  (wrap.clientWidth - 40) / img.width,
                  (wrap.clientHeight - 40) / img.height,
                );
                viewRef.current = {
                  scale: Math.max(0.05, Math.min(8, s)),
                  ox: 20, oy: 20,
                };
              }
              setStatus("");
              setTick((t) => t + 1);
            };
            img.onerror = () => setStatus("地图图像加载失败");
            img.src = mapFileURL(m.id, png, v.version);
            break;
          }
        }
      } catch {
        setStatus("加载失败（后端不可达？）");
      }
    })();
    return () => {
      stop = true;
    };
  }, [mapId]);

  // 重绘：原图 + 依次叠加操作（擦除=涂白，恢复=圈内还原原图）
  const draw = useCallback(() => {
    const cv = canvasRef.current;
    const img = imgRef.current;
    if (!cv) return;
    const wrap = wrapRef.current;
    if (wrap && (cv.width !== wrap.clientWidth || cv.height !== wrap.clientHeight)) {
      cv.width = wrap.clientWidth;
      cv.height = wrap.clientHeight;
    }
    const ctx = cv.getContext("2d");
    if (!ctx) return;
    const { scale, ox, oy } = viewRef.current;
    ctx.fillStyle = "#0b1220";
    ctx.fillRect(0, 0, cv.width, cv.height);
    if (!img) return;
    ctx.save();
    ctx.translate(ox, oy);
    ctx.scale(scale, scale);
    ctx.imageSmoothingEnabled = scale < 2;
    ctx.drawImage(img, 0, 0);
    for (const op of ops) {
      ctx.save();
      ctx.beginPath();
      op.poly.forEach(([x, y], i) => (i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y)));
      ctx.closePath();
      ctx.clip();
      if (op.tool === "erase") {
        ctx.fillStyle = "#ffffff";
        ctx.fillRect(0, 0, img.width, img.height);
      } else {
        ctx.drawImage(img, 0, 0);
      }
      ctx.restore();
    }
    ctx.restore();
    // 草稿多边形（屏幕空间叠画）
    if (draft && draft.length) {
      ctx.save();
      ctx.strokeStyle = tool === "erase" ? "#f87171" : "#4ade80";
      ctx.lineWidth = 1.5;
      ctx.setLineDash([6, 4]);
      ctx.beginPath();
      draft.forEach(([x, y], i) => {
        const sx = x * scale + ox;
        const sy = y * scale + oy;
        if (i === 0) ctx.moveTo(sx, sy);
        else ctx.lineTo(sx, sy);
      });
      ctx.stroke();
      ctx.setLineDash([]);
      for (const [x, y] of draft) {
        ctx.beginPath();
        ctx.arc(x * scale + ox, y * scale + oy, 3, 0, Math.PI * 2);
        ctx.fillStyle = tool === "erase" ? "#f87171" : "#4ade80";
        ctx.fill();
      }
      ctx.restore();
    }
  }, [ops, draft, tool, tick]);

  useEffect(() => {
    draw();
  }, [draw]);

  const screenToImg = (e: React.MouseEvent): [number, number] => {
    const cv = canvasRef.current;
    const rect = cv ? cv.getBoundingClientRect() : { left: 0, top: 0 };
    const { scale, ox, oy } = viewRef.current;
    return [(e.clientX - rect.left - ox) / scale, (e.clientY - rect.top - oy) / scale];
  };

  const closePoly = useCallback(() => {
    setDraft((d) => {
      if (d && d.length >= 3 && toolRef.current !== "pan") {
        const t = toolRef.current as "erase" | "restore";
        setOps((prev) => [...prev, { tool: t, poly: d }]);
        setRedo([]);
      }
      return null;
    });
  }, []);

  const onClick = (e: React.MouseEvent) => {
    if (tool === "pan") return;
    const p = screenToImg(e);
    setDraft((d) => {
      const arr = d ? d.slice() : [];
      // 点回到起点附近（屏幕 10px 内）→ 闭合
      if (arr.length >= 3) {
        const { scale, ox, oy } = viewRef.current;
        const [fx, fy] = arr[0];
        const dScreen = Math.hypot((p[0] - fx) * scale, (p[1] - fy) * scale);
        if (dScreen < 10) {
          window.setTimeout(closePoly, 0);
          return arr;
        }
      }
      arr.push(p);
      return arr;
    });
  };

  const onPointerDown = (e: React.PointerEvent) => {
    if (tool === "pan" || e.button === 1 || e.button === 2) {
      dragRef.current = { x: e.clientX, y: e.clientY };
      (e.target as HTMLElement).setPointerCapture(e.pointerId);
    }
  };

  const onPointerMove = (e: React.PointerEvent) => {
    const d = dragRef.current;
    if (!d) return;
    viewRef.current.ox += e.clientX - d.x;
    viewRef.current.oy += e.clientY - d.y;
    dragRef.current = { x: e.clientX, y: e.clientY };
    setTick((t) => t + 1);
  };

  const onPointerUp = () => {
    dragRef.current = null;
  };

  const onWheel = (e: React.WheelEvent) => {
    const cv = canvasRef.current;
    if (!cv) return;
    const rect = cv.getBoundingClientRect();
    const mx = e.clientX - rect.left;
    const my = e.clientY - rect.top;
    const v = viewRef.current;
    const k = e.deltaY < 0 ? 1.15 : 1 / 1.15;
    const ns = Math.max(0.05, Math.min(16, v.scale * k));
    v.ox = mx - ((mx - v.ox) / v.scale) * ns;
    v.oy = my - ((my - v.oy) / v.scale) * ns;
    v.scale = ns;
    setTick((t) => t + 1);
  };

  const undo = () => {
    setOps((prev) => {
      if (!prev.length) return prev;
      setRedo((r) => [...r, prev[prev.length - 1]]);
      return prev.slice(0, -1);
    });
  };

  const redoOp = () => {
    setRedo((r) => {
      if (!r.length) return r;
      setOps((prev) => [...prev, r[r.length - 1]]);
      return r.slice(0, -1);
    });
  };

  const save = async () => {
    const img = imgRef.current;
    if (!img) return;
    setStatus("保存中…");
    // 在原始分辨率上重放全部操作，产出最终 2D 图
    const off = document.createElement("canvas");
    off.width = img.width;
    off.height = img.height;
    const ctx = off.getContext("2d");
    if (!ctx) return;
    ctx.fillStyle = "#ffffff";
    ctx.fillRect(0, 0, off.width, off.height);
    ctx.drawImage(img, 0, 0);
    for (const op of ops) {
      ctx.save();
      ctx.beginPath();
      op.poly.forEach(([x, y], i) => (i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y)));
      ctx.closePath();
      ctx.clip();
      if (op.tool === "erase") {
        ctx.fillStyle = "#ffffff";
        ctx.fillRect(0, 0, off.width, off.height);
      } else {
        ctx.drawImage(img, 0, 0);
      }
      ctx.restore();
    }
    const dataUrl = off.toDataURL("image/png");
    try {
      await saveMapEdit(mapId, dataUrl.split(",")[1] || "", ops);
      setStatus("已保存为新版本（原始数据只读未动）");
      setOps([]);
      setRedo([]);
    } catch (e) {
      setStatus("保存失败：" + String(e));
    }
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <div className="row" style={{ padding: "10px 14px", borderBottom: "1px solid var(--border)", gap: 8, flexWrap: "wrap" }}>
        <button className="btn small" onClick={onBack}>← 返回地图中心</button>
        <span style={{ fontWeight: 700 }}>{entry ? entry.name : mapId}</span>
        {ver && <span className="muted">正在编辑 v{ver.version}（{ver.note}）</span>}
        <span style={{ flex: 1 }} />
        <button className={"btn small" + (tool === "erase" ? " primary" : "")} onClick={() => { setTool("erase"); setDraft(null); }}>
          套索擦除
        </button>
        <button className={"btn small" + (tool === "restore" ? " primary" : "")} onClick={() => { setTool("restore"); setDraft(null); }}>
          套索恢复
        </button>
        <button className={"btn small" + (tool === "pan" ? " primary" : "")} onClick={() => { setTool("pan"); setDraft(null); }}>
          平移
        </button>
        <button className="btn small" disabled={!ops.length} onClick={undo}>撤销</button>
        <button className="btn small" disabled={!redo.length} onClick={redoOp}>重做</button>
        <button className="btn small danger" disabled={!ops.length} onClick={() => { setOps([]); setRedo([]); }}>清空操作</button>
        <button className="btn small primary" disabled={!ops.length} onClick={() => void save()}>保存为新版本</button>
      </div>
      <div className="muted" style={{ padding: "6px 14px", fontSize: 11 }}>
        {tool === "pan"
          ? "拖拽平移，滚轮缩放"
          : "单击加点画多边形，双击或点回起点闭合；擦除=圈内变自由（白），恢复=圈内还原原图；滚轮缩放，右键/中键拖拽平移"}
        {status ? <span style={{ marginLeft: 12, color: "var(--accent)" }}>{status}</span> : null}
      </div>
      <div ref={wrapRef} style={{ flex: 1, minHeight: 0, position: "relative" }}>
        <canvas
          ref={canvasRef}
          style={{ position: "absolute", inset: 0, cursor: tool === "pan" ? "grab" : "crosshair" }}
          onClick={onClick}
          onDoubleClick={closePoly}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onWheel={onWheel}
        />
      </div>
    </div>
  );
}
