// 数据层：REST + WebSocket。后端不可达时明确报告不可用，绝不编造车队数据。
import { useCallback, useEffect, useRef, useState } from "react";
import type {
  APIKey,
  ActiveTakeover,
  AuditEntry,
  DeviceInfo,
  EventSnap,
  FleetSnap,
  GroupInfo,
  MapEntry,
  MapPublication,
  MapPointsResp,
  Me,
  NavPoint,
  NavRoute,
  OpenScope,
  PointCloudResp,
  UserInfo,
} from "./types";
// live: 已收到当前 WebSocket 状态帧；degraded: 保留最后一次已验证快照并用轮询恢复；
// unavailable: 本次页面生命周期内从未取得有效快照。三者不能混为一谈。
export type FleetSource = "live" | "degraded" | "unavailable";

const emptyFleet = (): FleetSnap => ({
  server_time_ns: 0,
  vehicles: [],
  takeover: { active: false },
  events: [],
});

let csrfToken: string | null = null;

async function ensureCsrfToken(): Promise<string> {
  if (csrfToken) return csrfToken;
  const r = await fetch("/api/auth/csrf", { credentials: "same-origin" });
  if (!r.ok) throw new Error("CSRF 令牌服务不可用");
  const body = (await r.json()) as { csrf_token?: string };
  if (!body.csrf_token) throw new Error("CSRF 令牌响应无效");
  csrfToken = body.csrf_token;
  return csrfToken;
}

export async function fetchJSON<T>(path: string, init?: RequestInit, timeoutMs = 4000): Promise<T> {
  const ctl = new AbortController();
  const timer = window.setTimeout(() => ctl.abort(), timeoutMs);
  try {
    const method = String(init?.method || "GET").toUpperCase();
    const headers = new Headers(init?.headers);
    if (["POST", "PUT", "PATCH", "DELETE"].includes(method) && path.startsWith("/api/")) {
      headers.set("X-CSRF-Token", await ensureCsrfToken());
    }
    const r = await fetch(path, {
      ...init,
      headers,
      credentials: "same-origin",
      signal: ctl.signal,
    });
    if (!r.ok) {
      let msg = "HTTP " + r.status;
      try {
        const j = (await r.json()) as { error?: string };
        if (j && j.error) msg = j.error;
      } catch {
        /* 正文不是 JSON 时保留状态码 */
      }
      if (r.status === 403 && path.startsWith("/api/")) csrfToken = null;
      throw new Error(msg);
    }
    return (await r.json()) as T;
  } finally {
    window.clearTimeout(timer);
  }
}

export function wsURL(path: string): string {
  const proto = window.location.protocol === "https:" ? "wss" : "ws";
  return proto + "://" + window.location.host + path;
}

// ---------- 新标签页打开独立页（接管驾驶页等） ----------

// 驾驶页（独立页面）地址：#/driveview/:id
export const driveViewURL = (vehicleId: string) => "#/driveview/" + encodeURIComponent(vehicleId);

// 新标签页打开独立页；被浏览器弹窗拦截时退回当前标签页跳转，保证一定能到达目标页。
// 接管等场景在异步 API 返回后才调用 window.open，可能已丢失用户手势（Chrome 会拦截弹窗），
// 返回 true=已开新标签，false=被拦截并已同标签跳转。
export function openTab(hash: string): boolean {
  const w = window.open(hash, "_blank");
  if (!w) {
    window.location.hash = hash;
    return false;
  }
  return true;
}

export function controlPost(path: string, body?: Record<string, unknown>): Promise<Record<string, unknown>> {
  return fetchJSON<Record<string, unknown>>(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body || {}),
  });
}

// 历史 /request、/renew 会绕过受信控制设备校验，商用模式已停用。
export const takeoverRelease = (driver: string) => controlPost("/api/takeover/release", { driver });
export const emergencyStop = (vehicleId: string) => controlPost("/api/emergency-stop", { vehicle_id: vehicleId });

// ---------- 接管设备绑定 ----------

