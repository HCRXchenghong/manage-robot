# 平台实施进度台账

## 1. 使用规则

本文件是项目唯一的进度记录。架构、功能、接口和安全要求见 [architecture-functional-specification.md](architecture-functional-specification.md)，不得在其他架构文档中记录完成状态、排期或“待完成”标记。

审计基线：2026-09-16。

最近验证：2026-09-16 已在真实 PostgreSQL + MQTT mTLS + Fleet + Vite 链路上
完成启动复测；Fleet `/readyz` 返回 `database_ready=true`、`mqtt_ready=true`、
`persistent_writes_ready=true`，运行中 Broker 的 ACL 与仓库版本校验和一致，
`0020`/`0021` 地图发布迁移已落库。本地开发证书和临时密钥仅用于隔离开发运行，
不能用于真实车辆或生产环境。

### 状态定义

| 状态 | 定义 |
|---|---|
| 已完成 | 已实现、已集成、已验证，且不依赖 mock、默认身份或未记录的人工前置条件。 |
| 进行中 | 存在实现或改动，但尚未完成集成验证，或仍有未闭合的安全/数据边界。 |
| 部分完成 | 有可用片段，但关键功能、生产约束或真实链路缺失。 |
| 未开始 | 没有与目标架构一致的实现。 |
| 阻塞 | 需要外部资源、硬件、供应商凭证或业务决策才能继续。 |
| 已移除 | 曾存在但已从运行路径删除；仍须检查引用、文档、构建产物与部署入口。 |

## 2. 总览

| 工作流 | 状态 | 当前结论 |
|---|---|---|
| 架构与功能基线 | 已完成 | 全量功能、边界、数据流、状态机、验收和无 mock 规则已固化到架构规范。 |
| 协议定义 | 进行中 | Go/Python Protobuf 绑定、Envelope 严格校验和确定性 HMAC `auth_tag` 已固化；控制 QUIC/中继/Gateway、MQTT、Gateway/Adapter/Arbiter 本机 UDS 已切到二进制，仍需完成兼容矩阵、重放状态和全部生产消息冻结。 |
| 车端 Adapter | 进行中 | ROS 1 已使用带 HMAC 的 Gateway 遥测 UDS 与 Arbiter 执行器 UDS；ROS 2/Apollo、签名标定包和实车验证仍需完成。 |
| Vehicle Gateway | 进行中 | Python Gateway 的 MQTT、控制 UDP、本机 UDS 均要求 Protobuf Envelope `auth_tag`，按角色/身份路由；已补 macOS/Linux UID/GID peer 校验和回执上限，生产身份、双链路、持久化 outbox 和设备证明仍未完成。 |
| Safety Arbiter | 进行中 | 独立 Go 内核已强制依赖执行器接口，在租约到期、撤销、看门狗和执行器故障时触发最小风险；生产二进制已移除旧 JSON 兼容入口，Protobuf LeaseGrant 同时校验外层 HMAC 与 Authority Ed25519，车型标定、审计和 HIL 仍需完成。 |
| Fleet 控制平面 | 进行中 | Go `server/fleet` 已含 HTTP、WSS、MQTT、PostgreSQL 和部分领域模块；配置写入、API Key、迁移 checksum、车辆注册、导航/租约/工作空间事务 outbox、遥测权威事务投影、就绪探针已补齐并完成本地真实依赖复测；服务边界和生产验证仍需完成。 |
| 身份与权限 | 部分完成 | 本地账号、角色、分组和 PostgreSQL token 摘要会话已存在；企业身份、MFA、设备证明、审计持久化闭环未完成。 |
| 接管与急停 | 进行中 | Fleet 内 DurableAuthority 已将租约/fencing/急停与签名 outbox 放入同一 PostgreSQL 事务，并以数据库唯一索引保护操作员互斥；双 QUIC、真实车端回执和设备/故障域证明仍需完成。 |
| 任务、路线与 ODD | 部分完成 | 路线与开放 API 代码存在；车辆真实确认、调度决策、ODD 策略与车端执行链未完成。 |
| 地图与点云 | 进行中 | 地图版本、转换、点云查看以及发布审批/outbox/车端 ACK/激活/回滚状态机已实现；对象存储、空间内核、真实 Map Agent 与生产权限仍未完成。 |
| 媒体 | 未完成 | 原有假摄像头和演示收发已删除；真实摄像头、WebRTC、SFU、录像与授权尚未实现。 |
| 远程工作空间与诊断 | 部分完成 | Python PTY 原型存在；会话与签名 TerminalGrant 已原子进入 PostgreSQL/outbox；反向安全通道、审批闭环、SOVD/UDS、审计和生产加固未完成。 |
| 前端运营平台 | 进行中 | React 页面和真实数据不可用态改造较多；全页面接口闭环、媒体、性能和可访问性验证未完成。 |
| 部署、可观测性与恢复 | 部分完成 | PostgreSQL、真实 Mosquitto mTLS Broker、Ingress Compose 配置存在并可启动；生产 Kubernetes、密钥治理、监控、备份恢复未完成。 |
| 无 mock 清理 | 进行中 | 前端 mock、假媒体、历史车队仿真代码、旧 UDP Authority、工作空间自动演示及其部署入口已删除；仍需完成媒体/诊断真实服务和构建产物审计。 |

## 3. 详细工作项

