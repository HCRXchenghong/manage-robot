# adapters/ 适配层

职责：把不同自动驾驶系统的原生消息翻译成统一的平台 Protobuf 消息，交给 Gateway 上行；并反向下发控制。

| 适配器 | 对接对象 | 说明 |
|---|---|---|
| `autoware_ros2/` | Autoware（ROS 2） | C++ rclcpp 节点，接 Autoware 原生 topic |
| `generic_ros2/` | 通用 ROS 2 车辆 | 配置化 topic 映射，低代码接入 |
| `ros1/` | **存量 ROS 1 车辆** | C++ roscpp 节点，直接订阅 ROS 1 topic 翻译；不走 `ros1_bridge`、不改动原车控制栈 |
| `apollo_cyber/` | Apollo | C++ Cyber RT Reader/Writer |

映射约定：
- 信号命名、单位对齐 COVESA VSS，平台扩展用 `Platform.*`。
- 点云等大流量不走遥测链路，走地图/录像通道。
- 能力声明进入 `GatewayCapabilities`（见介绍.md §6.4）。
