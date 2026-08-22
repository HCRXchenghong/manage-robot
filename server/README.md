# server/ 云端服务

全部云端服务，Go 实现。

| 目录 | 职责 |
|---|---|
| `api/` | 管理 API（车辆、用户、权限等），面向 web |
| `vehicle-access/` | MQTT 接入、连接、心跳、能力协商 |
| `control-authority/` | 接管审批、控制权租约、fencing token |
| `control-relay/` | 双 QUIC 边缘转发（控制命令双发） |
| `media-control/` | 媒体会话与 SFU 编排 |
| `diagnostics/` | 远程诊断（SOVD/UDS） |
| `workspace/` | SSH/PTY/GUI 会话 Broker |
| `fleet/` | 车队、任务、告警 |
| `map-service/` | 地图元数据与版本发布 |
| `map-workers/` | 点云转换工人：PDAL、COPC 转换、分块、导出 |
| `migrations/` | PostgreSQL/PostGIS 数据库迁移 |

数据落地：PostgreSQL/PostGIS 是业务真相源；Redis 只管在线状态/缓存；大文件进对象存储。