### P-001 架构与文档治理

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| 全量功能架构规范 | 已完成 | `docs/architecture-functional-specification.md` | 后续架构变更同步修订规范，不在其他架构文档写进度。 |
| 单一进度台账 | 已完成 | 本文件 | 每次代码或验证变化更新本文件。 |
| 历史文档去状态化 | 已完成 | 运营、身份、地图、导航、工作空间和控制中继文档已改为功能/接口规范；进度信息集中在本文件 | 后续架构变更继续只更新功能规范与本台账。 |
| 文档中的 mock/demo 表述 | 已完成 | 生产运行文档不再提供模拟车辆、假媒体、模拟终端或演示启动路径；测试边界单独声明 | 对新增文档执行同一审计。 |

### P-010 协议、数据模型与兼容性

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| `Envelope` 协议 | 进行中 | `protocols/platform/v1/` 已生成 Go 绑定，`protocols/gen/python/robot_agent_platform/` 已生成 Python 绑定；共享库已实现严格 schema 校验、确定性 HMAC-SHA256 `auth_tag` 与 round-trip/golden 测试；地图发布命令/ACK 已纳入版本化协议 | MQTT、车端 UDS 和全部遥测/资产/租约调用方切换为二进制 Protobuf；完善兼容矩阵与版本准入。 |
| 控制协议 | 进行中 | `control.proto` 已定义模式、租约、fencing、序号、TTL、回执；QUIC/Gateway/Arbiter 控制路径已接入二进制 payload、端到端 MAC 和 ACK | 将租约/能力/ODD、全部生产执行回执和兼容矩阵接入真实链路。 |
| 遥测协议 | 部分完成 | `telemetry.proto` 已定义 VSS 值、质量、时间和链路状态 | 固化信号字典、频率、精度、坐标系和兼容性测试。 |
| 能力协议 | 部分完成 | `capabilities.proto` 已覆盖系统、相机、Modem、地图和工作空间能力 | 建立数据库兼容矩阵与 Gateway 注册准入。 |
| VSS overlay | 进行中 | `protocols/vss/platform-overlay.yaml` 有本地改动 | 完成字段审核、单位、枚举、版本、兼容性与代码生成验证。 |
| 数据库领域模型 | 部分完成 | `server/migrations/0001` 至 `0006` 存在车辆、地图、API、身份等表 | 补齐组织、Gateway、证书、控制租约/命令/回执、任务、ODD、媒体、工作空间与不可变审计表。 |

### P-020 车端 Adapter

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| ROS 1 翻译函数 | 进行中 | `vehicle/adapters/ros1/adapter.py` 含状态、电池、ECU 映射和限幅；参数由严格标定对象提供 | 将标定包接入签名/版本准入并在台架冻结。 |
| ROS 1 真实节点 | 进行中 | `ros1_node.py` 通过 Gateway UDS 上报遥测，并在独立 Adapter 执行器 UDS 发布 Arbiter 已批准的真实 ECU 消息 | 接入 Gateway Protobuf、Peer ACL 和 ROS 集成测试。 |
| ROS 2 Adapter | 未开始 | 无实际 Adapter 实现 | 建立节点、QoS、坐标系、诊断和控制映射。 |
| Apollo Adapter | 未开始 | 无实际 Adapter 实现 | 建立 Cyber RT Reader/Writer、Protobuf 映射和控制链。 |
| Adapter 自检 | 已完成 | `adapter.py --self-test` 是唯一自检入口；无参数或未知参数直接拒绝启动；生产节点要求车辆/Gateway/标定/双 UDS 参数 | 将自检向协议 golden vector 和台架集成测试扩展。 |

### P-030 Vehicle Gateway 与车端安全内核

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| Gateway UDS | 进行中 | `vehicle/gateway/gateway.py` 已按 `adapter/arbiter/workspace` 角色握手；控制、遥测、ACK 均使用长度前缀 Protobuf，原始 payload/tag 透传并校验；对无 Arbiter、回执路由满载、过期和 macOS/Linux UID/GID peer 身份做失败闭环 | 完成组件生命周期、资源上限和组件证书/UID 绑定。 |
| MQTT mTLS 上行 | 部分完成 | Gateway 使用 paho MQTT、TLS、注册和遥测主题 | 替换开发证书默认值；Broker ACL、重连语义、投递确认、离线缓存与证书轮换。 |
| 控制预筛 | 进行中 | Gateway 对 Protobuf Envelope、身份、auth_tag 存在性、payload 字段、TTL 和消息类型进行预筛 | 不能以网关替代 Arbiter；补 Gateway 侧 tag 密钥策略、回执 outbox 与双边缘会话管理。 |
| 双链路 | 未完成 | 当前 Gateway 上行/下行未建立独立运营商、网卡与边缘会话模型 | 实现 A/B 链路绑定、健康、相关故障检测与策略输入。 |
| Safety Arbiter | 进行中 | `vehicle/safety-arbiter/` 已有独立签名、fencing、MAC、TTL、看门狗和强制 `Actuator` 接口；现已对原始 Protobuf Envelope/auth_tag 做最终验证，应用时间来自 Adapter 执行器回执，ACK 也签名 | 接入车型标定签名、审计和 HIL。 |
| 车端共享协议库 | 进行中 | `client/core/protocol.py` 使用生成 Python Protobuf 绑定，统一 Envelope、确定性 auth_tag、ControlCommand 和解析边界 | 完成所有 Adapter/Gateway/中继调用方迁移、版本兼容矩阵和发布包类型检查。 |

