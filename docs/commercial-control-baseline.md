# 商用安全控制基线

这份基线从本次改造起生效：真实车辆默认不可远程控制；只有完成下列全部
步骤、通过仿真/SIL/HIL 和封闭场地证据审查后，才可以按批准的 ODD 上线。

## 车辆激活

1. 管理员先登记车辆主数据（VIN/资产号、车型、ODD、组织和责任人）。
2. 管理员创建一次性 Gateway 引导凭证：`POST /api/vehicles/{id}/gateway-enrollments`。
   返回的凭证只显示一次、最长有效期 60 分钟，数据库只保存 SHA-256 摘要。
3. Gateway 在 TPM/安全芯片中生成不可导出私钥与 CSR；企业 CA 审批 CSR 并签发：
   - 证书 CN：`gateway_id`
   - 唯一 URI SAN：`spiffe://robot-agent/vehicle/{vehicle_id}/gateway/{gateway_id}`
4. Gateway 携带该证书和一次性凭证连接独立的 `gateway-bootstrap` mTLS 端口。
   服务端必须使用 `RequireAndVerifyClientCert`，不能通过 Nginx Header 或浏览器
   Cookie 替代设备证书。
5. 激活后 Broker ACL 只允许该 Gateway 访问自己的 namespace；Fleet-hub 继续对
   Topic、Envelope、证书绑定和车辆状态做第二次校验。

证书吊销将 Gateway 置为 `revoked`，车辆置为 `quarantined`；必须重新走受控
恢复流程，不能仅改数据库状态使其恢复在线。

## 租约与连续控制

- 一个业务接管会话可持续一班次；逻辑控制租约默认 45 秒，每 5 秒由受信控制
  代理以唯一 `request_id` 续租。
- 正常续租只延长 `valid_until`，不更换 `lease_id` 或 fencing token。
- 车辆租约的创建、续租和撤销均使用 PostgreSQL 串行化事务；fencing 跨服务
  重启保持单调递增。
- 车辆本地命令 TTL 与看门狗独立于租约；命令流中断时不能借续租“续命”，而是
  车端锁定最小风险状态，必须获得更高 fencing 的新租约才可重新武装。

旧 UDP Authority 和 `/api/takeover/request`、`/api/takeover/renew` 不属于商用控制
入口；所有接管都必须使用持久化 Authority、受信设备和车辆专属 Gateway。

## 车端仲裁器

`vehicle/safety-arbiter` 是独立 Go 进程，经 Gateway Unix socket 接收消息。它
要求配置受信任的 Authority Ed25519 公钥与命令 MAC 密钥，并拒绝：伪造租约、
错误车/Gateway、旧 fencing、过期租约、重复序号、TTL 过期、MAC 篡改和 ODD
限幅外命令。Gateway、MQTT、边缘中继和浏览器都不是最终安全决策者。

最小风险动作必须连接至经过车型标定和 HIL 验证的本地最小风险控制器；日志输出
只能作为审计证据，不能视为车辆已经完成安全停车。

## 尚未允许绕过的上线门槛

允许真车控制的上线门槛包括 HSM/KMS 签名适配、TPM 证明与企业 CA/CRL/OCSP、
受信控制代理设备密钥分发、车端最小风险执行器、双独立 QUIC relay、HA
Authority/outbox ACK、SBOM/OTA 和 HIL/封闭场地安全证据；任何门槛未满足时，
控制入口必须保持拒绝。
