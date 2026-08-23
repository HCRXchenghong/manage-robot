#!/usr/bin/env python3
# 假车模拟器（阶段 0）
#
# 角色：
#   1) 周期性向“云端”发送遥测 SignalUpdate（默认 127.0.0.1:9200）；
#   2) 接收 ControlCommand，按车端“安全仲裁器”逻辑校验，返回 ControlAck。
#
# 仲裁规则（对应 control.proto / 架构文档 §9）：
#   - 租约无效          -> REJECTED_LEASE
#   - fencing token 倒退 -> REJECTED_FENCING
#   - TTL 过期          -> REJECTED_STALE（宁可丢弃，不执行旧命令）
#   - 序号重复/倒退      -> REJECTED_STALE
#   - 超出安全限幅       -> REJECTED_LIMIT（低速 ODD：5 m/s）
#   - 全部通过          -> ACCEPTED 并执行
#   - 断链看门狗（第 6 步）：遥控中超过 WATCHDOG_MS 没收到新的有效命令，
#     判定链路中断 -> 进入最小风险状态，自主减速直到安全停稳；
#     只有 ACCEPTED 的命令能"续命"，过期/被拒的命令不算。
#
# 用法：python3 vehicle_sim.py [--listen 9100] [--cloud 127.0.0.1:9200] [--hz 2]

import argparse
import json
import math
import socket
import time

import common as C


class Arbiter:
    """车端安全仲裁器：控制命令是否执行，它说了算，云端不可绕过。

    除了逐条命令校验（租约/fencing/TTL/序号/限幅），还看守"命令流"本身：
    遥控行驶中若 WATCHDOG_MS 内没有新的有效命令到达，判定链路中断，
    进入最小风险状态并自主减速停车；驾驶员重新发来通过校验的新命令时
    恢复遥控（重新接管）。
    """

    MAX_SPEED_MPS = 5.0   # 第一阶段低速 ODD 演示限速
    WATCHDOG_MS = 800     # 断链判定：遥控中允许的最大"新有效命令"间隔
    DECEL_MPS2 = 1.2      # 最小风险减速度（m/s²，舒适且安全的减速）

    def __init__(self):
        self.last_seq = {}      # 会话 -> 已接受的最大序号
        self.last_fencing = 0   # 见过的最大 fencing token
        self.valid_lease = "valid-lease"
        self.mode = "autonomous"
        self.speed = 1.6        # 模拟车速（m/s）
        # ---- 断链最小风险（第 6 步）----
        self.state = "autonomous"      # autonomous / remote / minimum_risk / stopped
        self.last_accepted_ns = None   # 最近一条 ACCEPTED 命令到达时刻
        self._last_tick_ns = None      # 上次 tick 时刻（算减速步长用）

    def check(self, cmd):
        """返回 (ControlResult, 详情)。顺序：租约 → fencing → TTL → 序号 → 限幅。"""
        if cmd.get("lease_id") != self.valid_lease:
            return "CONTROL_RESULT_REJECTED_LEASE", "租约无效"

        tok = int(cmd.get("fencing_token", 0))
        if tok < self.last_fencing:
            return "CONTROL_RESULT_REJECTED_FENCING", f"fencing {tok} < 已见 {self.last_fencing}"

        issued = int(cmd.get("issued_monotonic_ns", 0))
        ttl_ms = int(cmd.get("ttl_ms", 0))
        age_ms = (C.mono_ns() - issued) / 1e6
        if age_ms > ttl_ms:
            return "CONTROL_RESULT_REJECTED_STALE", f"过期：age={age_ms:.0f}ms > ttl={ttl_ms}ms"

        sid = cmd.get("control_session_id", "")
        seq = int(cmd.get("command_sequence", 0))
        if seq <= self.last_seq.get(sid, -1):
            return "CONTROL_RESULT_REJECTED_STALE", f"序号重复/倒退：{seq}"

        which = cmd.get("command", {})
        if "direct" in which:
            d = which["direct"]
            if not (0.0 <= d.get("throttle_fraction", 0) <= 1.0) or \
               not (0.0 <= d.get("brake_fraction", 0) <= 1.0):
                return "CONTROL_RESULT_REJECTED_LIMIT", "油门/制动超出 0..1"
        if "motion" in which:
            m = which["motion"]
            if abs(m.get("target_speed_mps", 0)) > self.MAX_SPEED_MPS:
                return "CONTROL_RESULT_REJECTED_LIMIT", f"目标速度超出 {self.MAX_SPEED_MPS} m/s"

        self.last_fencing = max(self.last_fencing, tok)
        self.last_seq[sid] = seq
        # 接管/续命成功：只有 ACCEPTED 的命令能证明"驾驶员的线还活着"
        resumed = self.state in ("minimum_risk", "stopped")
        self.state = "remote"
        self.mode = "remote_control"
        self.last_accepted_ns = C.mono_ns()
        detail = "ok（链路恢复，重新接管）" if resumed else "ok"
        return "CONTROL_RESULT_ACCEPTED", detail

    def tick(self):
        """主循环周期性调用：推进断链看门狗与最小风险减速。"""
        now = C.mono_ns()
        dt = 0.0 if self._last_tick_ns is None else (now - self._last_tick_ns) / 1e9
        self._last_tick_ns = now
        if self.state == "remote" and self.last_accepted_ns is not None:
            gap_ms = (now - self.last_accepted_ns) / 1e6
            if gap_ms > self.WATCHDOG_MS:
                self.state = "minimum_risk"
                self.mode = "minimum_risk"
                print(f"[仲裁器] ⚠ {gap_ms:.0f}ms 未收到新的有效命令"
                      f"（>{self.WATCHDOG_MS}ms），判定链路中断 -> "
                      f"最小风险：自主减速停车")
        if self.state == "minimum_risk":
            self.speed = max(0.0, self.speed - self.DECEL_MPS2 * dt)
            if self.speed <= 0.0:
                self.state = "stopped"
                self.mode = "stopped"
                print("[仲裁器] ✓ 车辆已安全停稳，最小风险完成，等待重新接管")


