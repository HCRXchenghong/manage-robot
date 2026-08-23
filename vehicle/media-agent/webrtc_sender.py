#!/usr/bin/env python3
# 车端 WebRTC 视频发送器（第 5d 步）
#
# 架构文档 §7.4：远程驾驶的视频由车端推给远驾控制台。阶段 0 用两个进程
# 模拟"车端 -> 云端"，信令用文件交换（真实场景走 media-control 的信令通道）。
#
# 流程：
#   1. 建 RTCPeerConnection，挂上合成摄像头轨道
#   2. createOffer，写进 --offer 文件
#   3. 等接收端把 answer 写进 --answer 文件，读入并 setRemoteDescription
#   4. 开始推流，打印帧率统计
#
# 用法：
#   .venv/bin/python vehicle/media-agent/webrtc_sender.py \
#       --offer /tmp/ra_offer.sdp --answer /tmp/ra_answer.sdp

import argparse
import asyncio
import os
import sys
import time

from aiortc import RTCPeerConnection, RTCSessionDescription

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fake_camera import FakeCameraTrack  # noqa: E402


async def wait_ice_gathering(pc, timeout=5.0):
    """等 ICE 候选收集完，这样 SDP 里才带完整候选（本机直连也稳妥）。"""
    t0 = time.time()
    while pc.iceGatheringState != "complete":
        if time.time() - t0 > timeout:
            break
        await asyncio.sleep(0.05)


async def read_file(path, timeout=20.0):
    t0 = time.time()
    while True:
        if os.path.exists(path) and os.path.getsize(path) > 0:
            with open(path) as f:
                return f.read()
        if time.time() - t0 > timeout:
            raise TimeoutError(f"等待文件超时: {path}")
        await asyncio.sleep(0.1)


async def main_async(args):
    pc = RTCPeerConnection()
    track = FakeCameraTrack()
    pc.addTrack(track)
    print(f"[车端视频] 合成摄像头 {track.width}x{track.height}@{track.fps}fps 就绪")

    offer = await pc.createOffer()
    await pc.setLocalDescription(offer)
    await wait_ice_gathering(pc)
    with open(args.offer, "w") as f:
        f.write(pc.localDescription.sdp)
    print(f"[车端视频] offer 已写入 {args.offer}，等 answer ...")

    answer_sdp = await read_file(args.answer)
    await pc.setRemoteDescription(
        RTCSessionDescription(sdp=answer_sdp, type="answer"))
    print("[车端视频] answer 已读入，连接中 ...")

    t0 = time.time()
    last_n = 0
    last_t = t0
    while True:
        await asyncio.sleep(1.0)
        if pc.connectionState in ("failed", "closed", "disconnected"):
            print(f"[车端视频] 连接状态异常: {pc.connectionState}")
            break
        now = time.time()
        n = track._n
        fps = (n - last_n) / max(1e-6, now - last_t)
        print(f"[车端视频] 状态={pc.connectionState} 已产 {n} 帧 产帧率 {fps:.1f} fps")
        last_n, last_t = n, now
        if now - t0 >= args.duration:
            break

    print("[车端视频] 推流结束，关闭连接")
    await pc.close()


def main():
    ap = argparse.ArgumentParser(description="车端 WebRTC 视频发送器")
    ap.add_argument("--offer", default="/tmp/ra_offer.sdp")
    ap.add_argument("--answer", default="/tmp/ra_answer.sdp")
    ap.add_argument("--duration", type=float, default=6.0, help="推流时长(秒)")
    args = ap.parse_args()
    asyncio.run(main_async(args))


if __name__ == "__main__":
    main()

