-- 0012：补齐旧环境遗漏的 API Key 来源 IP 白名单字段。
-- 0004 在部分已初始化数据库中只完成了审计字段，不能假设迁移名称
-- 已存在就代表当前 schema 完整。
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS ips TEXT NOT NULL DEFAULT '[]';
