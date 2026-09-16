-- 0015：把接管互斥规则下沉到 PostgreSQL。
--
-- 进程内 TakeoverReg 只是 UI 投影，不能保护多 Fleet 实例之间的竞争。
-- DurableAuthority 已在事务内检查操作员占用；这个唯一索引把同一规则
-- 变成数据库级不变量，防止并发请求在不同实例上同时签发有效租约。
CREATE UNIQUE INDEX IF NOT EXISTS one_active_control_lease_per_driver
  ON control_leases(driver_id) WHERE state = 'active';
