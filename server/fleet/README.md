# server/fleet —— fleet-hub 运营控制平面

Go 单二进制承载运营大屏：受绑定 Gateway 的 MQTT(mTLS) 接入车端数据 ->
实时缓存与 PostgreSQL 真相源 -> REST /api/* + WebSocket /ws/fleet + 内嵌前端静态站。

## 定位

- 前端只依赖「协议契约」（REST/WS 的 JSON = protobuf 镜像），不依赖实现语言；
- 商用租约/fencing 由 PostgreSQL `DurableAuthority` 原子签发，LeaseGrant
  使用 Authority Ed25519 签名投递；最终执行仍归独立车端 Safety Arbiter；
- 未激活的 `vehicle_id ↔ gateway_id ↔ certificate` 组合不能通过 MQTT 自动建车；
- PostgreSQL 不可用时生产启动直接拒绝；仅显式传 `--allow-volatile` 可作本机诊断，严禁生产使用。
- MQTT 不可用时后台重试，并通过 `/api/runtime` 明确报告未就绪。

## 运行

    go build -o fleet-hub . && ./fleet-hub \
      -dsn <pg> -migrations <repo>/server/migrations \
      -mqtt-host <broker> -mqtt-port 8883 \
      -ca <broker-ca.pem> -cert <fleet-client.crt> -key <fleet-client.key> \
      -envelope-auth-key /secure/envelope-hmac.b64
    # 常用 flag：-addr :9800 -client-id
    #          -lease-signing-key /secure/authority-ed25519.b64
    #          -gateway-bootstrap-addr :9843 -gateway-bootstrap-cert ...

本地诊断若没有 PostgreSQL，必须显式使用 `--allow-volatile --disable-mqtt`。该模式
只验证 UI/HTTP 可用性，并通过 `/api/runtime` 报告 `volatile-diagnostic`；不会生成
车辆、恢复本地身份、写入业务数据或提供控制能力。

容器：deploy/compose/postgres.yml（:5433）、deploy/compose/ingress.yml（:443）。
首次启动可设置 `RA_BOOTSTRAP_ADMIN` 与 `RA_BOOTSTRAP_PASSWORD` 创建超级管理员；
密码不会打印到日志，账号会持久化在 PostgreSQL。

## 接口

| 路由 | 说明 |
|---|---|
| GET /api/fleet | 全量快照（前端唯一契约） |
| GET /api/vehicles/{id} | 单车详情 |
| GET /api/events?limit= | 事件（最新在前，环上限 200） |
| GET /api/pointcloud?vehicle_id= | 指定车辆上传的真实 PCD/CSV 点云；未上传返回错误 |
| GET /ws/fleet | 1Hz 状态 + 事件即时推 |
| GET /ws/terminal?vehicle_id= | 仅车端 workspace-agent 注册反向通道后开放；当前未注册会明确拒绝 |
| GET /api/runtime | PostgreSQL 与 MQTT 依赖就绪状态 |
| GET /api/audit?limit=&cursor=&key_id=&result=&from=&to= | 超级管理员审计查询；PostgreSQL keyset 游标分页 |
| GET /api/audit/export?limit=&key_id=&result=&from=&to= | 超级管理员审计 CSV 导出；最多 10000 行，不含密钥原文 |
| POST /api/takeover/take | 受信控制设备绑定后的车辆专属持久租约 |
| POST /api/takeover/keepalive | 幂等续租，租约仍由 PostgreSQL Authority 管理 |
| POST /api/takeover/release | 释放租约并推进车辆 fencing 纪元 |
| POST /api/emergency-stop | 签名撤销租约并触发车端最小风险流程 |
| / | go:embed web/dist（SPA 回退） |
| GET /debug/pprof/ | goroutine 自检 |

## 判定口径与边界

- 在线 = 6 秒内收到遥测（遥测由车端仲裁器驱动，车端死则遥测立停；
  gateway 的 status 心跳只代表网关怀着，不参与车辆在线判定）；
- 车速历史环 150 点；事件环 200；非控制遥测写入使用有界异步队列（256），不阻塞 MQTT 回调；控制回执和审计写入在确认前持久化；
- 遥测入库 1Hz 抽稀（上行 2Hz）；保留/滚动清理由运维负责；
- 全部 SQL 参数化（$1...）；迁移幂等（schema_migrations 记版本）。

## 验收速查

    curl -s "localhost:9800/api/pointcloud?vehicle_id=<已登记车辆ID>" | python3 -m json.tool
