// 系统配置：右下角「管理员」点开小弹窗 → 设置 → 本页（左侧分类）。
// 所有需要配置的项集中在这里：地图与底图 / 平台服务 / 车辆与底盘 / 安全与等保。
// 值来源：服务端 /api/config（权威）+ 浏览器本地（天地图 tk 等前端自用项双写）。
import { useEffect, useState } from "react";
import { fetchConfig, saveConfig } from "../api";
import { amapCfg } from "./GpsMap";

interface Props {
  onClose: () => void;
}

const CATS = [
  { id: "map", label: "地图与底图" },
  { id: "service", label: "平台服务" },
  { id: "vehicle", label: "车辆与底盘" },
  { id: "security", label: "安全与等保" },
];

interface FieldDef {
  cat: string;
  key: string;
  label: string;
  desc: string;
  kind: "text" | "number" | "select" | "switch" | "readonly";
  options?: { v: string; label: string }[];
}

const FIELDS: FieldDef[] = [
  { cat: "map", key: "tdt_tk", label: "天地图密钥 (tk)", desc: "驾驶舱/地图底图用；留空自动降级为 OSM 底图", kind: "text" },
  { cat: "map", key: "tdt_origin", label: "坐标原点（纬度,经度）", desc: "车辆本地坐标投射到天地图的锚点", kind: "text" },
  { cat: "map", key: "amap_key", label: "高德地图 Key", desc: "总览 GPS 轨迹底图（lbs.amap.com，Web端 JS API）；保存后实时生效", kind: "text" },
  { cat: "map", key: "amap_sec", label: "高德安全密钥", desc: "securityJsCode，与 Key 配套使用", kind: "text" },
  { cat: "map", key: "bev_cell", label: "3D→2D 默认格宽（米）", desc: "map_convert 一行命令的默认栅格分辨率", kind: "number" },
  { cat: "map", key: "maps_dir", label: "地图仓库目录", desc: "服务器侧地图存储位置（版本化）", kind: "readonly" },
  { cat: "service", key: "fleet_addr", label: "fleet-hub 监听地址", desc: "云端聚合服务（REST/WS/静态站）", kind: "readonly" },
  { cat: "service", key: "mqtt_addr", label: "MQTT Broker 地址", desc: "车云上行通道（mTLS）", kind: "text" },
  { cat: "service", key: "authority_addr", label: "控制权服务地址", desc: "control-authority（接管租约/急停）", kind: "text" },
  { cat: "service", key: "mqtt_tls", label: "MQTT mTLS 状态", desc: "车云链路证书双向认证（当前为启用）", kind: "switch" },
  { cat: "vehicle", key: "chassis_type", label: "默认底盘类型", desc: "影响轮速/转向展示与遥控语义", kind: "select", options: [
    { v: "ackermann", label: "阿克曼" },
    { v: "4w4s", label: "四轮四转" },
    { v: "diff_agv", label: "差速 AGV" },
  ] },
  { cat: "vehicle", key: "telemetry_hz", label: "遥测频率 (Hz)", desc: "车端上行遥测频率", kind: "number" },
  { cat: "vehicle", key: "map_push_s", label: "地图上报周期（秒）", desc: "车端地图实时上报间隔（sha256 去重）", kind: "number" },
  { cat: "security", key: "open_api_enabled", label: "开放 API 总开关", desc: "/open/v1/* 对外接口", kind: "switch" },
  { cat: "security", key: "audit_enabled", label: "审计记录", desc: "调用审计（含失败）留存", kind: "switch" },
  { cat: "security", key: "replay_window_s", label: "防重放窗口（秒）", desc: "签名时间戳允许偏差", kind: "number" },
  { cat: "security", key: "rate_limit", label: "限流（次/10s/Key）", desc: "每个 API Key 的调用频率上限", kind: "number" },
];

