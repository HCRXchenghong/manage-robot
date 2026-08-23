-- 0001_init.sql：运营大屏业务表（第 10 步）
CREATE TABLE IF NOT EXISTS vehicles (
  id TEXT PRIMARY KEY, gateway_id TEXT, stack TEXT, vin TEXT,
  cert_until TIMESTAMPTZ, first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen TIMESTAMPTZ);
CREATE TABLE IF NOT EXISTS vehicle_state (
  vehicle_id TEXT PRIMARY KEY REFERENCES vehicles(id),
  online BOOLEAN NOT NULL DEFAULT false, mode TEXT,
  speed_mps DOUBLE PRECISION, soc DOUBLE PRECISION,
  voltage DOUBLE PRECISION, gear TEXT, steer_rad DOUBLE PRECISION,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS telemetry_samples (
  vehicle_id TEXT NOT NULL, ts TIMESTAMPTZ NOT NULL,
  path TEXT NOT NULL, num DOUBLE PRECISION, txt TEXT);
CREATE INDEX IF NOT EXISTS idx_telemetry ON telemetry_samples (vehicle_id, ts DESC);
CREATE TABLE IF NOT EXISTS events (
  id BIGSERIAL PRIMARY KEY, ts TIMESTAMPTZ NOT NULL DEFAULT now(),
  vehicle_id TEXT, level TEXT NOT NULL, text TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS leases (
  id TEXT PRIMARY KEY, driver TEXT NOT NULL, fencing BIGINT NOT NULL,
  vehicle_id TEXT, valid_from TIMESTAMPTZ NOT NULL DEFAULT now(),
  valid_until TIMESTAMPTZ NOT NULL, state TEXT NOT NULL DEFAULT 'active');
CREATE TABLE IF NOT EXISTS audit_logs (
  id BIGSERIAL PRIMARY KEY, ts TIMESTAMPTZ NOT NULL DEFAULT now(),
  actor TEXT NOT NULL, action TEXT NOT NULL, detail JSONB);

