#!/usr/bin/env python3
# 控制权服务（第 8 步）：接管审批与租约 / fencing 发放
#
# 架构文档 §9：驾驶员要动车，必须先向 Control Authority 申请接管；
# 批准后拿到【租约 + fencing token】，带着它们发控制命令，车端仲裁器
# 才认。本服务就是这台"发证机关"（阶段 0 单机简化版）：
#   - request：某驾驶员申请接管 -> 发一张新租约（fencing 全局 +1，
#     单调递增），并把 LeaseGrant 经 Gateway 推给车端
#   - release：主动交还控制权 -> 推一张"空租约"（立即失效），车端收回权限
#   - 单人独占：同一时刻只允许一个接管者；新申请自动顶掉旧租约
#     （这正是 fencing token 的意义：旧租约的 token 更小，重放必被拒）
#
# 用法：
#   python3 server/control-authority/authority_service.py \
#       [--listen 9300] [--vehicle-gw 127.0.0.1:9100] [--lease-seconds 6]

import argparse
import json
import os
import socket
import sys
import time
import uuid

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
sys.path.insert(0, os.path.join(REPO, "client", "simulator"))
import common as C  # noqa: E402


class Authority:
    def __init__(self, gw_addr, lease_seconds):
        self.gw_addr = gw_addr
        self.lease_seconds = lease_seconds
        self.terminal_seconds = 60.0   # 终端令牌默认有效期（比驾驶租约长：运维会话更久）
        self.fencing = 0            # 全局单调，永不回退
        self.current = None         # (driver, lease_id, until_ns)

    def request(self, driver):
        if self.current and self.current[2] > time.time_ns():
            d, lid, until = self.current
            if d != driver:
                print(f"[authority] {driver} 申请接管，顶掉 {d} 的租约 {lid}")
        self.fencing += 1
        lease_id = f"lease-{uuid.uuid4().hex[:8]}"
        until = time.time_ns() + int(self.lease_seconds * 1e9)
        self.current = (driver, lease_id, until)
        self._push_grant(driver, lease_id, self.fencing, until)
        print(f"[authority] 批准 {driver} 接管：lease={lease_id} "
              f"fencing={self.fencing} 有效期 {self.lease_seconds}s")
        return {"ok": True, "lease_id": lease_id, "fencing_token": self.fencing,
                "valid_until_unix_ns": until}

    def release(self, driver):
        self._push_grant(driver, "", self.fencing, time.time_ns() - 1)
        self.current = None
        print(f"[authority] {driver} 交还控制权，租约已撤销")
        return {"ok": True}

    def terminal(self, driver):
        """第 9 步：发一张终端令牌（远程登录车端电脑用）。

        终端是最危险的接口，同样坚持"先授权再连接"：
        车端 workspace-agent 只认本服务签发、经 Gateway 送达的令牌。
        """
        token = f"term-{uuid.uuid4().hex[:12]}"
        until = time.time_ns() + int(self.terminal_seconds * 1e9)
        payload = {"driver_id": driver, "terminal_token": token,
                   "valid_until_unix_ns": until}
        env = C.make_envelope("platform.v1.TerminalGrant", payload, "authority",
                              sequence=self.fencing, ttl_ms=30000)
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
            s.sendto(json.dumps(env).encode("utf-8"), self.gw_addr)
        print(f"[authority] 发给 {driver} 终端令牌 {token} "
              f"有效期 {self.terminal_seconds:.0f}s")
        return {"ok": True, "token": token, "valid_until_unix_ns": until}

    def status(self):
        """只读查询当前接管状态（大屏轮询用，第 10 步）。

        不改变任何状态、不下发任何信封。
        """
        if self.current and self.current[2] > time.time_ns():
            d, lid, until = self.current
            return {"ok": True, "active": True, "driver": d,
                    "lease_id": lid, "fencing": self.fencing,
                    "valid_until_unix_ns": until}
        return {"ok": True, "active": False}

    def emergency_stop(self):
        """第 10 步：大屏触发的紧急停车。

        安全链路不变——仍走「发证 -> 车端仲裁器裁决」：
          1) 以 fencing+1 签发一张 3s 短租约（LeaseGrant 经 Gateway 推车端）；
          2) 用该租约 + 再递增的 fencing 下发一条零速运动命令；
          3) 之后故意不再续发命令 -> 车端看门狗 800ms 判定断链 ->
             最小风险（minimum_risk）-> 自主减速停稳（stopped）。
        """
        self.fencing += 1
        lease_id = f"estop-{uuid.uuid4().hex[:8]}"
        until = time.time_ns() + int(3e9)
        self._push_grant("estop", lease_id, self.fencing, until)
        self.current = ("estop", lease_id, until)
        time.sleep(0.05)  # 让 LeaseGrant 先到达车端，再发控制命令
        self.fencing += 1
        payload = {
            "control_session_id": "estop-session",
            "lease_id": lease_id,
            "fencing_token": self.fencing,
            "command_sequence": self.fencing,  # 复用全局单调 fencing 保证序号新鲜
            "issued_monotonic_ns": C.mono_ns(),
            "ttl_ms": 1500,
            "mode": "CONTROL_MODE_TARGET_MOTION",
            "command": {"motion": {"target_speed_mps": 0.0}},
        }
        env = C.make_envelope("platform.v1.ControlCommand", payload,
                              "estop-session", sequence=self.fencing, ttl_ms=1500)
        ack_result = "NO_ACK"
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
            s.settimeout(1.0)
            s.sendto(json.dumps(env).encode("utf-8"), self.gw_addr)
            try:
                data, _ = s.recvfrom(65535)
                ack_result = json.loads(data.decode("utf-8")).get(
                    "payload", {}).get("result", "?")
            except (TimeoutError, socket.timeout):
                pass
        print(f"[authority] 紧急停车已下发：lease={lease_id} "
              f"fencing={self.fencing} 回执={ack_result}")
        return {"ok": ack_result == "CONTROL_RESULT_ACCEPTED",
                "ack": ack_result, "fencing_token": self.fencing}

    def _push_grant(self, driver, lease_id, fencing, until_ns):
        payload = {"driver_id": driver, "lease_id": lease_id,
                   "fencing_token": fencing, "valid_until_unix_ns": until_ns}
        env = C.make_envelope("platform.v1.LeaseGrant", payload, "authority",
                              sequence=fencing, ttl_ms=30000)
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
            s.sendto(json.dumps(env).encode("utf-8"), self.gw_addr)


