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
import base64
import fcntl
import json
import os
import pty
import socket
import ssl
import struct
import subprocess
import termios
import threading
import time
import sys
from pathlib import Path

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
from cryptography.exceptions import InvalidSignature
from google.protobuf.message import DecodeError

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "protocols" / "gen" / "python"))
from robot_agent_platform.v1 import auth as proto_auth  # noqa: E402
from robot_agent_platform.v1 import envelope_pb2, local_pb2, workspace_pb2  # noqa: E402

LOCAL_PROTOCOL = "platform.v1.local"
MAX_MSG_BYTES = 1024 * 1024

SCROLLBACK_MAX = 65536            # 回放缓冲（字节）
AUDIT_PATH = "/tmp/ra-workspace-audit.log"
SHELL = ["/bin/bash", "--norc", "--noprofile"]
DENY_PATTERNS = [
    b"rm -rf /", b"rm -rf ~", b"reboot", b"shutdown",
    b"mkfs", b"dd if=/dev/zero of=/dev/", b"> /dev/sda",
]


def encode_local_frame(frame):
    if not isinstance(frame, local_pb2.LocalFrame) or frame.protocol != LOCAL_PROTOCOL:
        raise ValueError("本机工作空间帧协议无效")
    raw = frame.SerializeToString(deterministic=True)
    if not 0 < len(raw) <= MAX_MSG_BYTES:
        raise ValueError("本机工作空间帧大小无效")
    return struct.pack(">I", len(raw)) + raw


def recv_local_frame(conn):
    header = recv_exact(conn, 4)
    if not header:
        raise EOFError
    size = struct.unpack(">I", header)[0]
    if size == 0 or size > MAX_MSG_BYTES:
        raise ValueError("本机工作空间帧超限")
    raw = recv_exact(conn, size)
    try:
        frame = local_pb2.LocalFrame.FromString(raw)
    except DecodeError as exc:
        raise ValueError("本机工作空间帧无法解析") from exc
    return frame


def recv_exact(conn, size):
    out = bytearray()
    while len(out) < size:
        part = conn.recv(size - len(out))
        if not part:
            return b""
        out.extend(part)
    return bytes(out)


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
    def __init__(self, listen_port, uds_path, vehicle_id, gateway_id, tls_context,
                 envelope_auth_key, authority_keys):
        self.listen_port = listen_port
        self.uds_path = uds_path
        self.vehicle_id = vehicle_id
        self.gateway_id = gateway_id
        self.tls_context = tls_context
        self.tokens = {}                 # token -> (valid_until_unix_ns, session_id)
        self.tokens_lock = threading.Lock()
        self.envelope_auth_key = envelope_auth_key
        self.authority_keys = authority_keys
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
        conn.sendall(encode_local_frame(local_pb2.LocalFrame(
            kind=local_pb2.LocalFrame.KIND_HELLO,
            component="workspace", protocol=LOCAL_PROTOCOL)))
        print(f"[terminal] 已接入 Gateway UDS：{self.uds_path}", flush=True)
        while True:
            try:
                frame = recv_local_frame(conn)
            except (OSError, EOFError, ValueError):
                break
            if (frame.GetProtocol() != LOCAL_PROTOCOL or
                    frame.GetKind() != local_pb2.LocalFrame.KIND_ENVELOPE or
                    not frame.HasField("envelope")):
                audit("denied", reason="工作空间通道收到非 Envelope 帧")
                continue
            env = frame.envelope
            try:
                proto_auth.validate_envelope(
                    env, "platform.v1.TerminalGrant", self.vehicle_id, self.gateway_id
                )
                if not proto_auth.verify_envelope_auth(env, self.envelope_auth_key):
                    raise ValueError("Envelope.auth_tag 无效")
                grant = workspace_pb2.TerminalGrant.FromString(env.payload)
                self._accept_grant(env, grant)
            except (TypeError, ValueError, DecodeError, InvalidSignature) as exc:
                audit("denied", reason="TerminalGrant 校验失败", detail=str(exc))

    def _accept_grant(self, env, grant):
        if (grant.version != 1 or grant.vehicle_id != self.vehicle_id or
                grant.gateway_id != self.gateway_id or not grant.session_id or
                len(grant.terminal_token) < 32 or not grant.driver_id or
                not grant.device_id or grant.issued_at_unix_ns <= 0 or
                grant.valid_until_unix_ns <= time.time_ns() or
                grant.valid_until_unix_ns <= grant.issued_at_unix_ns or
                env.session_id != grant.session_id):
            raise ValueError("TerminalGrant 字段不符合安全契约")
        key = self.authority_keys.get(grant.authority_key_id)
        if key is None or len(grant.authority_signature) != 64:
            raise ValueError("TerminalGrant Authority 签名不受信任")
        unsigned = workspace_pb2.TerminalGrant()
        unsigned.CopyFrom(grant)
        unsigned.ClearField("authority_signature")
        Ed25519PublicKey.from_public_bytes(key).verify(
            grant.authority_signature, unsigned.SerializeToString(deterministic=True)
        )
        token = base64.urlsafe_b64encode(grant.terminal_token).decode("ascii")
        with self.tokens_lock:
            self.tokens[token] = (grant.valid_until_unix_ns, grant.session_id)
        print(f"[terminal] 收到令牌 driver={grant.driver_id} "
              f"有效期 {(grant.valid_until_unix_ns - time.time_ns()) / 1e9:.0f}s",
              flush=True)

    def token_ok(self, tok, session_id):
        with self.tokens_lock:
            grant = self.tokens.get(tok)
        return (grant is not None and grant[1] == session_id and
                grant[0] > time.time_ns())

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
            raw_conn, addr = srv.accept()
            try:
                conn = self.tls_context.wrap_socket(raw_conn, server_side=True)
            except ssl.SSLError as exc:
                audit("denied", src=str(addr), reason="客户端 mTLS 握手失败")
                print(f"[terminal] mTLS 握手失败：{type(exc).__name__}", flush=True)
                raw_conn.close()
                continue
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
        session_id = req.get("session_id", "")
        if not self.token_ok(tok, session_id):
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


