-- 0019：Gateway 能力声明与控制准入快照。
--
-- 能力不是车辆控制资格；每次注册都按协议兼容矩阵评估并原子保存。
-- 缺少 TPM、独立双 Modem 等安全条件时仍可保留监控能力，但 control_allowed
-- 必须为 false，控制 Authority 只能使用已通过控制准入的最新快照。
CREATE TABLE IF NOT EXISTS vehicle_capabilities (
  vehicle_id TEXT NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
  gateway_id TEXT NOT NULL REFERENCES vehicle_gateways(gateway_id) ON DELETE CASCADE,
  matrix_entry TEXT NOT NULL DEFAULT '',
  stack TEXT NOT NULL,
  stack_version TEXT NOT NULL,
  gateway_version TEXT NOT NULL,
  adapter_version TEXT NOT NULL,
  safety_arbiter_version TEXT NOT NULL,
  topic_mapping_version TEXT NOT NULL,
  monitoring_allowed BOOLEAN NOT NULL DEFAULT false,
  control_allowed BOOLEAN NOT NULL DEFAULT false,
  control_reason TEXT NOT NULL DEFAULT '',
  snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (vehicle_id, gateway_id),
  CHECK (NOT control_allowed OR monitoring_allowed)
);
CREATE INDEX IF NOT EXISTS vehicle_capabilities_control_idx
  ON vehicle_capabilities(vehicle_id, gateway_id, control_allowed, updated_at DESC);
