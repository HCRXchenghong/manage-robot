#!/usr/bin/env python3
# 车端双路视频发送器（第 7 步）
#
# 架构文档 §7.4/阶段 3：视频走双路冗余。车端把【同一路画面】同时从两个
# 网络出口（两个 modem / 两家运营商）发出；任一路断了，另一路仍完整，
# 收端按序号合并去重，画面不中断、不重建。
#
# 阶段 0 演示用 UDP 直发：每个视频帧编码成一个包，前 4 字节是帧序号，
# 后面是 VP8 编码数据。生产环境换成真正的 RTP（带序号/时间戳/分片），
# 合并逻辑同构。
#
# 用法（被 dual_video_demo.py 编排，也可单独跑）：
#   .venv/bin/python vehicle/media-agent/dual_video_sender.py \
#       --target-a 127.0.0.1:9501 --target-b 127.0.0.1:9502 --duration 6

import argparse
import asyncio
import fractions
import socket
import struct
import time

import numpy as np
import av

FPS = 10
WIDTH, HEIGHT = 320, 180


def make_img(n, w=WIDTH, h=HEIGHT):
    img = np.zeros((h, w, 3), dtype=np.uint8)
    img[:, :, 1] = (n * 8) % 128                       # 背景绿随帧变
    x = (n * 6) % (w - 40)                             # 亮块左右移动
    img[h // 2 - 20:h // 2 + 20, x:x + 40, :] = 255
    return img


def build_encoder(fps=FPS, w=WIDTH, h=HEIGHT, bitrate=200_000):
    enc = av.CodecContext.create("vp8", "w")
    enc.width = w
    enc.height = h
    enc.pix_fmt = "yuv420p"
    enc.bit_rate = bitrate
    enc.time_base = fractions.Fraction(1, fps)
    return enc


class DualVideoSender:
    """把每一帧同时发往 A、B 两个出口（冗余双发）。"""

    def __init__(self, targets, fps=FPS):
        self.targets = targets          # {"A": (host, port), "B": (host, port)}
        self.fps = fps
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.sent = {name: 0 for name in targets}

    async def run(self, duration):
        enc = build_encoder(self.fps)
        seq = 0
        t0 = time.monotonic()
        interval = 1.0 / self.fps
        while time.monotonic() - t0 < duration:
            fr = av.VideoFrame.from_ndarray(make_img(seq), format="rgb24")
            fr = fr.reformat(format="yuv420p")
            fr.pts = seq
            fr.time_base = fractions.Fraction(1, self.fps)
            pkts = enc.encode(fr)
            if pkts:
                payload = struct.pack(">I", seq) + bytes(pkts[0])
                for name, addr in self.targets.items():
                    self.sock.sendto(payload, addr)
                    self.sent[name] += 1
            seq += 1
            # 按帧率节奏推进
            wait = t0 + seq * interval - time.monotonic()
            if wait > 0:
                await asyncio.sleep(wait)
        return seq


def main():
    ap = argparse.ArgumentParser(description="车端双路视频发送器")
    ap.add_argument("--target-a", default="127.0.0.1:9501")
    ap.add_argument("--target-b", default="127.0.0.1:9502")
    ap.add_argument("--duration", type=float, default=6.0)
    args = ap.parse_args()

    def addr(s):
        h, p = s.split(":")
        return (h, int(p))

    sender = DualVideoSender({"A": addr(args.target_a),
                              "B": addr(args.target_b)})
    n = asyncio.run(sender.run(args.duration))
    print(f"[双路发送] 共产 {n} 帧；A 出口发 {sender.sent['A']} 包，"
          f"B 出口发 {sender.sent['B']} 包")


if __name__ == "__main__":
    main()

