-- 0022_inbound_sequence_cursors.sql：每个车辆/Gateway/消息会话的单调序列游标。
-- 精确去重账本防止同一包重复投影；本表额外防止 MQTT 无序回调或多实例
-- 竞争时，较旧序号覆盖已经确认的较新状态。session_id 必须由车端每次
-- 进程会话随机生成，不能在重启后复用。
CREATE TABLE IF NOT EXISTS inbound_message_cursors (
  vehicle_id TEXT NOT NULL,
  gateway_id TEXT NOT NULL,
  message_type TEXT NOT NULL,
  session_id TEXT NOT NULL,
  last_sequence BIGINT NOT NULL CHECK (last_sequence > 0),
  last_received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (vehicle_id, gateway_id, message_type, session_id)
);
CREATE INDEX IF NOT EXISTS inbound_message_cursors_received_idx
  ON inbound_message_cursors(last_received_at);
