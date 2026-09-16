-- 0010_kernel_domains.sql：商用内核领域表。
--
-- 约束：跨进程/跨重启的事实只落 PostgreSQL；消息 Broker 仅负责传输。
-- 大文件放对象存储，数据库只保存内容寻址、版本、授权和状态。

CREATE TABLE IF NOT EXISTS platform_outbox (
  id TEXT PRIMARY KEY,
  dedupe_key TEXT NOT NULL UNIQUE,
  aggregate_type TEXT NOT NULL,
  aggregate_id TEXT NOT NULL,
  gateway_id TEXT NOT NULL,
  channel TEXT NOT NULL,
  payload BYTEA NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'sending', 'delivered', 'dead')),
  attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  sent_at TIMESTAMPTZ,
  last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS platform_outbox_ready_idx
  ON platform_outbox(status, next_attempt_at, created_at);

CREATE TABLE IF NOT EXISTS navigation_routes (
  id TEXT PRIMARY KEY,
  vehicle_id TEXT NOT NULL REFERENCES vehicles(id) ON DELETE RESTRICT,
  name TEXT NOT NULL,
  remark TEXT NOT NULL DEFAULT '',
  points JSONB NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('queued', 'dispatched', 'cancelled')),
  created_at TIMESTAMPTZ NOT NULL,
  dispatched_at TIMESTAMPTZ,
  trace_id TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL DEFAULT 'console'
);
CREATE INDEX IF NOT EXISTS navigation_routes_vehicle_idx
  ON navigation_routes(vehicle_id, created_at DESC);

CREATE TABLE IF NOT EXISTS control_commands (
  id TEXT PRIMARY KEY,
  vehicle_id TEXT NOT NULL REFERENCES vehicles(id) ON DELETE RESTRICT,
  gateway_id TEXT NOT NULL,
  control_session_id TEXT NOT NULL,
  lease_id TEXT NOT NULL,
  fencing_token BIGINT NOT NULL CHECK (fencing_token > 0),
  command_sequence BIGINT NOT NULL CHECK (command_sequence > 0),
  trace_id TEXT NOT NULL,
  payload BYTEA NOT NULL,
  state TEXT NOT NULL DEFAULT 'dispatched'
    CHECK (state IN ('created', 'dispatched', 'accepted', 'rejected', 'expired', 'failed')),
  issued_at TIMESTAMPTZ NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(control_session_id, command_sequence)
);
CREATE INDEX IF NOT EXISTS control_commands_vehicle_idx
  ON control_commands(vehicle_id, created_at DESC);

CREATE TABLE IF NOT EXISTS control_acks (
  control_session_id TEXT NOT NULL,
  command_sequence BIGINT NOT NULL CHECK (command_sequence > 0),
  vehicle_id TEXT NOT NULL REFERENCES vehicles(id) ON DELETE RESTRICT,
  gateway_id TEXT NOT NULL,
  result TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  applied_monotonic_ns BIGINT NOT NULL DEFAULT 0,
  source_link TEXT NOT NULL DEFAULT '',
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY(control_session_id, command_sequence, source_link)
);
CREATE INDEX IF NOT EXISTS control_acks_vehicle_idx
  ON control_acks(vehicle_id, received_at DESC);

ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS odd_policy_version TEXT NOT NULL DEFAULT '';
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS certificate_fingerprint TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS odd_policies (
  id TEXT PRIMARY KEY,
  version TEXT NOT NULL,
  name TEXT NOT NULL,
  rules JSONB NOT NULL,
  sha256 TEXT NOT NULL,
  authority_key_id TEXT NOT NULL,
  authority_signature BYTEA NOT NULL,
  state TEXT NOT NULL DEFAULT 'draft'
    CHECK (state IN ('draft', 'active', 'retired', 'rejected')),
  created_by TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(name, version)
);
CREATE INDEX IF NOT EXISTS odd_policies_state_idx ON odd_policies(state, created_at DESC);

