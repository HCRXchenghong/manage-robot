-- 0013_map_object_manifest.sql：地图版本与内容清单权威化。
-- PostgreSQL 保存版本、文件清单和内容校验；当前本地部署使用受限文件系统作为
-- 内容存储，后续对象存储只需实现相同 object_key 读取契约，不能把 index.json 当事实源。

ALTER TABLE map_versions
  ADD COLUMN IF NOT EXISTS files JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN IF NOT EXISTS content_sha256 TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS content_size BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS map_objects (
  map_id TEXT NOT NULL REFERENCES maps(id) ON DELETE CASCADE,
  version INT NOT NULL,
  file_name TEXT NOT NULL,
  object_key TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
  content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (map_id, version, file_name),
  UNIQUE (object_key)
);
CREATE INDEX IF NOT EXISTS map_objects_object_key_idx ON map_objects(object_key);
