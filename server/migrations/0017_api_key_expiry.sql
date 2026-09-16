-- 0017：API Key 必须具备明确的失效时间。
--
-- 旧记录按创建时间补齐 90 天有效期；新记录由 Fleet 显式写入，并由
-- 应用层限制最大有效期。NULL 不再表示永久有效，避免遗留密钥无限期存活。
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
UPDATE api_keys SET expires_at = created_at + interval '90 days' WHERE expires_at IS NULL;
ALTER TABLE api_keys ALTER COLUMN expires_at SET DEFAULT (now() + interval '90 days');
ALTER TABLE api_keys ALTER COLUMN expires_at SET NOT NULL;
CREATE INDEX IF NOT EXISTS api_keys_expires_idx ON api_keys(expires_at);
