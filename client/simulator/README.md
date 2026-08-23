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

## 断链最小风险（第 6 步，"死人开关"）

遥控不是发一条命令就完事，而是持续的命令流。命令流一断（链路全断），
车辆绝不能照着最后一条命令一直跑——车端仲裁器内置看门狗：

- 遥控中超过 `WATCHDOG_MS`（默认 800ms）没收到新的**有效**命令 → 判定断链
- 进入最小风险状态：按 `DECEL_MPS2`（默认 1.2 m/s²）自主减速，直到安全停稳
- 只有 ACCEPTED 的命令能"续命"；过期/被拒的命令不算数
- 驾驶员重连后发来通过校验的新命令 → 恢复遥控（重新接管）

三阶段演示（先起 Gateway 和车端组件）：

    python3 vehicle/gateway/gateway.py
    python3 client/simulator/vehicle_side.py
    python3 client/simulator/minimum_risk_demo.py

预期：阶段 A 逐条 ✓；阶段 B 约 0.8s 后出现"⚠ 判定链路中断 → 自主减速
停车"，随后"✓ 车辆已安全停稳"；阶段 C 首条命令"✓ 执行（链路恢复，
重新接管）"。
## 接管生命周期（第 8 步：控制权服务 + 租约 + fencing）

- `takeover_demo.py`：两个驾驶员抢控制权——A 申请行驶 → B 顶替（fencing
  更大）→ A 被拒 → B 租约到期也被拒 → 车端看门狗 → 安全停车。
- 配套 `server/control-authority/authority_service.py`（发证机关）。
- 语义：fencing 由权限服务按接管纪元发放、一个租约一个值；命令序号
  `command_sequence` 负责去重/排序。