export async function fetchDevices(): Promise<DeviceInfo[]> {
  const r = await fetchJSON<{ devices: DeviceInfo[] }>("/api/devices");
  return r.devices || [];
}

export const bindDevice = (body: {
  type: string;
  name: string;
  link?: string;
  sn?: string;
  password?: string;
}) => controlPost("/api/devices", body);

export const unbindDevice = (id: string) =>
  controlPost("/api/devices/" + encodeURIComponent(id) + "/delete");

// ---------- 车辆接管（账号级：一账号一次一辆） ----------

export function takeoverTake(
  vehicleId: string,
  deviceId: string
): Promise<{ ok?: boolean; takeover?: ActiveTakeover; error?: string }> {
  return fetchJSON<{ ok?: boolean; takeover?: ActiveTakeover; error?: string }>("/api/takeover/take", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ vehicle_id: vehicleId, device_id: deviceId, request_id: crypto.randomUUID() }),
  }, 10000).catch((e) => ({ error: String(e && e.message ? e.message : e) }));
}

export function takeoverKeepalive(
  vehicleId: string,
  deviceId: string
): Promise<{ ok?: boolean; takeover?: ActiveTakeover; error?: string }> {
  return fetchJSON<{ ok?: boolean; takeover?: ActiveTakeover; error?: string }>("/api/takeover/keepalive", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ vehicle_id: vehicleId, device_id: deviceId, request_id: crypto.randomUUID() }),
  }, 10000).catch((e) => ({ error: String(e && e.message ? e.message : e) }));
}

export async function takeoverMine(): Promise<ActiveTakeover | null> {
  try {
    const r = await fetchJSON<{ active: boolean; takeover?: ActiveTakeover }>("/api/takeover/mine");
    return r.active && r.takeover ? r.takeover : null;
  } catch {
    return null;
  }
}

export async function takeoverActiveList(): Promise<ActiveTakeover[]> {
  try {
    const r = await fetchJSON<{ active: ActiveTakeover[] }>("/api/takeover/active");
    return r.active || [];
  } catch {
    return [];
  }
}

export const takeoverReleaseMine = () => controlPost("/api/takeover/release", {});

export function fetchPointCloud(vehicleId?: string): Promise<PointCloudResp> {
  const q = vehicleId ? "?vehicle_id=" + encodeURIComponent(vehicleId) : "";
  return fetchJSON<PointCloudResp>("/api/pointcloud" + q, undefined, 8000);
}

