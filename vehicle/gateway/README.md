# gateway/ Vehicle Gateway

车云通信的唯一入口：无图形化常驻后台，退出任何终端都不能让车辆离线。

## 当前实现（`gateway.py`）

- 车端组件经 Unix Domain Socket 接入（默认 `/tmp/ra-gw.sock`）
- 可选真实 Map Agent 以 `map=UID[:GID]` 接入同一本机 UDS，接收地图发布/回滚
  命令并只上送真实 `MapPublicationAck`；Map Agent 未连接时不生成成功回执
- 控制命令入口默认只监听 `127.0.0.1:9100`：仅本机受信 control-relay 可写入，跨主机载荷为 Protobuf `Envelope(ControlCommand)` 二进制
- 遥测/心跳经 mTLS + MQTT 上行；必须先在平台注册 Gateway 证书，未激活车辆默认拒绝
- 签名 LeaseGrant 只从 `gateway/{gateway_id}/lease` mTLS MQTT 下行；裸 UDP LeaseGrant 一律拒绝；控制命令必须有 256 位 `auth_tag` 与端到端 MAC
- 心跳（Heartbeat）与统计日志

防御纵深：网关只做“明显过期”预筛；租约、fencing、序号、限幅的完整裁决
在车端安全仲裁器完成——云端永远不能绕过仲裁器直接动车。

控制 UDP、本机 UDS 和 MQTT 全部使用 `protocols/protobuf/platform/v1` 生成的
长度前缀/二进制 Protobuf。每个 Envelope 都必须带确定性 HMAC-SHA256
`auth_tag`；Gateway、Fleet 和车端 Adapter/Arbiter 使用受控安装流程分发的
密钥文件，任何 JSON-line 兼容层都不属于生产路径。

生产 Topic 规范：

```text
gateway/{gateway_id}/vehicle/{vehicle_id}/register|telemetry|status
gateway/{gateway_id}/lease
gateway/{gateway_id}/map
```

Broker ACL 以 Gateway 客户端证书 CN 作为身份；Fleet-hub 还会校验 Topic、
Envelope 和 PostgreSQL 中已激活的 `vehicle_id ↔ gateway_id ↔ certificate` 绑定。

## 启动要求

    python3 vehicle/gateway/gateway.py \
      --vehicle-id <车辆ID> --gateway-id <网关ID> \
      --mqtt-host <broker> --ca <ca.crt> --cert <vehicle.crt> --key <vehicle.key> \
      --envelope-auth-key /secure/envelope-hmac.b64 \
      --component-peer adapter=2001:2001 --component-peer arbiter=2002:2002 \
      --component-peer workspace=2003:2003

有真实 Map Agent 时额外增加 `--component-peer map=2004:2004`，否则地图发布只会
停在等待车端确认状态。

不配置可信 MQTT、车辆 ID 或网关 ID 时，网关不会回退到本地 UDP 上行。