def main():
    ap = argparse.ArgumentParser(description="控制权服务（接管审批 + 租约发放）")
    ap.add_argument("--listen", type=int, default=9300)
    ap.add_argument("--vehicle-gw", default="127.0.0.1:9100")
    ap.add_argument("--lease-seconds", type=float, default=6.0)
    args = ap.parse_args()

    host, port = args.vehicle_gw.split(":")
    auth = Authority((host, int(port)), args.lease_seconds)

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("127.0.0.1", args.listen))
    sock.settimeout(0.5)
    print(f"[authority] 就绪 :{args.listen}，租约默认 {args.lease_seconds}s，"
          f"车端网关 {args.vehicle_gw}")
    while True:
        try:
            data, addr = sock.recvfrom(65535)
            req = json.loads(data.decode("utf-8"))
        except (TimeoutError, socket.timeout, OSError):
            continue
        except ValueError:
            continue
        op = req.get("op")
        driver = req.get("driver", "?")
        if op == "request":
            resp = auth.request(driver)
        elif op == "release":
            resp = auth.release(driver)
        elif op == "terminal":
            resp = auth.terminal(driver)
        elif op == "status":
            resp = auth.status()
        elif op == "emergency_stop":
            resp = auth.emergency_stop()
        else:
            resp = {"ok": False, "error": f"未知操作 {op}"}
        sock.sendto(json.dumps(resp).encode("utf-8"), addr)


if __name__ == "__main__":
    main()
