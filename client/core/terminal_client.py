#!/usr/bin/env python3
# 远驾终端客户端（第 9 步）：远程驾驶员打开"车端命令行"
#
# 流程（与控制同一套安全哲学）：
#   1. 先向控制权服务申请【终端令牌】（op=terminal）
#   2. 拿令牌连车端 terminal_agent（:9600），握手校验通过后附着到会话
#   3. 双向转发键盘输入 / shell 输出；断开后会话在车端保活，可重连
#
# 两种模式：
#   --script 'cmd1' 'cmd2' ...  脚本模式：依次发命令、收集输出（自动化验收用）
#   --interactive               交互模式：真实敲键盘（真人用），输入 exit 退出
#
# 用法：
#   python3 client/core/terminal_client.py --script 'uname -s' 'whoami'
#   python3 client/core/terminal_client.py --interactive

import argparse
import json
import socket
import sys
import time


def request_token(auth_addr, driver):
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(3.0)
    s.sendto(json.dumps({"op": "terminal", "driver": driver}).encode(),
             auth_addr)
    data, _ = s.recvfrom(65535)
    resp = json.loads(data.decode())
    if not resp.get("ok"):
        raise SystemExit(f"申请终端令牌失败：{resp}")
    return resp["token"]


def connect(host, port, token):
    """握手：发令牌，读 ok/失败，返回套接字（或抛错）。"""
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(5.0)
    s.connect((host, port))
    s.sendall((json.dumps({"token": token}) + "\n").encode())
    buf = b""
    while b"\n" not in buf:
        data = s.recv(4096)
        if not data:
            raise ConnectionError("agent 未响应握手")
        buf += data
    line = buf.split(b"\n", 1)[0]
    resp = json.loads(line.decode())
    if not resp.get("ok"):
        s.close()
        raise PermissionError(resp.get("reason", "附着被拒"))
    s.settimeout(None)
    return s, resp


def run_script(s, commands, settle=0.5):
    """脚本模式：逐条发命令，攒输出。"""
    out = []
    s.setblocking(False)

    def drain():
        while True:
            try:
                data = s.recv(4096)
            except (BlockingIOError, OSError):
                break
            if not data:
                break
            out.append(data)

    time.sleep(settle)      # 等回放的历史屏幕先到位
    drain()
    for cmd in commands:
        s.sendall((cmd + "\n").encode())
        time.sleep(settle)
        drain()
    time.sleep(settle)
    drain()
    return b"".join(out).decode("utf-8", "replace")


def run_interactive(s):
    """交互模式：stdin -> agent，agent -> stdout。输入 exit 退出。"""
    import threading
    print("（交互模式：输入直达车端 shell；输入 exit 退出。会话在车端保活。）")
    stop = False

    def reader():
        nonlocal stop
        while not stop:
            try:
                data = s.recv(4096)
            except OSError:
                break
            if not data:
                break
            sys.stdout.write(data.decode("utf-8", "replace"))
            sys.stdout.flush()

    t = threading.Thread(target=reader, daemon=True)
    t.start()
    try:
        while True:
            line = input()
            if line.strip() == "exit":
                break
            s.sendall((line + "\n").encode())
    except (EOFError, KeyboardInterrupt):
        pass
    stop = True


def main():
    ap = argparse.ArgumentParser(description="远驾终端客户端")
    ap.add_argument("--agent", default="127.0.0.1:9600")
    ap.add_argument("--authority", default="127.0.0.1:9300")
    ap.add_argument("--driver", default="remote-driver")
    ap.add_argument("--script", nargs="*", help="依次发送的命令")
    ap.add_argument("--interactive", action="store_true")
    args = ap.parse_args()

    ahost, aport = args.authority.split(":")
    thost, tport = args.agent.split(":")

    print(f"[终端客户端] 向 {args.authority} 申请终端令牌…")
    token = request_token((ahost, int(aport)), args.driver)
    print(f"[终端客户端] 拿到令牌，连接车端 {args.agent}…")
    s, resp = connect(thost, int(tport), token)
    print(f"[终端客户端] 附着成功，回放历史 {resp.get('replayed_bytes', 0)} 字节")

    try:
        if args.interactive:
            run_interactive(s)
        else:
            text = run_script(s, args.script or ["uname -s"])
            print("===== 车端输出 =====")
            print(text)
    finally:
        s.close()
        print("[终端客户端] 已断开（车端会话保活，可重连）")


if __name__ == "__main__":
    main()