export interface FleetState {
  snap: FleetSnap;
  source: FleetSource;
  wsConnected: boolean;
  // 浏览器本地接收时刻，仅用于明确数据新鲜度；不属于车辆遥测，也不会写回快照。
  lastVerifiedAt: number | null;
  lastLiveAt: number | null;
  refresh: () => void;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

function isFleetSnap(value: unknown): value is FleetSnap {
  if (!isRecord(value) || !isFiniteNumber(value.server_time_ns) || !Array.isArray(value.vehicles) || !Array.isArray(value.events)) {
    return false;
  }
  if (!isRecord(value.takeover) || typeof value.takeover.active !== "boolean") return false;
  return value.vehicles.every((vehicle) => {
    if (!isRecord(vehicle) || !isRecord(vehicle.gps) || !isRecord(vehicle.pose) || !isRecord(vehicle.capabilities)) return false;
    return typeof vehicle.vehicle_id === "string" && typeof vehicle.group === "string" &&
      typeof vehicle.chassis === "string" && typeof vehicle.online === "boolean" &&
      typeof vehicle.mode === "string" && typeof vehicle.gear === "string" &&
      typeof vehicle.gps.fix === "boolean" && typeof vehicle.pose.valid === "boolean" &&
      [vehicle.last_heartbeat_age_s, vehicle.speed_mps, vehicle.soc, vehicle.voltage, vehicle.steer_rad,
        vehicle.throttle_pct, vehicle.brake_pct, vehicle.gps.lat, vehicle.gps.lon, vehicle.gps.alt,
        vehicle.pose.x, vehicle.pose.y, vehicle.pose.yaw].every(isFiniteNumber);
  }) && value.events.every((event) =>
    isRecord(event) && isFiniteNumber(event.ts_ns) && typeof event.level === "string" && typeof event.text === "string"
  );
}

// useFleet：1Hz 状态 + 即时事件；WS 指数退避重连，重连后先拉全量对齐。
// 链路抖动时绝不清空已经验证的真实快照：以 degraded 明示降级，并持续 REST 校验恢复。
export function useFleet(enabled = true): FleetState {
  const [snap, setSnap] = useState<FleetSnap>(emptyFleet);
  const [source, setSource] = useState<FleetSource>("unavailable");
  const [wsConnected, setWsConnected] = useState(false);
  const [lastVerifiedAt, setLastVerifiedAt] = useState<number | null>(null);
  const [lastLiveAt, setLastLiveAt] = useState<number | null>(null);
  const hasVerifiedSnapshot = useRef(false);
  const wsConnectedRef = useRef(false);
  const newestServerTime = useRef(0);

  const applySnapshot = useCallback((next: FleetSnap, viaWS: boolean) => {
    // REST 与 WS 并行时，迟到的旧快照不得覆盖更新的车辆状态。
    if (next.server_time_ns < newestServerTime.current) return;
    newestServerTime.current = next.server_time_ns;
    const receivedAt = Date.now();
    hasVerifiedSnapshot.current = true;
    setSnap(next);
    setLastVerifiedAt(receivedAt);
    if (viaWS) {
      setLastLiveAt(receivedAt);
      setSource("live");
    } else {
      setSource(wsConnectedRef.current ? "live" : "degraded");
    }
  }, []);

  const pullFull = useCallback(async (): Promise<boolean> => {
    try {
      const payload = await fetchJSON<unknown>("/api/fleet");
      if (!isFleetSnap(payload)) throw new Error("fleet 响应不符合契约");
      applySnapshot(payload, false);
      return true;
    } catch {
      // 只有从未取得过可信数据时才显示空态；避免短暂网络错误造成整屏闪空。
      if (!hasVerifiedSnapshot.current) setSource("unavailable");
      else if (!wsConnectedRef.current) setSource("degraded");
      return false;
    }
  }, [applySnapshot]);

  useEffect(() => {
    if (!enabled) {
      // 未完成会话校验时不建立 WebSocket，也不触发反复 401 重连。
      // 这能消除登录页与主界面切换期间的竞态和闪屏。
      return;
    }
    let stopped = false;
    let ws: WebSocket | null = null;
    let reconnectTimer = 0;
    let retries = 0;

    const connectWS = () => {
      if (stopped) return;
      const socket = new WebSocket(wsURL("/ws/fleet"));
      ws = socket;
      socket.onopen = () => {
        if (stopped || ws !== socket) return;
        retries = 0;
        wsConnectedRef.current = true;
        setWsConnected(true);
        void pullFull();
      };
      socket.onmessage = (ev) => {
        if (stopped || ws !== socket) return;
        try {
          const msg = JSON.parse(ev.data as string) as { type: string; data: unknown };
          if (msg.type === "state" && isFleetSnap(msg.data)) {
            applySnapshot(msg.data, true);
          } else if (msg.type === "event" && isRecord(msg.data) && isFiniteNumber(msg.data.ts_ns) &&
            typeof msg.data.level === "string" && typeof msg.data.text === "string") {
            setSnap((prev) => ({
              ...prev,
              events: [msg.data as EventSnap, ...prev.events].slice(0, 200),
            }));
          }
        } catch {
          // 忽略畸形帧
        }
      };
      socket.onclose = () => {
        if (ws !== socket) return;
        wsConnectedRef.current = false;
        setWsConnected(false);
        if (stopped) return;
        setSource(hasVerifiedSnapshot.current ? "degraded" : "unavailable");
        // 不等待下一次轮询，断线后立即验证 HTTP 备用通道。
        void pullFull();
        retries += 1;
        const delay = Math.min(15000, 500 * Math.pow(2, Math.min(retries, 5)));
        reconnectTimer = window.setTimeout(connectWS, delay);
      };
      socket.onerror = () => {
        socket.close();
      };
    };

    const liveProbe = window.setInterval(() => {
      void pullFull();
    }, 10000);

    void pullFull();
    connectWS();

    return () => {
      stopped = true;
      wsConnectedRef.current = false;
      window.clearInterval(liveProbe);
      window.clearTimeout(reconnectTimer);
      ws?.close();
    };
  }, [applySnapshot, enabled, pullFull]);

  const refresh = useCallback(() => {
    void pullFull();
  }, [pullFull]);

  return { snap, source, wsConnected, lastVerifiedAt, lastLiveAt, refresh };
}

// ---------- 地图中心 ----------

export async function fetchMaps(): Promise<MapEntry[]> {
  const r = await fetchJSON<{ maps: MapEntry[] }>("/api/maps", undefined, 8000);
  return r.maps || [];
}

export function mapFileURL(id: string, name: string, version?: number): string {
  const v = version ? "&v=" + version : "";
  return "/api/maps/" + encodeURIComponent(id) + "/file?name=" + encodeURIComponent(name) + v;
}

export async function uploadMap(vehicleId: string, file: File, source: string): Promise<{ changed: boolean }> {
  const dataUrl = await new Promise<string>((resolve, reject) => {
    const rd = new FileReader();
    rd.onload = () => resolve(String(rd.result));
    rd.onerror = () => reject(new Error("读文件失败"));
    rd.readAsDataURL(file);
  });
  const base64 = dataUrl.split(",")[1] || "";
  const resp = await fetchJSON<{ ok: boolean; changed: boolean }>("/api/maps/upload", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      vehicle_id: vehicleId,
      name: file.name,
      source: source,
      data_base64: base64,
    }),
  }, 30000);
  return { changed: resp.changed };
}

