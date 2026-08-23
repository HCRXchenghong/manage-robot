#!/usr/bin/env python3
# 车端远程终端代理（第 9 步）
#
# 远驾不只是"开车"，有时要登录车端电脑看日志、重启进程、做诊断。
# 本代理就是该场景的车端入口（生产对应 server/workspace/ 的车端部分）。
#
# 四道安全门（终端是最危险的接口，门禁比控制更严）：
#   1. 先授权再连接：只认控制权服务签发、经 Gateway UDS 送达的终端令牌
#      （TerminalGrant），无效/过期一律拒绝并记审计
#   2. 全审计：attach / detach / denied / blocked 全部写审计日志
#   3. 危险命令拦截：黑名单（rm -rf /、reboot 等）直接不发给 shell
#   4. 会话保活：断线不杀会话（简化版 tmux），重连回放屏幕、接着用
#      —— 对应架构验收标准"断线后终端可恢复"
#
# 用法：
#   python3 vehicle/workspace-agent/terminal_agent.py \
#       [--listen 9600] [--uds /tmp/ra-gw.sock]

import argparse
import fcntl
import json
import os
import pty
import socket
import struct
import subprocess
import termios
import threading
import time

SCROLLBACK_MAX = 65536            # 回放缓冲（字节）
AUDIT_PATH = "/tmp/ra-workspace-audit.log"
SHELL = ["/bin/bash", "--norc", "--noprofile"]
DENY_PATTERNS = [
    b"rm -rf /", b"rm -rf ~", b"reboot", b"shutdown",
    b"mkfs", b"dd if=/dev/zero of=/dev/", b"> /dev/sda",
]


def audit(event, **kw):
    rec = {"ts_ns": time.time_ns(), "event": event}
    rec.update(kw)
    try:
        with open(AUDIT_PATH, "a") as f:
            f.write(json.dumps(rec, ensure_ascii=False) + "\n")
    except OSError:
        pass
    print(f"[terminal] audit: {event} {kw}", flush=True)


class ShellSession:
    """持久 PTY 会话：断线不杀进程，保留滚动缓冲（简化版 tmux）。"""

    def __init__(self, agent):
        self.agent = agent
        self.master = None
        self.proc = None
        self.lock = threading.Lock()
        self.scrollback = bytearray()

    def alive(self):
        return self.proc is not None and self.proc.poll() is None

    def ensure(self):
        with self.lock:
            if self.alive():
                return
            master, slave = pty.openpty()
            env = dict(os.environ, TERM="dumb")
            self.proc = subprocess.Popen(
                SHELL, stdin=slave, stdout=slave, stderr=slave,
                env=env, close_fds=True)
            os.close(slave)
            self.master = master
            fcntl.ioctl(master, termios.TIOCSWINSZ,
                        struct.pack("HHHH", 24, 100, 0, 0))
            self.scrollback.clear()
            threading.Thread(target=self._reader, daemon=True).start()
            audit("session_start", shell=SHELL[0], pid=self.proc.pid)

    def _reader(self):
        while True:
            try:
                data = os.read(self.master, 4096)
            except OSError:
                break
            if not data:
                break
            self.scrollback.extend(data)
            if len(self.scrollback) > SCROLLBACK_MAX:
                del self.scrollback[:len(self.scrollback) - SCROLLBACK_MAX]
            self.agent.push_to_client(data)
        audit("session_end", reason="shell 退出")

    def write(self, data):
        try:
            os.write(self.master, data)
        except OSError:
            pass


