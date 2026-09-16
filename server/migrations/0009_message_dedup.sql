-- 0009_message_dedup.sql：跨重启 MQTT 去重账本。
-- Envelope 的 TTL 负责拒绝过期重放；本表负责拒绝同一会话/序号的 QoS1
-- 重复投递和进程重启后的重复投影。按 received_at 定期保留有限窗口。
CREATE TABLE IF NOT EXISTS inbound_message_dedup (
  vehicle_id TEXT NOT NULL,
  gateway_id TEXT NOT NULL,
  message_type TEXT NOT NULL,
  session_id TEXT NOT NULL,
  sequence BIGINT NOT NULL CHECK (sequence > 0),
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (vehicle_id, gateway_id, message_type, session_id, sequence)
);
CREATE INDEX IF NOT EXISTS inbound_message_dedup_received_idx
  ON inbound_message_dedup(received_at);
