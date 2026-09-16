# control-relay：双链路 QUIC 边缘接入

`control_relay.py` 是单车辆、单 Gateway 的边缘控制接入进程。它只负责
传输和回执路由，不能签发租约、改变 fencing、校验 ODD 或替代车端安全仲裁器。

## 链路职责

```text
受信控制代理
  ├─ QUIC/mTLS + ALPN ra-control ─> edge-A ─┐
  └─ QUIC/mTLS + ALPN ra-control ─> edge-B ─┤
                                              └─ Gateway loopback UDP
                                                   └─ Safety Arbiter UDS
```

- 控制帧使用 Protobuf `Envelope(ControlCommand)` QUIC DATAGRAM，租约和会话控制面使用可靠控制面服务。
- 中继要求客户端证书由显式 `--client-ca` 签发，并限制为已绑定车辆和 Gateway。
- 每个控制命令以 `(control_session_id, command_sequence)` 建立短期回执路由；
  路由数量和生命周期均有限制，过期路由自动清理。
- 中继要求 Envelope `auth_tag` 和 ControlCommand 端到端 MAC 均存在，并拒绝
  身份、序号、TTL 或 payload 不一致的二进制帧。
- Gateway 只能绑定本机 loopback 控制入口；跨网络的安全身份由 QUIC mTLS
  提供，车端最终执行权属于 Safety Arbiter。

## 启动契约

所有身份、监听地址、证书和目标都必须显式提供：

```bash
.venv/bin/python server/control-relay/control_relay.py \
  --name edge-a --bind 10.0.10.12 --listen 9443 \
  --gateway 127.0.0.1:9100 \
  --vehicle-id VEHICLE_ID --gateway-id GATEWAY_ID \
  --client-ca /etc/robot-agent/control-client-ca.crt \
  --cert /etc/robot-agent/edge-a.crt \
  --key /etc/robot-agent/edge-a.key \
  --envelope-auth-key /etc/robot-agent/envelope-hmac.b64
```

禁止使用空身份、默认地址、共享开发证书或未认证的 QUIC 客户端启动。
`edge-a` 与 `edge-b` 应部署在不同故障域，并使用各自的服务证书。
