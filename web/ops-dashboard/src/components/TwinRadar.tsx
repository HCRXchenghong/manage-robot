// 激光点云（雷达式俯视图）：借鉴桌面数字孪生项目的 RadarDetection。
// 数据走真实接口 /api/pointcloud；世界坐标 → 自车坐标系（车头朝上），2s 轮询。
import { useEffect, useRef, useState } from "react";
import { fetchPointCloud } from "../api";
import type { VehicleSnap } from "../types";

const RANGE = 30; // 视野半径（米）

export default function TwinRadar({ vehicle }: { vehicle: VehicleSnap }) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const [count, setCount] = useState(0);

  useEffect(() => {
    let alive = true;
    const draw = async () => {
      try {
        const pc = await fetchPointCloud(vehicle.vehicle_id);
        if (!alive) return;
        const canvas = canvasRef.current;
        if (!canvas) return;
        const ctx = canvas.getContext("2d");
        if (!ctx) return;
        const W = canvas.width;
        const H = canvas.height;
        ctx.fillStyle = "#04070d";
        ctx.fillRect(0, 0, W, H);
        const cx = W / 2;
        const cy = H / 2;
        const scale = (Math.min(W, H) / 2 - 10) / RANGE;

        // 距离环 + 十字线
        ctx.strokeStyle = "rgba(56,189,248,.22)";
        ctx.lineWidth = 1;
        for (let r = 10; r <= RANGE; r += 10) {
          ctx.beginPath();
          ctx.arc(cx, cy, r * scale, 0, Math.PI * 2);
          ctx.stroke();
        }
        ctx.beginPath();
        ctx.moveTo(cx - RANGE * scale, cy);
        ctx.lineTo(cx + RANGE * scale, cy);
        ctx.moveTo(cx, cy - RANGE * scale);
        ctx.lineTo(cx, cy + RANGE * scale);
        ctx.stroke();

        // 世界坐标 → 自车坐标（车头朝上）
        const yaw = vehicle.pose.yaw;
        const cos = Math.cos(-yaw);
        const sin = Math.sin(-yaw);
        const px = vehicle.pose.x;
        const py = vehicle.pose.y;
        const plot = (x: number, y: number, color: string, size: number) => {
          const dx = x - px;
          const dy = y - py;
          const ex = dx * cos - dy * sin;
          const ey = dx * sin + dy * cos;
          if (Math.abs(ex) > RANGE || Math.abs(ey) > RANGE) return;
          ctx.fillStyle = color;
          ctx.fillRect(cx + ex * scale, cy - ey * scale, size, size);
        };

        let n = 0;
        const st = pc.static;
        for (let i = 0; i < st.count; i++) {
          plot(st.positions[i * 3], st.positions[i * 3 + 1], "rgba(148,197,255,.55)", 1.6);
          n++;
        }
        for (const vc of pc.vehicles) {
          const col = vc.vehicle_id === vehicle.vehicle_id ? "#4ade80" : "#f87171";
          for (let i = 0; i < vc.count; i++) {
            plot(vc.positions[i * 3], vc.positions[i * 3 + 1], col, 2);
            n++;
          }
        }
        // 自车标记
        ctx.fillStyle = "#38bdf8";
        ctx.fillRect(cx - 2, cy - 4, 4, 8);
        setCount(n);
      } catch {
        /* 后端未就绪保留旧帧 */
      }
    };
    void draw();
    const t = window.setInterval(() => void draw(), 2000);
    return () => {
      alive = false;
      window.clearInterval(t);
    };
  }, [vehicle.vehicle_id, vehicle.pose.x, vehicle.pose.y, vehicle.pose.yaw]);

  return (
    <div>
      <div className="twin-card-t" style={{ display: "flex", justifyContent: "space-between" }}>
        <span>激光点云</span>
        <span className="mono" style={{ color: "#38bdf8", fontWeight: 400 }}>{count} pts · 0.5 Hz</span>
      </div>
      <canvas ref={canvasRef} width={300} height={220} style={{ width: "100%", borderRadius: 8, background: "#04070d", border: "1px solid var(--border)" }} />
    </div>
  );
}
