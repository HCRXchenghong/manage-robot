# 循迹导航与开放 API 功能规范

本文件定义任务下发、路线生命周期和外部系统调用契约。人员身份、组织隔离和
控制安全边界分别遵循 [`plan-auth-org.md`](plan-auth-org.md)、
[`commercial-control-baseline.md`](commercial-control-baseline.md) 和
[`architecture-functional-specification.md`](architecture-functional-specification.md)。

## 循迹导航

运营人员选择已授权车辆，提交带坐标系、点序号、位置和可选到达时间的路线。服务端
在数据库事务中保存路线和任务，并把待下发消息投递到车辆专属 Gateway topic。车辆
必须返回接收、校验、开始、暂停、失败、取消或完成状态；云端不能以 MQTT 发布成功
冒充车辆已执行。

任务生命周期：

```text
draft → validated → scheduled → dispatched → accepted → running
                                                   ├→ paused
                                                   ├→ failed
                                                   ├→ cancelled
                                                   └→ completed
```

所有状态迁移必须带 `trace_id`、操作者、车辆、地图版本、ODD 校验结果和车端回执。
取消操作必须幂等，并且只能取消操作者有权访问的车辆任务。

## 控制台 API

- `GET /api/nav/routes`：按当前会话组织过滤路线。
- `POST /api/nav/routes`：创建路线并执行车辆能力、地图、ODD 和权限校验。
- `POST /api/nav/routes/{id}/cancel`：幂等取消路线并记录原因。

## 开放 API

| 方法 | 路径 | Scope |
|---|---|---|
| GET | `/open/v1/meta` | 任意有效 Key |
| GET | `/open/v1/vehicles` | `vehicle.read` |
| GET | `/open/v1/vehicles/{id}` | `vehicle.read` |
| GET | `/open/v1/vehicles/{id}/telemetry` | `telemetry.read` |
| GET | `/open/v1/vehicles/{id}/alarms` | `alarm.read` |
| GET | `/open/v1/nav/status?vehicle_id=` | `task.read` |
| GET | `/open/v1/nav/routes` | `task.read` |
| POST | `/open/v1/nav/command` | `task.write` |
| POST | `/open/v1/nav/cancel` | `task.write` |
| POST | `/open/v1/control/emergency-stop` | `control.write` |

每次调用都校验 Key 状态、scope、车辆白名单、来源地址、时间戳、nonce、签名和
请求体摘要。服务端对查询结果也执行车辆范围过滤；传入 route ID 不能越过车辆权限。

## 审计与故障语义

审计至少记录 Key、人员、方法、路径、车辆、trace、备注、来源地址、结果码和失败
原因。下行链路不可用时任务停留在可重试状态；服务端不得生成车辆回执、虚构任务
完成或以本地数据替代车辆状态。
