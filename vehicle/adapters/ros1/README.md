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

## 文件

| 文件 | 角色 | 运行环境 |
|---|---|---|
| `adapter.py` | 翻译核心 + 自检（`--demo`） | 任意机器，无依赖 |
| `ros1_node.py` | 车端真身：rospy 订阅/发布接线 | 仅小车（ROS 1 Noetic） |

## 现在就能验证

    python3 adapter.py --demo

用真实字段样例跑 11 项翻译检查（7.2 km/h→2.0 m/s、90°→π/2、超速限幅等）。

## 待实车标定的项（未冻结前禁止远驾）

- `ecu.motor` 注释为“目标速度”，单位/语义未确认。
- `shift_level` ↔ 挡位枚举映射是占位值。
- 方向盘限幅 ±30°、急停阈值 0.5 均为占位值。
