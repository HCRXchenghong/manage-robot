# client/ 远驾客户端

| 目录 | 职责 |
|---|---|
| `core/` | 平台接口、接管状态机、Protobuf 生成代码——Web 与 Windows 端共用 |
| `simulator/` | 无真车时使用：模拟车辆行驶与故障，阶段 0/1 先用它 |
| `device-sdk/` | 方向盘/踏板/急停设备抽象接口 |
| `windows/` | Windows 远驾客户端（React + WebView2/Wails + Go Agent），预留 |
