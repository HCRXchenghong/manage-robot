// GPS 定位（孪生页小地图）：高德暗色底图 + WGS84→GCJ-02 纠偏，复用总览 GpsMap 的配置与算法。
// 未配置 key 或无定位时显示坐标占位，不影响页面。
import { useEffect, useRef } from "react";
import { amapCfg, wgs84ToGcj02 } from "./GpsMap";
import { attachAMapPinch } from "./amapPinch";
import type { VehicleSnap } from "../types";

type AMapNS = any;
let amapPromise: Promise<AMapNS> | null = null;
function loadAMap(key: string): Promise<AMapNS> {
  const w = window as any;
  if (w.AMap) return Promise.resolve(w.AMap);
  if (amapPromise) return amapPromise;
  amapPromise = new Promise((resolve, reject) => {
    const s = document.createElement("script");
    s.src = "https://webapi.amap.com/maps?v=2.0&key=" + key;
    s.onload = () => resolve(w.AMap);
    s.onerror = () => reject(new Error("amap load failed"));
    document.head.appendChild(s);
  });
  return amapPromise;
}

// height：卡片默认 150，弹窗放大时传更大值；fill：地图撑满父容器（随窗口自适应）
export default function TwinGps({ vehicle, height = 150, fill }: { vehicle: VehicleSnap; height?: number; fill?: boolean }) {
  const boxRef = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<AMapNS>(null);
  const markerRef = useRef<AMapNS>(null);
  const userMovedRef = useRef(false);
  const pinchRef = useRef<(() => void) | null>(null);
  const g = vehicle.gps;

  // 容器尺寸变化时通知高德重排（fill 模式下随窗口缩放）
  useEffect(() => {
    const box = boxRef.current;
    if (!box) return;
    let timer = 0;
    const ro = new ResizeObserver(() => {
      window.clearTimeout(timer);
      timer = window.setTimeout(() => {
        const m = mapRef.current;
        if (m && typeof m.resize === "function") m.resize();
      }, 120);
    });
    ro.observe(box);
    return () => {
      window.clearTimeout(timer);
      ro.disconnect();
    };
  }, []);

  useEffect(() => {
    let alive = true;
    userMovedRef.current = false;
    const cfg = amapCfg();
    if (!cfg.key || !g.fix || !boxRef.current) return;
    const [lng, lat] = wgs84ToGcj02(g.lon, g.lat);
    loadAMap(cfg.key)
      .then((AMap) => {
        if (!alive || !boxRef.current || mapRef.current) return;
        const map = new AMap.Map(boxRef.current, {
          mapStyle: "amap://styles/dark",
          zoom: 17,
          center: [lng, lat],
          dragEnable: true,
          scrollWheel: true,
          touchZoom: true,
          doubleClickZoom: true,
          animateEnable: true,
        });
        map.on("dragstart", () => {
          userMovedRef.current = true;
        });
        const mk = new AMap.Marker({ position: [lng, lat] });
        map.add(mk);
        mapRef.current = map;
        markerRef.current = mk;
        pinchRef.current = attachAMapPinch(boxRef.current, map);
      })
      .catch(() => undefined);
    return () => {
      alive = false;
      if (pinchRef.current) {
        pinchRef.current();
        pinchRef.current = null;
      }
      if (mapRef.current) {
        try {
          mapRef.current.destroy();
        } catch {
          /* 忽略 */
        }
        mapRef.current = null;
        markerRef.current = null;
      }
    };
    // 仅初始化一次（换车时重建）
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [vehicle.vehicle_id]);

  useEffect(() => {
    if (mapRef.current && markerRef.current && g.fix) {
      const [lng, lat] = wgs84ToGcj02(g.lon, g.lat);
      markerRef.current.setPosition([lng, lat]);
      if (!userMovedRef.current) mapRef.current.setCenter([lng, lat]);
    }
  }, [g.lon, g.lat, g.fix]);

  return (
    <div style={fill ? { display: "flex", flexDirection: "column", flex: 1, minHeight: 0, height: "100%" } : undefined}>
      <div className="twin-card-t" style={fill ? { flex: "0 0 auto" } : undefined}>GPS定位</div>
      <div
        ref={boxRef}
        style={
          fill
            ? { flex: 1, minHeight: 0, borderRadius: 8, overflow: "hidden", background: "#0a1020", position: "relative" }
            : { height, borderRadius: 8, overflow: "hidden", background: "#0a1020", position: "relative" }
        }
      >
        {!g.fix && (
          <div className="muted" style={{ position: "absolute", inset: 0, display: "grid", placeItems: "center" }}>
            无 GPS 定位
          </div>
        )}
      </div>
      <div className="mono" style={{ fontSize: 11, color: "#38bdf8", marginTop: fill ? 4 : 6, flex: "0 0 auto" }}>
        {g.fix ? g.lat.toFixed(6) + ", " + g.lon.toFixed(6) + " · 海拔 " + g.alt.toFixed(1) + " m" : "—"}
      </div>
    </div>
  );
}