export default function SettingsModal({ onClose }: Props) {
  const [cat, setCat] = useState("map");
  const [cfg, setCfg] = useState<Record<string, unknown>>({});
  const [saved, setSaved] = useState("");

  useEffect(() => {
    void (async () => {
      try {
        const c = await fetchConfig();
        // 前端自用项优先取本地（比如天地图 tk 之前在大屏页填过）
        const tk = window.localStorage.getItem("tdt_tk");
        if (tk && !c["tdt_tk"]) c["tdt_tk"] = tk;
        const def = amapCfg();
        if (!c["amap_key"]) c["amap_key"] = window.localStorage.getItem("ra-cfg-amap-key") || def.key;
        if (!c["amap_sec"]) c["amap_sec"] = window.localStorage.getItem("ra-cfg-amap-sec") || def.sec;
        setCfg(c);
      } catch {
        /* 后端未就绪时用空值 */
      }
    })();
  }, []);

  const setVal = (key: string, v: unknown) => {
    setCfg((prev) => ({ ...prev, [key]: v }));
    setSaved("");
  };

  const doSave = async () => {
    try {
      const body: Record<string, unknown> = {};
      for (const f of FIELDS) {
        if (f.kind === "readonly") continue;
        body[f.key] = cfg[f.key];
      }
      await saveConfig(body);
      // 前端自用项双写本地
      if (typeof cfg["tdt_tk"] === "string") window.localStorage.setItem("tdt_tk", cfg["tdt_tk"]);
      if (typeof cfg["tdt_origin"] === "string") window.localStorage.setItem("tdt_origin", cfg["tdt_origin"]);
      if (typeof cfg["amap_key"] === "string") window.localStorage.setItem("ra-cfg-amap-key", cfg["amap_key"]);
      if (typeof cfg["amap_sec"] === "string") window.localStorage.setItem("ra-cfg-amap-sec", cfg["amap_sec"]);
      // 通知地图组件实时重建（总览 GPS 轨迹等）
      window.dispatchEvent(new CustomEvent("ra-cfg-changed"));
      setSaved("已保存");
    } catch (e) {
      setSaved("保存失败：" + String(e));
    }
  };

  const fields = FIELDS.filter((f) => f.cat === cat);

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal settings-modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <span style={{ fontWeight: 700, fontSize: 14 }}>系统配置</span>
          <button className="btn small ghost" onClick={onClose}>✕</button>
        </div>
        <div className="settings-body">
          <div className="settings-cats">
            {CATS.map((c) => (
              <button
                key={c.id}
                className={"settings-cat" + (cat === c.id ? " active" : "")}
                onClick={() => setCat(c.id)}
              >
                {c.label}
              </button>
            ))}
          </div>
          <div className="settings-fields">
            {fields.map((f) => (
              <div className="settings-field" key={f.key}>
                <div>
                  <div>{f.label}</div>
                  <div className="muted" style={{ fontSize: 11 }}>{f.desc}</div>
                </div>
                <div style={{ width: 260 }}>
                  {f.kind === "text" && (
                    <input className="input" value={String(cfg[f.key] ?? "")} onChange={(e) => setVal(f.key, e.target.value)} />
                  )}
                  {f.kind === "number" && (
                    <input
                      className="input" type="number" step="any"
                      value={String(cfg[f.key] ?? "")}
                      onChange={(e) => setVal(f.key, parseFloat(e.target.value) || 0)}
                    />
                  )}
                  {f.kind === "select" && (
                    <select className="input" value={String(cfg[f.key] ?? "")} onChange={(e) => setVal(f.key, e.target.value)}>
                      {(f.options || []).map((o) => (
                        <option key={o.v} value={o.v}>{o.label}</option>
                      ))}
                    </select>
                  )}
                  {f.kind === "switch" && (
                    <input
                      type="checkbox"
                      checked={Boolean(cfg[f.key])}
                      disabled={f.key === "mqtt_tls"}
                      onChange={(e) => setVal(f.key, e.target.checked)}
                    />
                  )}
                  {f.kind === "readonly" && (
                    <div className="mono muted">{String(cfg[f.key] ?? "-")}</div>
                  )}
                </div>
              </div>
            ))}
            <div className="btn-row mt">
              <button className="btn primary" onClick={() => void doSave()}>保存配置</button>
              {saved && <span className="muted">{saved}</span>}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
