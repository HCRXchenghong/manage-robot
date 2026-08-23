#!/usr/bin/env python3
# 云端 WebRTC 视频接收器（第 5d 步）——模拟远驾控制台收流
#
# 流程：
#   1. 等车端把 offer 写进 --offer 文件
#   2. setRemoteDescription(offer) -> createAnswer -> 写 --answer 文件
#   3. 收到视频轨道后逐帧解码，统计帧数 / 帧率 / 分辨率
#
# 用法：
#   .venv/bin/python server/media-control/webrtc_receiver.py \
#       --offer /tmp/ra_offer.sdp --answer /tmp/ra_answer.sdp

import argparse
import asyncio
import os
import time

from aiortc import RTCPeerConnection, RTCSessionDescription


async def read_file(path, timeout=20.0):
    t0 = time.time()
    while True:
        if os.path.exists(path) and os.path.getsize(path) > 0:
            with open(path) as f:
                return f.read()
        if time.time() - t0 > timeout:
            raise TimeoutError(f"等待文件超时: {path}")
        await asyncio.sleep(0.1)


async def consume(track, stats, duration):
    t0 = time.time()
    while True:
        try:
            frame = await asyncio.wait_for(track.recv(), timeout=5.0)
        except asyncio.TimeoutError:
            print("[收流端] 5 秒没收到帧，判定断流")
            break
        stats["frames"] += 1
        stats["w"], stats["h"] = frame.width, frame.height
        if stats["frames"] == 1:
            print(f"[收流端] 首帧到达：{frame.width}x{frame.height}")
        if time.time() - t0 >= duration:
            break
    stats["elapsed"] = time.time() - t0


async def main_async(args):
    pc = RTCPeerConnection()
    stats = {"frames": 0, "w": 0, "h": 0, "elapsed": 0.0}
    video_track = None
    got_track = asyncio.Event()

    @pc.on("track")
    def on_track(track):
        nonlocal video_track
        if track.kind == "video":
            print("[收流端] 收到视频轨道")
            video_track = track
            got_track.set()

    @pc.on("connectionstatechange")
    async def on_state():
        print(f"[收流端] 连接状态: {pc.connectionState}")

    offer_sdp = await read_file(args.offer)
    await pc.setRemoteDescription(RTCSessionDescription(sdp=offer_sdp, type="offer"))
    answer = await pc.createAnswer()
    await pc.setLocalDescription(answer)
    with open(args.answer, "w") as f:
        f.write(pc.localDescription.sdp)
    print(f"[收流端] answer 已写入 {args.answer}")

    try:
        await asyncio.wait_for(got_track.wait(), timeout=10.0)
    except asyncio.TimeoutError:
        print("[收流端] 一直没收到视频轨道，失败")
        await pc.close()
        raise SystemExit(1)

    await consume(video_track, stats, args.duration)
    fps = stats["frames"] / max(1e-6, stats["elapsed"])
    print(f"[收流端] 完成：{stats['elapsed']:.1f}s 收 {stats['frames']} 帧，"
          f"{stats['w']}x{stats['h']}，平均 {fps:.1f} fps")
    await pc.close()


def main():
    ap = argparse.ArgumentParser(description="云端 WebRTC 视频接收器")
    ap.add_argument("--offer", default="/tmp/ra_offer.sdp")
    ap.add_argument("--answer", default="/tmp/ra_answer.sdp")
    ap.add_argument("--duration", type=float, default=5.0, help="收流时长(秒)")
    args = ap.parse_args()
    asyncio.run(main_async(args))


if __name__ == "__main__":
    main()

