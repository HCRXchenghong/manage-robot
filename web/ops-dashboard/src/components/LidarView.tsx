// 激光雷达点云地图（计划任务 4 重点）：
//  - 2D 鸟瞰 / 3D 轨道一键切换；拖拽旋转(3D)/平移(2D)、滚轮缩放、点大小可调
//  - 多车同屏：每车点云按 pose 放入统一世界系；颜色按状态
//    （绿=行驶、黄=空闲、灰=离线、红=告警）；静态基础设施为暗蓝
//  - 数据源可配置：默认 /api/pointcloud；「配置点云」可本地加载
//    JSON/CSV（每行 x,y,z[,intensity]），替换静态场景、保留车辆
//    加载自定义地图后相机自动取景（包围盒居中），静态点按高度渐变着色
//  - 自动 3D→2D：BEV 高度切片投影生成占据网格（bev.ts，做法对齐 octomap_server），
//    可导出 ROS map_server 三件套（PNG + PGM + YAML）
//  - THREE.Points + BufferGeometry；超过 10 万点自动降采样
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Canvas, useFrame, useThree } from "@react-three/fiber";
import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";
import type { FleetSnap, PointCloudResp, Pose } from "../types";
import { fetchPointCloud } from "../api";
import { vehicleStatusOf } from "./VehicleTable";
import { bevColor, buildBev } from "./bev";

type ViewMode = "2d" | "3d";

const STATUS_RGB: Record<string, [number, number, number]> = {
  driving: [0.13, 0.77, 0.37],
  idle: [0.92, 0.7, 0.03],
  offline: [0.42, 0.45, 0.5],
  alert: [0.94, 0.27, 0.27],
};
const STATIC_RGB: [number, number, number] = [0.12, 0.29, 0.48];
const STATUS_CSS: Record<string, string> = {
  driving: "#22c55e",
  idle: "#eab308",
  offline: "#6b7280",
  alert: "#ef4444",
};

const MAX_POINTS = 100000;
const SCENE_CENTER = new THREE.Vector3(120, 0, 90);

// 点云数据 (x, y 地面, z 高度) -> three.js (x, z->y 高度, y->z)
function buildGeometry(
  positions: number[],
  rgb: [number, number, number],
  colored: boolean,
): THREE.BufferGeometry {
  const n = Math.floor(positions.length / 3);
  const stride = n > MAX_POINTS ? Math.ceil(n / MAX_POINTS) : 1;
  const count = Math.floor(n / stride);
  const pos = new Float32Array(count * 3);
  const col = new Float32Array(count * 3);
  let zmin = Infinity;
  let zmax = -Infinity;
  if (colored) {
    for (let i = 0; i < n; i++) {
      const h = positions[i * 3 + 2];
      if (h < zmin) zmin = h;
      if (h > zmax) zmax = h;
    }
    if (!Number.isFinite(zmin)) {
      zmin = 0;
      zmax = 1;
    }
  }
  let j = 0;
  for (let i = 0; i < n; i += stride) {
    pos[j * 3] = positions[i * 3];
    pos[j * 3 + 1] = positions[i * 3 + 2];
    pos[j * 3 + 2] = positions[i * 3 + 1];
    if (colored) {
      const span = Math.max(1e-6, zmax - zmin);
      const t = Math.min(1, Math.max(0, (positions[i * 3 + 2] - zmin) / span));
      col[j * 3] = 0.05 + 0.4 * t;
      col[j * 3 + 1] = 0.16 + 0.6 * t;
      col[j * 3 + 2] = 0.35 + 0.6 * t;
    } else {
      col[j * 3] = rgb[0];
      col[j * 3 + 1] = rgb[1];
      col[j * 3 + 2] = rgb[2];
    }
    j++;
  }
  const g = new THREE.BufferGeometry();
  g.setAttribute("position", new THREE.BufferAttribute(pos, 3));
  g.setAttribute("color", new THREE.BufferAttribute(col, 3));
  return g;
}

