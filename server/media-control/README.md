# media-control/ 媒体会话与转发

架构文档 §7.4：远程驾驶视频由车端推给远驾控制台。阶段 0 用 **点对点
WebRTC** 演示完整视频链路（产帧→编码→RTP→解码→收帧），生产里在此
基础上加 SFU 转发、信令服务与多路编排。

## 当前实现（阶段 0 本地版）

- `webrtc_receiver.py`：模拟远驾控制台收流端。读车端 offer → 回 answer →
  解码视频轨道，统计帧数/帧率/分辨率。
- 信令：阶段 0 用文件交换 SDP（`/tmp/ra_offer.sdp` / `ra_answer.sdp`），
  生产走 media-control 的信令通道（SDP + ICE 候选）。

## 与车端发送器的端到端演示

    # 终端 1：云端收流端
    .venv/bin/python server/media-control/webrtc_receiver.py --duration 5
    # 终端 2：车端发送器（合成摄像头）
    .venv/bin/python vehicle/media-agent/webrtc_sender.py --duration 6

预期：收流端 5 秒约收 70 帧（640x360，平均 ~14 fps），首帧正常到达。

## 依赖

- `aiortc`（WebRTC 实现）、`av`/PyAV（编解码）、`numpy`（画帧）
- 已确认本机可用编解码：VP8、H264。

## 后续（生产化）

- SFU：一对多转发（多个远驾席位/回放/审计同时看一路）
- 信令服务化：SDP/ICE 经 media-control，接入租约与控制权校验
- 真实相机接入 + 自适应码率、丢包恢复（NACK/PLI）、弱网演练

## 双路视频冗余 + 合并（第 7 步）

架构文档 §7.4 / 阶段 3："单视频链路断开时，备用健康则视频不重建"。

- `dual_video_receiver.py`：`Merger`（按帧序号合并去重 + 可模拟单路故障）
  + `DualVideoReceiver`（双 UDP 口监听 + 解码统计）。
- `dual_video_demo.py`：一个进程里同时跑车端双路发送器与收端合并器，
  用真实 UDP 回环模拟两条独立路径，分三阶段演示。

    .venv/bin/python server/media-control/dual_video_demo.py

预期：双路正常时去重取一份；B 路中断（丢 B 口包）期间画面由 A 路完整支撑、
不中断不重建；B 路恢复后继续。实测：60 唯一帧、解码 60 帧、断链期丢 21 包
仍无缝续播。

