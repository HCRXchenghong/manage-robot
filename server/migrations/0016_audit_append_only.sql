-- 0016：审计表只允许追加。
--
-- audit_log 记录开放 API 调用，audit_logs 记录平台操作；两者都是证据
-- 流，不能通过业务接口或误操作 UPDATE/DELETE。保留/归档由受控的
-- append-only 导出与分区策略完成，不在在线业务库中直接擦除记录。
CREATE OR REPLACE FUNCTION robot_agent_reject_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'audit records are append-only';
END;
$$;

DROP TRIGGER IF EXISTS audit_log_append_only ON audit_log;
CREATE TRIGGER audit_log_append_only
  BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION robot_agent_reject_audit_mutation();

DROP TRIGGER IF EXISTS audit_logs_append_only ON audit_logs;
CREATE TRIGGER audit_logs_append_only
  BEFORE UPDATE OR DELETE ON audit_logs
  FOR EACH ROW EXECUTE FUNCTION robot_agent_reject_audit_mutation();
