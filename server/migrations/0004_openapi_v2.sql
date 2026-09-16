-- 0004：API 平台 v2——调用备注、每 Key 功能/车辆白名单、审计增强（等保三级）。

-- 审计：补调用备注与涉及车辆，便于追溯「这次调用是干什么的、动的是哪辆车」。
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS remark TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS vehicle_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS audit_log_key_idx ON audit_log (key_id);
CREATE INDEX IF NOT EXISTS audit_log_result_idx ON audit_log (result);

-- Key：备注 + 功能白名单（未勾选的功能不允许）+ 车辆白名单（车端数据隔离）。
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS remark TEXT NOT NULL DEFAULT '';
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scopes TEXT NOT NULL DEFAULT '[]';
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS vehicles TEXT NOT NULL DEFAULT '[]';
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS ips TEXT NOT NULL DEFAULT '[]';
