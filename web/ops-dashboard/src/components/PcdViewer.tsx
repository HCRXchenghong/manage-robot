// PCD/CSV 三维地图查看器（地图中心/地图编辑共用，三维二维统一口径）。
// 数据来自 /api/maps/{id}/points（服务端解析 PCD ascii/binary 与 CSV，可选降采样密度），
// 渲染思路对齐 Potree / CloudCompare：自适应点大小 + 高度/强度双着色 + 轨道控制 + 俯视/复位，
// 提供点数密度档位（标准/精细/极致）与点大小调节，解决"模糊看不清"。
import { useEffect, useRef, useState } from "react";
import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";
import { fetchMapPoints } from "../api";
import type { MapPointsResp } from "../types";

interface Props {
  mapId: string;
  version?: number; // 不传=最新 3D 版本
  height?: number | string;
}

type ColorMode = "height" | "intensity" | "flat";

const QUALITIES = [
  { id: "std", label: "标准 60万", max: 600000 },
  { id: "fine", label: "精细 150万", max: 1500000 },
  { id: "ultra", label: "极致 300万", max: 3000000 },
];

export default function PcdViewer({ mapId, version, height }: Props) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState("加载点云中…");
  const [meta, setMeta] = useState("");
  const [quality, setQuality] = useState("std");
  const [colorMode, setColorMode] = useState<ColorMode>("height");
  const [sizeMul, setSizeMul] = useState(1);

  // 渲染句柄（供工具条实时调整，不重建点云）
  const matRef = useRef<THREE.PointsMaterial | null>(null);
  const baseSizeRef = useRef(0.05);
  const sceneRef = useRef<{
    scene: THREE.Scene;
    camera: THREE.PerspectiveCamera;
    controls: OrbitControls;
    center: THREE.Vector3;
    radius: number;
  } | null>(null);
  const cloudRef = useRef<{ geo: THREE.BufferGeometry; inten: Float32Array | null; zmin: number; span: number } | null>(null);

  // 加载 + 重建场景（地图/版本/密度变化时）
  useEffect(() => {
    const wrap = wrapRef.current;
    if (!wrap) return;
    let dead = false;
    let raf = 0;
    let renderer: THREE.WebGLRenderer | null = null;
    let controls: OrbitControls | null = null;
    let ro: ResizeObserver | null = null;
    let points: THREE.Points | null = null;
    let grid: THREE.GridHelper | null = null;
    const max = QUALITIES.find((x) => x.id === quality)?.max || 600000;
    setStatus("加载点云中（" + (QUALITIES.find((x) => x.id === quality)?.label || "") + "）…");
    setMeta("");
    sceneRef.current = null;
    cloudRef.current = null;
    matRef.current = null;

    const scene = new THREE.Scene();
    scene.background = new THREE.Color("#0b1220");
    const camera = new THREE.PerspectiveCamera(50, 1, 0.1, 20000);
    let dirty = true; // 按需渲染：只在视角/内容变化时真正绘制，静态时不消耗 GPU

    void fetchMapPoints(mapId, version, max)
      .then((pc: MapPointsResp) => {
        if (dead || !wrap) return;
        const n = Math.floor(pc.positions.length / 3);
        if (!n) {
          setStatus("点云为空");
          return;
        }
        const pos = new Float32Array(n * 3);
        const inten = pc.intensities && pc.intensities.length === n ? new Float32Array(n) : null;
        let zmin = Infinity;
        let zmax = -Infinity;
        let imin = Infinity;
        let imax = -Infinity;
        for (let i = 0; i < n; i++) {
          const z = pc.positions[i * 3 + 2];
          if (z < zmin) zmin = z;
          if (z > zmax) zmax = z;
          if (inten) {
            const iv = pc.intensities[i];
            if (iv < imin) imin = iv;
            if (iv > imax) imax = iv;
          }
        }
        if (!Number.isFinite(zmin)) {
          zmin = 0;
          zmax = 1;
        }
        if (!Number.isFinite(imin) || imax - imin < 1e-9) {
          imin = 0;
          imax = 1;
        }
        // 点云 (x, y 地面, z 高度) -> three.js (x, z 高度, y)，与总览点云同口径
        let cx = 0;
        let cy = 0;
        let cz = 0;
        for (let i = 0; i < n; i++) {
          pos[i * 3] = pc.positions[i * 3];
          pos[i * 3 + 1] = pc.positions[i * 3 + 2];
          pos[i * 3 + 2] = pc.positions[i * 3 + 1];
          cx += pos[i * 3];
          cy += pos[i * 3 + 1];
          cz += pos[i * 3 + 2];
        }
        cx /= n;
        cy /= n;
        cz /= n;
        let radius = 1;
        for (let i = 0; i < n; i++) {
          const d = Math.hypot(pos[i * 3] - cx, pos[i * 3 + 1] - cy, pos[i * 3 + 2] - cz);
          if (d > radius) radius = d;
        }
        const g = new THREE.BufferGeometry();
        g.setAttribute("position", new THREE.BufferAttribute(pos, 3));
        cloudRef.current = { geo: g, inten, zmin, span: Math.max(1e-6, zmax - zmin) };

        // 自适应点大小：场景越大点越小（Potree 同思路），保证近看清晰远看整体
        const baseSize = Math.min(0.5, Math.max(0.02, radius / 900));
        baseSizeRef.current = baseSize;
        const mat = new THREE.PointsMaterial({
          size: baseSize * sizeMul,
          vertexColors: true,
          sizeAttenuation: true,
        });
        matRef.current = mat;
        applyColors(colorMode);
        points = new THREE.Points(g, mat);
        scene.add(points);
        grid = new THREE.GridHelper(radius * 2.4, 24, 0x24406e, 0x16233f);
        grid.position.set(cx, zmin - 0.02, cz);
        scene.add(grid);

        renderer = new THREE.WebGLRenderer({ antialias: true });
        renderer.setPixelRatio(Math.min(1.5, window.devicePixelRatio || 1));
        wrap.appendChild(renderer.domElement);
        const w0 = Math.max(1, wrap.clientWidth);
        const h0 = Math.max(1, wrap.clientHeight);
        renderer.setSize(w0, h0);
        camera.aspect = w0 / h0;
        camera.updateProjectionMatrix();
        camera.position.set(cx + radius * 0.9, cy + radius * 0.8, cz + radius * 0.9);
        controls = new OrbitControls(camera, renderer.domElement);
        controls.target.set(cx, cy, cz);
        controls.enableDamping = true;
        controls.addEventListener("change", () => {
          dirty = true;
        });
        controls.update();
        dirty = true;
        sceneRef.current = {
          scene, camera, controls,
          center: new THREE.Vector3(cx, cy, cz),
          radius,
        };

        const tick = () => {
          raf = requestAnimationFrame(tick);
          // damping 惯性动画由 update() 推进；有变化才真正渲染，静止时零 GPU 开销
          if (controls?.update() || dirty) {
            dirty = false;
            renderer?.render(scene, camera);
          }
        };
        tick();
        ro = new ResizeObserver(() => {
          if (!renderer || !wrap) return;
          const w = Math.max(1, wrap.clientWidth);
          const h = Math.max(1, wrap.clientHeight);
          renderer.setSize(w, h);
          camera.aspect = w / h;
          camera.updateProjectionMatrix();
          dirty = true;
        });
        ro.observe(wrap);
        setStatus("");
        setMeta(
          pc.kind.toUpperCase() + " · v" + pc.version + " · 显示 " + pc.count + " 点" +
            (pc.sampled ? "（共 " + pc.total + " 点已降采样）" : "（全量 " + pc.total + " 点）"),
        );
      })
      .catch((e: unknown) => {
        if (!dead) setStatus("3D 点云加载失败：" + String(e));
      });

    return () => {
      dead = true;
      cancelAnimationFrame(raf);
      ro?.disconnect();
      controls?.dispose();
      if (points) {
        scene.remove(points);
        points.geometry.dispose();
        (points.material as THREE.Material).dispose();
      }
      if (grid) {
        scene.remove(grid);
        grid.geometry.dispose();
        (grid.material as THREE.Material).dispose();
      }
      renderer?.dispose();
      if (renderer && renderer.domElement.parentElement === wrap) {
        wrap.removeChild(renderer.domElement);
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mapId, version, quality]);

  const applyColors = (mode: ColorMode) => {
    const c = cloudRef.current;
    if (!c) return;
    const attr = c.geo.getAttribute("position") as THREE.BufferAttribute;
    const n = attr.count;
    const col = new Float32Array(n * 3);
    const ispan = cloudIntenSpan();
    for (let i = 0; i < n; i++) {
      let t = 0.5;
      if (mode === "height") {
        // three.js 里高度在 y 分量（转换后）
        const z = attr.getY(i);
        t = Math.min(1, Math.max(0, (z - c.zmin) / c.span));
      } else if (mode === "intensity" && c.inten) {
        t = Math.min(1, Math.max(0, (c.inten[i] - ispan[0]) / ispan[1]));
      }
      if (mode === "flat") {
        col[i * 3] = 0.35;
        col[i * 3 + 1] = 0.75;
        col[i * 3 + 2] = 0.95;
      } else if (mode === "intensity" && c.inten) {
        // 强度：暗→亮（黑→青白），类 CloudCompare 强度渐变
        col[i * 3] = 0.05 + 0.9 * t;
        col[i * 3 + 1] = 0.1 + 0.85 * t;
        col[i * 3 + 2] = 0.18 + 0.8 * t;
      } else {
        // 高度：深蓝→青绿
        col[i * 3] = 0.05 + 0.4 * t;
        col[i * 3 + 1] = 0.16 + 0.6 * t;
        col[i * 3 + 2] = 0.35 + 0.6 * t;
      }
    }
    c.geo.setAttribute("color", new THREE.BufferAttribute(col, 3));
    const mat = matRef.current;
    if (mat) mat.needsUpdate = true;
  };

  const cloudIntenSpan = (): [number, number] => {
    const c = cloudRef.current;
    if (!c || !c.inten) return [0, 1];
    let imin = Infinity;
    let imax = -Infinity;
    for (let i = 0; i < c.inten.length; i++) {
      if (c.inten[i] < imin) imin = c.inten[i];
      if (c.inten[i] > imax) imax = c.inten[i];
    }
    if (!Number.isFinite(imin) || imax - imin < 1e-9) return [0, 1];
    return [imin, Math.max(1e-9, imax - imin)];
  };

  useEffect(() => {
    applyColors(colorMode);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [colorMode]);

  useEffect(() => {
    const mat = matRef.current;
    if (mat) {
      mat.size = baseSizeRef.current * sizeMul;
      mat.needsUpdate = true;
    }
  }, [sizeMul]);

  const topView = () => {
    const s = sceneRef.current;
    if (!s) return;
    s.camera.position.set(s.center.x, s.center.y + s.radius * 1.6, s.center.z + 0.001);
    s.controls.target.copy(s.center);
    s.controls.update();
  };

  const resetView = () => {
    const s = sceneRef.current;
    if (!s) return;
    s.camera.position.set(
      s.center.x + s.radius * 0.9,
      s.center.y + s.radius * 0.8,
      s.center.z + s.radius * 0.9,
    );
    s.controls.target.copy(s.center);
    s.controls.update();
  };

  const hasInten = cloudRef.current?.inten != null;

  return (
    <div style={{ position: "relative", height: height ?? "60vh", minHeight: 200 }}>
      <div ref={wrapRef} style={{ position: "absolute", inset: 0 }} />

      {/* 工具条：密度/着色/点大小/视角 */}
      <div className="pcv-bar">
        <select className="input pcv-sel" value={quality} onChange={(e) => setQuality(e.target.value)} title="点云密度（越大越清晰，加载越慢）">
          {QUALITIES.map((x) => (
            <option key={x.id} value={x.id}>{x.label}</option>
          ))}
        </select>
        <select className="input pcv-sel" value={colorMode} onChange={(e) => setColorMode(e.target.value as ColorMode)} title="着色模式">
          <option value="height">高度着色</option>
          <option value="intensity" disabled={!hasInten}>强度着色{hasInten ? "" : "（无强度）"}</option>
          <option value="flat">单色</option>
        </select>
        <label className="pcv-size" title="点大小">
          点大小
          <input type="range" min={0.4} max={4} step={0.1} value={sizeMul} onChange={(e) => setSizeMul(Number(e.target.value))} />
        </label>
        <button className="btn small" onClick={topView}>俯视</button>
        <button className="btn small" onClick={resetView}>复位</button>
      </div>

      {status && (
        <div
          style={{
            position: "absolute", inset: 0, display: "flex",
            alignItems: "center", justifyContent: "center",
            color: "var(--text-dim)", fontSize: 12, pointerEvents: "none",
          }}
        >
          {status}
        </div>
      )}
      {meta && (
        <div
          style={{
            position: "absolute", left: 8, bottom: 6, fontSize: 11, color: "var(--text-dim)",
            background: "rgba(11,18,32,.72)", padding: "2px 8px", borderRadius: 4, pointerEvents: "none",
          }}
        >
          {meta}
        </div>
      )}
      <div style={{ position: "absolute", right: 8, bottom: 6, fontSize: 11, color: "var(--text-dim)", pointerEvents: "none" }}>
        拖拽旋转 · 滚轮缩放 · 右键平移
      </div>
    </div>
  );
}
