-- 0003：告警与事件成熟化 + 车辆注册/分组隔离配套。
-- 事件带分组：非超管只能查本分组日志；平台级事件（vehicle_id 为空）仅超管可见。
ALTER TABLE events ADD COLUMN IF NOT EXISTS group_id TEXT NOT NULL DEFAULT '';
-- 每用户「已读水位」：总览小窗只展示水位之后的事件；告警页全量保留。
CREATE TABLE IF NOT EXISTS event_read_marks (
  user_id    TEXT PRIMARY KEY,
  read_until TIMESTAMPTZ NOT NULL
);
-- 车辆：分组落库（重启不丢）、底盘类型、孪生模型上传时间。
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS group_id TEXT NOT NULL DEFAULT '';
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS chassis TEXT NOT NULL DEFAULT '';
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS model_updated_at TIMESTAMPTZ;
-- 地图归属：上传者（超管/管理员账号）；vehicle 同步的走 source 字段。
ALTER TABLE maps ADD COLUMN IF NOT EXISTS group_id TEXT NOT NULL DEFAULT '';
ALTER TABLE maps ADD COLUMN IF NOT EXISTS uploader TEXT NOT NULL DEFAULT '';
-- 索引：事件按分组+时间检索
CREATE INDEX IF NOT EXISTS idx_events_group_ts ON events (group_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events (ts DESC);