### P-040 Fleet 控制平面

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| Go 服务入口 | 部分完成 | `server/fleet/main.go` 可启动 HTTP、PostgreSQL、MQTT 和静态站 | 配置治理、结构化日志、健康端点、依赖就绪、容器化与生产配置。 |
| PostgreSQL 依赖保护 | 进行中 | 默认启动拒绝无 PostgreSQL；`--allow-volatile` 仅本地诊断，Middleware 已拒绝业务/身份/安全写操作；迁移版本记录已增加 SHA-256 checksum，已执行迁移被篡改时拒绝启动；`/readyz` 现在同时验证 Ping、真实事务写入和 MQTT；异步写队列满载时改为有界同步回退并暴露背压计数，不再静默丢弃写入 | 增加多实例迁移/故障切换测试，并将遥测批量投影纳入可恢复写入协议。 |
| 遥测状态投影 | 进行中 | `state.go` 维护在线、状态、事件和采样；有效 MQTT 遥测的去重领取、`vehicle_state`、`telemetry_samples` 与车辆在线生命周期已在同一 PostgreSQL 事务提交；同车回调在进程内串行化 | 建立严格序列游标/多实例乱序策略、质量时效投影、分区保留和跨实例故障演练。 |
| MQTT 消费 | 进行中 | `mqtt.go` 已切到二进制 Envelope，并在 topic/注册中心校验前执行 schema、时效和 HMAC 校验；有效载荷先完成类型解析再领取去重位；新增 `0018_mqtt_dead_letters.sql`，拒绝消息只持久化摘要元数据，不保存原始 payload，并暴露死信计数 | 实现按证书主体与车辆绑定的授权、双链路重放状态、死信处置/告警和多实例故障演练。 |
| HTTP/WSS | 部分完成 | `http.go`、`ws.go` 和认证逻辑存在；有 WS Origin/会话测试；开放 API 来源 IP 默认使用 TCP 对端，仅显式配置的反向代理 CIDR 才能提供可信 `X-Forwarded-For`；JSON 请求体严格限 4KB，pprof 同时要求 loopback 与 super 会话 | 分离领域路由、统一错误模型、限流/CORS、端到端授权和负载测试。 |
| 进程内模块 | 部分完成 | 地图、导航、配置、事件、设备、开放 API 等很多状态仍在进程内；配置变更已增加类型/范围校验、PostgreSQL 事务提交后更新内存并记录 `updated_by`；车辆手动注册已改为数据库先提交 | 把其余权威写操作迁移到数据库事务，明确缓存失效与多实例行为。 |

### P-050 身份、权限与审计

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| 本地账号与会话 | 进行中 | `auth.go` 有密码策略、PBKDF2、验证码、会话和锁定逻辑；`0006_identity.sql` 有用户/分组表，`0008_auth_sessions.sql` 仅保存 token SHA-256 摘要并支持重启后按 Cookie 懒恢复；API 写操作已加入双提交 CSRF，volatile 不恢复身份；登出、会话过期、改密踢出和删号踢出写入追加式平台审计 | 完成会话撤销结果的事务化审计关联、OIDC/SAML/MFA。 |
| 用户与分组隔离 | 部分完成 | `groups.go`、`filterSnapFor()`、WS 授权逻辑存在 | 扩展到全部资源并以数据库关系做强制授权；补越权测试矩阵。 |
| API Key/HMAC | 部分完成 | `openapi.go` 已从 PostgreSQL 恢复 API Key；创建、修改、撤销、最近使用时间和到期时间先持久化再更新内存；0017 将永久 Key 改为禁止，默认 90 天、最大 365 天，支持 RFC3339 到期时间或有效期秒数，过期请求返回 `EXPIRED` 并写入审计；管理审计已使用 PostgreSQL keyset 游标分页，支持日期/Key/结果筛选和受保护 CSV 导出；`openapi_test.go` 与真实 PostgreSQL 集成测试覆盖生命周期 | 完成密钥轮换/双 Key 过渡、服务账号和 OpenAPI 契约自动生成。 |
| 控制设备 | 部分完成 | `devices.go`、前端绑定界面和接管前设备在线检查存在 | 实现真实受信设备代理、硬件证明、标定、心跳和吊销。 |
| 短信登录 | 进行中 | `SendSmsCode()` 明确拒绝未配置网关；前端已隐藏手机号登录入口，不展示不可用能力 | 接入真实供应商并完成回调、限流、模板、审计和集成测试后再开放。 |
| 审计不可变性 | 部分完成 | `0016_audit_append_only.sql` 已禁止 `audit_log`/`audit_logs` UPDATE/DELETE；开放 API 审计同步写入 `audit_log`，启动恢复最近 500 条；管理查询从 PostgreSQL 使用稳定 keyset 游标分页，支持日期/Key/结果过滤与受超级管理员保护的 CSV 导出；导出仅包含证据字段，不含密钥原文 | 统一 `audit_log`/`audit_logs` 领域模型、追加写保留/归档、导出下载审计和完整性校验。 |

