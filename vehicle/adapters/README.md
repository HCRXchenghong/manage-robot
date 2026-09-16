# adapters/ 适配层

职责：把不同自动驾驶系统的原生消息翻译成统一的平台 Protobuf 消息，交给 Gateway 上行；并反向下发控制。

| 适配器 | 对接对象 | 说明 |
|---|---|---|
| `autoware_ros2/` | Autoware（ROS 2） | 规划中的 C++ `rclcpp` 节点；当前仅保留目录占位 |
| `generic_ros2/` | 通用 ROS 2 车辆 | 规划中的配置化 topic 映射；当前仅保留目录占位 |
| `ros1/` | **存量 ROS 1 车辆** | 当前实现为 Python 翻译核心和 `rospy` 节点；不走 `ros1_bridge`、不改动原车控制栈 |
| `apollo_cyber/` | Apollo | 规划中的 C++ Cyber RT Reader/Writer；当前仅保留目录占位 |

映射约定：
- 信号命名、单位对齐 COVESA VSS，平台扩展用 `Platform.*`。
- 点云等大流量不走遥测链路，走地图/录像通道。
- 能力声明进入 `GatewayCapabilities`（见 [`../docs/architecture-functional-specification.md`](../docs/architecture-functional-specification.md) 的车辆能力章节）。
