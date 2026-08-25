// GPS 定位与行驶轨迹（借鉴自用户 1.html）：高德暗色底图 + WGS84→GCJ-02 纠偏，
// 每车一个标记 + 一条轨迹线；key/安全密钥在「设置-地图与底图」配置，保存后实时重绘。
import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import type { FleetSnap } from "../types";
import { vehicleStatusOf } from "./VehicleTable";

const STATUS_CSS: Record<string, string> = {
  driving: "#22c55e",
  idle: "#eab308",
  offline: "#6b7280",
  alert: "#ef4444",
};

const DEFAULT_KEY = "8fc7b0efe1e18f1eb1fa5f56edab6d4c";
const DEFAULT_SEC = "912ca5fe79cd5964d78df0a923116a6f";

export function amapCfg(): { key: string; sec: string } {
  let key = "";
  let sec = "";
  try {
    key = window.localStorage.getItem("ra-cfg-amap-key") || "";
    sec = window.localStorage.getItem("ra-cfg-amap-sec") || "";
  } catch {
    /* 忽略 */
  }
  return { key: key || DEFAULT_KEY, sec: sec || DEFAULT_SEC };
}

/* ---- WGS-84 -> GCJ-02（高德使用 GCJ-02；中国大陆以外原样返回） ---- */
const GCJ_A = 6378245.0;
const GCJ_EE = 0.00669342162296594323;
function outOfChina(lng: number, lat: number) {
  return lng < 72.004 || lng > 137.8347 || lat < 0.8293 || lat > 55.8271;
}
function tLat(x: number, y: number) {
  let r = -100 + 2 * x + 3 * y + 0.2 * y * y + 0.1 * x * y + 0.2 * Math.sqrt(Math.abs(x));
  r += ((20 * Math.sin(6 * x * Math.PI) + 20 * Math.sin(2 * x * Math.PI)) * 2) / 3;
  r += ((20 * Math.sin(y * Math.PI) + 40 * Math.sin(y / 3 * Math.PI)) * 2) / 3;
  r += ((160 * Math.sin(y / 12 * Math.PI) + 320 * Math.sin(y / 30 * Math.PI)) * 2) / 3;
  return r;
}
function tLng(x: number, y: number) {
  let r = 300 + x + 2 * y + 0.1 * x * x + 0.1 * x * y + 0.1 * Math.sqrt(Math.abs(x));
  r += ((20 * Math.sin(6 * x * Math.PI) + 20 * Math.sin(2 * x * Math.PI)) * 2) / 3;
  r += ((20 * Math.sin(x * Math.PI) + 40 * Math.sin(x / 3 * Math.PI)) * 2) / 3;
  r += ((150 * Math.sin(x / 12 * Math.PI) + 300 * Math.sin(x / 30 * Math.PI)) * 2) / 3;
  return r;
}
export function wgs84ToGcj02(lng: number, lat: number): [number, number] {
  if (outOfChina(lng, lat)) return [lng, lat];
  let dLat = tLat(lng - 105, lat - 35);
  let dLng = tLng(lng - 105, lat - 35);
  const radLat = (lat / 180) * Math.PI;
  let magic = Math.sin(radLat);
  magic = 1 - GCJ_EE * magic * magic;
  const sm = Math.sqrt(magic);
  dLat = (dLat * 180) / ((GCJ_A * (1 - GCJ_EE) / (magic * sm)) * Math.PI);
  dLng = (dLng * 180) / ((GCJ_A / sm) * Math.cos(radLat) * Math.PI);
  return [lng + dLng, lat + dLat];
}