// 命令式创建相机并 set({camera})，避免内置相机元素的 makeDefault 类型问题；
// 2D 用正交俯视，3D 用透视轨道。
function Rig({
  mode,
  follow,
  center,
  radius,
}: {
  mode: ViewMode;
  follow: THREE.Vector3 | null;
  center: THREE.Vector3;
  radius: number;
}) {
  const { gl, set, size } = useThree();
  const ref = useRef<OrbitControls | null>(null);

  useEffect(() => {
    let cam: THREE.PerspectiveCamera | THREE.OrthographicCamera;
    if (mode === "3d") {
      const p = new THREE.PerspectiveCamera(50, size.width / Math.max(1, size.height), 0.1, 8000);
      p.up.set(0, 1, 0);
      p.position.set(center.x + radius * 0.95, center.y + radius * 1.15, center.z + radius * 0.95);
      cam = p;
    } else {
      const aspect = size.width / Math.max(1, size.height);
      const d = Math.max(12, radius * 1.1);
      const o = new THREE.OrthographicCamera(-d * aspect, d * aspect, d, -d, 1, 8000);
      o.up.set(0, 0, -1);
      o.position.set(center.x, center.y + radius * 3 + 60, center.z);
      o.zoom = 1;
      cam = o;
    }
    const c = new OrbitControls(cam, gl.domElement);
    c.enableDamping = true;
    c.dampingFactor = 0.12;
    c.target.copy(center);
    c.minDistance = 1;
    c.maxDistance = radius * 12;
    if (mode === "2d") {
      c.enableRotate = false;
      c.screenSpacePanning = true;
    }
    cam.lookAt(c.target);
    c.update();
    set({ camera: cam });
    ref.current = c;
    return () => {
      ref.current = null;
      c.dispose();
    };
  }, [mode, gl, set, size.width, size.height, center, radius]);

  useEffect(() => {
    if (follow && ref.current) ref.current.target.copy(follow);
  }, [follow]);

  useFrame(() => {
    ref.current?.update();
  });
  return null;
}

function VehicleMarker(props: {
  pose: Pose;
  color: string;
  selected: boolean;
  onClick: () => void;
}) {
  const { pose, color, selected, onClick } = props;
  return (
    <group position={[pose.x, 0, pose.y]} rotation={[0, Math.PI / 2 - pose.yaw, 0]}>
      <mesh rotation={[Math.PI / 2, 0, 0]} position={[0, 1.4, 0]} onClick={onClick}>
        <coneGeometry args={[1.3, 3.2, 14]} />
        <meshStandardMaterial color={color} emissive={color} emissiveIntensity={0.35} />
      </mesh>
      {selected && (
        <mesh rotation={[-Math.PI / 2, 0, 0]} position={[0, 0.15, 0]}>
          <ringGeometry args={[3.1, 3.9, 40]} />
          <meshBasicMaterial color="#38bdf8" side={THREE.DoubleSide} transparent opacity={0.9} />
        </mesh>
      )}
    </group>
  );
}

interface Props {
  snap: FleetSnap;
  selectedId?: string | null;
  onSelect?: (id: string) => void;
  // 地图内悬浮卡片（总览大屏）：左=车辆列表/告警与事件，右=详情/接管/终端
  overlayLeft?: ReactNode;
  overlayRight?: ReactNode;
}

