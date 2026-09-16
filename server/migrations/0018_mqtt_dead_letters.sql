-- 0018：MQTT 入站拒绝证据。
--
-- 仅保存经过摘要的拒绝消息元数据，不保存原始 payload，避免把车端凭据或
-- 业务数据复制进审计库。该表用于故障定位、攻击检测和死信处置；它不是
-- 业务事实，也不能让一条被拒绝的消息重新进入控制或遥测投影。
CREATE TABLE IF NOT EXISTS mqtt_ingress_dead_letters (
  id BIGSERIAL PRIMARY KEY,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  topic TEXT NOT NULL,
  gateway_id TEXT NOT NULL DEFAULT '',
  vehicle_id TEXT NOT NULL DEFAULT '',
  message_type TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  sequence BIGINT NOT NULL DEFAULT 0 CHECK (sequence >= 0),
  reason TEXT NOT NULL,
  payload_sha256 TEXT NOT NULL,
  payload_size INT NOT NULL CHECK (payload_size >= 0)
);
CREATE INDEX IF NOT EXISTS mqtt_ingress_dead_letters_received_idx
  ON mqtt_ingress_dead_letters(received_at DESC);
CREATE INDEX IF NOT EXISTS mqtt_ingress_dead_letters_identity_idx
  ON mqtt_ingress_dead_letters(gateway_id, vehicle_id, received_at DESC);
