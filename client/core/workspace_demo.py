#!/usr/bin/env python3
# 远程终端端到端验收（第 9 步）
#
# 自动验证四道安全门 + 会话保活，一条命令跑完：
#   1. 授权门禁：有效令牌 -> 附着成功；无效令牌 -> 被拒
#   2. 命令执行：脚本模式发命令，拿到车端输出
#   3. 危险命令拦截：rm -rf / 命中黑名单，被挡下且不执行
#   4. 会话保活 + 断线重连：第一次设 VAR=42 并断开；重连后
#      回放历史、且 echo 仍能读到 VAR=42（证明是同一个会话）
#
# 前置：起 gateway / authority_service / terminal_agent。
# 用法：python3 client/core/workspace_demo.py

import socket
import sys
import time

AGENT = ("127.0.0.1", 9600)
AUTH = ("127.0.0.1", 9300)

PASS = "[OK]"
FAIL = "[NO]"
results = []


def check(name, ok, detail=""):
    results.append(ok)
    print(f"  {PASS if ok else FAIL} {name}" + (f"（{detail}）" if detail else ""))


def request_token(driver):
    import json
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(3.0)
    s.sendto(json.dumps({"op": "terminal", "driver": driver}).encode(), AUTH)
    data, _ = s.recvfrom(65535)
    return json.loads(data.decode())


def script_session(commands, token=None, driver="demo"):
    """开一个脚本会话：申请令牌(可选)->附着->发命令->收输出->断开。"""
    import json
    if token is None:
        resp = request_token(driver)
        if not resp.get("ok"):
            return None, resp
        token = resp["token"]
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(5.0)
    s.connect(AGENT)
    s.sendall((json.dumps({"token": token}) + "\n").encode())
    buf = b""
    while b"\n" not in buf:
        buf += s.recv(4096)
    line = buf.split(b"\n", 1)[0]
    resp = json.loads(line.decode())
    if not resp.get("ok"):
        s.close()
        return None, resp
    s.setblocking(False)
    out = []

    def drain():
        while True:
            try:
                d = s.recv(4096)
            except (BlockingIOError, OSError):
                break
            if not d:
                break
            out.append(d)

    time.sleep(0.6)
    drain()
    for c in commands:
        s.sendall((c + "\n").encode())
        time.sleep(0.5)
        drain()
    time.sleep(0.6)
    drain()
    s.close()
    return b"".join(out).decode("utf-8", "replace"), resp


def main():
    print("=== 门 1：授权门禁 ===")
    out, resp = script_session(["echo AUTH-OK"], token="bogus-token-123")
    check("无效令牌被拒", out is None and not resp.get("ok"),
          resp.get("reason", ""))

    print("=== 门 2：有效令牌 -> 命令执行 ===")
    out, resp = script_session(["echo MARKER-ALPHA", "uname -s"], driver="alice")
    ok2 = out is not None and "MARKER-ALPHA" in out
    check("有效令牌附着并执行命令", ok2,
          "输出含 MARKER-ALPHA" if ok2 else f"resp={resp}")

    print("=== 门 3：危险命令拦截 ===")
    out, resp = script_session(["rm -rf /", "echo STILL-ALIVE"], driver="bob")
    blocked = out is not None and "BLOCKED" in out
    alive = out is not None and "STILL-ALIVE" in out
    check("rm -rf / 被拦下", blocked)
    check("拦下后会话仍可用", alive)

    print("=== 门 4：会话保活 + 断线重连 ===")
    out_a, resp_a = script_session(["VAR=42", "echo SET-OK"], driver="carol")
    out_b, resp_b = script_session(["echo VAR-IS-$VAR"], driver="carol")
    replayed = resp_b.get("replayed_bytes", 0) if resp_b else 0
    same_session = out_b is not None and "VAR-IS-42" in out_b
    check("重连回放了历史屏幕", replayed > 0, f"回放 {replayed} 字节")
    check("重连后仍是同一会话（读到 VAR=42）", same_session)

    passed = sum(results)
    total = len(results)
    verdict = "ALL PASS" if passed == total else "HAS FAILURES"
    print(f"\n验收结果：{passed}/{total} 通过 {verdict}")
    sys.exit(0 if passed == total else 1)


if __name__ == "__main__":
    main()

