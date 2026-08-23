# control-relay/ QUIC 控制中继（边缘接入点）

远程驾驶控制链路的"公网入口"。架构文档 §7.5：接管期间，控制命令
不走普通遥测通道，而是经 **两条独立的 QUIC 连接**双发，消除单点故障。
本服务模拟生产环境中的边缘接入点（edge-A / edge-B），生产部署时两者
应位于不同区域/不同运营商的故障域；本机演示用两个端口模拟。

## 职责（阶段 0 本地版，`control_relay.py`）

1. 与远驾客户端建立 **mTLS QUIC** 连接（复用 `deploy/pki/dev` 开发证书，
   ALPN = `ra-control`）
2. 收到控制 **DATAGRAM 帧**（RFC 9221，不可靠、低延迟，适合高频控制）
   → 原样转发到车端 Gateway 控制入口（UDP :9100）
3. Gateway 的 ControlAck → 按 (会话, 序号) 找回原连接，DATAGRAM 送回客户端

## 端到端演示（双链路控制）

    # 终端 1/2：车端网关 + 车端组件（假总线+翻译官+仲裁器）
    python3 vehicle/gateway/gateway.py
    python3 client/simulator/vehicle_side.py
    # 终端 3/4：两个边缘中继
    .venv/bin/python server/control-relay/control_relay.py --name edge-A --listen 9443
    .venv/bin/python server/control-relay/control_relay.py --name edge-B --listen 9444
    # 终端 5：双链路客户端
    .venv/bin/python client/core/dual_link_control.py --links a,b --seq 10 --fencing 200

预期：同一命令两条链路各送一次，车端仲裁器 **只执行一次**——
一条链路回 `ACCEPTED`，另一条回 `REJECTED_STALE`（序号重复已去重）。
杀掉任一中继后再发，剩余链路仍能送达执行；恢复后双发依旧一去重。

## 踩过的坑（写在这里，避免再踩）

- aioquic 的 `send_datagram_frame` 在底层 `QuicConnection`（`_quic`）上，
  不在 `QuicConnectionProtocol` 上；直接操作 `_quic` 入队后必须手动
  `transmit()`，否则数据只进队列、不上网络。
- 转发命令和接收回执必须用 **同一个 UDP 端口**：Gateway 按"来包源地址"
  回执，若收发是两个端口，回执会飞进没人读的端口，客户端只能干等超时。

## 后续（生产化）

- 与 `control-authority/` 联动：校验租约有效性与 fencing token 连续性
- 高频控制走 DATAGRAM、租约/会话管理走可靠 STREAM（分流）
- Go 重写 + 多区域部署，接入真实调度与就近接入选择

