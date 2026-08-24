// 数据层：REST + WebSocket + 断网降级（计划任务 4：连不上自动用 mock）。
import { useCallback, useEffect, useRef, useState } from "react";
import type {
  APIKey,
  AuditEntry,
  EventSnap,
  FleetSnap,
  GroupInfo,
  MapEntry,
  Me,
  NavPoint,
  NavRoute,
  PointCloudResp,
  UserInfo,
} from "./types";
import { mockFleet, tickMock } from "./mock";

export type FleetSource = "live" | "mock";

export async function fetchJSON<T>(path: string, init?: RequestInit, timeoutMs = 4000): Promise<T> {
  const ctl = new AbortController();
  const timer = window.setTimeout(() => ctl.abort(), timeoutMs);
  try {
    const r = await fetch(path, { ...init, signal: ctl.signal });
    if (!r.ok) {
      let msg = "HTTP " + r.status;
      try {
        const j = (await r.json()) as { error?: string };
        if (j && j.error) msg = j.error;
      } catch {
        /* 正文不是 JSON 时保留状态码 */
      }
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

export function controlPost(path: string, body?: Record<string, unknown>): Promise<Record<string, unknown>> {
  return fetchJSON<Record<string, unknown>>(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body || {}),
  });
}

export const takeoverRequest = (driver: string) => controlPost("/api/takeover/request", { driver });
export const takeoverRenew = (driver: string) => controlPost("/api/takeover/renew", { driver });
export const takeoverRelease = (driver: string) => controlPost("/api/takeover/release", { driver });
export const emergencyStop = () => controlPost("/api/emergency-stop");

export function fetchPointCloud(vehicleId?: string): Promise<PointCloudResp> {
  const q = vehicleId ? "?vehicle_id=" + encodeURIComponent(vehicleId) : "";
  return fetchJSON<PointCloudResp>("/api/pointcloud" + q, undefined, 8000);
}

export interface FleetState {
  snap: FleetSnap;
  source: FleetSource;
  wsConnected: boolean;
  refresh: () => void;
}

// useFleet：1Hz 状态 + 即时事件；WS 指数退避重连，重连后先拉全量对齐。
// 后端不可达时降级 mock 演示数据，并每 10s 探测恢复。
export function useFleet(): FleetState {
  const [snap, setSnap] = useState<FleetSnap>(() => mockFleet());
  const [source, setSource] = useState<FleetSource>("mock");
  const [wsConnected, setWsConnected] = useState(false);
  const sourceRef = useRef<FleetSource>("mock");
  sourceRef.current = source;

  const pullFull = useCallback(async (): Promise<boolean> => {
    try {
      const s = await fetchJSON<FleetSnap>("/api/fleet");
      setSnap(s);
      setSource("live");
      return true;
    } catch {
      setSource("mock");
      return false;
    }
  }, []);

  useEffect(() => {
    let stopped = false;
    let ws: WebSocket | null = null;
    let reconnectTimer = 0;
    let retries = 0;

    const connectWS = () => {
      if (stopped) return;
      ws = new WebSocket(wsURL("/ws/fleet"));
      ws.onopen = () => {
        retries = 0;
        setWsConnected(true);
        void pullFull();
      };
      ws.onmessage = (ev) => {
        try {
          const msg = JSON.parse(ev.data as string) as { type: string; data: unknown };
          if (msg.type === "state") {
            setSnap(msg.data as FleetSnap);
            setSource("live");
          } else if (msg.type === "event") {
            setSnap((prev) => ({
              ...prev,
              events: [msg.data as EventSnap, ...prev.events].slice(0, 200),
            }));
          }
        } catch {
          // 忽略畸形帧
        }
      };
      ws.onclose = () => {
        setWsConnected(false);
        if (stopped) return;
        retries += 1;
        const delay = Math.min(15000, 500 * Math.pow(2, Math.min(retries, 5)));
        reconnectTimer = window.setTimeout(connectWS, delay);
      };
      ws.onerror = () => {
        ws?.close();
      };
    };

    // mock 模式下让画面“活”起来；live 时不动数据（WS 驱动）
    const mockTimer = window.setInterval(() => {
      if (sourceRef.current === "mock") setSnap((prev) => tickMock(prev));
    }, 1000);
    const liveProbe = window.setInterval(() => {
      if (sourceRef.current === "mock") void pullFull();
    }, 10000);

    void pullFull();
    connectWS();

    return () => {
      stopped = true;
      window.clearInterval(mockTimer);
      window.clearInterval(liveProbe);
      window.clearTimeout(reconnectTimer);
      ws?.close();
    };
  }, [pullFull]);

  const refresh = useCallback(() => {
    void pullFull();
  }, [pullFull]);

  return { snap, source, wsConnected, refresh };
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
      author: "admin",
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
    body: JSON.stringify({ author: "admin", png_base64: pngBase64, ops: ops }),
  }, 30000);
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

export async function fetchKeys(): Promise<APIKey[]> {
  const r = await fetchJSON<{ keys: APIKey[] }>("/api/openkeys");
  return r.keys || [];
}

export async function createKey(name: string): Promise<{ key: APIKey; secret: string }> {
  return fetchJSON<{ key: APIKey; secret: string }>("/api/openkeys", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name: name }),
  });
}

export async function revokeKey(id: string): Promise<void> {
  await fetchJSON("/api/openkeys/" + encodeURIComponent(id) + "/revoke", { method: "POST" });
}

export async function fetchAudit(limit = 100): Promise<AuditEntry[]> {
  const r = await fetchJSON<{ audit: AuditEntry[] }>("/api/audit?limit=" + limit);
  return r.audit || [];
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

// 手机号登录：发送短信验证码（演示环境明文返回验证码，生产替换为短信通道）。
export async function sendSmsCode(phone: string): Promise<{ demo_code?: string; note?: string }> {
  return fetchJSON<{ demo_code?: string; note?: string }>("/api/auth/sms/send", {
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
  username: string; display_name: string; password: string; role: string; groups: string[];
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