### P-060 接管、租约与急停

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| 控制 Authority | 进行中 | 旧 UDP/in-memory Authority 已移除；`server/fleet/durable_authority.go` 实现 PostgreSQL 事务、车辆专属租约、fencing、签名撤销、急停及事务 outbox；操作员唯一有效租约由 0015 数据库索引保护 | 补充分布式接管状态机、真实下行回执、专属急停权限和端到端验证。 |
| 车辆专属租约 | 进行中 | Fleet API 要求 `vehicle_id`、控制设备和幂等 request ID；租约与 fencing 在 PostgreSQL 事务内更新 | 完成所有权威写操作的数据库事务测试和多实例验证。 |
| 接管登记 | 部分完成 | `takeover_reg.go` 以 PostgreSQL `control_leases` 恢复活跃租约并作为 UI 投影；Authority 负责真正互斥与续租 | 继续完善数据库状态机、跨实例故障恢复和真实控制设备绑定。 |
| 急停 API | 进行中 | 前端已有二次确认；正在改为明确传递 `vehicle_id` | 移除浏览器 `sessionStorage` 伪锁定；接入车端确认、专属权限、审计和解除流程。 |
| 双 QUIC 中继 | 进行中 | `control_relay.py`、`dual_link_control.py` 与 Gateway 控制 UDP 已使用 Protobuf `Envelope(ControlCommand/ControlAck)`、确定性 `auth_tag`、端到端 MAC、显式双向 mTLS 和有限回执路由 | 完成双独立故障域部署、设备证明、可靠回执 outbox、车端 UDS Protobuf 化和 HIL 验证。 |
| ODD 准入与最小风险 | 未完成 | 协议枚举和原型看门狗存在 | 构建可配置、签名、车端执行、HIL 验证的 ODD 与最小风险状态机。 |

### P-070 车队、路线、任务与地图

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| 车辆列表、状态、事件 | 部分完成 | Fleet API 和前端页存在 | 完成真实数据闭环、质量标签、分页、查询、告警处置和访问控制。 |
| 路线与任务 | 部分完成 | `nav.go`、`routes.go`、`NavRoute.tsx` 存在；路线/取消与签名下行 outbox 已在同一事务提交，Broker 接收与车端确认仍分开 | 接入车端接收/确认、调度、ODD、状态反馈、幂等与回放。 |
| 地图中心 | 进行中 | `maps.go`、`mappcd.go`、`MapEdit.tsx` 等存在；地图发布申请、双人审批、可靠 outbox、Broker 投递状态、车端 ACK、激活和回滚已接入 PostgreSQL | 切换对象存储、后台 Worker、格式/坐标验证、真实 Map Agent、生产级发布权限与空间内核。 |
| 地图发布生命周期 | 进行中 | `map_publication.go`、`0020_map_publication_lifecycle.sql`、`0021_map_rollback_target.sql`；发布与回滚区分来源记录和历史目标版本，ACK 事务更新状态并防止跨车辆/版本确认；Fleet 地图发布集成测试通过 | 接入真实车端 Map Agent、生产对象存储/Worker、真实 Broker/车辆 ACK 端到端验收和发布审批策略。 |
| 点云查看 | 部分完成 | `PcdViewer.tsx`、地图接口与 CSV/PCD 处理代码存在 | 对接真实资产库与权限、分块/COPC、性能和精度验证。 |
| 空间数据 | 未完成 | PostGIS 尚未作为空间内核使用 | 建立坐标系、地理围栏、路线、轨迹、索引、ODD 查询与迁移。 |
| 地图转换工具位置 | 进行中 | PCD/CSV/多栈导入工具已迁移至 `map-engine/tools/`，Fleet 默认路径会按仓库位置解析；转换结果进入版本化地图资产并可参与发布生命周期 | 接入对象存储、Worker、PostGIS、生产资产校验和大文件处理。 |

### P-080 媒体、视频与录像

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| 假摄像头与演示收发 | 已移除 | `vehicle/media-agent/fake_camera.py`、演示 sender/receiver 已删除 | 清理历史文档、构建产物和所有引用。 |
| 运营端视频占位替代 | 进行中 | `VideoPanel.tsx` 仅显示真实媒体未注册状态，不创建 synthetic canvas | 完成真实媒体目录、会话授权、不可用状态、播放器错误处理与浏览器验证。 |
| 真实媒体 Agent | 未开始 | 车端媒体目录中的旧原型已删除，没有替代实现 | 实现硬件采集、编码、时间戳、双路贡献流、健康与证书。 |
| Edge/SFU/录像 | 未开始 | `media/` 仅有 README，`server/media-control` 已删除旧演示 | 选定并实现 SFU/TURN、媒体登记、双流合并、录像、权限和观测。 |
| 控制媒体准入 | 未开始 | 无真实 glass-to-glass 指标 | 定义并接入媒体年龄、帧率、丢包和解码健康策略。 |

### P-090 工作空间、诊断与远程图形

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| PTY 代理原型 | 部分完成 | `vehicle/workspace-agent/terminal_agent.py` 要求绑定车辆/Gateway、TerminalGrant 和 TLS 1.3 双向证书，具备令牌、PTY、回放和审计边界 | 替换本地 TCP 直连、字符串黑名单和单会话模型；接入反向安全通道、审批、录制和数据库审计。 |
| 终端客户端 | 已移除 | 旧 UDP Authority 终端客户端已删除；前端只展示真实 workspace-agent 反向会话状态 | 以 Workspace Broker、受信控制代理和正式会话协议替代。 |
| 自动工作空间演示 | 已移除 | `client/core/workspace_demo.py` 已删除，Workspace 文档不再提供自动演示入口 | 以真实反向安全通道、审批和 ASAM SOVD 实现替代。 |
| ASAM SOVD/UDS/DoIP | 未开始 | 无实现 | 建立诊断服务目录、代理、权限、审计与车辆 Adapter。 |
| RViz/RQt 图形会话 | 未开始 | 前端有 RViz 风格页面，但无真实图形代理 | 实现车辆端图形会话、访问令牌、流转发与资源限制。 |

