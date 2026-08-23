# server/fleet —— fleet-hub（第 10 步运营大屏云端服务）

Go 单二进制承载运营大屏：MQTT(mTLS) 接入车端数据 -> 内存状态模型
（+ PostgreSQL 持久化）-> REST /api/* + WebSocket /ws/fleet + 内嵌前端静态站。

## 定位

- 前端只依赖「协议契约」（REST/WS 的 JSON = protobuf 镜像），不依赖实现语言；
- 审批/租约/fencing 仍归 control-authority，执不执行仍归车端仲裁器——
  本服务只做「聚合 + 转发 + 格式化」，安全链路一环不碰；
- PG 不可用自动降级纯内存模式（大屏可用、不落库）；MQTT 不可用后台重试。

## 运行

    go build -o fleet-hub . && ./fleet-hub
    # 常用 flag：-addr :9800 -dsn <pg> -migrations server/migrations
    #          -mqtt-host -mqtt-port -ca -cert -key -client-id -authority

容器：deploy/compose/postgres.yml（:5433）、deploy/compose/ingress.yml（:443）。
一键演示：deploy/demo/dashboard_demo.sh。

## 接口

| 路由 | 说明 |
|---|---|
| GET /api/fleet | 全量快照（前端唯一契约） |
| GET /api/vehicles/{id} | 单车详情 |
| GET /api/events?limit= | 事件（最新在前，环上限 200） |
| GET /api/pointcloud?vehicle_id= | 激光雷达点云（阶段 1 合成；阶段 2 换 COPC/PCD，接口不变） |
| GET /ws/fleet | 1Hz 状态 + 事件即时推 |
| GET /ws/terminal?vehicle_id= | 阶段 1 模拟终端回显（阶段 2 接真 PTY） |
| POST /api/takeover/{request,renew,release} | 接管桥接（转发 authority UDP） |
| POST /api/emergency-stop | 紧急停车（3s 短租约 + 零速指令，车端看门狗收敛） |
| / | go:embed web/dist（SPA 回退） |
| GET /debug/pprof/ | goroutine 自检 |

## 判定口径与边界

- 在线 = 6 秒内收到遥测（遥测由车端仲裁器驱动，车端死则遥测立停；
  gateway 的 status 心跳只代表网关怀着，不参与车辆在线判定）；
- 车速历史环 150 点；事件环 200；DB 写入异步队列（256），不阻塞 MQTT 回调；
- 遥测入库 1Hz 抽稀（上行 2Hz）；保留/滚动清理由运维负责（阶段 2 分区 + TTL）；
- 全部 SQL 参数化（$1...）；迁移幂等（schema_migrations 记版本）。

## 验收速查

    curl -s localhost:9800/api/fleet | python3 -m json.tool
    curl -s "localhost:9800/api/events?limit=5"
    curl -s localhost:9800/api/pointcloud | head -c 200
    curl -s -X POST localhost:9800/api/emergency-stop   # 2s 内 stopped + critical 事件
