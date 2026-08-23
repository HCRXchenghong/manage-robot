# 运营大屏实施计划（第 10 步）

状态：待实施 ｜ 产出审核：Codex ｜ 预计工作量：1～2 天（单人）
仓库：Robot-agent/ ｜ 上游依赖：第 5b 步（mTLS + MQTT）已合入

> 执行约定：每个任务都有「验收」小节，做完自测通过再进入下一个任务。
> 全部完成后提交一个或多个 commit（格式见任务 6），由 Codex 按第 5 节清单审核。

---

## 0. 目标

浏览器打开一个页面，实时看到：

1. 车辆在线状态（在线/离线、最近心跳距今多久）
2. 车辆姿态：运行模式（自动驾驶 / 远程驾驶 / 最小风险 / 已停稳）、实时车速曲线
3. 电池 SoC、电压、挡位
4. 接管状态：当前谁在驾驶、租约编号、fencing、到期倒计时
5. 事件流：模式切换、上下线、接管变化、最小风险触发（按严重级别着色）

数据全部走既有 MQTT 上行（第 5b 步），不新造车端链路；前端零构建、零 CDN。

## 1. 架构

~~~
车端                                    云端                          浏览器
vehicle_side.py ─UDS─> gateway.py ─mTLS/MQTT─> mosquitto(ra-mqtt:8883)
                       (--uplink mqtt)              │
                                                    ▼
                              authority_service(:9300) <─UDP轮询─ fleet_hub.py(新, :9800)
                                                                      │
                                                       HTTP /api/fleet + SSE /api/events
                                                                      ▼
                                                              index.html/app.js
~~~

新增代码只有三处：车端一个小补丁（任务 1）、fleet_hub.py（任务 2）、
前端三件套（任务 4）；authority 加一个只读查询操作（任务 3）。

## 2. 现有接口事实（已逐一核对，照抄即可，勿猜测）

### 2.1 MQTT

- Broker：容器 ra-mqtt，端口 8883，强制 mTLS（use_identity_as_username，
  身份=客户端证书 CN）。不要重建容器；用 docker ps 确认在跑。
- 话题（QoS 1，payload 为 JSON 信封）：
  - vehicle/sim-veh-001/register —— GatewayCapabilities，retained
  - vehicle/sim-veh-001/telemetry —— SignalUpdate（2Hz）
  - vehicle/sim-veh-001/status —— Heartbeat（0.5Hz，约 2 秒一条）
- 云端侧订阅证书：deploy/pki/dev/ 下 ca.crt + access.crt + access.key
  （CN=vehicle-access；vehicle-access/access_service.py 用的就是这套）。
- paho 用法（务必照抄此签名，v2 回调 API）：

~~~python
import paho.mqtt.client as mqtt
cli = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2,
                  client_id="fleet-hub", protocol=mqtt.MQTTv5)
cli.tls_set(ca_certs=ca, certfile=cert, keyfile=key)
cli.on_connect = on_connect   # (client, userdata, flags, reason_code, properties=None)
cli.on_message = on_message   # (client, userdata, msg)
cli.connect(host, port, keepalive=15)
cli.loop_start()
cli.subscribe("vehicle/#", qos=1)
~~~

### 2.2 信封（JSON，protobuf 镜像）

字段：schema_major, schema_minor, message_type, vehicle_id, gateway_id,
session_id, sequence, utc_time_ns, monotonic_time_ns, ttl_ms, trace_id, payload。

SignalUpdate.payload 形如：

~~~json
{"signals": [{"path": "Vehicle.Speed",
              "value": {"number": 1.6},
              "sample_monotonic_ns": 123456789,
              "quality": "SIGNAL_QUALITY_GOOD"}]}
~~~

value 里 number 与 text 二选一（按信号类型）。

### 2.3 会收到的信号（vehicle_side 经 ros1 适配器翻译）

| path | 值类型 | 含义 |
|---|---|---|
| Vehicle.Speed | number | 车速 m/s |
| Vehicle.Chassis.SteeringWheel.Angle | number | 方向盘转角 rad |
| Vehicle.Powertrain.Transmission.CurrentGear | text | 挡位 |
| Platform.Autonomy.OperationMode | text | 运行模式（任务 1 修好后反映真实状态） |
| Vehicle.Powertrain.TractionBattery.StateOfCharge | number | SoC，0..1 |
| Vehicle.Powertrain.TractionBattery.Voltage | number | 电池电压 |

模式取值（车端仲裁器维护，见 client/simulator/vehicle_sim.py 的 Arbiter）：
autonomous / remote_control / minimum_risk / stopped。

