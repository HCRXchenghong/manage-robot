#!/usr/bin/env python3
# 网络故障注入器（简化版，阶段 0）
#
# 坐在控制客户端和假车之间，模拟公网弱网：
#   --loss 0.3        30% 随机丢包
#   --delay-ms 80     每个包附加 80ms 延迟
#   --down-after 0    转发 0 个包后开始全部丢弃（模拟断链）
#
# 用法：
#   python3 netem.py --listen 9101 --out 127.0.0.1:9100 --loss 0.3
#   然后客户端指向它：control_client.py --target 127.0.0.1:9101

import argparse
import random
import socket
import threading
import time


def main():
    ap = argparse.ArgumentParser(description="网络故障注入器")
    ap.add_argument("--listen", type=int, default=9101, help="面向客户端的端口")
    ap.add_argument("--out", default="127.0.0.1:9100", help="假车地址")
    ap.add_argument("--loss", type=float, default=0.0, help="丢包率 0..1")
    ap.add_argument("--delay-ms", type=float, default=0.0, help="附加延迟毫秒")
    ap.add_argument("--down-after", type=int, default=-1,
                    help="转发 N 个客户端包后模拟断链（-1=不断）")
    args = ap.parse_args()

    ohost, oport = args.out.split(":")
    out_addr = (ohost, int(oport))

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("0.0.0.0", args.listen))

    state = {"client": None, "count": 0}
    print(f"[netem] :{args.listen} -> {args.out} "
          f"loss={args.loss} delay={args.delay_ms}ms down_after={args.down_after}")

    def forward(data, addr):
        if addr == out_addr:
            # 来自假车的回执 -> 转回客户端
            if state["client"]:
                sock.sendto(data, state["client"])
            return
        # 来自客户端的包
        state["client"] = addr
        state["count"] += 1
        n = state["count"]
        if 0 <= args.down_after < n:
            print(f"[netem] 断链中：丢弃第 {n} 个包")
            return
        if random.random() < args.loss:
            print(f"[netem] 随机丢弃第 {n} 个包")
            return
        if args.delay_ms > 0:
            time.sleep(args.delay_ms / 1000.0)
        sock.sendto(data, out_addr)

    while True:
        data, addr = sock.recvfrom(65535)
        threading.Thread(target=forward, args=(data, addr), daemon=True).start()


if __name__ == "__main__":
    main()
