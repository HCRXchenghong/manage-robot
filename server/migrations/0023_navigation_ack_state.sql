-- 0023_navigation_ack_state.sql：导航车端 ACK 与执行状态机。
--
-- dispatched 只表示 MQTT Broker 接收；只有已认证的 NavigationAck 才能把
-- 任务推进到 accepted/started/completed/cancelled，避免平台虚报车辆执行。
ALTER TABLE navigation_routes
  DROP CONSTRAINT IF EXISTS navigation_routes_status_check;

ALTER TABLE navigation_routes
  ADD CONSTRAINT navigation_routes_status_check CHECK (status IN (
    'queued', 'dispatched', 'accepted', 'running', 'completed',
    'cancel_requested', 'cancelled', 'cancel_rejected',
    'rejected', 'failed'
  ));

ALTER TABLE navigation_routes
  ADD COLUMN IF NOT EXISTS vehicle_ack_result TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS vehicle_ack_detail TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS vehicle_ack_current_point INT NOT NULL DEFAULT 0
    CHECK (vehicle_ack_current_point >= 0),
  ADD COLUMN IF NOT EXISTS vehicle_ack_at TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE navigation_routes SET updated_at = COALESCE(dispatched_at, created_at)
  WHERE updated_at IS NULL;

CREATE INDEX IF NOT EXISTS navigation_routes_state_idx
  ON navigation_routes(vehicle_id, status, updated_at DESC);
