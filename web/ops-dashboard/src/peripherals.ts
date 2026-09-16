// peripherals.ts：浏览器外设探测（远程接管设备状态的数据来源）。
//  - 键盘：只要页面可交互即视为就绪（无需 Web API 枚举）。
//  - 航模遥控器（USB）/ 罗技方向盘：走 Gamepad API。
//    浏览器要求页面有过交互后才暴露设备列表，这里用 800ms 轮询兜底，
//    同时监听 gamepadconnected/disconnected 事件即时刷新。
import { useEffect, useState } from "react";

export interface GamepadInfo {
  index: number;
  id: string;
  connected: boolean;
  mapping: string;
  // 名称含方向盘特征（Logitech G29/G920/G923、Driving Force 等）→ 判为方向盘
  isWheel: boolean;
}

const WHEEL_RE = /logitech|g25|g27|g29|g920|g923|driving force|wheel|方向盘/i;

export function scanGamepads(): GamepadInfo[] {
  if (typeof navigator === "undefined" || !navigator.getGamepads) return [];
  const list: GamepadInfo[] = [];
  let pads: (Gamepad | null)[] = [];
  try {
    pads = Array.from(navigator.getGamepads());
  } catch {
    return [];
  }
  for (const gp of pads) {
    if (gp && gp.connected) {
      list.push({
        index: gp.index,
        id: gp.id || ("gamepad-" + gp.index),
        connected: true,
        mapping: gp.mapping || "",
        isWheel: WHEEL_RE.test(gp.id || ""),
      });
    }
  }
  return list;
}

// useGamepads：持续轮询外设列表（设备插拔即时反映到状态条）。
export function useGamepads(intervalMs = 800): GamepadInfo[] {
  const [pads, setPads] = useState<GamepadInfo[]>([]);
  useEffect(() => {
    let last = "";
    let timer = 0;
    const scan = () => {
      const list = scanGamepads();
      const key = list.map((p) => p.index + ":" + p.id).join("|");
      if (key !== last) {
        last = key;
        setPads(list);
      }
    };
    scan();
    timer = window.setInterval(scan, intervalMs);
    const onHotplug = () => scan();
    window.addEventListener("gamepadconnected", onHotplug);
    window.addEventListener("gamepaddisconnected", onHotplug);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("gamepadconnected", onHotplug);
      window.removeEventListener("gamepaddisconnected", onHotplug);
    };
  }, [intervalMs]);
  return pads;
}

export const DEVICE_TYPE_LABEL: Record<string, string> = {
  console: "自研一体机",
  rc: "航模遥控器",
  keyboard: "键盘",
  wheel: "罗技方向盘",
};

