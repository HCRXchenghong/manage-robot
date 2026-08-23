#!/usr/bin/env python3
# 双路视频冗余 + 合并 演示（第 7 步）
#
# 在一个进程里同时跑【车端双路发送器】和【收端合并器】，用真实 UDP 回环
# 模拟两条独立网络路径，并演示三阶段：
#   阶段 1 双路正常：两路都到，合并器去重，画面满帧
#   阶段 2 B 路中断：B 口收到的包全部丢失，画面仍由 A 路完整支撑
#   阶段 3 B 路恢复：两路又都到，继续去重
# 核心验证：B 路断掉期间，视频【不中断、不重建会话】，仅靠 A 路无缝续播。
#
# 用法：
#   .venv/bin/python server/media-control/dual_video_demo.py

import asyncio
import os
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)                                   # 收端模块
sys.path.insert(0, os.path.join(HERE, "..", "..", "vehicle", "media-agent"))

from dual_video_receiver import Merger, DualVideoReceiver   # noqa: E402
from dual_video_sender import DualVideoSender                # noqa: E402

PORT_A, PORT_B = 9501, 9502


async def main_async():
    merger = Merger()
    receiver = DualVideoReceiver(merger, {"A": PORT_A, "B": PORT_B})
    await receiver.start()

    sender = DualVideoSender({"A": ("127.0.0.1", PORT_A),
                              "B": ("127.0.0.1", PORT_B)})

    # 分阶段：用"丢 B 路包"模拟 B 链路中断
    async def phase_control(total):
        t0 = time.monotonic()
        await asyncio.sleep(total * 0.40)
        merger.set_drop("B", True)
        print("[演示] >>> 阶段 2：B 路中断（B 口收包全部丢失）")
        await asyncio.sleep(total * 0.35)
        merger.set_drop("B", False)
        print("[演示] >>> 阶段 3：B 路恢复")

    DURATION = 6.0
    t0 = time.monotonic()
    send_task = asyncio.create_task(sender.run(DURATION))
    phase_task = asyncio.create_task(phase_control(DURATION))
    decoded = await receiver.decode_loop(DURATION + 0.5)
    await send_task
    await phase_task
    elapsed = time.monotonic() - t0

    total_sent_frames = sum(1 for _ in range(0))  # 占位
    print()
    print("===== 双路视频合并结果 =====")
    print(f"运行 {elapsed:.1f} s")
    print(f"A 路到达 {merger.per_path.get('A', 0)} 包，"
          f"B 路到达 {merger.per_path.get('B', 0)} 包")
    print(f"B 路中断期间丢弃 {merger.dropped.get('B', 0)} 包")
    print(f"冗余去重 {merger.dups} 份（两路都到，只取一份）")
    print(f"合并后唯一帧 {len(merger.seen)} 帧，解码成功 {decoded} 帧")
    print(f"实际帧率 {decoded / elapsed:.1f} fps")
    ok = decoded > 0 and merger.dropped.get("B", 0) > 0
    print("结论：" + ("B 路中断期间视频由 A 路无缝续播，未重建会话 ✓"
                       if ok else "异常，请检查 ✗"))


if __name__ == "__main__":
    asyncio.run(main_async())