export async function convertMap(id: string): Promise<void> {
  await fetchJSON("/api/maps/" + encodeURIComponent(id) + "/convert", { method: "POST" }, 60000);
}

export async function saveMapEdit(id: string, pngBase64: string, ops: unknown): Promise<void> {
  await fetchJSON("/api/maps/" + encodeURIComponent(id) + "/edit", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ png_base64: pngBase64, ops: ops }),
  }, 30000);
}

// 删除整张地图（含全部版本与服务器文件；车上原件不动）。
export async function deleteMap(id: string): Promise<void> {
  await fetchJSON<{ ok: boolean }>("/api/maps/" + encodeURIComponent(id), { method: "DELETE" }, 8000);
}

export async function fetchMapPublications(vehicleId?: string): Promise<MapPublication[]> {
  const q = vehicleId ? "?vehicle_id=" + encodeURIComponent(vehicleId) : "";
  const r = await fetchJSON<{ publications: MapPublication[] }>("/api/maps/publications" + q, undefined, 8000);
  return r.publications || [];
}

export async function requestMapPublication(mapId: string, vehicleId: string, version: number, coordinateFrame: string): Promise<MapPublication> {
  const r = await fetchJSON<{ publication: MapPublication }>("/api/maps/" + encodeURIComponent(mapId) + "/publish", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ vehicle_id: vehicleId, version, coordinate_frame: coordinateFrame }),
  }, 10000);
  return r.publication;
}

export async function approveMapPublication(id: string): Promise<MapPublication> {
  const r = await fetchJSON<{ publication: MapPublication }>("/api/maps/publications/" + encodeURIComponent(id) + "/approve", { method: "POST" }, 10000);
  return r.publication;
}

export async function rollbackMapPublication(id: string): Promise<MapPublication> {
  const r = await fetchJSON<{ publication: MapPublication }>("/api/maps/publications/" + encodeURIComponent(id) + "/rollback", { method: "POST" }, 10000);
  return r.publication;
}

