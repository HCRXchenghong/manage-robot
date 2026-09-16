-- 0011：身份与开放 API Key 的持久化一致性。
-- 兼容已执行过旧版 0004 的数据库：迁移编号不变时，新增字段仍必须
-- 由后续幂等迁移补齐，避免服务启动后才在异步写入路径中暴露 schema 漂移。
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS remark TEXT NOT NULL DEFAULT '';
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scopes TEXT NOT NULL DEFAULT '[]';
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS vehicles TEXT NOT NULL DEFAULT '[]';
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS ips TEXT NOT NULL DEFAULT '[]';
-- last_used_at 仅用于审计/运营展示，不参与鉴权判定。
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS api_keys_last_used_idx ON api_keys(last_used_at DESC NULLS LAST);
