-- 0020：地图发布可重复执行、车端确认和回滚状态。
--
-- 同一不可变版本可以在不同时间重新发布（例如回滚），因此旧版的
-- map_id/version/vehicle_id 唯一约束不能继续存在。发布 ID 仍是每次操作
-- 的幂等边界，车辆确认和错误详情只作为证据落库。

ALTER TABLE map_publications
  DROP CONSTRAINT IF EXISTS map_publications_map_id_version_vehicle_id_key;

ALTER TABLE map_publications
  DROP CONSTRAINT IF EXISTS map_publications_state_check;

ALTER TABLE map_publications
  ADD CONSTRAINT map_publications_state_check CHECK (state IN (
    'requested', 'approved', 'dispatched', 'confirmed', 'active',
    'superseded', 'rolled_back', 'rejected'
  ));

ALTER TABLE map_publications
  ADD COLUMN IF NOT EXISTS action TEXT NOT NULL DEFAULT 'apply'
    CHECK (action IN ('apply', 'rollback')),
  ADD COLUMN IF NOT EXISTS content_sha256 TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS coordinate_frame TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS vehicle_ack_result TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS vehicle_ack_detail TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS vehicle_ack_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS map_publications_dedupe_idx
  ON map_publications(vehicle_id, map_id, version, created_at DESC);
CREATE INDEX IF NOT EXISTS map_publications_active_idx
  ON map_publications(vehicle_id, state, updated_at DESC);