def telemetry_payload(arb):
    """生成一批假遥测（SignalUpdate，对应 telemetry.proto）。"""
    ts = C.mono_ns()
    return {"signals": [
        {"path": "Vehicle.Speed",
         "value": {"number": round(arb.speed + 0.05 * math.sin(ts / 1e9), 3)},
         "sample_monotonic_ns": ts, "quality": "SIGNAL_QUALITY_GOOD"},
        {"path": "Platform.Autonomy.OperationMode",
         "value": {"text": arb.mode},
         "sample_monotonic_ns": ts, "quality": "SIGNAL_QUALITY_GOOD"},
        {"path": "Platform.Network.LinkA.RttP95",
         "value": {"number": 38},
         "sample_monotonic_ns": ts, "quality": "SIGNAL_QUALITY_GOOD"},
    ]}


def main():
    ap = argparse.ArgumentParser(description="假车模拟器")
    ap.add_argument("--listen", type=int, default=9100, help="监听 UDP 端口")
    ap.add_argument("--cloud", default="127.0.0.1:9200", help="遥测发送目标")
    ap.add_argument("--hz", type=float, default=2.0, help="遥测频率")
    args = ap.parse_args()

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("0.0.0.0", args.listen))
    sock.settimeout(0.05)

    chost, cport = args.cloud.split(":")
    cloud_addr = (chost, int(cport))

    arb = Arbiter()
    seq = 0
    interval = 1.0 / args.hz
    last_send = 0.0

    print(f"[假车] 监听 :{args.listen}，遥测 -> {args.cloud} @ {args.hz}Hz")
    while True:
        arb.tick()
        # 1) 接收并裁决控制命令
        try:
            data, addr = sock.recvfrom(65535)
            env = json.loads(data.decode("utf-8"))
            if env.get("message_type") == "platform.v1.ControlCommand":
                cmd = env["payload"]
                result, detail = arb.check(cmd)
                accepted = result == "CONTROL_RESULT_ACCEPTED"
                ack_payload = {
                    "control_session_id": cmd.get("control_session_id", ""),
                    "command_sequence": cmd.get("command_sequence", 0),
                    "result": result,
                    "detail": detail,
                    "applied_monotonic_ns": C.mono_ns() if accepted else 0,
                }
                ack = C.make_envelope("platform.v1.ControlAck", ack_payload,
                                      env.get("session_id", ""), sequence=0)
                C.send_json(sock, addr, ack)
                tag = "✓ 执行" if accepted else "✗ 拒绝"
                print(f"[仲裁器] {tag} seq={cmd.get('command_sequence')} "
                      f"-> {result}（{detail}）")
                if accepted and "motion" in cmd.get("command", {}):
                    arb.speed = cmd["command"]["motion"].get("target_speed_mps", arb.speed)
        except (TimeoutError, socket.timeout):
            pass
        except OSError:
            pass
        except Exception as e:  # 坏消息不能打挂假车
            print(f"[假车] 消息解析失败：{e}")

        # 2) 周期上报遥测
        now = time.monotonic()
        if now - last_send >= interval:
            last_send = now
            seq += 1
            env = C.make_envelope("platform.v1.SignalUpdate",
                                  telemetry_payload(arb), "sim-session",
                                  sequence=seq, ttl_ms=5000)
            try:
                C.send_json(sock, cloud_addr, env)
            except OSError:
                pass


if __name__ == "__main__":
    main()
