# Control Authority 领域边界

控制权服务负责接管准入、车辆专属租约、fencing 纪元、续租、撤销、急停和
安全审计。当前服务实现位于 `server/fleet/durable_authority.go`，以 PostgreSQL
为唯一权威数据源，并以 Authority Ed25519 签名的 `LeaseGrant` 经 Gateway
投递到车端 Safety Arbiter。

## 不可绕过的约束

- 车辆、操作员和受信控制设备必须明确绑定；一个车辆和一个操作员同时只能有
  一个有效接管关系。
- 租约、续租、释放、急停和 fencing 递增均在数据库事务中完成；进程内状态只
  是页面投影，不承担安全真相。
- 云端只负责授权和投递，最终是否执行、是否进入最小风险状态由车端
  `vehicle/safety-arbiter` 决定。
- PostgreSQL 不可用时控制写操作必须拒绝；本地 volatile 进程不能充当生产
  Authority。
