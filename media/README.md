# media/ 视频链路

| 目录 | 职责 |
|---|---|
| `ingress/` | 视频接入层（WHIP/RTP），收车辆两路贡献流 |
| `rtp-merge/` | 双路 RTP 合并去重（RFC 7198 / ST 2022-7 思想），实现无感换源 |
| `sfu/` | WebRTC SFU 分发（LiveKit/Pion/coturn），双节点 |
