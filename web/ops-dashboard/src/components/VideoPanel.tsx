import { useEffect, useRef, useState } from "react";
import type { VehicleSnap } from "../types";

// 阶段 1：模拟画面（canvas 动效）。阶段 2 换 media-control 的 WebRTC 真实流，
// 组件接口不变（只换 <canvas> 为 <video>）。

interface Props {
  vehicle: VehicleSnap | null;
  height?: number;
}

export default function VideoPanel({ vehicle, height = 220 }: Props) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const [playing, setPlaying] = useState(true);
  const [camera, setCamera] = useState<"front" | "top">("front");
  const playingRef = useRef(playing);
  playingRef.current = playing;
  const cameraRef = useRef(camera);
  cameraRef.current = camera;

  useEffect(() => {
    const cv = canvasRef.current;
    if (!cv) return;
    const ctx = cv.getContext("2d");
    if (!ctx) return;
    let raf = 0;
    let t = 0;

    const draw = () => {
      const w = cv.width;
      const h = cv.height;
      if (playingRef.current) t += 1;
      // 天空 / 地面
      const horizon = cameraRef.current === "front" ? h * 0.45 : h * 0.2;
      const sky = ctx.createLinearGradient(0, 0, 0, horizon);
      sky.addColorStop(0, "#0a1a33");
      sky.addColorStop(1, "#12395e");
      ctx.fillStyle = sky;
      ctx.fillRect(0, 0, w, horizon);
      const ground = ctx.createLinearGradient(0, horizon, 0, h);
      ground.addColorStop(0, "#1b2c22");
      ground.addColorStop(1, "#0d1512");
      ctx.fillStyle = ground;
      ctx.fillRect(0, horizon, w, h - horizon);
      // 透视车道线
      ctx.strokeStyle = "rgba(220,230,247,0.55)";
      ctx.lineWidth = 2;
      for (let i = -3; i <= 3; i++) {
        ctx.beginPath();
        ctx.moveTo(w / 2 + i * w * 0.16, horizon);
        ctx.lineTo(w / 2 + i * w * 0.9, h);
        ctx.stroke();
      }
      // 移动的虚线路标
      ctx.strokeStyle = "rgba(56,189,248,0.8)";
      for (let k = 0; k < 6; k++) {
        const p = ((t * 4 + k * 90) % (h - horizon)) / (h - horizon);
        const y = horizon + p * (h - horizon);
        const scale = p;
        ctx.lineWidth = 2 + scale * 5;
        ctx.beginPath();
        ctx.moveTo(w / 2, y);
        ctx.lineTo(w / 2, y + 12 + scale * 26);
        ctx.stroke();
      }
      // 远处障碍框
      const bx = w / 2 + Math.sin(t / 60) * w * 0.1;
      ctx.fillStyle = "rgba(234,179,8,0.65)";
      ctx.fillRect(bx - 14, horizon - 16, 28, 16);
      // 时间戳水印
      ctx.fillStyle = "rgba(159,211,255,0.9)";
      ctx.font = "11px monospace";
      ctx.fillText(new Date().toISOString().replace("T", " ").slice(0, 19), 8, h - 8);
      raf = requestAnimationFrame(draw);
    };
    raf = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(raf);
  }, []);

  const vid = vehicle ? vehicle.vehicle_id : "sim-veh-001";

  const screenshot = () => {
    const cv = canvasRef.current;
    if (!cv) return;
    const a = document.createElement("a");
    a.href = cv.toDataURL("image/png");
    a.download = vid + "-" + Date.now() + ".png";
    a.click();
  };

  const fullscreen = () => {
    void canvasRef.current?.requestFullscreen();
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", minHeight: 0, flex: 1 }}>
      <div className="panel-title">
        <span>视频监控 · {vid}</span>
        <span className="hint">阶段 1 模拟画面，阶段 2 接 WebRTC 双流</span>
      </div>
      <div className="video-box">
        <canvas ref={canvasRef} width={640} height={height} />
        <span className="video-meta">{vid} · {camera === "front" ? "前向机位" : "俯视机位"}</span>
        <span className="video-live">{playing ? "LIVE" : "PAUSED"}</span>
      </div>
      <div className="btn-row mt">
        <button className="btn small" onClick={() => setPlaying((p) => !p)}>
          {playing ? "暂停" : "播放"}
        </button>
        <button className="btn small" onClick={fullscreen}>全屏</button>
        <button className="btn small" onClick={screenshot}>截图</button>
        <button className="btn small" onClick={() => setCamera((c) => (c === "front" ? "top" : "front"))}>
          切换机位
        </button>
      </div>
    </div>
  );
}