export default function LidarView({ snap, selectedId, onSelect, overlayLeft, overlayRight }: Props) {
  const [mode, setMode] = useState<ViewMode>("3d");
  const [pointSize, setPointSize] = useState(2);
  const [follow, setFollow] = useState(false);
  const [cloud, setCloud] = useState<PointCloudResp | null>(null);
  const [customStatic, setCustomStatic] = useState<{ positions: number[]; label: string } | null>(null);
  const [error, setError] = useState("");
  const [rigKey, setRigKey] = useState(0);
  const [gridOn, setGridOn] = useState(true);
  const [cellSize, setCellSize] = useState(0.2);
  const fileRef = useRef<HTMLInputElement | null>(null);

  const load = useCallback(() => {
    fetchPointCloud()
      .then((c) => {
        setCloud(c);
        setError("");
      })
      .catch((e) => setError("点云加载失败（后端不可达时地图仅显示车辆标记）：" + String(e)));
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const staticGeo = useMemo(() => {
    const src = customStatic ? customStatic.positions : cloud ? cloud.static.positions : null;
    if (!src || src.length < 3) return null;
    return buildGeometry(src, STATIC_RGB, true);
  }, [cloud, customStatic]);

  // 视图取景：自定义地图按其包围盒自动居中取景；否则沿用默认合成场景中心
  const viewFit = useMemo(() => {
    const center = SCENE_CENTER.clone();
    let radius = 150;
    if (customStatic && customStatic.positions.length >= 3) {
      const p = customStatic.positions;
      let x0 = Infinity;
      let x1 = -Infinity;
      let y0 = Infinity;
      let y1 = -Infinity;
      const n = Math.floor(p.length / 3);
      for (let i = 0; i < n; i++) {
        const x = p[i * 3];
        const y = p[i * 3 + 1];
        if (x < x0) x0 = x;
        if (x > x1) x1 = x;
        if (y < y0) y0 = y;
        if (y > y1) y1 = y;
      }
      if (Number.isFinite(x0)) {
        center.set((x0 + x1) / 2, 0, (y0 + y1) / 2);
        radius = Math.max(10, Math.hypot(x1 - x0, y1 - y0) / 2);
      }
    }
    return { center, radius };
  }, [customStatic]);

  // 自动 3D→2D：当前点云投影为 BEV 占据网格（做法见 bev.ts）
  const bev = useMemo(() => {
    const src = customStatic ? customStatic.positions : cloud ? cloud.static.positions : null;
    if (!src || src.length < 3) return null;
    return buildBev(src, cellSize);
  }, [cloud, customStatic, cellSize]);

  const gridTexture = useMemo(() => {
    if (!bev) return null;
    const canvas = document.createElement("canvas");
    canvas.width = bev.nx;
    canvas.height = bev.ny;
    const ctx = canvas.getContext("2d");
    if (!ctx) return null;
    const img = ctx.createImageData(bev.nx, bev.ny);
    const span = Math.max(1e-6, bev.z1 - bev.z0);
    for (let iy = 0; iy < bev.ny; iy++) {
      for (let ix = 0; ix < bev.nx; ix++) {
        const idx = iy * bev.nx + ix;
        const p = idx * 4;
        if (bev.occ[idx]) {
          const t = Math.min(1, Math.max(0, (bev.maxz[idx] - bev.z0) / span));
          const rgb = bevColor(t);
          img.data[p] = Math.round(rgb[0] * 255);
          img.data[p + 1] = Math.round(rgb[1] * 255);
          img.data[p + 2] = Math.round(rgb[2] * 255);
          img.data[p + 3] = 255;
        }
      }
    }
    ctx.putImageData(img, 0, 0);
    const tex = new THREE.CanvasTexture(canvas);
    tex.magFilter = THREE.NearestFilter;
    tex.minFilter = THREE.LinearMipmapLinearFilter;
    return tex;
  }, [bev]);

  useEffect(() => {
    return () => {
      gridTexture?.dispose();
    };
  }, [gridTexture]);

  // 2D 取景：缩放到「占据区域」包围盒，避免稀疏离群点把网格衬得看不见
  const fitOcc = useMemo(() => {
    if (!bev || bev.occupiedCells === 0) return null;
    const center = new THREE.Vector3((bev.fx0 + bev.fx1) / 2, 0, (bev.fy0 + bev.fy1) / 2);
    const radius = Math.max(6, (Math.hypot(bev.fx1 - bev.fx0, bev.fy1 - bev.fy0) / 2) * 1.1);
    return { center, radius };
  }, [bev]);

  const fit = mode === "2d" && fitOcc ? fitOcc : viewFit;

  const vehicleGeos = useMemo(() => {
    if (!cloud) return [];
    return cloud.vehicles.map((vc) => {
      const v = snap.vehicles.find((x) => x.vehicle_id === vc.vehicle_id);
      const status = v ? vehicleStatusOf(v) : "offline";
      return {
        id: vc.vehicle_id,
        status,
        geo: buildGeometry(vc.positions, STATUS_RGB[status], false),
      };
    });
  }, [cloud, snap.vehicles]);

  // 旧几何释放
  useEffect(() => {
    return () => {
      staticGeo?.dispose();
      vehicleGeos.forEach((v) => v.geo.dispose());
    };
  }, [staticGeo, vehicleGeos]);

  const followVec = useMemo(() => {
    if (!follow || !selectedId) return null;
    const v = snap.vehicles.find((x) => x.vehicle_id === selectedId);
    if (!v) return null;
    return new THREE.Vector3(v.pose.x, 0, v.pose.y);
  }, [follow, selectedId, snap.vehicles]);

  const onFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files && e.target.files[0];
    e.target.value = "";
    if (!f) return;
    const txt = await f.text();
    const positions: number[] = [];
    if (f.name.toLowerCase().endsWith(".json")) {
      try {
        const j = JSON.parse(txt);
        const src = Array.isArray(j) ? j : j.positions || (j.static && j.static.positions);
        if (Array.isArray(src)) {
          if (typeof src[0] === "number") {
            for (const num of src) positions.push(num);
          } else {
            for (const row of src) {
              if (Array.isArray(row)) positions.push(row[0], row[1], row[2]);
              else positions.push(row.x, row.y, row.z);
            }
          }
        }
      } catch {
        setError("JSON 解析失败");
        return;
      }
    } else {
      for (const line of txt.split(/\r?\n/)) {
        const parts = line.trim().split(/[,;\t ]+/).map(Number);
        if (parts.length >= 3 && Number.isFinite(parts[0]) && Number.isFinite(parts[1]) && Number.isFinite(parts[2])) {
          positions.push(parts[0], parts[1], parts[2]);
        }
      }
    }
    if (positions.length >= 3) {
      setCustomStatic({ positions, label: f.name });
      setError("");
    } else {
      setError("未解析出有效点：JSON 需含 positions 数组，CSV 每行 x,y,z[,intensity]");
    }
  };

  const selectedVehicle = snap.vehicles.find((v) => v.vehicle_id === selectedId) || null;

  // 导出 ROS map_server 三件套：PNG（人看）+ PGM + YAML（车端导航栈直接吃）
  const exportBev = () => {
    if (!bev) return;
    const base =
      (customStatic ? customStatic.label.replace(/\.[^.]+$/, "") : "synthetic-map") + "-bev";
    const S = 4;
    const canvas = document.createElement("canvas");
    canvas.width = bev.nx * S;
    canvas.height = bev.ny * S;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    ctx.fillStyle = "#0b1220";
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    const span = Math.max(1e-6, bev.z1 - bev.z0);
    for (let iy = 0; iy < bev.ny; iy++) {
      for (let ix = 0; ix < bev.nx; ix++) {
        const idx = iy * bev.nx + ix;
        if (!bev.occ[idx]) continue;
        const t = Math.min(1, Math.max(0, (bev.maxz[idx] - bev.z0) / span));
        const rgb = bevColor(t);
        ctx.fillStyle =
          "rgb(" +
          Math.round(rgb[0] * 255) +
          "," +
          Math.round(rgb[1] * 255) +
          "," +
          Math.round(rgb[2] * 255) +
          ")";
        ctx.fillRect(ix * S, iy * S, S, S);
      }
    }
    const dl = (name: string, blob: Blob) => {
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = name;
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 5000);
    };
    canvas.toBlob((b) => {
      if (b) dl(base + ".png", b);
    });
    // PGM P5（map_server 约定：0=占据 205=未知 254=空闲）
    const raw = new Uint8Array(bev.nx * bev.ny);
    for (let i = 0; i < raw.length; i++) raw[i] = bev.occ[i] ? 0 : 205;
    const header = "P5\n" + bev.nx + " " + bev.ny + "\n255\n";
    const pgm = new Uint8Array(header.length + raw.length);
    for (let i = 0; i < header.length; i++) pgm[i] = header.charCodeAt(i);
    pgm.set(raw, header.length);
    dl(base + ".pgm", new Blob([pgm.buffer], { type: "image/x-portable-graymap" }));
    const yaml =
      "image: " + base + ".pgm\n" +
      "resolution: " + bev.cell + "\n" +
      "origin: [" + bev.x0.toFixed(3) + ", " + bev.y0.toFixed(3) + ", 0.000]\n" +
      "negate: 0\n" +
      "occupied_thresh: 0.65\n" +
      "free_thresh: 0.196\n";
    dl(base + ".yaml", new Blob([yaml], { type: "text/yaml" }));
  };

  return (
    <div className="lidar-wrap">
      <div className="lidar-toolbar">
        <div className="tabs">
          <button className={"tab" + (mode === "2d" ? " active" : "")} onClick={() => setMode("2d")}>2D 鸟瞰</button>
          <button className={"tab" + (mode === "3d" ? " active" : "")} onClick={() => setMode("3d")}>3D 轨道</button>
        </div>
        <span className="muted" style={{ fontSize: 11 }}>点大小</span>
        <input type="range" min={1} max={6} step={0.5} value={pointSize} onChange={(e) => setPointSize(Number(e.target.value))} />
        <label className="btn small" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
          视角跟随选中
        </label>
        <label className="btn small" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
          <input type="checkbox" checked={gridOn} onChange={(e) => setGridOn(e.target.checked)} />
          2D 网格（3D 自动生成）
        </label>
        <select className="btn small" value={String(cellSize)} onChange={(e) => setCellSize(Number(e.target.value))}>
          <option value="0.1">格 0.1m</option>
          <option value="0.2">格 0.2m</option>
          <option value="0.5">格 0.5m</option>
        </select>
        <button className="btn small" onClick={exportBev} disabled={!bev}>导出 2D 地图</button>
        <span className="spacer" />
        {customStatic && (
          <button className="btn small" onClick={() => setCustomStatic(null)}>
            恢复默认场景（当前：{customStatic.label}）
          </button>
        )}
        <button className="btn small" onClick={() => fileRef.current?.click()}>配置点云</button>
        <input ref={fileRef} type="file" accept=".json,.csv,.txt" style={{ display: "none" }} onChange={(e) => void onFile(e)} />
        <button className="btn small" onClick={load}>刷新</button>
        <button className="btn small" onClick={() => setRigKey((k) => k + 1)}>复位视角</button>
      </div>
      <div className="lidar-canvas">
        <Canvas dpr={[1, 1.5]} gl={{ antialias: true }}>
          <color attach="background" args={["#060b16"]} />
          <ambientLight intensity={0.7} />
          <directionalLight position={[200, 300, 100]} intensity={0.8} />
          <Rig
            key={mode + ":" + rigKey + ":" + fit.radius.toFixed(1)}
            mode={mode}
            follow={followVec}
            center={fit.center}
            radius={fit.radius}
          />
          {staticGeo && !(gridOn && bev && mode === "2d") && (
            <points geometry={staticGeo}>
              <pointsMaterial
                vertexColors
                size={mode === "2d" ? pointSize : pointSize * 0.45}
                sizeAttenuation={mode === "3d"}
                transparent
                opacity={0.92}
              />
            </points>
          )}
          {bev && gridOn && gridTexture && (
            <mesh
              rotation={[-Math.PI / 2, 0, 0]}
              position={[bev.x0 + (bev.nx * bev.cell) / 2, 0.05, bev.y0 + (bev.ny * bev.cell) / 2]}
            >
              <planeGeometry args={[bev.nx * bev.cell, bev.ny * bev.cell]} />
              <meshBasicMaterial map={gridTexture} transparent depthWrite={false} />
            </mesh>
          )}
          {vehicleGeos.map((vc) => (
            <points key={vc.id} geometry={vc.geo}>
              <pointsMaterial
                vertexColors
                size={mode === "2d" ? pointSize + 0.5 : (pointSize + 0.5) * 0.45}
                sizeAttenuation={mode === "3d"}
                transparent
                opacity={selectedId && selectedId !== vc.id ? 0.45 : 1}
              />
            </points>
          ))}
          {snap.vehicles.map((v) => (
            <VehicleMarker
              key={v.vehicle_id}
              pose={v.pose}
              color={STATUS_CSS[vehicleStatusOf(v)]}
              selected={v.vehicle_id === selectedId}
              onClick={() => onSelect && onSelect(v.vehicle_id)}
            />
          ))}
        </Canvas>
        <div className="lidar-legend">
          <div className="legend-row"><span className="legend-chip" style={{ background: "#22c55e" }} />在线 · 行驶</div>
          <div className="legend-row"><span className="legend-chip" style={{ background: "#eab308" }} />在线 · 空闲</div>
          <div className="legend-row"><span className="legend-chip" style={{ background: "#6b7280" }} />离线</div>
          <div className="legend-row"><span className="legend-chip" style={{ background: "#ef4444" }} />告警（最小风险）</div>
          <div className="legend-row"><span className="legend-chip" style={{ background: "#1e4a7a" }} />静态基础设施</div>
          {bev && gridOn && (
            <div className="legend-row">
              网格 {bev.nx}×{bev.ny} · 占据 {bev.occupiedCells} · 切片 {bev.z0.toFixed(1)}~{bev.z1.toFixed(1)}m
            </div>
          )}
        </div>
        <div className="lidar-hint">
          {mode === "3d" ? "拖拽旋转 · 滚轮缩放 · 点击锥体选车" : "拖拽平移 · 滚轮缩放 · 点击锥体选车"}
          {selectedVehicle ? " · 选中 " + selectedVehicle.vehicle_id : ""}
        </div>
        {error && (
          <div
            className="lidar-hint"
            style={{ top: 10, left: 10, right: "auto", bottom: "auto", transform: "none", color: "#fca5a5", zIndex: 8 }}
          >
            {error}
          </div>
        )}
        {overlayLeft && <div className="map-overlay left">{overlayLeft}</div>}
        {overlayRight && <div className="map-overlay right">{overlayRight}</div>}
      </div>
    </div>
  );
}