CREATE TABLE IF NOT EXISTS map_publications (
  id TEXT PRIMARY KEY,
  map_id TEXT NOT NULL,
  version INT NOT NULL,
  vehicle_id TEXT NOT NULL REFERENCES vehicles(id) ON DELETE RESTRICT,
  state TEXT NOT NULL DEFAULT 'requested'
    CHECK (state IN ('requested', 'approved', 'dispatched', 'confirmed', 'active', 'rolled_back', 'rejected')),
  compatibility JSONB NOT NULL DEFAULT '{}'::jsonb,
  previous_publication_id TEXT,
  requested_by TEXT NOT NULL,
  approved_by TEXT NOT NULL DEFAULT '',
  vehicle_confirmed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(map_id, version, vehicle_id)
);
CREATE INDEX IF NOT EXISTS map_publications_vehicle_idx
  ON map_publications(vehicle_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS workspace_approvals (
  id TEXT PRIMARY KEY,
  vehicle_id TEXT NOT NULL REFERENCES vehicles(id) ON DELETE RESTRICT,
  requester TEXT NOT NULL,
  approver TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL,
  reason TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'requested'
    CHECK (state IN ('requested', 'approved', 'rejected', 'expired', 'revoked')),
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  decided_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS workspace_approvals_vehicle_idx
  ON workspace_approvals(vehicle_id, state, expires_at);

CREATE TABLE IF NOT EXISTS workspace_sessions (
  id TEXT PRIMARY KEY,
  approval_id TEXT NOT NULL REFERENCES workspace_approvals(id) ON DELETE RESTRICT,
  vehicle_id TEXT NOT NULL REFERENCES vehicles(id) ON DELETE RESTRICT,
  gateway_id TEXT NOT NULL,
  operator_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  state TEXT NOT NULL DEFAULT 'awaiting_vehicle'
    CHECK (state IN ('awaiting_vehicle', 'active', 'closed', 'expired', 'revoked')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  closed_at TIMESTAMPTZ,
  close_reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS workspace_sessions_vehicle_idx
  ON workspace_sessions(vehicle_id, state, expires_at);

CREATE TABLE IF NOT EXISTS workspace_audit (
  id BIGSERIAL PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES workspace_sessions(id) ON DELETE RESTRICT,
  operator_id TEXT NOT NULL,
  vehicle_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  outcome TEXT NOT NULL,
  detail JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS workspace_audit_session_idx
  ON workspace_audit(session_id, created_at DESC);

CREATE TABLE IF NOT EXISTS media_streams (
  id TEXT PRIMARY KEY,
  vehicle_id TEXT NOT NULL REFERENCES vehicles(id) ON DELETE RESTRICT,
  camera_id TEXT NOT NULL,
  edge_id TEXT NOT NULL,
  protocol TEXT NOT NULL,
  codec TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'offline'
    CHECK (state IN ('offline', 'registering', 'online', 'degraded', 'revoked')),
  stream_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  last_seen_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(vehicle_id, camera_id)
);

CREATE TABLE IF NOT EXISTS media_access_grants (
  id TEXT PRIMARY KEY,
  stream_id TEXT NOT NULL REFERENCES media_streams(id) ON DELETE RESTRICT,
  operator_id TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS control_devices (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL REFERENCES auth_users(username) ON DELETE RESTRICT,
  type TEXT NOT NULL,
  name TEXT NOT NULL,
  link TEXT NOT NULL DEFAULT '',
  serial_number TEXT NOT NULL DEFAULT '',
  pass_salt TEXT NOT NULL DEFAULT '',
  pass_hash TEXT NOT NULL DEFAULT '',
  attestation_sha256 TEXT NOT NULL DEFAULT '',
  firmware_version TEXT NOT NULL DEFAULT '',
  calibrated BOOLEAN NOT NULL DEFAULT false,
  online BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ,
  UNIQUE(type, serial_number)
);
ALTER TABLE control_devices DROP CONSTRAINT IF EXISTS control_devices_type_serial_number_key;
CREATE UNIQUE INDEX IF NOT EXISTS control_devices_serial_idx
  ON control_devices(type, serial_number) WHERE serial_number <> '';

CREATE TABLE IF NOT EXISTS platform_config (
  key TEXT PRIMARY KEY,
  value JSONB NOT NULL,
  updated_by TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS spatial_geofences (
  id TEXT PRIMARY KEY,
  group_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('allowed', 'restricted', 'slow_zone', 'parking')),
  coordinate_frame TEXT NOT NULL,
  geometry_geojson JSONB NOT NULL,
  min_speed_mps DOUBLE PRECISION,
  max_speed_mps DOUBLE PRECISION,
  version INT NOT NULL DEFAULT 1,
  state TEXT NOT NULL DEFAULT 'draft'
    CHECK (state IN ('draft', 'active', 'retired')),
  created_by TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 生产镜像启用 PostGIS 后，将 GeoJSON 投影为真正的空间列和 GiST 索引。
-- 开发数据库若未安装扩展，业务仍保持安全拒绝（只是不提供空间查询），
-- 不会把普通经纬度列冒充 PostGIS。
DO $$
BEGIN
  BEGIN
    CREATE EXTENSION IF NOT EXISTS postgis;
    EXECUTE 'ALTER TABLE spatial_geofences ADD COLUMN IF NOT EXISTS geom geometry(Geometry,4326)';
    EXECUTE 'CREATE INDEX IF NOT EXISTS spatial_geofences_geom_idx ON spatial_geofences USING GIST (geom)';
  EXCEPTION WHEN undefined_file OR feature_not_supported THEN
    RAISE WARNING 'PostGIS extension unavailable; spatial query endpoints remain disabled';
  END;
END $$;
