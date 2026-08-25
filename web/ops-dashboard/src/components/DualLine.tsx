// 油门/刹车线性折线图（0-100% 双序列），canvas 轻量绘制。
import { useEffect, useRef } from "react";

interface Props {
  a: number[]; // 油门 %
  b: number[]; // 刹车 %
  height?: number;
}

export default function DualLine({ a, b, height = 110 }: Props) {
  const ref = useRef<HTMLCanvasElement | null>(null);

  useEffect(() => {
    const cv = ref.current;
    if (!cv) return;
    const ctx = cv.getContext("2d");
    if (!ctx) return;
    const w = cv.width;
    const h = cv.height;
    ctx.clearRect(0, 0, w, h);
    // 网格（25% 一档）
    ctx.strokeStyle = "rgba(34,48,79,0.8)";
    ctx.lineWidth = 1;
    for (let i = 1; i < 4; i++) {
      const y = (h / 4) * i;
      ctx.beginPath();
      ctx.moveTo(0, y);
      ctx.lineTo(w, y);
      ctx.stroke();
    }
    const draw = (data: number[], color: string) => {
      if (!data || data.length < 2) return;
      const step = w / Math.max(1, data.length - 1);
      ctx.beginPath();
      data.forEach((v, i) => {
        const x = i * step;
        const y = h - (Math.min(100, Math.max(0, v)) / 100) * (h - 6) - 3;
        if (i === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
      });
      ctx.strokeStyle = color;
      ctx.lineWidth = 1.6;
      ctx.stroke();
    };
    draw(a, "#22c55e"); // 油门 绿
    draw(b, "#ef4444"); // 刹车 红
  }, [a, b, height]);

  return (
    <span style={{ display: "block" }}>
      <canvas ref={ref} className="spark" width={560} height={height} />
      <span style={{ display: "flex", gap: 12, fontSize: 10, color: "var(--text-dim)", marginTop: 2 }}>
        <span><span style={{ color: "#22c55e" }}>—</span> 油门 %</span>
        <span><span style={{ color: "#ef4444" }}>—</span> 刹车 %</span>
      </span>
    </span>
  );
}