### 2.4 端口表（勿冲突）

| 端口 | 占用 |
|---|---|
| 8883 | mosquitto（mTLS） |
| 9100 | gateway 控制入口（UDP） |
| 9200 | 云端 UDP 演示通道 |
| 9300 | authority_service（UDP） |
| 9443/9444 | QUIC 中继 edge-A/B |
| 9501/9502 | 双路视频演示 |
| 9600 | 终端代理 |
| **9800** | **本步新增：大屏 HTTP** |

### 2.5 环境坑（必读）

- 用 .venv 里的 python 前必须：
  export DYLD_LIBRARY_PATH=/opt/homebrew/opt/expat/lib
  （Python 3.14 自带 expat 有 bug，不设会崩）
- 仓库路径含空格："robot- manage"，shell 里一律加引号
- 后台进程：分终端跑，或 nohup ... > 日志 2>&1 & ；
  注意某些 IDE 集成终端关闭时会带走子进程
- 新依赖：无（paho 已在 .venv；HTTP/SSE 用标准库）

## 3. 任务分解

### 任务 1：车端上报真实运行模式（小补丁）

文件：client/simulator/vehicle_side.py 的 telemetry_loop。
现状：is_autodrive 恒为 True，翻译出的 OperationMode 恒为 autonomous，
大屏看不到接管/最小风险状态变化。

精确改法（在 signals 组装完成后、make_envelope 之前插入覆盖逻辑）：

~~~python
            signals = adapter.translate_vehicle_status(vs, ts) + \
                adapter.translate_battery(bat, ts)
            # 第 10 步：让大屏看到仲裁器真实状态
            # （arb.mode ∈ autonomous/remote_control/minimum_risk/stopped）
            for s in signals:
                if s["path"] == "Platform.Autonomy.OperationMode":
                    s["value"] = {"text": self.arb.mode}
~~~

不要改 adapter.py（翻译层保持"只翻译"的职责；覆盖属于演示层决策）。

验收：
1. 起 gateway（--uplink mqtt）+ vehicle_side
2. 用 2.6 的调试订阅脚本看 /telemetry
3. 另开终端跑 python3 client/simulator/takeover_demo.py（authority 也要起）
   应看到 OperationMode 随阶段变化：remote_control -> …（租约到期后
   看门狗触发）minimum_risk -> stopped

### 任务 2：车队状态中枢 fleet_hub.py（核心，约 300 行）

新文件：server/fleet/fleet_hub.py。Python 标准库 + paho，无其他依赖。

职责：
1) MQTT 接入：按 2.1 连接，订阅 vehicle/#，按话题第二、三段解析
   （第二段=vehicle_id，第三段=register|telemetry|status|misc）
2) 内存状态模型（dict，vehicle_id -> 车辆状态）：
   - online：6 秒内收到过 /status 的 Heartbeat 即在线（心跳 2s 一条，
     3 条容忍）；用 monotonic 计时，单独线程每 1s 巡检超时
   - signals：path -> {"value": ..., "ts_ns": utc_time_ns}（存最新值）
   - speed_history：collections.deque(maxlen=150)，每收到
     Vehicle.Speed 追加一次（2Hz ≈ 75 秒曲线）
   - mode：取自 Platform.Autonomy.OperationMode
   - capabilities：来自 /register（retained，连上就会收到）
3) 事件推导（事件结构见下），环形上限 200 条：
   - mode 变化：minimum_risk=critical，stopped=warn，
     remote_control/autonomous=info（文本示例："模式切换：
     remote_control -> minimum_risk"）
   - 在线<->离线：离线=critical，恢复=info
   - 接管状态变化（来自任务 3 轮询）：新接管=info，到期/撤销=warn
4) HTTP 服务（http.server.ThreadingHTTPServer，:9800）：
   - GET /api/fleet -> 快照 JSON（schema 见下）
   - GET /api/events -> SSE：先回放最近 50 条（event: init），
     之后每来一条新事件推一行（event: new）；客户端断开要能
     捕获 BrokenPipeError/ConnectionError 并清理该客户端
   - GET / 及其他路径 -> 静态目录 server/fleet/web/
     （用 functools.partial(SimpleHTTPRequestHandler, directory=...)）
5) 接管状态轮询：线程每 1s 向 127.0.0.1:9300 发
   {"op": "status"}（UDP，超时 0.5s），结果并入 /api/fleet 与事件流

线程安全：所有共享状态用一把 threading.Lock；SSE 客户端列表同样。
事件结构：

