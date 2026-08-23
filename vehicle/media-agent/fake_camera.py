#!/usr/bin/env python3
# 合成摄像头（第 5d 步）：没有真车相机时，用"画出来的画面"走完整条视频链路
# 供 WebRTC 发送端当视频源；生产里替换为真实相机采集节点即可。
#
# 每帧 640x360 RGB：一个移动的亮块 + 随帧数变化的背景，
# 方便肉眼确认"画面在动、帧在更新"，也方便统计帧率。

import asyncio
import fractions
import time

import numpy as np
from av import VideoFrame
from aiortc.mediastreams import MediaStreamTrack

FPS = 15
WIDTH, HEIGHT = 640, 360


class FakeCameraTrack(MediaStreamTrack):
    kind = "video"

    def __init__(self, fps=FPS, width=WIDTH, height=HEIGHT):
        super().__init__()
        self.fps = fps
        self.width = width
        self.height = height
        self._start = None
        self._n = 0

    def _make_frame(self, n):
        img = np.zeros((self.height, self.width, 3), dtype=np.uint8)
        shade = (n * 6) % 128                      # 背景绿随帧数缓慢变亮
        img[:, :, 1] = shade
        img[:, :, 0] = (np.arange(self.width) // 8) % 64   # 红色竖纹
        x = (n * 10) % (self.width - 80)           # 白色亮块从左往右循环移动
        img[140:220, x:x + 80, :] = 255
        return img

    async def recv(self):
        if self._start is None:
            self._start = time.time()
        else:
            wait = self._start + self._n / self.fps - time.time()
            if wait > 0:
                await asyncio.sleep(wait)
        frame = VideoFrame.from_ndarray(self._make_frame(self._n), format="rgb24")
        frame.pts = self._n
        frame.time_base = fractions.Fraction(1, self.fps)
        self._n += 1
        return frame

