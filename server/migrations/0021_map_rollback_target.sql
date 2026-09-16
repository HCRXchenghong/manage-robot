-- 0021：显式记录回滚目标发布记录。
-- previous_publication_id 表示被回滚的当前发布；目标可能是同一地图版本
-- 的历史发布，必须单独记录，才能在车端 APPLIED 后恢复正确的审计状态。
ALTER TABLE map_publications
  ADD COLUMN IF NOT EXISTS rollback_target_publication_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS map_publications_rollback_target_idx
  ON map_publications(rollback_target_publication_id)
  WHERE rollback_target_publication_id <> '';
