#!/usr/bin/env python3
# ROS 1 存量车最小 Adapter —— 翻译核心（仿真版）
#
# 职责（架构文档 §4 / §5.1）：
#   上行：ROS 1 原生消息  -> 平台 VSS 信号（SignalUpdate）
#   下行：平台 ControlCommand -> ROS 1 ECU 消息
#
# 本文件不依赖 ROS，可单独运行验证翻译逻辑：
#   python3 adapter.py --demo
# 车上的 rospy 真身在 ros1_node.py，翻译逻辑全部复用这里。
#
# 映射依据：
#   - 车端真实字段：车端代码/src/hardware/can_bridge_ros1/src/can_msgs
#   - 平台信号定义：protocols/vss/（VSS + Platform.* overlay）

import math
import sys
import time

# ---------- 单位换算 ----------
KMH_TO_MPS = 1000.0 / 3600.0   # km/h -> m/s
DEG_TO_RAD = math.pi / 180.0   # 度 -> 弧度

# ---------- 安全边界（与模拟器仲裁器一致的低速 ODD） ----------
MAX_SPEED_MPS = 5.0     # 第一阶段限速
MAX_STEER_DEG = 30.0    # 方向盘限幅（占位值，待实车标定）

# ---------- 枚举映射（未标定，占位，须实车确认） ----------
GEAR_TO_PLATFORM = {0: "GEAR_UNSPECIFIED", 1: "GEAR_DRIVE",
                    2: "GEAR_NEUTRAL", 3: "GEAR_REVERSE", 4: "GEAR_PARK"}
GEAR_TO_VEHICLE = {v: k for k, v in GEAR_TO_PLATFORM.items()}


def clamp(v, lo, hi):
    return max(lo, min(hi, v))


def now_ns():
    return time.monotonic_ns()


def signal(path, value, ts_ns, quality="SIGNAL_QUALITY_GOOD"):
    """构造一条平台信号（对应 telemetry.proto 的 Signal）。"""
    return {"path": path, "value": value,
            "sample_monotonic_ns": ts_ns, "quality": quality}


# ================= 上行翻译：ROS 1 -> 平台 =================

def translate_vehicle_status(msg, ts_ns):
    """can_msgs/vehicle_status -> VSS 信号列表。"""
    out = []
    if "cur_speed" in msg:  # km/h -> m/s
        out.append(signal("Vehicle.Speed",
                          {"number": round(msg["cur_speed"] * KMH_TO_MPS, 6)}, ts_ns))
    if "cur_steer" in msg:  # ° -> rad
        out.append(signal("Vehicle.Chassis.SteeringWheel.Angle",
                          {"number": round(msg["cur_steer"] * DEG_TO_RAD, 6)}, ts_ns))
    if "shift_level" in msg:
        out.append(signal("Vehicle.Powertrain.Transmission.CurrentGear",
                          {"text": GEAR_TO_PLATFORM.get(msg["shift_level"],
                                                        "GEAR_UNSPECIFIED")}, ts_ns))
    if "is_autodrive" in msg:
        out.append(signal("Platform.Autonomy.OperationMode",
                          {"text": "autonomous" if msg["is_autodrive"] else "manual"},
                          ts_ns))
    return out


def translate_battery(msg, ts_ns):
    """can_msgs/battery -> VSS 信号列表。"""
    out = []
    if "capacity" in msg:  # % -> 0..1
        out.append(signal("Vehicle.Powertrain.TractionBattery.StateOfCharge",
                          {"number": clamp(msg["capacity"] / 100.0, 0.0, 1.0)}, ts_ns))
    if "voltage" in msg:
        out.append(signal("Vehicle.Powertrain.TractionBattery.Voltage",
                          {"number": msg["voltage"]}, ts_ns))
    return out


TRANSLATORS = {
    "vehicle_status": translate_vehicle_status,
    "battery": translate_battery,
}


# ================= 下行翻译：平台 -> ROS 1 =================

def control_to_ecu(cmd_payload):
    """ControlCommand payload -> can_msgs/ecu 字典。

    注意：只做“翻译 + 限幅”，是否执行由车端安全仲裁器裁决。
    """
    ecu = {"motor": 0.0, "steer": 0.0, "brake": False, "shift": 0}
    which = cmd_payload.get("command", {})

    if "motion" in which:
        speed = clamp(which["motion"].get("target_speed_mps", 0.0),
                      0.0, MAX_SPEED_MPS)
        # TODO(标定)：ecu.motor 注释为“目标速度”，单位/语义须实车确认后冻结。
        ecu["motor"] = round(speed / KMH_TO_MPS, 3)

    if "direct" in which:
        d = which["direct"]
        steer_deg = d.get("steering_angle_target_rad", 0.0) / DEG_TO_RAD
        ecu["steer"] = round(clamp(steer_deg, -MAX_STEER_DEG, MAX_STEER_DEG), 3)
        ecu["brake"] = d.get("brake_fraction", 0.0) > 0.5  # TODO(标定)：急停阈值
        ecu["shift"] = GEAR_TO_VEHICLE.get(d.get("gear", "GEAR_UNSPECIFIED"), 0)

    return ecu