class WorkspaceAgent:
    def __init__(self, listen_port, uds_path):
        self.listen_port = listen_port
        self.uds_path = uds_path
        self.tokens = {}                 # token -> valid_until_unix_ns
        self.tokens_lock = threading.Lock()
        self.session = ShellSession(self)
        self.client = None               # 当前远端客户端套接字（独占）
        self.client_lock = threading.Lock()
        self.input_buf = bytearray()     # 输入行缓冲（危险命令扫描用）

    # ---------- PTY 输出 -> 已连接客户端 ----------
    def push_to_client(self, data):
        with self.client_lock:
            c = self.client
        if c is not None:
            try:
                c.sendall(data)
            except OSError:
                pass

    # ---------- Gateway UDS：接收终端令牌 ----------
    def uds_loop(self):
        while True:
            conn = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            try:
                conn.connect(self.uds_path)
                break
            except OSError:
                conn.close()
                time.sleep(0.3)
        print(f"[terminal] 已接入 Gateway UDS：{self.uds_path}", flush=True)
        buf = b""
        while True:
            try:
                data = conn.recv(65535)
            except OSError:
                break
            if not data:
                break
            buf += data
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                try:
                    rec = json.loads(line.decode("utf-8"))
                except (ValueError, UnicodeDecodeError):
                    continue
                if rec.get("kind") != "terminal":
                    continue
                p = rec.get("env", {}).get("payload", {})
                tok = p.get("terminal_token", "")
                until = int(p.get("valid_until_unix_ns", 0))
                if tok:
                    with self.tokens_lock:
                        self.tokens[tok] = until
                    print(f"[terminal] 收到令牌 driver={p.get('driver_id')} "
                          f"有效期 {(until - time.time_ns()) / 1e9:.0f}s",
                          flush=True)

    def token_ok(self, tok):
        with self.tokens_lock:
            until = self.tokens.get(tok)
        return until is not None and until > time.time_ns()

    # ---------- 输入安全：黑名单拦截 ----------
    def check_input(self, data):
        self.input_buf.extend(data)
        if len(self.input_buf) > 4096:
            del self.input_buf[:len(self.input_buf) - 4096]
        for pat in DENY_PATTERNS:
            if pat in self.input_buf:
                self.input_buf.clear()
                return pat
        idx = self.input_buf.rfind(b"\n")   # 命令按行扫描，已回车的可丢
        if idx >= 0:
            del self.input_buf[:idx + 1]
        return None

    # ---------- TCP：远端客户端 ----------
    def serve(self):
        srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        srv.bind(("127.0.0.1", self.listen_port))
        srv.listen(4)
        print(f"[terminal] 监听 127.0.0.1:{self.listen_port}"
              f"（需令牌才可附着）", flush=True)
        while True:
            conn, addr = srv.accept()
            threading.Thread(target=self.handle_client, args=(conn, addr),
                             daemon=True).start()

    def handle_client(self, conn, addr):
        conn.settimeout(10)
        buf = b""
        while b"\n" not in buf:
            try:
                data = conn.recv(4096)
            except OSError:
                conn.close()
                return
            if not data:
                conn.close()
                return
            buf += data
        line = buf.split(b"\n", 1)[0]
        try:
            req = json.loads(line.decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            req = {}
        tok = req.get("token", "")
        if not self.token_ok(tok):
            audit("denied", src=str(addr), reason="令牌无效或已过期")
            conn.sendall((json.dumps(
                {"ok": False, "reason": "终端令牌无效或已过期"}) + "\n").encode())
            conn.close()
            return
        with self.client_lock:
            if self.client is not None:
                conn.sendall((json.dumps(
                    {"ok": False, "reason": "已有活动连接（独占）"}) + "\n").encode())
                conn.close()
                return
            self.client = conn
        self.session.ensure()
        replay = bytes(self.session.scrollback)
        try:
            conn.sendall((json.dumps(
                {"ok": True, "session": "veh-shell",
                 "replayed_bytes": len(replay)}) + "\n").encode())
            if replay:
                conn.sendall(replay)
        except OSError:
            pass
        audit("attach", src=str(addr), replayed=len(replay))
        conn.settimeout(None)
        try:
            while True:
                data = conn.recv(4096)
                if not data:
                    break
                bad = self.check_input(data)
                if bad is not None:
                    audit("blocked", pattern=bad.decode("utf-8", "replace"))
                    notice = ("\r\n[BLOCKED] 危险命令命中车端安全策略（"
                              + bad.decode("utf-8", "replace")
                              + "），未执行\r\n")
                    try:
                        conn.sendall(notice.encode())
                    except OSError:
                        pass
                    continue
                self.session.write(data)
        finally:
            with self.client_lock:
                if self.client is conn:
                    self.client = None
            audit("detach", src=str(addr))
            try:
                conn.close()
            except OSError:
                pass
            print("[terminal] 客户端断开，会话保活等待重连", flush=True)


def main():
    ap = argparse.ArgumentParser(description="车端远程终端代理")
    ap.add_argument("--listen", type=int, default=9600)
    ap.add_argument("--uds", default="/tmp/ra-gw.sock")
    args = ap.parse_args()
    agent = WorkspaceAgent(args.listen, args.uds)
    threading.Thread(target=agent.uds_loop, daemon=True).start()
    agent.serve()


if __name__ == "__main__":
    main()