~~~json
{"ts_ns": 1787400000000000000, "level": "critical",
 "vehicle_id": "sim-veh-001", "text": "模式切换：remote_control -> minimum_risk"}
~~~

/api/fleet 快照 schema：

~~~json
{
  "server_time_ns": 1787400000000000000,
  "vehicles": [{
    "vehicle_id": "sim-veh-001",
    "online": true,
    "last_heartbeat_age_s": 1.2,
    "mode": "remote_control",
    "speed_mps": 1.5,
    "signals": {"Vehicle.Speed": {"number": 1.5}},
    "speed_history": [1.4, 1.5, 1.5],
    "capabilities": {"stack": "AUTONOMY_STACK_ROS1"}
  }],
  "takeover": {"active": true, "driver": "driver-B",
               "lease_id": "lease-abc", "fencing": 2,
               "seconds_left": 3.4}
}
~~~

takeover 无接管时为 {"active": false}；seconds_left 由
(valid_until_unix_ns - server 当前墙钟)/1e9 计算。

验收：
1. 起链路后 curl 127.0.0.1:9800/api/fleet：有车辆、online=true、
   speed_history 在增长（隔 2 秒 curl 两次对比）
2. curl -N 127.0.0.1:9800/api/events：先收到 init 事件包，之后
   运行任意演示脚本能看到 new 事件滚出
3. 杀掉 vehicle_side：6 秒内 /api/fleet 里 online 变 false 且事件流
   出现离线 critical 事件

### 任务 3：authority 增加只读状态查询（小补丁）

文件：server/control-authority/authority_service.py。
在 main() 的 op 分发处增加一个分支，并在 Authority 类里实现：

~~~python
    def status(self):
        if self.current and self.current[2] > time.time_ns():
            d, lid, until = self.current
            return {"ok": True, "active": True, "driver": d,
                    "lease_id": lid, "fencing": self.fencing,
                    "valid_until_unix_ns": until}
        return {"ok": True, "active": False}
~~~

注意：这是只读操作，不改变任何状态、不下发任何信封。

验收：
1. 手工验证：向 :9300 发 {"op":"status"}，无接管时返回
   {"ok": true, "active": false}
2. 跑 takeover_demo 过程中再查，能返回 active/lease/driver/fencing

### 任务 4：前端三件套（零构建、零 CDN、全中文）

新目录：server/fleet/web/，文件：index.html、app.js、style.css。

布局（深色大屏）：
- 顶栏：标题"车云运营大屏" ｜ 在线车辆 x/y ｜ 当前时间
- 主区左侧：每辆车一张卡片
  * 大号车速（m/s，一位小数）+ canvas 速度曲线（画 speed_history）
  * 模式徽章：autonomous=蓝 / manual=黄 / remote_control=绿 /
    minimum_risk=红且闪烁 / stopped=灰
  * 电池：SoC 百分比进度条 + 电压 + 挡位
  * 底部小字：最近心跳 x.x 秒前
- 主区右侧：
  * 接管面板：驾驶员 / 租约 / fencing / 到期倒计时（秒，保留一位小数）；
    无接管时显示"当前无人接管"
  * 事件流：最新在上，最多显示 50 条；info=灰白、warn=琥珀、
    critical=红；每条含时间（时:分:秒）与文本

实现要求：
- 数据：setInterval 每 1s fetch /api/fleet 全量刷新（简单可靠）；
  事件流用 EventSource 连 /api/events（init 回放 + new 增量）
- 倒计时：用 /api/fleet 的 server_time_ns 与 takeover 到期时间差，
  本地按秒递减，不依赖浏览器墙钟
- 曲线参考实现（可改，效果等价即可）：

~~~js
function drawSpark(canvas, data) {
  var ctx = canvas.getContext("2d");
  var w = canvas.width, h = canvas.height;
  ctx.clearRect(0, 0, w, h);
  if (!data || data.length < 2) return;
  var maxV = Math.max(5, Math.max.apply(null, data));
  ctx.beginPath();
  for (var i = 0; i < data.length; i++) {
    var x = i / (data.length - 1) * w;
    var y = h - 2 - (data[i] / maxV) * (h - 6);
    if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
  }
  ctx.strokeStyle = "#22d3ee";
  ctx.lineWidth = 2;
  ctx.stroke();
}
~~~

- 配色建议：背景 #0b0f14，卡片 #121821，主色 #22d3ee，
  warn #f59e0b，critical #ef4444
- 禁止：任何外部 CDN/字体/图表库；禁止构建工具

