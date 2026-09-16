// 与 Go fleet-hub 的 /api/fleet 契约一一对应（字段名冻结，勿改）。

export interface Pose {
  valid: boolean;
  frame?: string;
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
  kind?: string; // veh=车辆侧（告警与事件）；sys=系统侧（系统审计）
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
  expires_ns?: number;
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

export interface MapPublication {
  id: string;
  map_id: string;
  version: number;
  vehicle_id: string;
  state: string;
  action: "apply" | "rollback";
  compatibility?: Record<string, unknown>;
  previous_publication_id?: string;
  rollback_target_publication_id?: string;
  requested_by: string;
  approved_by?: string;
  content_sha256: string;
  coordinate_frame: string;
  vehicle_ack_result?: string;
  vehicle_ack_detail?: string;
  vehicle_ack_ns?: number;
  created_ns: number;
  updated_ns: number;
}

// /api/maps/{id}/points：3D 预览点云（服务端解析 PCD/CSV 后降采样，形状同 /api/pointcloud）
export interface MapPointsResp {
  map_id: string;
  version: number;
  kind: string; // pcd|csv
  name: string;
  count: number;
  total: number;
  sampled: boolean;
  positions: number[]; // xyz xyz ...
  intensities: number[]; // 可能为空
}

// ---- 循迹导航 ----

export interface NavPoint {
  name: string;
  x: number;
  y: number;
  at_ns?: number; // 计划到达时刻（定时路点，可选）
  dwell_s?: number; // 到点停留秒数（可选，0=不停）
}

export interface NavRoute {
  id: string;
  vehicle_id: string;
  name: string;
  points: NavPoint[];
  status: string; // queued|dispatched|accepted|running|completed|cancel_requested|cancelled|cancel_rejected|rejected|failed
  created_ns: number;
  dispatched_ns?: number;
  vehicle_ack_result?: string;
  vehicle_ack_detail?: string;
  current_point?: number;
  vehicle_ack_ns?: number;
  updated_ns?: number;
  trace_id?: string;
  origin: string; // console|open_api
}

// ---- 开放 API（等保三级） ----

export interface OpenScope {
  id: string; // vehicle.read|telemetry.read|alarm.read|task.read|task.write|control.write
  name: string;
  desc: string;
  kind: "read" | "write";
}

export interface APIKey {
  id: string;
  name: string;
  remark?: string;
  prefix: string;
  scopes: string[]; // 功能白名单：未勾选的功能不允许调用
  vehicles: string[]; // 车辆白名单："*"=全部；空=任何车辆不可访问
  ips: string[]; // 来源 IP 白名单：固定允许调用的一或多个 IP/网段；空=不限来源
  created_ns: number;
  expires_ns: number; // 必填到期时间；永久 Key 不存在
  revoked_ns?: number;
  last_used_ns?: number;
}

export interface AuditEntry {
  id?: number;
  ts_ns: number;
  key_id?: string;
  key_name?: string;
  method: string;
  path: string;
  result: string; // ok|auth_failed|denied|locked|replay|rate_limited|bad_request|error
  http: number;
  ip?: string;
  trace_id?: string;
  vehicle_id?: string;
  remark?: string;
  detail?: string;
  source?: "api" | "platform";
  actor?: string;
  action?: string;
}

// ---- 账号与组织（等保三级） ----

export interface Me {
  username: string;
  role: string; // super|group_admin|user
  groups: string[];
  display_name?: string;
}

// ---- 接管设备绑定 ----

export type DeviceType = "console" | "rc" | "keyboard" | "wheel";

export interface DeviceInfo {
  id: string;
  type: DeviceType;
  name: string;
  link?: string; // 自研一体机接入链接
  sn?: string; // 自研一体机 SN 码
  owner: string;
  online: boolean;
  created_ns: number;
}

// ---- 账号级接管登记（一账号一辆 / 一车一账号） ----

export interface ActiveTakeover {
  vehicle_id: string;
  driver: string;
  device_id: string;
  lease_id: string;
  fencing: number;
  until_ns: number;
  started_ns: number;
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