// 3D 预览点云：服务端解析 PCD/CSV 并降采样（默认最新 3D 版本，?v= 指定版本，?max= 点数上限）。
export async function fetchMapPoints(id: string, version?: number, max?: number): Promise<MapPointsResp> {
  const p = new URLSearchParams();
  if (version) p.set("v", String(version));
  if (max) p.set("max", String(max));
  const q = p.toString();
  return fetchJSON<MapPointsResp>("/api/maps/" + encodeURIComponent(id) + "/points" + (q ? "?" + q : ""), undefined, 60000);
}

// ---- 三维/二维统一判定（全项目同一口径）----

// 版本链里是否含 3D 点云（.pcd/.csv）
export function mapHas3D(m: MapEntry): boolean {
  return m.versions.some((v) => v.files.some((f) => f.endsWith(".pcd") || f.endsWith(".csv")));
}

// 最新一份 2D 栅格（.png）所在版本；没有返回 null
export function mapLatestPng(m: MapEntry): { version: number; name: string } | null {
  for (let i = m.versions.length - 1; i >= 0; i--) {
    const v = m.versions[i];
    const png = v.files.find((f) => f.endsWith(".png"));
    if (png) return { version: v.version, name: png };
  }
  return null;
}

// 最新一份 3D 点云（.pcd/.csv）所在版本；没有返回 null
export function mapLatest3D(m: MapEntry): { version: number; name: string } | null {
  for (let i = m.versions.length - 1; i >= 0; i--) {
    const v = m.versions[i];
    const f = v.files.find((x) => x.endsWith(".pcd") || x.endsWith(".csv"));
    if (f) return { version: v.version, name: f };
  }
  return null;
}

// ---------- 系统配置 ----------

export async function fetchConfig(): Promise<Record<string, unknown>> {
  return fetchJSON<Record<string, unknown>>("/api/config");
}

export async function saveConfig(kv: Record<string, unknown>): Promise<void> {
  await fetchJSON("/api/config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(kv),
  });
}

// ---------- 循迹导航 ----------

export async function fetchRoutes(): Promise<NavRoute[]> {
  const r = await fetchJSON<{ routes: NavRoute[] }>("/api/nav/routes");
  return r.routes || [];
}

export async function submitRoute(vehicleId: string, name: string, points: NavPoint[]): Promise<NavRoute> {
  const r = await fetchJSON<{ ok: boolean; route: NavRoute }>("/api/nav/routes", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ vehicle_id: vehicleId, name: name, points: points }),
  });
  return r.route;
}

export async function cancelRoute(id: string): Promise<void> {
  await fetchJSON("/api/nav/routes/" + encodeURIComponent(id) + "/cancel", { method: "POST" });
}

// ---------- 开放 API 管理 ----------

// 车辆清单（建 Key 选车辆白名单用）。
export async function fetchFleetSnap(): Promise<FleetSnap> {
  return fetchJSON<FleetSnap>("/api/fleet", undefined, 8000);
}

export async function fetchKeys(): Promise<APIKey[]> {
  const r = await fetchJSON<{ keys: APIKey[] }>("/api/openkeys");
  return r.keys || [];
}

