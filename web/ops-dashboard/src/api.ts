// 数据层：REST + WebSocket + 断网降级（计划任务 4：连不上自动用 mock）。
import { useCallback, useEffect, useRef, useState } from "react";
import type { EventSnap, FleetSnap, PointCloudResp } from "./types";
import { mockFleet, tickMock } from "./mock";

export type FleetSource = "live" | "mock";

export async function fetchJSON<T>(path: string, init?: RequestInit, timeoutMs = 4000): Promise<T> {
  const ctl = new AbortController();
  const timer = window.setTimeout(() => ctl.abort(), timeoutMs);
  try {
    const r = await fetch(path, { ...init, signal: ctl.signal });
    if (!r.ok) throw new Error("HTTP " + r.status);
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
