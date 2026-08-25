// 与 Go fleet-hub 的 /api/fleet 契约一一对应（字段名冻结，勿改）。

export interface Pose {
  x: number;
  y: number;
  yaw: number;
}

export interface GpsSnap {
  fix: boolean;
  lat: number;
  lon: number;
  alt: number;
}

export interface VehicleSnap {
  vehicle_id: string;
  group: string;
  chassis: string;
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
  throttle_history: number[] | null;
  brake_history: number[] | null;
  throttle_pct: number;
  brake_pct: number;
  accel_mps2?: number;
  accel_history?: number[] | null;
  cabin_temp_c?: number;
  cabin_humidity_pct?: number;
  gps: GpsSnap;
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

// ---- 地图中心 ----

export interface MapVersion {
  version: number;
  files: string[];
  kind: string; // pcd|csv|png
  note?: string;
  author?: string;
  created_ns: number;
  derived_from?: string;
  meta?: Record<string, unknown>;
}

export interface MapEntry {
  id: string;
  vehicle_id: string;
  name: string;
  kind: string; // 3d_pcd|3d_csv|2d_png
  points?: number;
  sha256: string;
  size: number;
  source: string; // vehicle_push|manual|derived
  created_ns: number;
  updated_ns: number;
  versions: MapVersion[];
  latest_kind?: string;
  has_2d: boolean;
}

// ---- 循迹导航 ----

export interface NavPoint {
  name: string;
  x: number;
  y: number;
  at_ns?: number; // 计划到达时刻（定时路点，可选）
}

export interface NavRoute {
  id: string;
  vehicle_id: string;
  name: string;
  points: NavPoint[];
  status: string; // queued|dispatched|cancelled
  created_ns: number;
  dispatched_ns?: number;
  trace_id?: string;
  origin: string; // console|open_api
}

// ---- 开放 API（等保三级） ----

export interface APIKey {
  id: string;
  name: string;
  prefix: string;
  created_ns: number;
  revoked_ns?: number;
  last_used_ns?: number;
}

export interface AuditEntry {
  ts_ns: number;
  key_id?: string;
  key_name?: string;
  method: string;
  path: string;
  result: string; // ok|auth_failed|replay|rate_limited|bad_request|error
  http: number;
  ip?: string;
  trace_id?: string;
  detail?: string;
}

// ---- 账号与组织（等保三级） ----

export interface Me {
  username: string;
  role: string; // super|group_admin|user
  groups: string[];
  display_name?: string;
}

export interface GroupInfo {
  id: string;
  name: string;
  max_admins: number;
  max_users: number;
  created_ns: number;
  admins?: number;
  users?: number;
}

export interface UserInfo {
  username: string;
  display_name: string;
  role: string;
  groups: string[];
  created_ns: number;
}
