# 运营大屏实施计划 v2（第 10 步）—— React + Go + PostgreSQL + WAF

状态：待实施 ｜ 产出审核：Codex ｜ 预计工作量：3～5 天（单人）
技术栈决策（与架构文档阶段 1 一致）：前端 React(TypeScript+Vite)，
云端服务 Go，PostgreSQL/PostGIS 为业务真相源，nginx 反向代理 + 限流
作为第一阶段 WAF，OIDC 登录列入第二阶段（配置预留、不阻塞验收）。

> 执行约定：按任务顺序做，每个任务有「验收」；全部完成后提交，
> Codex 按第 5 节清单审核。v1 计划（Python hub + 零构建前端）作废。

---

## 0. 现状架构（as-is，先认清再改）

当前仓库是「阶段 0 验证原型」，全部 Python 单文件脚本：

- 车端：gateway.py（UDS 接入/预筛/路由）、vehicle_side.py（仲裁器）、
  ros1 适配器、media-agent（WebRTC/双路视频）、workspace-agent（终端）
- 云端原型：control-authority（租约/fencing）、control-relay（QUIC）、
  media-control（收流合并）、vehicle-access（MQTT 接入演示）
- 传输：车内 UDS；车云 MQTT5(mTLS) / UDP 演示 / QUIC / WebRTC
- 消息：protocols/ 下 Protobuf 的 JSON 镜像（信封格式见第 2 节）
- 无数据库（全内存）、无 WAF、无登录、无 Go、无正式前端

定位：这层是「协议与安全逻辑的实验台」，不会被扔掉——车端继续用它做
参照实现；云端逐个服务用 Go 重写（见第 6 节迁移表）。

## 1. 目标架构（to-be）

~~~
浏览器 React(Vite+TS)
   │  HTTPS（nginx：TLS 终结 + 限流 + WAF 基础规则）[OIDC 二期]
   ▼