// 创建 Key：名称/备注 + 功能白名单 + 车辆白名单（未勾选的功能/车辆一律不允许）。
export async function createKey(body: {
  name: string;
  remark: string;
  scopes: string[];
  vehicles: string[];
  ips: string[];
  expires_at?: string; // RFC3339；与 expires_in_s 二选一，缺省使用服务端默认有效期
  expires_in_s?: number;
}): Promise<{ key: APIKey; secret: string }> {
  return fetchJSON<{ key: APIKey; secret: string }>("/api/openkeys", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

// 修改 Key：字段缺省即不动。
export async function updateKey(
  id: string,
  body: { name?: string; remark?: string; scopes?: string[]; vehicles?: string[]; ips?: string[]; expires_at?: string; expires_in_s?: number },
): Promise<{ key: APIKey }> {
  return fetchJSON<{ key: APIKey }>("/api/openkeys/" + encodeURIComponent(id) + "/update", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

export async function revokeKey(id: string): Promise<void> {
  await fetchJSON("/api/openkeys/" + encodeURIComponent(id) + "/revoke", { method: "POST" });
}

// 功能权限目录（建 Key/改 Key 时的勾选项来源）。
export async function fetchScopes(): Promise<OpenScope[]> {
  const r = await fetchJSON<{ scopes: OpenScope[] }>("/api/openscopes");
  return r.scopes || [];
}

export interface AuditPageResp {
  audit: AuditEntry[];
  next_cursor?: string;
  has_more?: boolean;
}

// 审计查询：服务端使用 PostgreSQL keyset 游标，前端不使用 OFFSET。
export async function fetchAuditPage(params?: { limit?: number; source?: "api" | "platform" | "all"; key_id?: string; result?: string; cursor?: string }): Promise<AuditPageResp> {
  const q = new URLSearchParams();
  q.set("limit", String(params?.limit ?? 100));
  if (params?.source) q.set("source", params.source);
  if (params?.key_id) q.set("key_id", params.key_id);
  if (params?.result) q.set("result", params.result);
  if (params?.cursor) q.set("cursor", params.cursor);
  const r = await fetchJSON<AuditPageResp>("/api/audit?" + q.toString());
  return { audit: r.audit || [], next_cursor: r.next_cursor || "", has_more: !!r.has_more };
}

export function auditExportURL(params?: { limit?: number; source?: "api" | "platform" | "all"; key_id?: string; result?: string }): string {
  const q = new URLSearchParams();
  q.set("limit", String(params?.limit ?? 10000));
  if (params?.source) q.set("source", params.source);
  if (params?.key_id) q.set("key_id", params.key_id);
  if (params?.result) q.set("result", params.result);
  return "/api/audit/export?" + q.toString();
}

// ---------- 登录与账号（等保三级） ----------

export async function fetchCaptcha(): Promise<{ captcha_id: string; image: string }> {
  return fetchJSON<{ captcha_id: string; image: string }>("/api/captcha");
}

// 登录第一段：账密校验，通过返回预认证 token（人机验证在第二段）。
export async function login(username: string, password: string): Promise<{ preauth: string; note?: string }> {
  return fetchJSON<{ preauth: string; note?: string }>("/api/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      username: username,
      password: password,
    }),
  }, 10000);
}

// 登录第二段：人机验证通过 → 服务端发会话 Cookie。
export async function verifyLogin(preauth: string, captchaId: string, captchaAnswer: string): Promise<void> {
  await fetchJSON("/api/auth/verify", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      preauth: preauth,
      captcha_id: captchaId,
      captcha_answer: captchaAnswer,
    }),
  }, 10000);
}

// 手机号登录：仅在已配置的短信网关提供验证码后可用。
export async function sendSmsCode(phone: string): Promise<{ note?: string }> {
	return fetchJSON<{ note?: string }>("/api/auth/sms/send", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ phone: phone }),
  }, 10000);
}

// 手机号+短信验证码登录：短信码即人机验证，通过直接进入。
export async function loginPhone(phone: string, code: string): Promise<void> {
  await fetchJSON("/api/auth/login-phone", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ phone: phone, code: code }),
  }, 10000);
}

export async function logout(): Promise<void> {
  try {
    await fetchJSON("/api/auth/logout", { method: "POST" });
  } catch {
    /* 会话已失效也照样回登录页 */
  }
}

export async function fetchMe(): Promise<Me | null> {
  try {
    return await fetchJSON<Me>("/api/auth/me");
  } catch {
    return null;
  }
}

export async function changePassword(oldPwd: string, newPwd: string): Promise<void> {
  await fetchJSON("/api/auth/password", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ old: oldPwd, new: newPwd }),
  });
}

// ---------- 组织管理 ----------

export async function fetchGroups(): Promise<GroupInfo[]> {
  const r = await fetchJSON<{ groups: GroupInfo[] }>("/api/admin/groups");
  return r.groups || [];
}