def load_hmac_key(path):
    info = os.stat(path)
    if info.st_mode & 0o077:
        raise ValueError("Envelope HMAC 密钥文件权限必须为 0600")
    with open(path, "rb") as f:
        raw = base64.b64decode(f.read().strip(), validate=True)
    if len(raw) < 32:
        raise ValueError("Envelope HMAC 密钥至少需要 256 位")
    return raw


def load_authority_key(spec):
    if "=" not in spec:
        raise ValueError("authority-public-key 格式应为 key_id=/secure/path/key.b64")
    key_id, path = spec.split("=", 1)
    if not key_id or not path:
        raise ValueError("authority-public-key 格式无效")
    info = os.stat(path)
    if info.st_mode & 0o077:
        raise ValueError("Authority 公钥文件权限必须为 0600 或 0640")
    with open(path, "rb") as f:
        raw = base64.b64decode(f.read().strip(), validate=True)
    if len(raw) != 32:
        raise ValueError("Authority 公钥必须为 256 位 Ed25519 公钥")
    return key_id, raw


def main():
    ap = argparse.ArgumentParser(description="车端远程终端代理")
    ap.add_argument("--listen", type=int, required=True)
    ap.add_argument("--uds", required=True)
    ap.add_argument("--vehicle-id", required=True)
    ap.add_argument("--gateway-id", required=True)
    ap.add_argument("--cert", required=True, help="终端服务端证书")
    ap.add_argument("--key", required=True, help="终端服务端私钥")
    ap.add_argument("--client-ca", required=True, help="受信工作空间客户端 CA")
    ap.add_argument("--envelope-auth-key", required=True,
                    help="Envelope HMAC-SHA256 密钥文件（0600、base64）")
    ap.add_argument("--authority-public-key", action="append", required=True,
                    help="可重复：key_id=/secure/path/authority-public-key.b64")
    args = ap.parse_args()
    ca = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ca.minimum_version = ssl.TLSVersion.TLSv1_3
    ca.verify_mode = ssl.CERT_REQUIRED
    ca.load_cert_chain(args.cert, args.key)
    ca.load_verify_locations(args.client_ca)
    try:
        envelope_auth_key = load_hmac_key(args.envelope_auth_key)
        authority_keys = {}
        for spec in args.authority_public_key:
            key_id, key = load_authority_key(spec)
            authority_keys[key_id] = key
    except (OSError, ValueError) as exc:
        raise SystemExit(f"工作空间安全材料不可用：{exc}")
    agent = WorkspaceAgent(args.listen, args.uds, args.vehicle_id, args.gateway_id, ca,
                           envelope_auth_key, authority_keys)
    threading.Thread(target=agent.uds_loop, daemon=True).start()
    agent.serve()


if __name__ == "__main__":
    main()
