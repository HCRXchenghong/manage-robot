# vehicle/ 车端

跑在车辆上的所有程序。平台不直接和自动驾驶系统对话，全部经由这里的组件中转。

| 目录 | 职责 | 技术 |
|---|---|---|
| `gateway/` | Vehicle Gateway：常驻后台服务，负责注册、心跳、遥测上行、命令下发；车云通信的唯一入口 | Go |
| `adapters/` | 适配器层：把各自动驾驶系统的原生消息翻译成平台 Protobuf（详见其 README） | C++ |
| `safety-arbiter/` | 安全仲裁器：对控制命令做最终裁决（租约、fencing token、序号、TTL、限幅），云端不可绕过 | Go |
| `media-agent/` | 视频采集与双路 RTP 推流（硬件编码） | GStreamer |
| `workspace-agent/` | 远程终端（SSH/PTY）与 GUI 会话代理 | Go |
| `map-agent/` | 地图下载、校验、原子切换、回滚 | Go |
| `packaging/` | systemd 服务、deb/rpm 打包与安装升级 | systemd |
| `vehiclectl/` | 车端 TUI/CLI，配置与状态查看；与守护进程生命周期解耦 | Go (Bubble Tea) |

约定：
- Adapter 与 Gateway 之间走 Unix Domain Socket + Protobuf，不把 ROS/Cyber 原生类型泄露到云端。
- Gateway 必须是无图形化后台服务；退出任何终端菜单都不能导致车辆离线。