export async function createGroup(name: string): Promise<void> {
  await fetchJSON("/api/admin/groups", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name: name }),
  });
}

export async function updateGroup(id: string, patchBody: Record<string, unknown>): Promise<void> {
  await fetchJSON("/api/admin/groups/" + encodeURIComponent(id), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patchBody),
  });
}

export async function deleteGroup(id: string): Promise<void> {
  await fetchJSON("/api/admin/groups/" + encodeURIComponent(id) + "/delete", { method: "POST" });
}

export async function fetchUsers(): Promise<UserInfo[]> {
  const r = await fetchJSON<{ users: UserInfo[] }>("/api/admin/users");
  return r.users || [];
}

export async function createUser(body: {
  username: string; display_name: string; password: string; role: string; groups: string[]; phone?: string;
}): Promise<void> {
  await fetchJSON("/api/admin/users", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

export async function setUserGroups(username: string, groups: string[]): Promise<void> {
  await fetchJSON("/api/admin/users/" + encodeURIComponent(username) + "/groups", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ groups: groups }),
  });
}

export async function resetUserPassword(username: string, password: string): Promise<void> {
  await fetchJSON("/api/admin/users/" + encodeURIComponent(username) + "/reset", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password: password }),
  });
}

export async function deleteUser(username: string): Promise<void> {
  await fetchJSON("/api/admin/users/" + encodeURIComponent(username) + "/delete", { method: "POST" });
}


// ---------- 告警与事件成熟化 / 车辆注册 / 孪生模型 ----------
export interface EventRow {
  id: number;
  ts_ns: number;
  vehicle_id: string;
  level: string;
  kind: string; // veh=车辆侧（告警与事件）；sys=系统侧（系统审计）
  text: string;
  group_id: string;
}
export interface EventListResp {
  items: EventRow[];
  total: number;
  page: number;
  page_size: number;
}
export interface EventStats {
  total: number;
  today: number;
  critical: number;
  warn: number;
  info: number;
}
export interface EventQuery {
  page?: number;
  page_size?: number;
  level?: string;
  vehicle?: string;
  q?: string;
  from?: string;
  to?: string;
  kind?: string;
}
export async function fetchEventStats(kind: string): Promise<EventStats> {
  return await fetchJSON<EventStats>("/api/events/stats?kind=" + encodeURIComponent(kind));
}
export function fetchEvents(q: EventQuery): Promise<EventListResp> {
  const p = new URLSearchParams();
  if (q.page) p.set("page", String(q.page));
  if (q.page_size) p.set("page_size", String(q.page_size));
  if (q.level && q.level !== "all") p.set("level", q.level);
  if (q.vehicle && q.vehicle !== "all") p.set("vehicle", q.vehicle);
  if (q.q) p.set("q", q.q);
  if (q.from) p.set("from", q.from);
  if (q.to) p.set("to", q.to);
  if (q.kind === "veh" || q.kind === "sys") p.set("kind", q.kind);
  return fetchJSON<EventListResp>("/api/events?" + p.toString(), undefined, 8000);
}
export const fetchReadmark = () => fetchJSON<{ read_until_ns: number }>("/api/events/readmark");
export const postEventsRead = () => controlPost("/api/events/read");
export const clearEvents = (body: Record<string, unknown>) => controlPost("/api/events/clear", body);
export const registerVehicle = (body: Record<string, unknown>) => controlPost("/api/vehicles", body);
export const fetchGroupNames = () =>
  fetchJSON<{ groups: { id: string; name: string }[] }>("/api/groups");

function bufToB64(buf: ArrayBuffer): string {
  const u8 = new Uint8Array(buf);
  let s = "";
  const CH = 0x8000;
  for (let i = 0; i < u8.length; i += CH) {
    s += String.fromCharCode(...u8.subarray(i, i + CH));
  }
  return btoa(s);
}
export async function uploadVehicleModel(id: string, file: File) {
  const b64 = bufToB64(await file.arrayBuffer());
  return controlPost("/api/vehicles/" + encodeURIComponent(id) + "/model", { data: b64 });
}