### P-100 React 运营与远驾界面

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| 无后端伪数据 | 已移除 | `web/ops-dashboard/src/mock.ts`、通用默认 GLB 和假媒体已删除；`api.ts` 使用 `live/degraded/unavailable`，控制数据面不再接收 JSON/假回退；源码、静态资源和 Fleet 内嵌产物审计无运行路径命中 | 页面真实媒体、终端、点云和车辆接入仍按各自服务边界显示不可用态，不能以空态代替真实服务。 |
| 页面框架 | 部分完成 | 总览、车辆、地图、路线、驾驶、视频、终端、审计、API、管理等页面存在 | 按真实后端契约逐页验收空态、权限态、错误态、加载态与响应式表现。 |
| WebSocket 稳定性 | 部分完成 | `api.ts` 有重连、全量对齐和 degrade 状态 | 长时间运行、网络抖动、权限变更、会话失效与多标签测试。 |
| 急停界面 | 进行中 | 正在移除前端本地“急停锁定”状态 | 重新构建后确认只显示车端回执和真实车辆状态。 |
| 登录界面 | 部分完成 | 账号密码 + 图形验证码可用；未配置真实短信供应商时手机号入口已关闭 | 接入真实短信或企业 OIDC/MFA，并完成可访问性和错误处理。 |
| 视频/终端/RViz | 部分完成 | 页面存在但真实服务不完整 | 只在真实受权服务可用时启用，禁止模拟输出或伪视图。 |

### P-110 部署、基础设施与可观测性

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| PostgreSQL Compose | 部分完成 | `deploy/compose/postgres.yml` 存在 | 密码/密钥外置、备份、恢复、升级、HA 与迁移策略。 |
| Ingress/WAF | 部分完成 | `deploy/compose/ingress.yml`、nginx 配置存在 | 生产证书、统一安全头、速率限制测试、OIDC、零信任网络策略。 |
| MQTT 配置 | 部分完成 | `deploy/compose/mqtt.yml`、`mosquitto.conf`、ACL 和开发 mTLS 证书可启动真实 Broker | 生产 ACL、证书轮换、审计、集群、持久化与告警。 |
| Kubernetes | 未开始 | `deploy/kubernetes/` 为空 | 编写 Kubernetes 清单、网络策略、密钥、HPA、PDB、区域拓扑和恢复操作。 |
| 监控与追踪 | 未开始 | `deploy/monitoring/` 为空 | OpenTelemetry、Prometheus、Loki、Tempo、Grafana、告警和 SLO。 |
| 密钥治理 | 未完成 | `deploy/pki/dev/` 含开发 CA 与私钥 | 迁出/撤销开发私钥，接入生产 CA/KMS/TPM，实施轮换与吊销。 |
| 演示部署脚本 | 已移除 | 原 `deploy/demo/dashboard_demo.sh` 已删除，不再存在模拟器启动入口 | 对其余部署清单持续执行发布前审计。 |

### P-120 无 Mock 清理与验证

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| 前端 mock 数据 | 已移除 | `web/ops-dashboard/src/mock.ts` 与 `public/models/default.glb` 已删除，API/数字孪生页均改为明确不可用态；当前源码、静态资源、构建产物和运行路径均无伪数据入口 | 真实车辆、媒体、终端、点云接入后继续按接口契约验收，不允许恢复前端回退数据。 |
| 假媒体 | 已移除 | 假摄像头和演示收发文件已删除 | 检查 Git 历史外的当前引用、README、打包和部署文件。 |
| 模拟车辆目录 | 已移除 | 历史车队仿真服务、Web UI、测试与运行时上传资产均已删除 | 后续验证仅使用受控 `tests/` 夹具、HIL 与封闭场地流程。 |
| 硬编码车辆 | 已清理 | `server/vehicle-access/access_service.py` 不再使用固定车辆白名单 | 以数据库/受控配置维持车辆绑定与地图索引。 |
| 双链路控制客户端 | 进行中 | `client/core/dual_link_control.py` 要求显式双链路、车辆/Gateway/租约/MAC、CA、客户端证书和私钥；控制命令已使用 Protobuf、确定性 `auth_tag`，无默认链路入口 | 补真实边缘设备证明、ACK 端到端认证、可靠回执 outbox 和 HIL 验证。 |
| ROS 1 默认 demo | 已移除 | `adapter.py` 无参或未知参数均拒绝；仅 `--self-test` 运行隔离自检 | 将自检迁移为协议 golden vector。 |
| 假终端演示 | 已移除 | `workspace_demo.py` 及其文档入口已删除 | 完成真实工作空间代理的审批、审计和诊断协议。 |
| 文档与路径 | 已完成 | 已移除历史车队仿真入口、旧 UDP Authority、工作空间自动演示和独立 MQTT access 演示服务，并将地图工具迁入 `map-engine/tools/`；功能文档已去状态化 | 对新增构建产物和部署清单持续执行发布前审计。 |

