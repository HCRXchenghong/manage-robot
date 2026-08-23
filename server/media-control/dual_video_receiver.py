#!/usr/bin/env python3
# 收端双路视频合并器（第 7 步）——模拟远驾控制台的"双网卡收流"
#
# 两个 UDP 监听口 = 两个网络入口（modem A / modem B）。
# 同一路画面会从两个口各来一份；合并器按【帧序号】去重：
#   - 两路都好：帧从先到的一路取，另一路那份记为"冗余去重"
#   - 一路断了：帧全走另一路，画面不中断、不需要重建会话
# 合并后统一解码并统计：唯一帧数、各路到达数、去重数、实际帧率。
#
# 用法（被 dual_video_demo.py 编排）。

import asyncio
import struct

import av


class Merger:
    """按帧序号合并两路冗余流：去重 + 可模拟单路故障（丢包窗口）。"""

    def __init__(self):
        self.seen = set()
        self.queue = asyncio.Queue()
        self.per_path = {}       # 路径 -> 到达包数
        self.dups = 0            # 冗余去重数（两路都到，取一份）
        self.dropped = {}        # 路径 -> 模拟故障丢弃数
        self.drop = {}           # 路径 -> 是否处于"故障"状态

    def set_drop(self, path, on):
        self.drop[path] = on

    def offer(self, path, seq, data):
        self.per_path[path] = self.per_path.get(path, 0) + 1
        if self.drop.get(path):
            self.dropped[path] = self.dropped.get(path, 0) + 1
            return
        if seq in self.seen:
            self.dups += 1
            return
        self.seen.add(seq)
        self.queue.put_nowait((seq, data))


class PathListener(asyncio.DatagramProtocol):
    def __init__(self, name, merger):
        self.name = name
        self.merger = merger

    def datagram_received(self, data, addr):
        if len(data) < 5:
            return
        (seq,) = struct.unpack(">I", data[:4])
        self.merger.offer(self.name, seq, data[4:])


class DualVideoReceiver:
    """双口监听 + 合并 + 解码统计。"""

    def __init__(self, merger, ports):
        self.merger = merger
        self.ports = ports        # {"A": port, "B": port}
        self.decoded = 0

    async def start(self):
        loop = asyncio.get_running_loop()
        for name, port in self.ports.items():
            await loop.create_datagram_endpoint(
                lambda name=name: PathListener(name, self.merger),
                local_addr=("127.0.0.1", port))

    async def decode_loop(self, duration):
        dec = av.CodecContext.create("vp8", "r")
        end = asyncio.get_running_loop().time() + duration
        while asyncio.get_running_loop().time() < end:
            try:
                seq, data = await asyncio.wait_for(self.merger.queue.get(),
                                                   timeout=1.0)
            except asyncio.TimeoutError:
                continue
            frames = dec.decode(av.Packet(data))
            self.decoded += len(frames)
        return self.decoded

