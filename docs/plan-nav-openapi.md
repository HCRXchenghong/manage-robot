# 循迹导航 + 开放 API（等保三级）实施记录 · v1

> 本轮交付：控制台「循迹导航」页、「API 平台」页，以及配套的云端能力。

## 1. 循迹导航（控制台）

- 页面：选车 → 多路点表（点名 / X / Y / 定时到达，可增删、排序）→ 下发。
- 后端：POST /api/nav/routes 存任务并经 MQTT 发布 vehicle/{id}/nav（mTLS，QoS1）；
  取消走 POST /api/nav/routes/{id}/cancel → vehicle/{id}/nav_cancel。
- 任务状态：queued（MQTT 未就绪排队）→ dispatched → cancelled；事件环同步记录。
- 下一步：车端 gateway 订阅 nav 话题转导航栈（ROS1 move_base / ROS2 nav2 序列点）。

## 2. 开放 API（别的平台调车）

- 接口（/open/v1/*）：
  - POST /open/v1/nav/command：某车从 A 点到 B 点（可含途经点、trace_id）；
  - POST /open/v1/nav/cancel：按车或按任务取消；
  - GET /open/v1/nav/status?vehicle_id=、GET /open/v1/vehicles：只读查询。
- 密钥：POST /api/openkeys 创建（明文只返回一次，服务端只存 SHA-256 哈希）；
  可撤销；GET /api/audit 查审计。

## 3. 等保三级控制点对照

| 控制点 | 落实 |
|---|---|
| 身份鉴别 | API Key 哈希存储、明文一次性发放 |
| 访问控制 | Key 可撤销、接口面收敛（导航指令 + 只读） |
| 安全审计 | 成功/失败全记录（原因、IP、trace），内存环 + PG audit_log 落库 |
| 抗重放 | 时间戳 ±300s 窗口（可配）+ nonce 一次性（10 分钟去重） |
| 数据完整性 | HMAC-SHA256 签名覆盖 方法/路径/时间戳/nonce/请求体哈希；签名密钥 = hex(SHA256(secret)) |
| 通信保密 | 车云 mTLS；开放面生产经 nginx+TLS 入口 |
| 资源限制 | 每 Key 10s 滑动窗口限流（默认 20 次，可配） |

## 4. 已验证

- 合法签名调用 → 任务创建并下发（origin=open_api，trace 透传）；
- 篡改请求体（签名不变）→ 拒；重放同一 nonce → 拒；取消调用 → 状态 cancelled；
- 审计页可见成功/失败全部记录。
