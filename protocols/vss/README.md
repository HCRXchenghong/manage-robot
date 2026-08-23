# vss/ 信号字典

## VSS 是什么（大白话）

VSS（COVESA Vehicle Signal Specification）是汽车行业的开放标准，
给车上每个信号起了**全世界统一的名字、规定好单位**。
比如车速永远叫 `Vehicle.Speed`、单位永远是 m/s；
位置叫 `Vehicle.CurrentLocation.Latitude`。

好处：云端代码不用认识每台车的私有字段名。不管底层是
ROS 1 的 `cur_speed`（km/h）还是 Autoware 的某个 topic，
Adapter 一律翻译成 `Vehicle.Speed`（m/s）再上报。

## 本目录内容

- `platform-overlay.yaml`：本项目在 VSS 6.0 之上的 `Platform.*` 扩展
  （自动驾驶状态、远驾租约、链路质量），草案 v0.1。
- 基础信号（车速、位置、挡位、电池等）直接采用 VSS 官方定义，不在这里重复。

## 存量 ROS 1 车辆映射草案（杭州公园小车）

以下是 `vehicle/adapters/ros1/` 的第一版映射起点，
基于车端代码 `hardware/can_bridge_ros1/src/can_msgs` 的真实字段：

### 上行（车 → 云）

| VSS 信号 | ROS 1 来源 | 转换 |
|---|---|---|
| `Vehicle.Speed` | `vehicle_status.cur_speed` | km/h × (1000/3600) → m/s |
| `Vehicle.Chassis.SteeringWheel.Angle` | `vehicle_status.cur_steer` | ° → rad |
| `Vehicle.Powertrain.Transmission.CurrentGear` | `vehicle_status.shift_level` | 枚举映射表待定 |
| `Vehicle.Powertrain.TractionBattery.StateOfCharge` | `battery.capacity` | % → 0..1 |
| `Vehicle.Powertrain.TractionBattery.Voltage` | `battery.voltage` | V，直接映射 |
| `Vehicle.AutonomousDriving.Enabled`（待定名） | `vehicle_status.is_autodrive` | bool |

### 下行（云 → 车）

| 平台消息 | ROS 1 目标 | 转换 |
|---|---|---|
| `ControlCommand.TargetMotion.target_speed_mps` | `ecu.motor`（目标速度） | m/s → km/h（车端语义确认后定稿） |
| `ControlCommand.DirectActuation.steering_angle_target_rad` | `ecu.steer` | rad → °，限幅 |
| `ControlCommand.DirectActuation` 急停 | `ecu.brake` | bool 紧急停车 |
| `ControlCommand.DirectActuation.gear` | `ecu.shift` | 枚举映射 |

> 注意：`ecu.motor` 注释写的是“目标速度”，具体单位与限幅
> 必须拿到实车标定后冻结，接入前不得上线远驾。