### P-130 验证与质量门禁

| 项目 | 状态 | 证据/现状 | 剩余动作 |
|---|---|---|---|
| 前端类型与构建 | 已完成 | 当前工作树 `npm run typecheck`、`npm run build` 通过；Vite `127.0.0.1:5173` 与 Fleet 静态站均可访问；手机号不可用入口已移除；模型缺失不再回退通用资产；API 平台显示/编辑 Key 到期时间；浏览器刷新后真实空态与依赖状态稳定 | 补充多浏览器、长连接、媒体和无障碍自动化。 |
| Go 测试与静态分析 | 已完成 | Fleet、Safety Arbiter 与协议模块的 `go test -race`、`go vet` 通过；真实 PostgreSQL 地图、导航 outbox、API Key 和接管投影集成测试通过，且测试清理顺序已修正为先清资源再关闭 DB | 补充数据库/MQTT/多实例集成测试。 |
| Python 语法检查 | 已完成 | 全量 `vehicle client server map-engine` Python 语法检查通过；ROS 1 翻译自检通过 | 增加 Adapter/Gateway 本机 socket 集成测试。 |
| 差异检查 | 已完成 | 当前工作树 `git diff --check` 通过 | 发布前继续执行。 |
| 安全测试 | 进行中 | Safety Arbiter 已覆盖伪造租约、重复帧、旧 fencing、TTL、MAC 篡改、看门狗和执行器回执；Gateway 已覆盖 UID/GID 解析与本机 peer 读取；QUIC Relay 已覆盖错误 HMAC 拒绝；Fleet 有权限/WS/API/审计游标/pprof/请求体上限测试；MQTT 拒绝消息不再消耗有效载荷的去重位，并记录摘要死信证据；遥测事务覆盖重复投影与失败回滚 | 建立完整身份、越权、证书、上传、Broker ACL、分布式接管和 HIL 攻击矩阵。 |
| HIL/SIL | 未开始 | `tests/hil/`、`tests/simulation/` 尚未建立 | 建立台架、封闭场地、故障注入和最小风险验收。 |
| 长稳与性能 | 未开始 | 无持久运行报告 | 测试 WSS、MQTT、控制、媒体、数据库、地图作业的容量、故障恢复与资源泄漏。 |

## 4. 当前变更安全提示

以下项目仍属于**进行中**，不能作为实车安全能力宣称：

1. `protocols/protobuf/` 已有 Go/Python 生成绑定与 `auth_tag` 共享实现；MQTT、车端 UDS 和全部生产调用方尚未完成二进制迁移。
2. `client/core/dual_link_control.py` 已发送 Protobuf 命令并解析 Protobuf 回执；仍需真实边缘设备证明、ACK 端到端认证和可靠回执闭环。
3. `server/fleet/durable_authority.go` 仍需完成分布式接管投影、急停专属权限和真实车端回执闭环。
4. `vehicle/safety-arbiter/` 仍需车型标定签名、HIL/SIL 与执行器故障注入验证。

本台账中的验证只覆盖列出的自动化检查；在上述项目闭合前，不得把本地诊断服务连接到真实车辆。

## 5. 2026-09-16 收尾验证记录

本轮在隔离开发环境完成了以下复测与清理：