interface Props {
  snap: FleetSnap;
  selectedId?: string | null;
  onSelect?: (id: string) => void;
  overlayLeft?: ReactNode;
  overlayRight?: ReactNode;
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AM = any;

export default function GpsMap({ snap, selectedId, onSelect, overlayLeft, overlayRight }: Props) {
  const boxRef = useRef<HTMLDivElement | null>(null);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const mapRef = useRef<AM>(null);
  const markersRef = useRef<Record<string, AM>>({});
  const linesRef = useRef<Record<string, AM>>({});
  const ptsRef = useRef<Record<string, [number, number][]>>({});
  const readyRef = useRef(false);
  const lastDragRef = useRef(0);
  const [follow, setFollow] = useState(true);
  const followRef = useRef(follow);
  followRef.current = follow;
  const [msg, setMsg] = useState("");
  const [coordText, setCoordText] = useState("无定位");
  const selectedRef = useRef(selectedId);
  selectedRef.current = selectedId;
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;

  // 初始化 / key 变化时重建
  useEffect(() => {
    let disposed = false;
    const init = () => {
      if (disposed || !boxRef.current) return;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const AMap = (window as any).AMap;
      if (typeof AMap === "undefined") {
        setMsg("高德地图初始化失败：请检查 Key 与安全密钥是否匹配（设置 → 地图与底图）");
        return;
      }
      const map = new AMap.Map(boxRef.current, {
        zoom: 16,
        center: [116.397, 39.908],
        viewMode: "2D",
        mapStyle: "amap://styles/dark",
      });
      map.on("dragstart", () => (lastDragRef.current = Date.now()));
      mapRef.current = map;
      readyRef.current = true;
      ptsRef.current = {};
      setMsg("");
    };
    const cfg = amapCfg();
    const keyOk = cfg.key.length >= 10 && cfg.sec.length >= 10;
    if (!keyOk) {
      setMsg("尚未配置高德地图 key：请到 设置 → 地图与底图 填写 Key 与安全密钥");
      return () => {
        disposed = true;
      };
    }
    // 先销毁旧实例，再（重新）加载 SDK
    if (mapRef.current) {
      try {
        mapRef.current.destroy();
      } catch {
        /* 忽略 */
      }
      mapRef.current = null;
      markersRef.current = {};
      linesRef.current = {};
    }
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    (window as any)._AMapSecurityConfig = { securityJsCode: cfg.sec };
    const s = document.createElement("script");
    s.src = "https://webapi.amap.com/maps?v=2.0&key=" + cfg.key;
    s.onload = () => init();
    s.onerror = () => setMsg("高德地图 SDK 加载失败：请检查网络或 Key 是否正确");
    document.head.appendChild(s);
    const onCfg = () => {
      // 实时修改 key：销毁后走一次重载流程
      if (mapRef.current) {
        try {
          mapRef.current.destroy();
        } catch {
          /* 忽略 */
        }
        mapRef.current = null;
        markersRef.current = {};
        linesRef.current = {};
      }
      const c2 = amapCfg();
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (window as any)._AMapSecurityConfig = { securityJsCode: c2.sec };
      const s2 = document.createElement("script");
      s2.src = "https://webapi.amap.com/maps?v=2.0&key=" + c2.key;
      s2.onload = () => init();
      document.head.appendChild(s2);
    };
    window.addEventListener("ra-cfg-changed", onCfg);
    return () => {
      disposed = true;
      window.removeEventListener("ra-cfg-changed", onCfg);
      if (mapRef.current) {
        try {
          mapRef.current.destroy();
        } catch {
          /* 忽略 */
        }
        mapRef.current = null;
      }
    };
  }, []);

  // 遥测驱动：标记 + 轨迹
  useEffect(() => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const AMap = (window as any).AMap;
    const map = mapRef.current;
    if (!readyRef.current || !map || typeof AMap === "undefined") return;
    let focus: [number, number] | null = null;
    for (const v of snap.vehicles) {
      if (!v.gps || !v.gps.fix) continue;
      const p = wgs84ToGcj02(v.gps.lon, v.gps.lat);
      const color = STATUS_CSS[vehicleStatusOf(v)] || "#38bdf8";
      // 轨迹
      const pts = ptsRef.current[v.vehicle_id] || (ptsRef.current[v.vehicle_id] = []);
      const last = pts[pts.length - 1];
      if (!last || Math.abs(last[0] - p[0]) > 1e-8 || Math.abs(last[1] - p[1]) > 1e-8) {
        pts.push(p);
        if (pts.length > 3000) pts.shift();
        let line = linesRef.current[v.vehicle_id];
        if (!line) {
          line = new AMap.Polyline({
            path: pts,
            strokeColor: color,
            strokeWeight: 3,
            strokeOpacity: 0.9,
            lineJoin: "round",
          });
          map.add(line);
          linesRef.current[v.vehicle_id] = line;
        } else {
          line.setPath(pts);
        }
      }
      // 标记
      let mk = markersRef.current[v.vehicle_id];
      if (!mk) {
        const el = document.createElement("div");
        el.className = "gps-mark";
        el.style.background = color;
        el.title = v.vehicle_id;
        el.onclick = () => onSelectRef.current && onSelectRef.current(v.vehicle_id);
        mk = new AMap.Marker({ content: el, position: p, offset: new AMap.Pixel(-8, -8), zIndex: 120 });
        map.add(mk);
        markersRef.current[v.vehicle_id] = mk;
      } else {
        mk.setPosition(p);
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        (mk.getContent() as HTMLElement | null)?.style &&
          ((mk.getContent() as HTMLElement).style.background = color);
      }
      if (v.vehicle_id === selectedRef.current) focus = p;
      setCoordText(v.gps.lat.toFixed(6) + ", " + v.gps.lon.toFixed(6) + " · 海拔 " + v.gps.alt.toFixed(1) + " m");
    }
    if (focus && followRef.current && Date.now() - lastDragRef.current > 8000) {
      map.setCenter(focus);
    }
  }, [snap]);

  return (
    <div className="lidar-wrap">
      <div className="lidar-toolbar">
        <div className="tabs">
          <button className="tab active">GPS 轨迹</button>
        </div>
        <span className="muted" style={{ fontSize: 11 }}>{coordText}</span>
        <span className="spacer" />
        <label className="btn small" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
          跟随选中车辆
        </label>
      </div>
      <div className="lidar-canvas">
        <div ref={boxRef} className="gps-map" />
        {msg && (
          <div className="lidar-hint" style={{ top: 10, left: "50%", transform: "translateX(-50%)", color: "#fca5a5", pointerEvents: "auto" }}>
            {msg}
          </div>
        )}
        {overlayLeft && <div className="map-overlay left">{overlayLeft}</div>}
        {overlayRight && <div className="map-overlay right">{overlayRight}</div>}
      </div>
    </div>
  );
}
