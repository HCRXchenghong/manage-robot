// 与 Go fleet-hub 的 /api/fleet 契约一一对应（字段名冻结，勿改）。

export interface Pose {
  x: number;
  y: number;
  yaw: number;
}

export interface VehicleSnap {
  vehicle_id: string;
  online: boolean;
  last_heartbeat_age_s: number;
  mode: string;
  speed_mps: number;
  soc: number;
  voltage: number;
  gear: string;
  steer_rad: number;
  wheel_speeds?: number[] | null;
  speed_history: number[] | null;
  capabilities: Record<string, string>;
  pose: Pose;
}

export interface TakeoverSnap {
  active: boolean;
  driver?: string;
  lease_id?: string;
  fencing?: number;
  seconds_left?: number;
}

export interface EventSnap {
  ts_ns: number;
  level: string;
  vehicle_id?: string;
  text: string;
}

export interface FleetSnap {
  server_time_ns: number;
  vehicles: VehicleSnap[];
  takeover: TakeoverSnap;
  events: EventSnap[];
}

export interface CloudPart {
  count: number;
  positions: number[];
  intensities: number[];
}

export interface VehicleCloud {
  vehicle_id: string;
  pose: Pose;
  count: number;
  positions: number[];
  intensities: number[];
}

export interface PointCloudResp {
  scene_id: string;
  generated_ns: number;
  static: CloudPart;
  vehicles: VehicleCloud[];
}
