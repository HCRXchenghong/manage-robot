// GPS 定位（孪生页小地图）：高德暗色底图 + WGS84→GCJ-02 纠偏，复用总览 GpsMap 的配置与算法。
// 未配置 key 或无定位时显示坐标占位，不影响页面。
import { useEffect, useRef } from "react";
import { amapCfg, wgs84ToGcj02 } from "./GpsMap";
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

export default function TwinGps({ vehicle }: { vehicle: VehicleSnap }) {
  const boxRef = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<AMapNS>(null);
  const markerRef = useRef<AMapNS>(null);
  const g = vehicle.gps;

  useEffect(() => {
    let alive = true;
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
        });
        const mk = new AMap.Marker({ position: [lng, lat] });
        map.add(mk);
        mapRef.current = map;
        markerRef.current = mk;
      })
      .catch(() => undefined);
    return () => {
      alive = false;
    };
    // 仅初始化一次（换车时重建）
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [vehicle.vehicle_id]);

  useEffect(() => {
    if (mapRef.current && markerRef.current && g.fix) {
      const [lng, lat] = wgs84ToGcj02(g.lon, g.lat);
      markerRef.current.setPosition([lng, lat]);
      mapRef.current.setCenter([lng, lat]);
    }
  }, [g.lon, g.lat, g.fix]);

  return (
    <div>
      <div className="twin-card-t">GPS定位</div>
      <div
        ref={boxRef}
        style={{ height: 150, borderRadius: 8, overflow: "hidden", border: "1px solid var(--border)", background: "#0a1020", position: "relative" }}
      >
        {!g.fix && (
          <div className="muted" style={{ position: "absolute", inset: 0, display: "grid", placeItems: "center" }}>
            无 GPS 定位
          </div>
        )}
      </div>
      <div className="mono" style={{ fontSize: 11, color: "#38bdf8", marginTop: 6 }}>
        {g.fix ? g.lat.toFixed(6) + ", " + g.lon.toFixed(6) + " · 海拔 " + g.alt.toFixed(1) + " m" : "—"}
      </div>
    </div>
  );
}