# ================= 自检演示 =================

def _approx(a, b, eps=1e-6):
    return abs(a - b) <= eps


def run_demo():
    """用真实字段样例验证翻译正确性，全部通过才算合格。"""
    fails = 0
    ts = now_ns()

    def check(name, ok, detail):
        nonlocal fails
        print(f"  [{'PASS' if ok else 'FAIL'}] {name}  {detail}")
        if not ok:
            fails += 1

    print("=== 上行：ROS 1 -> 平台（VSS） ===")
    vs = {"cur_speed": 7.2, "cur_steer": 90.0, "shift_level": 1, "is_autodrive": True}
    sigs = {s["path"]: s["value"] for s in translate_vehicle_status(vs, ts)}
    check("vehicle_status.cur_speed 7.2 km/h -> Vehicle.Speed",
          _approx(sigs["Vehicle.Speed"]["number"], 2.0),
          f"7.2 km/h = {sigs['Vehicle.Speed']['number']} m/s（期望 2.0）")
    check("vehicle_status.cur_steer 90° -> 方向盘转角",
          _approx(sigs["Vehicle.Chassis.SteeringWheel.Angle"]["number"], math.pi / 2),
          f"90° = {sigs['Vehicle.Chassis.SteeringWheel.Angle']['number']:.6f} rad（期望 π/2）")
    check("vehicle_status.shift_level 1 -> 挡位",
          sigs["Vehicle.Powertrain.Transmission.CurrentGear"]["text"] == "GEAR_DRIVE",
          f"= {sigs['Vehicle.Powertrain.Transmission.CurrentGear']['text']}")
    check("vehicle_status.is_autodrive True -> 运行模式",
          sigs["Platform.Autonomy.OperationMode"]["text"] == "autonomous",
          "= autonomous")

    bat = {"capacity": 85.0, "voltage": 52.3}
    bsigs = {s["path"]: s["value"] for s in translate_battery(bat, ts)}
    check("battery.capacity 85% -> SOC",
          _approx(bsigs["Vehicle.Powertrain.TractionBattery.StateOfCharge"]["number"], 0.85),
          f"= {bsigs['Vehicle.Powertrain.TractionBattery.StateOfCharge']['number']}")
    check("battery.voltage 52.3V -> 电压",
          _approx(bsigs["Vehicle.Powertrain.TractionBattery.Voltage"]["number"], 52.3),
          "= 52.3 V")

    print("=== 下行：平台 -> ROS 1（ECU） ===")
    cmd_motion = {"command": {"motion": {"target_speed_mps": 2.0}}}
    ecu = control_to_ecu(cmd_motion)
    check("TargetMotion 2.0 m/s -> ecu.motor",
          _approx(ecu["motor"], 7.2, 1e-3),
          f"= {ecu['motor']}（km/h 语义，待实车标定确认）")

    cmd_over = {"command": {"motion": {"target_speed_mps": 20.0}}}
    ecu2 = control_to_ecu(cmd_over)
    check("超速 20 m/s -> 限幅到 5 m/s",
          _approx(ecu2["motor"], 18.0, 1e-3),
          f"= {ecu2['motor']}（= 5.0 m/s）")

    cmd_direct = {"command": {"direct": {
        "steering_angle_target_rad": 3.14, "brake_fraction": 0.9, "gear": "GEAR_DRIVE"}}}
    ecu3 = control_to_ecu(cmd_direct)
    check("DirectActuation 转向 3.14 rad -> 限幅 ±30°",
          _approx(ecu3["steer"], 30.0, 1e-3),
          f"= {ecu3['steer']}°")
    check("DirectActuation brake 0.9 -> ecu.brake",
          ecu3["brake"] is True, "= True（急停）")
    check("DirectActuation gear GEAR_DRIVE -> ecu.shift",
          ecu3["shift"] == 1, f"= {ecu3['shift']}（映射表待标定）")

    print(f"\n结果：{'全部通过 ✓' if fails == 0 else f'{fails} 项失败 ✗'}")
    return 0 if fails == 0 else 1


if __name__ == "__main__":
    sys.exit(run_demo() if "--demo" in sys.argv else run_demo())