- Fleet 使用真实 PostgreSQL、Mosquitto MQTT mTLS 和持久化写入启动；`/readyz` 已验证 `database_ready=true`、`mqtt_ready=true`、`persistent_writes_ready=true`。
- 真实两段式本地登录已验证：账号 `ops_admin`，密码仅通过本地隔离开发环境交付，不得用于真实车辆或生产环境。
- 已通过浏览器核对总览、地图、导航、接管、视频、审计和管理页面；无车辆、媒体、终端、点云时显示真实空态或明确不可用态。
- 前端 `npm run typecheck`、`npm run build`，Fleet/Protocols/Safety Arbiter 的 `go test -race`、`go vet`、Fleet 构建及 `git diff --check` 已通过。
- 当前源码和 Fleet 嵌入前端产物未发现 mock、假摄像头、假终端、合成画面或演示入口引用；前端地图上传/编辑作者改为当前登录会话身份，不再使用硬编码 `admin`。
- 已通过审计页面清理 8 条伪急停车辆事件和 352 条旧系统演示事件；另精准清理 67 条无 `vehicle_id` 的旧内存接管租约及旧通用账号 `admin`、`operator`。
- 当前数据库应保持为空车队基线：用户仅 `ops_admin`，车辆、控制设备、地图、路线、工作空间会话、控制租约、控制命令、ControlAck、事件均为 0；保留的两条 `audit_logs` 仅是清理操作审计记录。
- 本轮进一步发现 9800 的旧进程仍缓存过期账号，已先确认 PID 和端口，再仅重启该 Fleet 进程；重启后数据库与浏览器管理页均只显示 `ops_admin`，避免以内存旧状态作为权威数据。
- 使用项目 `.venv` 重跑 Python 协议 round-trip、Relay HMAC 安全边界和 Gateway 二进制本机协议测试，结果为全部通过；系统 `python3` 因未安装 protobuf 的失败不计入项目测试结果。
- 通过浏览器逐页复核总览、车辆列表、地图中心、循迹导航、远程接管、视频监控、审计中心、远程终端、API 平台和组织管理；所有无真实资源场景均显示空态/不可用态，未见假数据回退。
- 前端源码、静态资源和 Fleet 内嵌产物的 mock/假摄像头/假终端/合成画面/演示入口搜索无命中（仅本台账、架构规范和测试边界中的规则性文字保留相关词）。
- 本轮重新执行 `npm run typecheck`、`npm run build`、Fleet/Protocols/Safety Arbiter 的 `go test -race` 与 Fleet `go vet`；所有命令通过。真实 PostgreSQL API Key 集成测试通过，测试生成的 Key 已清理，数据库基线仍为 `api_keys=0`。
- 本轮停止占用端口的旧 Fleet/Vite 进程并以最新二进制/源码重启；`5173` 返回 HTTP 200，Fleet `/healthz` 返回 `status=ok`，`/readyz` 返回 persistent 模式且 PostgreSQL、MQTT、持久化写入全部为 `true`。
- 本轮再次对运行源码和构建相关目录执行无 Mock/假媒体/合成数据/演示入口搜索，无命中；浏览器当前页面显示真实数据库、MQTT 和实时推送状态，空车队显示为无在线车辆。
- 本轮将导航路线/取消、控制租约签发/续租/释放/急停、工作空间会话与签名授权改为事务 outbox：业务事实与已认证下行记录同事务提交；新增 0015 数据库唯一索引，保护跨 Fleet 实例的操作员单活跃租约不变量；`TakeoverReg` 重启时从 PostgreSQL 恢复活跃租约投影。
- 本轮新增真实持久化写入探针：`/readyz` 只有 PostgreSQL Ping、临时表事务写入回滚和 MQTT 全部就绪时返回 200；`/metrics` 暴露 `robot_agent_persistent_writes_ready`，最新二进制已重新通过 Fleet race/vet、PostgreSQL 集成测试并启动复核。
- 本轮发现并修正真实集成测试的清理顺序问题：原测试在 `t.Cleanup` 前关闭数据库，导致测试夹具残留；现四组集成测试均先清理车辆/地图/路线/outbox/租约/API Key，再关闭连接。已删除此前遗留的 14 条测试车辆、2 张测试地图、5 条测试路线、5 条测试 outbox 和 1 条测试租约。
- 清理后基线复核为 `auth_users=1 (ops_admin)`、`vehicles=0`、`maps=0`、`navigation_routes=0`、`platform_outbox=0`、`active control_leases=0`、`workspace_sessions=0`、`audit_logs=2`，0015 迁移已落库；最新 Fleet 二进制运行于 `127.0.0.1:9800`，Vite 运行于 `127.0.0.1:5173`。
- 本轮完成 API Key 生命周期闭环：新增 0017 `expires_at` 非空约束与索引，创建/修改/重启恢复均携带到期时间；默认有效期 90 天、最大 365 天，管理 API 支持 `expires_at`（RFC3339）或 `expires_in_s` 二选一；过期 Key 不进入 nonce/限流成功路径，返回 401 `EXPIRED`，并按 Key、来源、备注写入 `expired` 审计结果。
- 本轮通过 Fleet race 测试、vet、前端 typecheck/build，真实 PostgreSQL API Key 持久化集成测试通过；Gateway Python 协议测试从正确模块目录运行，6 项全部通过。
- 本轮将本地遗留的明文浏览器会话缓存 `data/auth-sessions.json` 清空；该文件不再被当前认证实现读取，身份权威仅来自 PostgreSQL，运行态仍保持 `ops_admin` 单一账号与空车队基线。
- 本轮将开放 API 审计从可丢弃的通用异步写队列改为有界同步 PostgreSQL 写入，并新增跨重启/多实例的审计读取合并；真实 PostgreSQL 集成测试已验证 API Key 到期字段和创建审计证据均可恢复。
- 本轮为开放 API 审计增加 PostgreSQL keyset 游标分页、日期范围过滤和超级管理员 CSV 导出；导出使用受限批次读取，不使用 OFFSET 或一次性加载全表，游标包含时间/行 ID/来源并拒绝跨来源伪造。
- 本轮为 MQTT 入站拒绝增加 `mqtt_ingress_dead_letters` 摘要证据表和 Prometheus 计数；先解析类型 payload 再写去重账本，ControlAck 先以幂等方式持久化后再领取去重位，降低数据库故障导致回执丢失的风险。
- 本轮修正开放 API 来源 IP 信任边界：默认拒绝客户端伪造 `X-Forwarded-For`，只有 `trusted_proxy_cidrs` 配置命中的直接代理可提供转发地址；同时为登出、会话过期、改密和删号踢出增加不含 token/密码的追加式平台审计。
- 本轮将 Fleet 异步 DB 队列满载行为从“丢弃”改为有界同步回退，新增 `robot_agent_db_write_queue_fallbacks_total` 计数；正常状态仍异步，不再把持久化缺口隐藏在日志历史之外。

上述验证只证明本地隔离环境的真实依赖链和空态行为，不能替代车端、硬件、供应商或生产集群验收。ROS 2/Apollo、双独立运营商链路、TPM/设备证明、车端 Workspace Broker、SOVD/UDS/DoIP、签名 ODD/HIL、PostGIS 完整空间服务、地图对象存储/Worker/回滚、真实媒体/WebRTC/SFU/TURN/录像、企业身份/MFA、Kubernetes、可观测性、备份恢复、多浏览器长稳性能和 HIL/SIL 仍按本台账前述条目处理，禁止用 mock 或演示实现替代。

