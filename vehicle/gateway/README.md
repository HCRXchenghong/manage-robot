# gateway/ Vehicle Gateway

车云通信的唯一入口：无图形化常驻后台，退出任何终端都不能让车辆离线。

## 当前实现（阶段 0 本地版，`gateway.py`）

- 车端组件经 Unix Domain Socket 接入（默认 `/tmp/ra-gw.sock`）
- 控制命令入口（UDP :9100）：信封级过期预筛 → 路由给车端组件
- 遥测/心跳/回执统一转发上行到云端端点（UDP，默认 127.0.0.1:9200）
- 心跳（Heartbeat）与统计日志

防御纵深：网关只做“明显过期”预筛；租约、fencing、序号、限幅的完整裁决
在车端安全仲裁器完成——云端永远不能绕过仲裁器直接动车。

## 与假车/翻译官的串联（端到端演示）

    # 终端 1：网关
    python3 vehicle/gateway/gateway.py
    # 终端 2：车端组件（假总线 + 翻译官 + 仲裁器）
    python3 client/simulator/vehicle_side.py
    # 终端 3：假云端
    python3 client/simulator/cloud_listen.py
    # 终端 4：发控制命令（7 场景）
    python3 client/simulator/control_client.py --scenario all

## 后续（第 5b 步）

- 上行换成 mTLS 注册 + MQTT 5 遥测（替换 UDP 演示通道）
- 控制双链路：两条独立 QUIC 连接 + fencing/租约协同
- 能力协商：启动时上报 `GatewayCapabilities`
- Go 重写常驻服务与 systemd 打包（`packaging/`）
