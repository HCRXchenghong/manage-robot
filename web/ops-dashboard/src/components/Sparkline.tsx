import { useEffect, useRef } from "react";

// 车速历史曲线（150 点环），canvas 轻量绘制。
export default function Sparkline({ data, height = 110 }: { data: number[]; height?: number }) {
  const ref = useRef<HTMLCanvasElement | null>(null);

  useEffect(() => {
    const cv = ref.current;
    if (!cv) return;
    const ctx = cv.getContext("2d");
    if (!ctx) return;
    const w = cv.width;
    const h = cv.height;
    ctx.clearRect(0, 0, w, h);
    // 网格
    ctx.strokeStyle = "rgba(34,48,79,0.8)";
    ctx.lineWidth = 1;
    for (let i = 1; i < 4; i++) {
      const y = (h / 4) * i;
      ctx.beginPath();
      ctx.moveTo(0, y);
      ctx.lineTo(w, y);
      ctx.stroke();
    }
    if (data.length < 2) return;
    const max = Math.max(1, ...data) * 1.15;
    const step = w / Math.max(1, data.length - 1);
    // 面积
    ctx.beginPath();
    ctx.moveTo(0, h);
    data.forEach((v, i) => ctx.lineTo(i * step, h - (v / max) * (h - 8)));
    ctx.lineTo((data.length - 1) * step, h);
    ctx.closePath();
    const grad = ctx.createLinearGradient(0, 0, 0, h);
    grad.addColorStop(0, "rgba(56,189,248,0.35)");
    grad.addColorStop(1, "rgba(56,189,248,0.02)");
    ctx.fillStyle = grad;
    ctx.fill();
    // 线
    ctx.beginPath();
    data.forEach((v, i) => {
      const x = i * step;
      const y = h - (v / max) * (h - 8);
      if (i === 0) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    });
    ctx.strokeStyle = "#38bdf8";
    ctx.lineWidth = 1.6;
    ctx.stroke();
  }, [data, height]);

  return <canvas ref={ref} className="spark" width={560} height={height} />;
}
