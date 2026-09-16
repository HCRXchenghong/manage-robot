// 油门/刹车线性折线图（0-100% 双序列），canvas 轻量绘制。
// fill：画布尺寸跟随父容器（卡片内随窗口自适应）。
import { useEffect, useRef } from "react";

interface Props {
  a: number[]; // 油门 %
  b: number[]; // 刹车 %
  height?: number;
  fill?: boolean;
}

export default function DualLine({ a, b, height = 110, fill = false }: Props) {
  const ref = useRef<HTMLCanvasElement | null>(null);
  const boxRef = useRef<HTMLDivElement | null>(null);
  const dataRef = useRef<{ a: number[]; b: number[] }>({ a, b });
  dataRef.current = { a, b };
  const paintRef = useRef<() => void>(() => {});

  paintRef.current = () => {
    const cv = ref.current;
    if (!cv) return;
    const ctx = cv.getContext("2d");
    if (!ctx) return;
    const w = cv.width;
    const h = cv.height;
    const { a: da, b: db } = dataRef.current;
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
    draw(da, "#22c55e"); // 油门 绿
    draw(db, "#ef4444"); // 刹车 红
  };

  useEffect(() => {
    paintRef.current();
  }, [a, b]);

  // fill 模式：画布分辨率跟随容器并重绘
  useEffect(() => {
    if (!fill) return;
    const box = boxRef.current;
    const cv = ref.current;
    if (!box || !cv) return;
    const ro = new ResizeObserver(() => {
      const r = box.getBoundingClientRect();
      cv.width = Math.max(60, Math.floor(r.width));
      cv.height = Math.max(30, Math.floor(r.height));
      paintRef.current();
    });
    ro.observe(box);
    return () => ro.disconnect();
  }, [fill]);

  const legend = (
    <span style={{ display: "flex", gap: 12, fontSize: 10, color: "var(--text-dim)", marginTop: 2, flex: "0 0 auto" }}>
      <span><span style={{ color: "#22c55e" }}>—</span> 油门 %</span>
      <span><span style={{ color: "#ef4444" }}>—</span> 刹车 %</span>
    </span>
  );

  if (fill) {
    return (
      <div style={{ display: "flex", flexDirection: "column", flex: 1, minHeight: 0 }}>
        <div ref={boxRef} style={{ position: "relative", flex: 1, minHeight: 0 }}>
          <canvas
            ref={ref}
            style={{ position: "absolute", inset: 0, width: "100%", height: "100%", display: "block" }}
          />
        </div>
        {legend}
      </div>
    );
  }

  return (
    <span style={{ display: "block" }}>
      <canvas ref={ref} className="spark" width={560} height={height} />
      {legend}
    </span>
  );
}
