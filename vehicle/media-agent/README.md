# media-agent/ 车端媒体代理

车端视频采集与发送。阶段 0 用**合成摄像头**走通 WebRTC 视频链路，
生产里替换为真实相机采集节点。

## 当前实现（阶段 0 本地版）

- `fake_camera.py`：合成摄像头（640x360@15fps），画面里一个移动亮块 +
  随帧变化的背景，便于确认"画面在动、帧在更新"。
- `webrtc_sender.py`：车端 WebRTC 发送器。建连接 → 写 offer → 读 answer
  → 推流，打印产帧率。

## 端到端演示

见 `server/media-control/README.md`（收流端）。

## 后续（生产化）

- 真实相机采集（多路：前视/环视/驾驶位）
- 与控制链路一样做双路冗余与优先级，弱网自适应码率
## 双路视频发送器（第 7 步）

- `dual_video_sender.py`：把同一画面【冗余双发】到 A/B 两个网络出口，
  每帧编码成一个包（前 4 字节帧序号 + VP8 数据）。任一路断了，另一路仍完整。
  配合 `server/media-control/dual_video_demo.py` 演示。

