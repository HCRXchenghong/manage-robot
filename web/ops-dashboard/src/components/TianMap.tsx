import { useEffect, useRef, useState } from "react";
import {
  divIcon,
  latLng,
  map as createMap,
  marker,
  polyline,
  tileLayer,
  type Map as LMap,
  type Marker,
  type Polyline,
  type TileLayer,
} from "leaflet";
import "leaflet/dist/leaflet.css";
import type { VehicleSnap } from "../types";

// 底部地图：天地图 WMTS 服务（配置 tk 后启用）；未配置 tk 时降级 OSM 底图演示。
// 车辆坐标（米）以可配置原点映射为经纬度，标记随遥测移动并留轨迹。

interface Origin {
  lat: number;
  lon: number;
}

function toLL(o: Origin, x: number, y: number) {
  const dLat = y / 111320;
  const dLon = x / (111320 * Math.cos((o.lat * Math.PI) / 180));
  return latLng(o.lat + dLat, o.lon + dLon);
}

function loadOrigin(): Origin {
  try {
    const j = JSON.parse(localStorage.getItem("tdt_origin") || "");
    if (typeof j.lat === "number" && typeof j.lon === "number") return j as Origin;
  } catch {
    // ignore
  }
  return { lat: 39.9042, lon: 116.4074 };
}

export default function TianMap({ vehicle }: { vehicle: VehicleSnap | null }) {
  const boxRef = useRef<HTMLDivElement | null>(null);
  const mapRef = useRef<LMap | null>(null);
  const markerRef = useRef<Marker | null>(null);
  const trailRef = useRef<Polyline | null>(null);
  const trailPts = useRef<ReturnType<typeof latLng>[]>([]);
  const tilesRef = useRef<TileLayer[]>([]);
  const [tk, setTk] = useState(localStorage.getItem("tdt_tk") || "");
  const [tkInput, setTkInput] = useState(localStorage.getItem("tdt_tk") || "");
  const [origin, setOrigin] = useState<Origin>(loadOrigin);

  useEffect(() => {
    if (!boxRef.current || mapRef.current) return;
    const m = createMap(boxRef.current, { zoomControl: true, attributionControl: false });
    m.setView([origin.lat, origin.lon], 18);
    mapRef.current = m;
    const t = window.setTimeout(() => m.invalidateSize(), 150);
    return () => {
      window.clearTimeout(t);
      m.remove();
      mapRef.current = null;
      markerRef.current = null;
      trailRef.current = null;
      tilesRef.current = [];
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const m = mapRef.current;
    if (!m) return;
    tilesRef.current.forEach((t) => t.remove());
    tilesRef.current = [];
    const subs = ["0", "1", "2", "3", "4", "5", "6", "7"];
    if (tk.trim()) {
      const key = tk.trim();
      const vec = tileLayer(
        "https://t{s}.tianditu.gov.cn/vec_w/wmts?SERVICE=WMTS&REQUEST=GetTile&VERSION=1.0.0&LAYER=vec&STYLE=default&TILEMATRIXSET=w&FORMAT=tiles&TILECOL={x}&TILEROW={y}&TILEMATRIX={z}&tk=" + key,
        { subdomains: subs, maxZoom: 18 },
      );
      const cva = tileLayer(
        "https://t{s}.tianditu.gov.cn/cva_w/wmts?SERVICE=WMTS&REQUEST=GetTile&VERSION=1.0.0&LAYER=cva&STYLE=default&TILEMATRIXSET=w&FORMAT=tiles&TILECOL={x}&TILEROW={y}&TILEMATRIX={z}&tk=" + key,
        { subdomains: subs, maxZoom: 18 },
      );
      vec.addTo(m);
      cva.addTo(m);
      tilesRef.current = [vec, cva];
    } else {
      const osm = tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", { maxZoom: 19 });
      osm.addTo(m);
      tilesRef.current = [osm];
    }
  }, [tk]);

  useEffect(() => {
    const m = mapRef.current;
    if (!m || !vehicle) return;
    const ll = toLL(origin, vehicle.pose.x, vehicle.pose.y);
    const yawDeg = (vehicle.pose.yaw * 180) / Math.PI;
    const icon = divIcon({
      className: "veh-divicon",
      html: '<div class="veh-arrow" style="transform: rotate(' + yawDeg.toFixed(0) + 'deg)">▲</div>',
      iconSize: [22, 22],
      iconAnchor: [11, 11],
    });
    if (!markerRef.current) {
      markerRef.current = marker(ll, { icon }).addTo(m);
    } else {
      markerRef.current.setLatLng(ll);
      markerRef.current.setIcon(icon);
    }
    const last = trailPts.current[trailPts.current.length - 1];
    if (!last || last.distanceTo(ll) > 1) {
      trailPts.current.push(ll);
      if (trailPts.current.length > 300) trailPts.current.shift();
      if (!trailRef.current) {
        trailRef.current = polyline(trailPts.current, { color: "#38bdf8", weight: 2 }).addTo(m);
      } else {
        trailRef.current.setLatLngs(trailPts.current);
      }
    }
  }, [vehicle, origin]);

  const saveTk = () => {
    localStorage.setItem("tdt_tk", tkInput.trim());
    setTk(tkInput.trim());
  };
  const saveOrigin = (latStr: string, lonStr: string) => {
    const lat = Number(latStr);
    const lon = Number(lonStr);
    if (!Number.isFinite(lat) || !Number.isFinite(lon)) return;
    const o = { lat, lon };
    localStorage.setItem("tdt_origin", JSON.stringify(o));
    setOrigin(o);
    trailPts.current = [];
    if (trailRef.current) trailRef.current.setLatLngs([]);
    mapRef.current?.setView([lat, lon], 18);
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", minHeight: 0, flex: 1 }}>
      <div className="panel-title">
        <span>地图 · 天地图服务</span>
        <span className="hint">
          {tk ? "天地图 WMTS 已启用" : "未配置天地图 tk，演示用 OSM 底图"}
        </span>
        <span className="spacer" />
        <input
          className="input"
          style={{ width: 180 }}
          placeholder="天地图 tk key"
          value={tkInput}
          onChange={(e) => setTkInput(e.target.value)}
        />
        <button className="btn small" onClick={saveTk}>保存 tk</button>
        <input
          className="input"
          style={{ width: 90 }}
          defaultValue={String(origin.lat)}
          onBlur={(e) => saveOrigin(e.target.value, String(origin.lon))}
        />
        <input
          className="input"
          style={{ width: 90 }}
          defaultValue={String(origin.lon)}
          onBlur={(e) => saveOrigin(String(origin.lat), e.target.value)}
        />
      </div>
      <div className="tianmap-box" ref={boxRef} />
    </div>
  );
}

