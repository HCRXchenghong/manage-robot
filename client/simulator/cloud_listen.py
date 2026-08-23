#!/usr/bin/env python3
# 假云端：监听 UDP 端口，打印收到的平台信封（第 5b 步起由真实云服务替代）
#
# 用法：python3 cloud_listen.py [--listen 9200]

import argparse
import json
import socket


def main():
    ap = argparse.ArgumentParser(description="假云端监听器")
    ap.add_argument("--listen", type=int, default=9200)
    args = ap.parse_args()

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("0.0.0.0", args.listen))
    print(f"[云端] 监听 :{args.listen}")
    count = 0
    while True:
        data, addr = sock.recvfrom(65535)
        try:
            env = json.loads(data.decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            continue
        count += 1
        mtype = env.get("message_type", "?").split(".")[-1]
        extra = ""
        if mtype == "SignalUpdate":
            paths = [s.get("path", "?") for s in env.get("payload", {}).get("signals", [])]
            extra = f" 信号={len(paths)}"
        print(f"[云端] #{count} {mtype} 车={env.get('vehicle_id')} "
              f"seq={env.get('sequence')}{extra}")


if __name__ == "__main__":
    main()