验收：
1. 浏览器打开 127.0.0.1:9800 看到车辆卡片、曲线在动
2. 断开车端进程：卡片在约 6 秒后显示离线态
3. 断网刷新页面：页面自身不崩（接口失败时静默重试）

### 任务 5：端到端演示编排 + 最终验收

新文件：deploy/demo/dashboard_demo.sh（zsh/bash 均可），职责：
1. 检查 mosquitto 容器在跑（docker ps），不在则提示启动命令并退出
2. 依次 nohup 启动：gateway --uplink mqtt、vehicle_side、
   authority_service、fleet_hub（日志到 /tmp/ra-demo-*.log）
3. 打印：大屏地址、各日志路径、停止方式（给出 pkill 清单）
4. 提示后续手工演示顺序（见下）

最终验收剧本（人工执行，边做边看页面；全部符合才算本步完成）：

| # | 操作 | 页面预期 |
|---|---|---|
| a | 只起基础链路 | 在线 1 辆，模式=autonomous，车速基线约 1.6，曲线在走 |
| b | 跑 takeover_demo.py | 接管面板出现 driver-A -> driver-B，倒计时走；模式变 remote_control；到期后命令被拒 |
| c | 跑 minimum_risk_demo.py | 模式 remote_control -> minimum_risk（红色闪烁）-> stopped；事件流出 critical；速度曲线降到 0 |
| d | kill vehicle_side | 约 6 秒后卡片变离线，事件流出 critical"离线" |

### 任务 6：文档、进度与提交

1. 新文件 server/fleet/README.md：参考现有各模块 README 风格
   （当前实现/演示方法/后续生产化），说明 fleet_hub 的职责与接口
2. 根 README.md"当前进度"清单末尾追加一行：
   - [x] 第 10 步：运营大屏（fleet_hub 汇聚 MQTT 遥测 + 接管状态，
     Web 实时展示：在线/模式/车速曲线/电池/接管倒计时/事件流）
3. git 提交（可分任务多次提交，最终至少包含一个汇总）：
   feat(10): 运营大屏（fleet_hub + SSE 推送 + 零依赖前端，剧本 a-d 全通过）
4. 提交前自查：git status 干净；无 /tmp 文件、无调试脚本混入

## 4. 审核清单（Codex 将逐项检查）

1. 无新增第三方依赖；前端无任何 CDN/外链
2. MQTT 连接为 mTLS + access 证书；paho 用 CallbackAPIVersion.VERSION2
3. fleet_hub 共享状态全部加锁；SSE 客户端断开有清理，无僵尸线程
4. 事件/速度环均有上限，长时间运行无内存膨胀
5. OperationMode 补丁与任务 1 给定 diff 一致，未改动 adapter 翻译层
6. 离线判定窗口为 6 秒（心跳 2s x 3 容忍）
7. 任务 5 剧本 a/b/c/d 可复现（审核时会重跑）
8. authority 的 status 操作确为只读（不改变状态、不下发信封）
9. README、根进度、commit 信息风格与既有仓库一致
10. 无对既有安全逻辑（仲裁器/网关/权限服务）的行为改动

## 5. 常见坑速查

- paho 回调签名：on_connect(client, userdata, flags, reason_code,
  properties=None)，少一个参数会静默不回调
- retained 消息：fleet_hub 一连上就会收到上一次的 register，属正常
- SSE 输出格式：每条消息以空行结束；Content-Type: text/event-stream；
  加 Cache-Control: no-cache；Connection 保持
- ThreadingHTTPServer 下每个 SSE 客户端占一个线程，客户端列表务必
  在断开/异常路径里移除
- canvas 曲线归一化：max(5, max(data))，避免全零时除零/贴边
- 时间基准：倒计时用服务端 server_time_ns，不用浏览器 Date
- vehicle_side 的 OperationMode 覆盖要在 make_envelope 之前做，
  别改 common.py/adapter.py

## 6. MQTT 调试订阅脚本（自测用，勿提交）

~~~
export DYLD_LIBRARY_PATH=/opt/homebrew/opt/expat/lib
cd Robot-agent
.venv/bin/python - <<'PY'
import paho.mqtt.client as mqtt
def on_msg(c, u, msg):
    print(msg.topic, msg.payload[:160])
cli = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2, client_id="dbg-sub", protocol=mqtt.MQTTv5)
cli.tls_set(ca_certs="deploy/pki/dev/ca.crt", certfile="deploy/pki/dev/access.crt", keyfile="deploy/pki/dev/access.key")
cli.on_message = on_msg
cli.connect("localhost", 8883)
cli.subscribe("vehicle/#", qos=1)
cli.loop_forever()
PY
~~~

