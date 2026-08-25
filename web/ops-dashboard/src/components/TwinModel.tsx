// 数字孪生中心模型：优先加载车辆自定义 GLB（/api/vehicles/{id}/model），404 回退平台默认模型。
// 借鉴桌面「数字孪生」项目的 Robot3D：自动居中缩放 + 轨道控制 + 四角取景框。
import { Suspense, useEffect, useRef, useState } from "react";
import { Canvas } from "@react-three/fiber";
import { OrbitControls, useGLTF } from "@react-three/drei";
import * as THREE from "three";

function Model({ url, powered }: { url: string; powered: boolean }) {
  const { scene } = useGLTF(url);
  const ref = useRef<THREE.Group>(null);
  const scaled = useRef(false);

  // 重新计算包围盒：居中 + 自动缩放适配场景
  useEffect(() => {
    if (!scene || scaled.current) return;
    scene.traverse((c) => {
      const m = c as THREE.Mesh;
      if (m.isMesh && m.geometry) {
        m.geometry.computeBoundingBox();
        m.geometry.computeBoundingSphere();
      }
    });
    const box = new THREE.Box3().setFromObject(scene);
    const center = box.getCenter(new THREE.Vector3());
    const size = box.getSize(new THREE.Vector3());
    scene.position.sub(center);
    const maxDim = Math.max(size.x, size.y, size.z) || 1;
    const s = 2.4 / maxDim;
    if (ref.current) ref.current.scale.set(s, s, s);
    scaled.current = true;
  }, [scene]);

  // 上电白色 / 断电灰色
  useEffect(() => {
    if (!scene) return;
    scene.traverse((c) => {
      const m = c as THREE.Mesh;
      if (m.isMesh && m.material) {
        const mats = Array.isArray(m.material) ? m.material : [m.material];
        for (const mat of mats) {
          const sm = mat as THREE.MeshStandardMaterial;
          if (sm.color) sm.color.setHex(powered ? 0xffffff : 0x666666);
        }
      }
    });
  }, [scene, powered]);

  return (
    <group ref={ref} position={[0, -1.2, 0]}>
      <primitive object={scene} rotation={[Math.PI / 2, 0, 0]} />
    </group>
  );
}

export default function TwinModel({ vehicleId, powered }: { vehicleId: string; powered: boolean }) {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    setUrl(null);
    const custom = "/api/vehicles/" + encodeURIComponent(vehicleId) + "/model";
    fetch(custom)
      .then((r) => {
        if (!alive) return;
        setUrl(r.ok ? custom : "/models/default.glb");
      })
      .catch(() => {
        if (alive) setUrl("/models/default.glb");
      });
    return () => {
      alive = false;
    };
  }, [vehicleId]);

  return (
    <div className="twin-model">
      <Canvas
        camera={{ position: [5, 1.5, 5], fov: 40 }}
        dpr={[1, 2]}
        gl={{ antialias: true, alpha: true }}
      >
        <ambientLight intensity={powered ? 0.6 : 0.25} />
        <directionalLight position={[10, 10, 5]} intensity={powered ? 1 : 0.4} />
        <pointLight position={[-10, -10, -5]} intensity={0.4} color="#00ffff" />
        {url && (
          <Suspense fallback={null}>
            <Model url={url} powered={powered} />
          </Suspense>
        )}
        <OrbitControls enablePan target={[0, -0.6, 0]} minDistance={1.5} maxDistance={12} />
      </Canvas>
      <div className="model-overlay">
        <div className="overlay-corner top-left"></div>
        <div className="overlay-corner top-right"></div>
        <div className="overlay-corner bottom-left"></div>
        <div className="overlay-corner bottom-right"></div>
      </div>
      {!url && (
        <div className="muted" style={{ position: "absolute", inset: 0, display: "grid", placeItems: "center" }}>
          模型加载中…
        </div>
      )}
    </div>
  );
}
