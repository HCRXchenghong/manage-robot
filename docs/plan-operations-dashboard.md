# 运营控制台功能规范

运营控制台是 React + TypeScript + Vite 前端与 Go `fleet-hub` 的浏览器工作面。
浏览器只呈现已验证的 REST/WSS 状态并提交受权操作，不承担车辆安全裁决，也不
生成车辆、视频、点云、终端或控制回执。

## 运行拓扑

```text
Browser
  │ HTTPS/WSS
  ▼
nginx/WAF
  │
  ▼
Go fleet-hub :9800 ── PostgreSQL/PostGIS（业务真相源）
       │
       ├─ MQTT 5/mTLS ── Gateway namespace
       ├─ Durable Authority ── signed LeaseGrant
       ├─ Workspace Broker ── workspace-agent
       ├─ Media service ── WebRTC/SFU
       └─ Map service/worker ── object storage
```

`/api/runtime` 暴露数据库、MQTT、持久化写入和运行模式；`/healthz` 只表示进程
可响应；`/readyz` 只有依赖全部就绪时返回成功。volatile 诊断模式必须明确标识，
不产生业务写入或控制权限。

## 页面与功能

| 页面 | 功能边界 |
|---|---|
| 总览大屏 | 车辆状态、速度/电量/挡位、健康、事件、点云/GPS 视图、终端和视频入口 |
| 车辆列表 | 按组织、在线状态、模式和关键字查询；分页、详情联动和权限过滤 |
| 地图中心 | 地图版本、上传、转换、编辑、2D/3D 预览、发布和回滚入口 |
| 循迹导航 | 选择车辆、编辑带坐标系的路线点、校验后提交、取消和车辆回执跟踪 |
| 远程接管 | 绑定受信控制设备、申请/续租/交还租约、fencing、状态确认、急停二次确认 |
| 视频监控 | 真实媒体目录、会话授权、机位切换、WebRTC 播放和媒体健康状态 |
| 审计中心 | 身份、授权、接管、急停、任务、地图、终端和 API 调用的筛选与导出 |
| 远程终端 | 仅展示受权 workspace 会话；建立、断开、恢复和审计终端状态 |
| API 平台 | API Key、scope、车辆白名单、来源地址、有效期、备注和审计查询 |
| 组织管理 | 用户、角色、组织配额、控制设备绑定和会话撤销 |

### 点云视图

LidarView 使用 `THREE.Points` 和 `BufferGeometry`，支持 2D 鸟瞰、3D 轨道、缩放、
旋转、平移、点大小、多车统一坐标系、高亮跟随和图例。数据只来自
`GET /api/pointcloud?vehicle_id=` 或地图服务真实切片；服务不可用时显示原因，
不显示静态场景替代品。

### 视频、终端和 RViz

媒体、终端和图形会话分别使用独立的服务目录、短期令牌和权限。未注册真实服务时，
页面保留操作上下文并显示不可用原因；不渲染合成画面、不回显虚构终端输出，也不
使用浏览器本地状态冒充车辆在线或接管状态。

## 数据层契约

`GET /api/fleet` 返回：

```json
{
  "server_time_ns": 0,
  "vehicles": [{
    "vehicle_id": "registered-id",
    "online": false,
    "mode": "",
    "speed_mps": 0,
    "soc": 0,
    "voltage": 0,
    "gear": "",
    "speed_history": [],
    "capabilities": {},
    "gps": {"fix": false, "lat": 0, "lon": 0, "alt": 0},
    "pose": {"valid": false, "x": 0, "y": 0, "yaw": 0}
  }],
  "takeover": {"active": false},
  "events": []
}
```

车辆必须来自已激活 Gateway 的真实注册；`online` 由实时遥测时效判定，`pose.valid`
和 `gps.fix` 区分“无定位”与合法零值。WSS 断线后客户端以 `degraded` 标识最近
一次已验证快照，并立即通过 REST 全量重新校准；未经验证的数据不能覆盖新快照。

## 接口与错误状态

- REST 请求使用超时、请求体上限、统一 JSON 错误和幂等 request ID。
- WSS 发送带服务端时间的全量状态与事件；客户端断开需释放订阅和资源。
- 所有控制、地图、任务、终端和设备写操作先经过人员/组织/车辆权限检查。
- 车辆或依赖未就绪时返回明确的 `401/403/409/503`，禁止成功响应伪造车端回执。
- 前端所有页面覆盖加载、空数据、权限拒绝、依赖断开、过期会话和恢复状态。

## 性能与安全约束

- 前端不得依赖 CDN 或外链运行时脚本；构建产物由 fleet-hub 单二进制嵌入。
- WebSocket 使用指数退避和 REST 校准；事件和历史数据有服务端上限。
- 生产入口使用 TLS、CSP、HSTS、限流、Origin/CSRF 校验和 no-store 敏感响应头。
- 急停按钮必须明确车辆、权限和二次确认；最终状态以车端 Arbiter 回执和遥测确认。