Go fleet-hub（server/fleet/）
   │  REST /api/*  +  WebSocket /ws/fleet
   │  订阅 MQTT（paho.mqtt.golang，mTLS，access 证书）
   ▼
mosquitto(ra-mqtt:8883) <──mTLS── 车端 gateway(--uplink mqtt)
   │
   ▼
PostgreSQL/PostGIS（业务真相源） + Redis（在线状态缓存，可选）
~~~

关键原则：前端只依赖「协议契约」（REST/WS 的 JSON = protobuf 镜像），
不依赖实现语言。后端从 Python 换 Go，前端一行不改——这就是第 2 步先
冻结协议的价值。

## 2. 现有接口事实（已核对，照抄勿猜）

- MQTT 话题（QoS1，JSON 信封）：vehicle/sim-veh-001/register（retained，
  GatewayCapabilities）/ telemetry（SignalUpdate，2Hz）/ status（Heartbeat，2s）
- 云端订阅证书：deploy/pki/dev/ 的 ca.crt + access.crt + access.key
- 信封字段：schema_major/minor, message_type, vehicle_id, gateway_id,
  session_id, sequence, utc_time_ns, monotonic_time_ns, ttl_ms, trace_id, payload
- SignalUpdate.payload.signals[]：path + value(number|text) +
  sample_monotonic_ns + quality
- 信号清单：Vehicle.Speed(m/s)、Vehicle.Chassis.SteeringWheel.Angle(rad)、
  Vehicle.Powertrain.Transmission.CurrentGear、Platform.Autonomy.OperationMode
  （autonomous/remote_control/minimum_risk/stopped，任务 1 修好后为真值）、
  Vehicle.Powertrain.TractionBattery.StateOfCharge(0..1)/Voltage
- 端口：8883 MQTT、9100 控制、9300 authority、9443/9444 QUIC、
  9501/9502 视频、9600 终端；新增 9800 fleet-hub HTTP
- 环境坑：.venv python 需 export DYLD_LIBRARY_PATH=/opt/homebrew/opt/expat/lib；
  仓库路径含空格要加引号；Go 用 brew 的 go（先 go version 确认 >=1.21）

## 3. 任务分解

### 任务 1：车端上报真实运行模式（小补丁，同 v1）

client/simulator/vehicle_side.py 的 telemetry_loop，在 signals 组装后、
make_envelope 前插入：

~~~python
            for s in signals:
                if s["path"] == "Platform.Autonomy.OperationMode":
                    s["value"] = {"text": self.arb.mode}
~~~

验收：起 gateway(--uplink mqtt)+vehicle_side+authority，跑 takeover_demo，
订阅 telemetry 可见 OperationMode 随阶段变化。

### 任务 2：Go fleet-hub（server/fleet/，核心）

Go 模块：server/fleet/go.mod（module robot-agent/server/fleet）。
依赖仅：github.com/eclipse/paho.mqtt.golang、github.com/lib/pq
（或 pgx）、标准库。禁止 Web 框架（标准库 net/http 足够）。

职责与接口：
1) MQTT 接入：mTLS + access 证书，订阅 vehicle/#；按话题段解析
2) 状态模型（内存，供 WS/REST 直读）+ 落库（见任务 5 表）：
   - 在线判定：6 秒无 Heartbeat 即离线（巡检 goroutine 1s）
   - 信号最新值、车速历史环（150 点）、模式、capabilities
   - 事件推导：mode 变化（minimum_risk=critical、stopped=warn）、
     上下线（critical/info）、接管变化（warn/info）；环上限 200
3) HTTP（:9800，net/http）：
   - GET /api/fleet        全量快照（schema 同 v1，见下）
   - GET /api/vehicles/:id 单车详情（含 speed_history、capabilities）
   - GET /api/events?limit=50  事件列表
   - WS /ws/fleet          推送 {"type":"state"|"event", ...}，1Hz 状态
     + 事件即时推；客户端断开要清理 goroutine
   - GET / 静态服务前端构建产物（go:embed web/dist）
4) 接管状态：每 1s 向 127.0.0.1:9300 UDP 发 {"op":"status"} 合并进快照
5) 点云配置接口（给激光雷达地图用）：
   - GET /api/pointcloud?vehicle_id=  返回该车点云（阶段 1 返回内置合成
     场景的 JSON；阶段 2 换 COPC/PCD 切片，接口不变）

/api/fleet 快照 schema（前端唯一契约）：

~~~json
{"server_time_ns": 0,
 "vehicles": [{"vehicle_id":"sim-veh-001","online":true,
   "last_heartbeat_age_s":1.2,"mode":"remote_control","speed_mps":1.5,
   "soc":0.78,"voltage":48.6,"gear":"D","steer_rad":-0.2,
   "speed_history":[1.4,1.5],"capabilities":{"stack":"AUTONOMY_STACK_ROS1"},
   "pose":{"x":120.0,"y":80.0,"yaw":0.6}}],
 "takeover":{"active":true,"driver":"zhangsan","lease_id":"LEASE-x",
   "fencing":2,"seconds_left":3.4},
 "events":[{"ts_ns":0,"level":"critical","vehicle_id":"...","text":"..."}]}
~~~

pose 字段：阶段 1 由 hub 内置演示位姿表（每车固定坐标）；阶段 2 接定位。

验收：
1) go vet ./... && go build ./... 通过
2) 起链路后 curl /api/fleet 有车、online=true；隔 2s 两次 speed_history 增长
3) websocat 或浏览器连 /ws/fleet，杀 vehicle_side 后 6s 内收到离线事件
4) 内存无泄漏：跑 10 分钟 goroutine 数稳定（/debug/pprof 可选）

### 任务 3：authority 增加 status 只读查询（同 v1，小补丁）

server/control-authority/authority_service.py 增加 op=status 分支
（只读，不改状态、不下发信封），返回 active/driver/lease_id/fencing/
valid_until_unix_ns。验收：手工 UDP 查询 + takeover_demo 过程中查询。

### 任务 4：React 前端（web/ops-dashboard/，Vite + TS）

依赖白名单：react、react-dom、typescript、vite、three、
@react-three/fiber、xterm、@xterm/addon-fit。禁止任何 CDN/外链运行时资源。

结构：

~~~
web/ops-dashboard/
  package.json  vite.config.ts（dev 代理 /api、/ws -> 127.0.0.1:9800）
  src/main.tsx  src/App.tsx（侧栏路由，7 个分类）
  src/api.ts（REST + WebSocket；连不上自动降级 src/mock.ts 演示数据）
  src/mock.ts（与 UI 预览图同款的演示数据）
  src/pages/ Overview Vehicles VehicleDetail Drive Video Alerts Terminal
  src/components/ TopBar StatCards LidarView VehicleTable EventFeed
                  TakeoverPanel TerminalPanel VideoPanel
~~~

页面/组件规格（与 UI 预览图一致）：
- 顶栏：MQTT 服务器/网关心跳状态点、当前时间；左 logo「燃石创想
  数字孪生运维平台」；底部侧栏管理员 admin
- 侧栏 7 分类：总览大屏 / 车辆列表 / 车辆详情 / 远程驾驶·接管 /
  视频监控 / 告警与事件 / 远程终端
- 总览：5 张统计卡（在线/总数、行驶中、接管人数、告警数、链路健康度）
  + 激光雷达地图（LidarView）+ 三栏面板（列表+告警 | 详情+视频 |
  接管+终端），按钮齐全：自动刷新、全屏投屏、时间范围、刷新、
  申请接管、续租、交还控制权、紧急停车（红色、二次确认）、播放/全屏/
  截图/机位、打开终端/断开/重连、标记已读、导出日志
- 车辆列表：搜索 + 状态筛选页签 + 表格（ID/状态/模式/车速/电量/心跳），
  行点击联动详情与地图
- 告警：级别筛选 + 车辆筛选 + 列表 + 导出

LidarView 激光雷达点云规格（重点）：
1) 2D 鸟瞰 / 3D 轨道一键切换；拖拽旋转(3D)/平移(2D)、滚轮缩放、点大小可调
2) 多车同屏：每车点云按 pose 放入统一世界系；颜色按状态
   （绿=行驶、黄=空闲、灰=离线、红=告警）；静态基础设施点为暗蓝
3) 点车标签或表格行 -> 高亮该车 + 视角跟随
4) 数据源可配置：默认调 /api/pointcloud；「配置点云」按钮可本地加载
   JSON/CSV（每行 x,y,z[,intensity]），加载后替换静态场景、保留车辆
5) 性能：THREE.Points + BufferGeometry，10 万点内流畅；超出降采样
6) 图例面板（在线行驶/在线空闲/离线/告警中）与缩放/复位/2D/3D 控件

TerminalPanel：xterm.js 连车端（阶段 1 走 hub 代理的模拟回显即可，
接口预留 WS /ws/terminal；阶段 2 接 workspace-agent 真实 PTY）。

验收：
1) npm run build 通过，产物被 Go embed 后单二进制可开页面
2) 无后端时打开（mock 降级）页面完整可交互
3) 有后端时：列表/详情/事件/接管倒计时全部真实数据；地图 2D/3D 切换、
   加载自定义点云、多车高亮三项逐项通过
4) 紧急停车后 2s 内模式变 stopped 且事件流出 critical

### 任务 5：PostgreSQL 与迁移（server/migrations/0001_init.sql）

~~~sql
CREATE TABLE vehicles (
  id TEXT PRIMARY KEY, gateway_id TEXT, stack TEXT, vin TEXT,
  cert_until TIMESTAMPTZ, first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen TIMESTAMPTZ);
CREATE TABLE vehicle_state (
  vehicle_id TEXT PRIMARY KEY REFERENCES vehicles(id),
  online BOOLEAN NOT NULL DEFAULT false, mode TEXT,
  speed_mps DOUBLE PRECISION, soc DOUBLE PRECISION,
  voltage DOUBLE PRECISION, gear TEXT, steer_rad DOUBLE PRECISION,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE telemetry_samples (
  vehicle_id TEXT NOT NULL, ts TIMESTAMPTZ NOT NULL,
  path TEXT NOT NULL, num DOUBLE PRECISION, txt TEXT);
CREATE INDEX idx_telemetry ON telemetry_samples (vehicle_id, ts DESC);
CREATE TABLE events (
  id BIGSERIAL PRIMARY KEY, ts TIMESTAMPTZ NOT NULL DEFAULT now(),
  vehicle_id TEXT, level TEXT NOT NULL, text TEXT NOT NULL);
CREATE TABLE leases (
  id TEXT PRIMARY KEY, driver TEXT NOT NULL, fencing BIGINT NOT NULL,
  vehicle_id TEXT, valid_from TIMESTAMPTZ NOT NULL DEFAULT now(),
  valid_until TIMESTAMPTZ NOT NULL, state TEXT NOT NULL DEFAULT 'active');
CREATE TABLE audit_logs (
  id BIGSERIAL PRIMARY KEY, ts TIMESTAMPTZ NOT NULL DEFAULT now(),
  actor TEXT NOT NULL, action TEXT NOT NULL, detail JSONB);
~~~

- deploy/compose/postgres.yml：postgres:16 + 挂载 migrations；hub 启动时
  幂等执行迁移（schema_migrations 表记录版本）
- 所有 SQL 必须参数化（$1...），禁止字符串拼接
- 遥测采样落库做 1Hz 抽稀（不全量存 2Hz），保留策略注释写明

验收：docker compose up -d 后 hub 自动建表；跑演示后 psql 查询
vehicles/vehicle_state/events/leases 有数据；重复重启 hub 迁移不报错。

### 任务 6：入口与 WAF（deploy/compose/ingress.yml + nginx.conf）

- nginx 反代 :443 -> fleet-hub:9800，TLS 终结（复用 deploy/pki/dev 或
  自签新证书），限流（limit_req 10r/s 突发 20）、禁隐藏路径、
  安全头（CSP、X-Frame-Options、HSTS）
- OIDC（dex 或企业 IdP）列为阶段 2：本期只在 nginx 留 auth_request
  注释位与前端登录页占位路由，不阻塞验收
- 验收：curl -k https://127.0.0.1/ 200；连续 100 次请求触发 429 可见；
  安全头齐全（curl -kI 检查）

### 任务 7：端到端演示编排 + 最终验收

deploy/demo/dashboard_demo.sh：检查 mosquitto/postgres -> 起
gateway(--uplink mqtt)、vehicle_side、authority、fleet-hub（go run 或
编译产物）-> 打印 https 地址与日志路径。

验收剧本（人工，边做边看）：

| # | 操作 | 预期 |
|---|---|---|
| a | 只起基础链路 | 在线 1 辆、模式 autonomous、速度曲线走、地图见车 |
| b | takeover_demo | 接管面板 driver 变化、倒计时走；模式 remote_control |
| c | minimum_risk_demo | 模式 minimum_risk(红)->stopped；事件 critical；曲线归零 |
| d | kill vehicle_side | 6s 内离线；事件 critical |
| e | 地图操作 | 2D/3D 切换、缩放旋转、加载自定义点云、多车高亮 |

### 任务 8：文档与提交

- server/fleet/README.md、web/ops-dashboard/README.md（风格同既有）
- 根 README 进度追加：- [x] 第 10 步：运营大屏（React + Go fleet-hub +
  PostgreSQL + nginx/WAF 一期；激光雷达 2D/3D 点云地图、多车同屏）
- commit：feat(10): 运营大屏（React 前端 + Go fleet-hub + PG + 点云地图）

## 4. 审核清单（Codex 逐项检查）

1. 前端无 CDN/外链运行时；依赖在白名单内；build 产物被 Go embed
2. Go：vet/build 通过；无 Web 框架；MQTT 用 paho.mqtt.golang + mTLS
3. WS/HTTP 并发安全；客户端断开清理 goroutine；无 goroutine 泄漏
4. SQL 全参数化；迁移幂等；遥测抽稀与保留策略明确
5. 离线判定 6s；事件/历史环有上限
6. OperationMode 补丁与任务 1 一致；未动仲裁器/网关安全逻辑
7. 地图五项（2D/3D、缩放旋转、自定义点云、多车高亮、图例）逐项可复现
8. 剧本 a-e 全过；nginx 限流与安全头生效
9. authority status 只读；README/进度/commit 风格一致

## 5. 坑速查

- paho.mqtt.golang：opts.SetOrderMatters(false) + AutoReconnect；
  TLS 用 tls.Config 载三张证书；ClientID 唯一
- go:embed 前端产物：//go:embed all:web/dist；SPA 回退 index.html
- vite dev 代理 ws 要写 ws: true
- three 点云：PointsMaterial sizeAttenuation 在 2D 正交下关掉；
  大数据量用 Float32BufferAttribute 一次上传
- xterm 需调 fit addon，容器尺寸变化要 refit
- React WS 重连：指数退避 + 重连后先拉 /api/fleet 全量对齐
- 时间基准：倒计时用 server_time_ns，不用浏览器墙钟

## 6. 迁移表（本期之后，Go 重写顺序）

| 现 Python 原型 | Go 目标 | 阶段 |
|---|---|---|
| fleet_hub（本期直接 Go 新建） | server/fleet | 本期 |
| authority_service | server/control-authority | 二期 |
| access_service | server/vehicle-access | 二期 |
| control_relay | server/control-relay | 三期 |
| 车端各 agent | 保持 Python/或 C++，车端不强制 Go | 不定 |

