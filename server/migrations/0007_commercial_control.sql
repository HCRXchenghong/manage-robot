-- 0007_commercial_control.sql
-- 商用控制基线：车辆/Gateway 身份、一次性激活、持久化控制纪元与租约。
-- 任何真实车辆都必须先有 active Gateway 记录；MQTT 上报不再自动建车。

ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS lifecycle_state TEXT NOT NULL DEFAULT 'registered'
  CHECK (lifecycle_state IN ('registered', 'provisioning', 'offline', 'online', 'degraded', 'maintenance', 'quarantined', 'retired'));

CREATE TABLE IF NOT EXISTS vehicle_gateways (
  gateway_id TEXT PRIMARY KEY,
  vehicle_id TEXT NOT NULL REFERENCES vehicles(id) ON DELETE RESTRICT,
  certificate_sha256 TEXT NOT NULL UNIQUE,
  certificate_subject TEXT NOT NULL,
  certificate_not_after TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL DEFAULT 'active'
    CHECK (status IN ('pending', 'active', 'revoked', 'quarantined')),
  activated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ,
  revoke_reason TEXT NOT NULL DEFAULT '',
  last_authenticated_at TIMESTAMPTZ,
  UNIQUE(vehicle_id, gateway_id)
);
CREATE INDEX IF NOT EXISTS idx_vehicle_gateways_vehicle ON vehicle_gateways(vehicle_id, status);

-- Token 只保存 SHA-256 摘要；原文只在创建响应中返回一次。
CREATE TABLE IF NOT EXISTS gateway_enrollments (
  id TEXT PRIMARY KEY,
  vehicle_id TEXT NOT NULL REFERENCES vehicles(id) ON DELETE RESTRICT,
  gateway_id TEXT NOT NULL,
  token_sha256 TEXT NOT NULL UNIQUE,
  expected_spiffe_id TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_by TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  consumed_at TIMESTAMPTZ,
  consumed_certificate_sha256 TEXT NOT NULL DEFAULT '',
  CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS idx_gateway_enrollments_open ON gateway_enrollments(vehicle_id, gateway_id, expires_at)
  WHERE consumed_at IS NULL;

-- fencing 是每辆车的持久化控制纪元；不允许依赖进程内计数。
CREATE TABLE IF NOT EXISTS vehicle_control_epochs (
  vehicle_id TEXT PRIMARY KEY REFERENCES vehicles(id) ON DELETE RESTRICT,
  fencing_token BIGINT NOT NULL DEFAULT 0 CHECK (fencing_token >= 0),
  active_lease_id TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS control_leases (
  id TEXT PRIMARY KEY,
  vehicle_id TEXT NOT NULL REFERENCES vehicles(id) ON DELETE RESTRICT,
  gateway_id TEXT NOT NULL REFERENCES vehicle_gateways(gateway_id) ON DELETE RESTRICT,
  driver_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  fencing_token BIGINT NOT NULL CHECK (fencing_token > 0),
  state TEXT NOT NULL CHECK (state IN ('active', 'released', 'expired', 'revoked')),
  issued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  valid_until TIMESTAMPTZ NOT NULL,
  released_at TIMESTAMPTZ,
  release_reason TEXT NOT NULL DEFAULT '',
  CHECK (valid_until > issued_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_control_lease_per_vehicle
  ON control_leases(vehicle_id) WHERE state = 'active';
CREATE INDEX IF NOT EXISTS idx_control_leases_driver ON control_leases(driver_id, state, valid_until DESC);

-- 续租请求由客户端生成 request_id；重复提交只返回同一结果，不生成新租约或 fencing。
CREATE TABLE IF NOT EXISTS control_lease_renewals (
  lease_id TEXT NOT NULL REFERENCES control_leases(id) ON DELETE CASCADE,
  request_id TEXT NOT NULL,
  valid_until TIMESTAMPTZ NOT NULL,
  renewed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (lease_id, request_id)
);

CREATE TABLE IF NOT EXISTS control_security_events (
  id BIGSERIAL PRIMARY KEY,
  ts TIMESTAMPTZ NOT NULL DEFAULT now(),
  vehicle_id TEXT REFERENCES vehicles(id) ON DELETE SET NULL,
  gateway_id TEXT,
  lease_id TEXT,
  actor TEXT NOT NULL DEFAULT '',
  event_type TEXT NOT NULL,
  outcome TEXT NOT NULL,
  detail JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_control_security_events_vehicle_ts
  ON control_security_events(vehicle_id, ts DESC);