## 6. 2026-09-16 最终质量门禁复测

- 重新构建前端并将产物嵌入最新 Fleet 二进制；`npm run typecheck`、`npm run build` 均通过，Vite 入口 `http://127.0.0.1:5173/` 返回 HTTP 200，Fleet 静态入口 `http://127.0.0.1:9800/` 返回 HTTP 200。
- 停止并重新启动本项目 Fleet 进程后，真实端点复核通过：`/healthz` 返回 `{"status":"ok"}`；`/readyz` 返回 `database_ready=true`、`mqtt_ready=true`、`persistent_writes_ready=true`、`mode="persistent"`。
- 从真实 PostgreSQL 读取迁移记录确认 `0018_mqtt_dead_letters.sql` 已应用，并保留 checksum 校验；Fleet 启动日志确认 PostgreSQL 迁移幂等完成、MQTT mTLS 连接并订阅成功。
- Fleet `go test -race -count=1 ./...`、Protocols `go test -race -count=1 ./...`、Safety Arbiter `go test -race -count=1 ./...`（含 Unix socket 集成测试）和 Fleet `go vet ./...` 均通过。
- 项目 `.venv` 中的 Python Protobuf round-trip、Gateway 本机二进制协议 6 项测试、Control Relay HMAC 安全边界测试 1 项及全量 Python 13 个文件语法检查均通过；Python 测试按各模块目录运行以保持其显式导入边界。
- 最终执行 `git diff --check` 通过；对当前源码、静态资源、Fleet 内嵌产物及文件路径执行 mock、假媒体、合成数据、模拟器和演示入口审计，无运行路径命中。测试注释和进度/架构规则中的文字不属于运行实现。
- `/metrics` 已确认暴露 `robot_agent_mqtt_ready`、`robot_agent_persistent_writes_ready`、`robot_agent_mqtt_dead_letters_total` 和 `robot_agent_db_write_queue_fallbacks_total`；当前死信与异步队列回退计数均为 0。

## 7. 2026-09-16 地图发布与运行收尾复核

- 最新 Fleet 进程仍运行于 `127.0.0.1:9800`，Vite 仍运行于 `127.0.0.1:5173`；前端入口返回 HTML，Fleet `/healthz` 返回 `status=ok`，`/readyz` 返回持久化模式且 PostgreSQL、MQTT、持久化写入全部就绪。
- 真实 PostgreSQL 已应用并校验 `0020_map_publication_lifecycle.sql` 与 `0021_map_rollback_target.sql`；`map_publications`、车辆、地图、outbox 和活跃控制租约均为 0，`ops_admin` 为唯一本地隔离开发账号，审计记录为 2 条清理证据。
- 运行中 `ra-mqtt-current` 容器实际加载的 `mosquitto.acl` 与 `deploy/compose/mqtt/mosquitto.acl` SHA-256 完全一致；Gateway 自有 namespace 的地图读取和 Fleet 地图下行写入权限已在当前 Broker 配置中生效。
- 地图发布申请、审批、可靠 outbox、Broker 投递状态、车端 `MapPublicationAck`、激活、回滚及回滚目标版本区分已通过真实 PostgreSQL 集成测试；没有使用模拟车辆或假传输，测试 ACK 仅作为受控协议夹具。
- `git diff --check` 通过；运行源码、静态资源和构建相关目录的 mock、假媒体、合成数据、模拟器和演示入口审计没有运行路径命中。仓库中保留的规则性文档和测试夹具说明不属于生产运行实现。

## 8. 2026-09-16 遥测权威事务与 HTTP 安全复核

- MQTT 有效遥测现在由 `State.ApplyTelemetryDurably` 处理：去重序列、车辆 `last_seen/lifecycle_state`、最新 `vehicle_state` 和抽稀后的 `telemetry_samples` 在同一 PostgreSQL 事务中提交；数据库任一写入失败会回滚去重声明，后续 QoS 重试不会被静默吞掉。
- MQTT 回调关闭顺序保证后，单车投影在进程内使用独立互斥，避免 `SetOrderMatters(false)` 并发回调把同一车辆的内存快照写乱；接收时间与信号采样时间分开保存，在线判定只由当前有效遥测驱动。
- `RequireMonitoring` 现在同时检查车辆生命周期；`quarantined`/`retired` 车辆即使保留历史能力快照，也不会重新进入监控投影或控制路径。
- `readJSONBody` 已修正为对已知长度和 chunked 请求都执行硬性 4KB 上限；超过上限返回 413，不再因分段读取停止而继续解析未读请求体。
- `/debug/pprof/*` 已收敛为 loopback + super 管理员双重准入；新增 HTTP 安全测试确认远端无 profile 泄露、未登录返回 401、非超管返回 403。
- 新增 `TestApplyTelemetryDurablyCommitsProjectionAndDedupTogether`，使用真实 PostgreSQL 验证首次提交、重复不重复投影以及车辆不存在时事务回滚且不消耗去重序列。

本次复测仍不改变第 4 节安全提示：真实车辆、生产证书/密钥、TPM/KMS、独立双链路、车端硬件、媒体基础设施、PostGIS/对象存储 Worker、企业身份、Kubernetes、监控恢复以及 HIL/SIL 尚未具备可验收的外部条件，不能以本地空车队运行态代替这些验收。
