// 激光雷达点云地图（计划任务 4 重点）：
//  - 2D 鸟瞰 / 3D 轨道一键切换；拖拽旋转(3D)/平移(2D)、滚轮缩放、点大小可调
//  - 多车同屏：每车点云按 pose 放入统一世界系；颜色按状态
//    （绿=行驶、黄=空闲、灰=离线、红=告警）；静态基础设施为暗蓝
//  - 数据源可配置：默认 /api/pointcloud；「配置点云」可本地加载
//    JSON/CSV（每行 x,y,z[,intensity]），替换静态场景、保留车辆
//  - THREE.Points + BufferGeometry；超过 10 万点自动降采样
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Canvas, useFrame, useThree } from "@react-three/fiber";
import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";
import type { FleetSnap, PointCloudResp, Pose } from "../types";
import { fetchPointCloud } from "../api";
import { vehicleStatusOf } from "./VehicleTable";

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
function buildGeometry(positions: number[], rgb: [number, number, number]): THREE.BufferGeometry {
  const n = Math.floor(positions.length / 3);
  const stride = n > MAX_POINTS ? Math.ceil(n / MAX_POINTS) : 1;
  const count = Math.floor(n / stride);
  const pos = new Float32Array(count * 3);
  const col = new Float32Array(count * 3);
  let j = 0;
  for (let i = 0; i < n; i += stride) {
    pos[j * 3] = positions[i * 3];
    pos[j * 3 + 1] = positions[i * 3 + 2];
    pos[j * 3 + 2] = positions[i * 3 + 1];
    col[j * 3] = rgb[0];
    col[j * 3 + 1] = rgb[1];
    col[j * 3 + 2] = rgb[2];
    j++;
  }
  const g = new THREE.BufferGeometry();
  g.setAttribute("position", new THREE.BufferAttribute(pos, 3));
  g.setAttribute("color", new THREE.BufferAttribute(col, 3));
  return g;
}

// 命令式创建相机并 set({camera})，避免内置相机元素的 makeDefault 类型问题；
// 2D 用正交俯视，3D 用透视轨道。
function Rig({ mode, follow }: { mode: ViewMode; follow: THREE.Vector3 | null }) {
  const { gl, set, size } = useThree();
  const ref = useRef<OrbitControls | null>(null);

  useEffect(() => {
    let cam: THREE.PerspectiveCamera | THREE.OrthographicCamera;
    if (mode === "3d") {
      const p = new THREE.PerspectiveCamera(50, size.width / Math.max(1, size.height), 1, 4000);
      p.up.set(0, 1, 0);
      p.position.set(320, 260, 320);
      cam = p;
    } else {
      const aspect = size.width / Math.max(1, size.height);
      const d = 135;
      const o = new THREE.OrthographicCamera(-d * aspect, d * aspect, d, -d, 1, 4000);
      o.up.set(0, 0, -1);
      o.position.set(SCENE_CENTER.x, 460, SCENE_CENTER.z);
      o.zoom = 1;
      cam = o;
    }
    const c = new OrbitControls(cam, gl.domElement);
    c.enableDamping = true;
    c.dampingFactor = 0.12;
    c.target.copy(SCENE_CENTER);
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
  }, [mode, gl, set, size.width, size.height]);

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
}

export default function LidarView({ snap, selectedId, onSelect }: Props) {
  const [mode, setMode] = useState<ViewMode>("3d");
  const [pointSize, setPointSize] = useState(2);
  const [follow, setFollow] = useState(false);
  const [cloud, setCloud] = useState<PointCloudResp | null>(null);
  const [customStatic, setCustomStatic] = useState<{ positions: number[]; label: string } | null>(null);
  const [error, setError] = useState("");
  const [rigKey, setRigKey] = useState(0);
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
    return buildGeometry(src, STATIC_RGB);
  }, [cloud, customStatic]);

  const vehicleGeos = useMemo(() => {
    if (!cloud) return [];
    return cloud.vehicles.map((vc) => {
      const v = snap.vehicles.find((x) => x.vehicle_id === vc.vehicle_id);
      const status = v ? vehicleStatusOf(v) : "offline";
      return {
        id: vc.vehicle_id,
        status,
        geo: buildGeometry(vc.positions, STATUS_RGB[status]),
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
          <Rig key={mode + ":" + rigKey} mode={mode} follow={followVec} />
          {staticGeo && (
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
        </div>
        <div className="lidar-hint">
          {mode === "3d" ? "拖拽旋转 · 滚轮缩放 · 点击锥体选车" : "拖拽平移 · 滚轮缩放 · 点击锥体选车"}
          {selectedVehicle ? " · 选中 " + selectedVehicle.vehicle_id : ""}
        </div>
        {error && <div className="lidar-hint" style={{ top: 10, bottom: "auto", color: "#fca5a5" }}>{error}</div>}
      </div>
    </div>
  );
}
