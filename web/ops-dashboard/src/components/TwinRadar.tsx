// 激光点云（雷达式俯视图）：借鉴桌面数字孪生项目的 RadarDetection。
// 数据走真实接口 /api/pointcloud；世界坐标 → 自车坐标系（车头朝上），2s 轮询。
// fill：画布分辨率跟随父容器（ResizeObserver），栅格卡片 / 弹窗里随窗口自适应。
import { useEffect, useRef, useState } from "react";
import { fetchPointCloud } from "../api";
import type { VehicleSnap } from "../types";

const RANGE = 30; // 视野半径（米）

// large：弹窗放大视图用更大的画布分辨率；fill：自适应父容器尺寸
export default function TwinRadar({ vehicle, large, fill }: { vehicle: VehicleSnap; large?: boolean; fill?: boolean }) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const boxRef = useRef<HTMLDivElement | null>(null);
  const drawRef = useRef<() => void>(() => {});
  const [count, setCount] = useState(0);
  // 视野半径（米）：触控板捏合连续调节；点云数据缓存，缩放时立即重绘不重新请求
  const rangeRef = useRef(RANGE);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const pcRef = useRef<any>(null);

  // fill 模式：画布分辨率跟随容器，resize 后立即重绘
  useEffect(() => {
    if (!fill) return;
    const box = boxRef.current;
    const cv = canvasRef.current;
    if (!box || !cv) return;
    const ro = new ResizeObserver(() => {
      const r = box.getBoundingClientRect();
      cv.width = Math.max(80, Math.floor(r.width));
      cv.height = Math.max(80, Math.floor(r.height));
      drawRef.current();
    });
    ro.observe(box);
    return () => ro.disconnect();
  }, [fill]);

  useEffect(() => {
    let alive = true;
    const render = () => {
      const pc = pcRef.current;
      const canvas = canvasRef.current;
      if (!pc || !canvas) return;
      const ctx = canvas.getContext("2d");
      if (!ctx) return;
      const R = rangeRef.current;
      const W = canvas.width;
      const H = canvas.height;
      ctx.fillStyle = "#04070d";
      ctx.fillRect(0, 0, W, H);
      const cx = W / 2;
      const cy = H / 2;
      const scale = (Math.min(W, H) / 2 - 10) / R;

      // 距离环 + 十字线（环间距随视野半径自适应）
      const step = R >= 25 ? 10 : R >= 12 ? 5 : 2;
      ctx.strokeStyle = "rgba(56,189,248,.22)";
      ctx.lineWidth = 1;
      for (let r = step; r <= R; r += step) {
        ctx.beginPath();
        ctx.arc(cx, cy, r * scale, 0, Math.PI * 2);
        ctx.stroke();
      }
      ctx.beginPath();
      ctx.moveTo(cx - R * scale, cy);
      ctx.lineTo(cx + R * scale, cy);
      ctx.moveTo(cx, cy - R * scale);
      ctx.lineTo(cx, cy + R * scale);
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
        if (Math.abs(ex) > R || Math.abs(ey) > R) return;
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
    };
    const refresh = async () => {
      try {
        const pc = await fetchPointCloud(vehicle.vehicle_id);
        if (!alive) return;
        pcRef.current = pc;
        render();
      } catch {
        /* 后端未就绪保留旧帧 */
      }
    };
    drawRef.current = render;
    void refresh();
    const t = window.setInterval(() => void refresh(), 2000);
    return () => {
      alive = false;
      drawRef.current = () => {};
      window.clearInterval(t);
    };
  }, [vehicle.vehicle_id, vehicle.pose.x, vehicle.pose.y, vehicle.pose.yaw]);

  // 触控板捏合（wheel+ctrlKey）缩放视野半径：从缓存立即重绘，丝滑不掉帧
  useEffect(() => {
    const cv = canvasRef.current;
    if (!cv) return;
    const onWheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      e.stopPropagation();
      rangeRef.current = Math.min(60, Math.max(6, rangeRef.current * Math.exp(e.deltaY * 0.002)));
      drawRef.current();
    };
    cv.addEventListener("wheel", onWheel, { passive: false });
    return () => cv.removeEventListener("wheel", onWheel);
  }, []);

  if (fill) {
    return (
      <div style={{ display: "flex", flexDirection: "column", flex: 1, minHeight: 0, height: "100%" }}>
        <div ref={boxRef} style={{ position: "relative", flex: 1, minHeight: 0 }}>
          <canvas
            ref={canvasRef}
            style={{ position: "absolute", inset: 0, width: "100%", height: "100%", borderRadius: 8, background: "#04070d" }}
          />
          <span className="mono" style={{ position: "absolute", top: 6, right: 8, fontSize: 10.5, color: "#38bdf8", opacity: 0.9 }}>
            {count} pts · 0.5 Hz
          </span>
        </div>
      </div>
    );
  }

  return (
    <div>
      <div className="twin-card-t" style={{ display: "flex", justifyContent: "space-between" }}>
        <span>激光点云</span>
        <span className="mono" style={{ color: "#38bdf8", fontWeight: 400 }}>{count} pts · 0.5 Hz</span>
      </div>
      <canvas
        ref={canvasRef}
        width={large ? 680 : 300}
        height={large ? 480 : 220}
        style={{ width: "100%", borderRadius: 8, background: "#04070d" }}
      />
    </div>
  );
}
