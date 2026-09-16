# adapters/ros1/ 存量 ROS 1 车辆适配器

给杭州公园小车（纯 ROS 1）配的“翻译官”。平台和车互相听不懂对方的话，
这个适配器站在中间翻译，两边都不需要改。

## 数据流

    上行（车 → 云）
    ROS 1 总线 /vehicle_status、/battery
      → adapter.py 翻译核心（km/h→m/s、°→rad、%→0..1，对齐 VSS）
      → 平台 SignalUpdate → Gateway → 云端

    下行（云 → 车）
    云端 ControlCommand → Gateway
      → 车端安全仲裁器校验（租约/fencing/TTL/限幅）
      → adapter.py 翻译成 can_msgs/ecu → ROS 1 总线

    循迹（云 → 车 → 云）
    云端 NavigationCommand → Gateway → ros1_node.py
      → 真实 move_base action 逐个执行路点
      → NavigationAck（接受/开始/完成/取消/失败）→ Gateway → Fleet

## 文件

| 文件 | 角色 | 运行环境 |
|---|---|---|
| `adapter.py` | 翻译核心 + 确定性自检（`--self-test`） | 任意机器，无依赖 |
| `ros1_node.py` | 车端真身：rospy、Gateway UDS、Arbiter 执行器接线 | 仅小车（ROS 1 Noetic） |

## 现在就能验证

    python3 adapter.py --self-test

用真实字段样例跑 11 项翻译检查（7.2 km/h→2.0 m/s、90°→π/2、超速限幅等）。

## 运行要求

`ros1_node.py` 必须同时提供 `--vehicle-id`、`--gateway-id`、`--gateway-uds`、
`--arbiter-uds`、`--calibration`、`--navigation-action` 和 `--navigation-frame`。
标定 JSON 必须包含版本、车速/转向限幅、
制动阈值、挡位双向映射和驻车挡位；程序不会内置车型参数，也不会在缺少标定
时以“安全默认值”执行控制。

执行路径是：Safety Arbiter 校验通过 → `--arbiter-uds` → `ros1_node.py` →
`can_msgs/ecu` → CAN bridge。Gateway UDS 的 Adapter 连接同时接收已认证的
`NavigationCommand`；导航仅通过显式配置的真实 ROS 1 `move_base` action 执行，
未连接 action server 时报告失败，不会伪造成功 ACK。
