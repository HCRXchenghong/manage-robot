// 离线演示数据（与 UI 预览图同款气质）：后端不可达时自动降级展示。
import type { FleetSnap, VehicleSnap } from "./types";

const POSES = [
  { x: 120, y: 80, yaw: 0.6 },
  { x: 45, y: 140, yaw: 2.1 },
  { x: 195, y: 55, yaw: 4.0 },
];

function seedHistory(base: number): number[] {
  const out: number[] = [];
  for (let i = 0; i < 60; i++) out.push(Math.max(0, base + Math.sin(i / 6) * 0.15));
  return out;
}

export function mockFleet(): FleetSnap {
  const now = Date.now() * 1e6;
  const vehicles: VehicleSnap[] = [
    {
      vehicle_id: "sim-veh-001", online: true, last_heartbeat_age_s: 0.8,
      mode: "autonomous", speed_mps: 1.6, soc: 0.85, voltage: 52.3,
      gear: "D", steer_rad: 0.02, speed_history: seedHistory(1.6),
      capabilities: { stack: "AUTONOMY_STACK_ROS1", stack_version: "ROS 1 Noetic", gateway_version: "0.1.0-sim" },
      pose: POSES[0],
    },
    {
      vehicle_id: "sim-veh-002", online: true, last_heartbeat_age_s: 1.1,
      mode: "autonomous", speed_mps: 0, soc: 0.62, voltage: 49.8,
      gear: "P", steer_rad: 0, speed_history: seedHistory(0),
      capabilities: { stack: "AUTONOMY_STACK_ROS1", stack_version: "ROS 1 Noetic" },
      pose: POSES[1],
    },
    {
      vehicle_id: "sim-veh-003", online: false, last_heartbeat_age_s: 42,
      mode: "stopped", speed_mps: 0, soc: 0.31, voltage: 46.1,
      gear: "P", steer_rad: 0, speed_history: seedHistory(0),
      capabilities: { stack: "AUTONOMY_STACK_ROS2", stack_version: "Humble" },
      pose: POSES[2],
    },
  ];
  return {
    server_time_ns: now,
    vehicles,
    takeover: { active: false },
    events: [
      { ts_ns: now - 2e9, level: "info", vehicle_id: "sim-veh-001", text: "演示数据：后端未连接，正在展示模拟遥测" },
      { ts_ns: now - 9e9, level: "warn", vehicle_id: "sim-veh-003", text: "链路丢失：6 秒无遥测，判定离线" },
      { ts_ns: now - 30e9, level: "info", vehicle_id: "sim-veh-002", text: "车辆注册（网关 gw-local-002，栈 AUTONOMY_STACK_ROS1）" },
    ],
  };
}

// mock 模式 1Hz 推进：车速抖动、历史环增长、离线车心跳龄增长。
export function tickMock(prev: FleetSnap): FleetSnap {
  const now = Date.now() * 1e6;
  const vehicles = prev.vehicles.map((v) => {
    if (!v.online) return { ...v, last_heartbeat_age_s: v.last_heartbeat_age_s + 1 };
    const speed = v.mode === "autonomous"
      ? Math.max(0, 1.6 + Math.sin(now / 4e9 + v.pose.x) * 0.25)
      : 0;
    const hist = [...(v.speed_history || []), Number(speed.toFixed(2))].slice(-150);
    return {
      ...v,
      speed_mps: Number(speed.toFixed(2)),
      speed_history: hist,
      last_heartbeat_age_s: 0.9,
      soc: Math.max(0.05, v.soc - 0.0001),
    };
  });
  return { ...prev, server_time_ns: now, vehicles };
}
