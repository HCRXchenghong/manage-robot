# simulator/ 模拟器（阶段 0）

真车和云端还不存在时，用这三个程序把整条协议链路跑通、把安全关卡验证掉。
全部用 Python 标准库实现，无需安装任何依赖。

> 说明：当前消息用 JSON「镜像」Protobuf 结构（字段与
> `protocols/protobuf/platform/v1/` 一一对应），方便先跑起来；
> 工具链就位后切换真正的 Protobuf 序列化，结构不变。

## 三个程序

| 程序 | 角色 | 类比 |
|---|---|---|
| `vehicle_sim.py` | 假车：上报遥测 + 内置安全仲裁器裁决控制命令 | 车端 Gateway + Safety Arbiter |
| `control_client.py` | 假驾驶员：发送控制命令（含 6 种故障场景） | 远驾客户端 |
| `netem.py` | 网络故障注入：丢包、延迟、断链 | 公网弱网 |

## 快速演示（3 个终端）

    # 终端 1：启动假车
    python3 vehicle_sim.py

    # 终端 2：启动弱网注入（可选；--down-after 0 = 直接断链）
    python3 netem.py --listen 9101 --out 127.0.0.1:9100 --loss 0.3

    # 终端 3：跑完整演示（7 条命令，验证全部安全关卡）
    python3 control_client.py --scenario all
    # 或走弱网：--target 127.0.0.1:9101

## 演示验证了什么

对应架构文档阶段 0 验收目标：重复和过期控制在车端 100% 拒绝。

| # | 场景 | 预期裁决 |
|---|---|---|
| 1 | 正常命令 | ACCEPTED |
| 2 | 重复序号 | REJECTED_STALE |
| 3 | TTL 过期 | REJECTED_STALE |
| 4 | 无效租约 | REJECTED_LEASE |
| 5 | fencing 倒退 | REJECTED_FENCING |
| 6 | 目标速度 20 m/s（超 5） | REJECTED_LIMIT |
| 7 | 恢复正常 | ACCEPTED |
