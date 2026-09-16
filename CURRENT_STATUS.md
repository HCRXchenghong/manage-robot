# Robot-agent 当前状态

最后更新：2026-09-17  目标发布：`v1.0.0`

这份文档是 GitHub 发布页的快速入口。它说明当前提交包含什么、验证到什么程度，
以及哪些能力仍然不能用于真实车辆或生产环境。详细的逐项进度以
[`docs/implementation-progress.md`](docs/implementation-progress.md) 为准；功能边界和
验收要求以 [`docs/architecture-functional-specification.md`](docs/architecture-functional-specification.md)
为准。

## 发布内容

本次发布使用 `Desktop/robot- manage/Robot-agent` 工作区的最新版本，而不是
`robot- manage1/Robot-agent`。两个副本从同一 Git 基线分叉；前者包含更多协议、Fleet
持久化、安全仲裁、地图发布、前端页面、测试和数据库迁移改动，因此以它作为发布源。

已纳入本版本的主要内容：

- 平台 Protobuf、Python/Go 生成绑定、Envelope 校验和确定性 HMAC；
- Fleet 的 PostgreSQL 迁移、车辆/能力/导航/地图发布/控制租约/审计/outbox 领域代码；
- Gateway、ROS 1 适配器、独立 Safety Arbiter 和控制中继的安全边界；
- 运营前端、地图/点云页面、审计和急停等页面；
- 安全、协议、持久化和地图发布测试，以及开发部署配置和文档。

## 已验证

截至 2026-09-16 的最近一次隔离环境复测记录如下：

- 前端 `npm run typecheck`、`npm run build` 通过；
- Fleet、Protocols、Safety Arbiter 的 `go test -race -count=1 ./...` 通过，Fleet `go vet ./...` 通过；
- Python Protobuf round-trip、Gateway 二进制协议、控制中继安全边界和 Python 语法检查通过；
- Fleet 在真实 PostgreSQL、MQTT mTLS 和 Vite 链路下启动，`/healthz`、`/readyz` 和 `/metrics` 可用；
- `git diff --check` 通过，运行路径未发现 mock 车辆、假媒体或演示入口。

这些结果只证明本地隔离依赖链可运行，不等于真实车辆或生产验收。

## 当前问题与限制

以下项目在本版本中明确未完成，不能通过文档或本地空车队运行状态视为已完成：

| 优先级 | 问题 | 影响 |
|---|---|---|
| 阻断上线 | ROS 2、Apollo、真实车型标定、HIL/封闭场地证据尚未完成 | 不能证明不同车型上的执行器安全和 ODD 行为 |
| 阻断上线 | TPM/HSM、企业 CA/CRL/OCSP、设备证明、生产密钥轮换尚未接入 | `deploy/pki/dev/` 只适合隔离开发，不能用于真车或生产 |
| 阻断上线 | 两个独立故障域的 QUIC、真实车端回执和 HA Authority/outbox 尚未完成 | 不能宣称远程控制具备商用级链路容错 |
| 高 | WebRTC/SFU/TURN、真实摄像头、录像和媒体授权尚未实现 | 视频与控制准入仍不能按生产媒体质量验收 |
| 高 | PostGIS、对象存储/地图 Worker、真实 Map Agent 尚未接入 | 地图/点云能力目前是本地服务和协议层实现 |
| 高 | OIDC/SAML、MFA、服务账号和全资源越权矩阵尚未完成 | 身份与权限不能视为企业级闭环 |
| 中 | Kubernetes、监控告警、备份恢复、容量/长稳和多实例故障演练尚未完成 | 不能直接按生产集群部署 |
| 中 | 迁移文件存在两个 `0003_*.sql`，当前加载器按完整文件名执行 | 现有启动不受影响，但后续迁移应采用全局唯一编号/名称 |
| 低 | 仓库保留开发证书 `.crt/.csr` 作为本地夹具，私钥由忽略规则排除 | 发布包不能被误当成生产 PKI；正式环境应重新签发证书 |

## 发布边界

- `data/`、`.venv/`、`node_modules/`、Fleet 本地二进制以及 `*.key`、`*.pem` 等运行期或
  私密文件被 `.gitignore` 排除，不属于 GitHub 发布内容；
- `deploy/pki/dev/` 中已跟踪的证书/CSR 是公开的开发夹具，不包含私钥；使用前仍应确认
  运行环境不会把它们带入生产；
- 本版本保留历史代码的删除和重命名，以当前工作区为准，不回退到旧演示/模拟器路径；
- 生产控制入口必须继续遵守 [`docs/commercial-control-baseline.md`](docs/commercial-control-baseline.md)
  的拒绝默认和上线门槛。

## 推荐后续顺序

1. 先完成 TPM/HSM、企业 CA、控制设备证明、车型标定和 HIL 证据；
2. 再完成双独立 QUIC、真实车端 ACK、媒体链路和多实例故障演练；
3. 最后补齐 PostGIS/对象存储、企业身份、Kubernetes、备份恢复和生产性能验收。

