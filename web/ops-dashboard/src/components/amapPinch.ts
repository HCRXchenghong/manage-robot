// 高德地图不会处理浏览器送出的 Ctrl/Meta + wheel（触控板捏合）。
// 这里只补齐这一类事件：保持当前地图中心，连续更新缩放级别；普通滚轮仍交给高德原生逻辑。
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function attachAMapPinch(el: HTMLElement, map: any): () => void {
  let targetZoom = Number(map.getZoom?.() || 16);
  let lastApplyAt = 0;
  let settleTimer = 0;
  const syncZoom = () => {
    if (settleTimer) return;
    targetZoom = Number(map.getZoom?.() || targetZoom);
  };
  map.on?.("zoomend", syncZoom);
  const applyWheel = (raw: any) => {
    const e = raw?.originEvent || raw?.originalEvent || raw;
    if (!e?.ctrlKey && !e?.metaKey) return;
    e.preventDefault?.();
    e.stopPropagation?.();
    // 同一输入可能同时到达 DOM 与高德 mousewheel，短间隔去重，避免双倍缩放。
    const now = performance.now();
    if (now - lastApplyAt < 3) return;
    lastApplyAt = now;
    // 触控板会高频送出较小 delta；累计到 targetZoom 后连续变倍，避免逐格跳动。
    const deltaY = Number(e.deltaY ?? (e.wheelDelta != null ? -e.wheelDelta : raw?.deltaY) ?? 0);
    const delta = Math.max(-0.8, Math.min(0.8, -deltaY * 0.008));
    if (!delta) return;
    targetZoom = Math.max(3, Math.min(20, targetZoom + delta));
    map.setZoom(targetZoom, true);
    el.dataset.pinchZoom = targetZoom.toFixed(2);
    window.clearTimeout(settleTimer);
    settleTimer = window.setTimeout(() => {
      settleTimer = 0;
      targetZoom = Number(map.getZoom?.() || targetZoom);
    }, 240);
  };
  const onWheel = (e: WheelEvent) => applyWheel(e);
  const onMapWheel = (e: any) => applyWheel(e);
  el.addEventListener("wheel", onWheel, { passive: false, capture: true });
  map.on?.("mousewheel", onMapWheel);
  return () => {
    window.clearTimeout(settleTimer);
    el.removeEventListener("wheel", onWheel, true);
    map.off?.("mousewheel", onMapWheel);
    map.off?.("zoomend", syncZoom);
  };
}
