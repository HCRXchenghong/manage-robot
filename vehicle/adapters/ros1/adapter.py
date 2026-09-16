#!/usr/bin/env python3
# ROS 1 存量车 Adapter —— 翻译核心
#
# 职责（架构文档 §4 / §5.1）：
#   上行：ROS 1 原生消息  -> 平台 VSS 信号（SignalUpdate）
#   下行：平台 ControlCommand -> ROS 1 ECU 消息
#
# 本文件不依赖 ROS；生产节点必须显式提供版本化实车标定文件。
# 仅可用显式的 --self-test 运行确定性单元自检；无参数不会启动任何流程。
# 车上的 rospy 真身在 ros1_node.py，翻译逻辑全部复用这里。
#
# 映射依据：
#   - 车端真实字段：车端代码/src/hardware/can_bridge_ros1/src/can_msgs
#   - 平台信号定义：protocols/vss/（VSS + Platform.* overlay）

import math
import json
import sys
import time

# ---------- 单位换算 ----------
KMH_TO_MPS = 1000.0 / 3600.0   # km/h -> m/s
DEG_TO_RAD = math.pi / 180.0   # 度 -> 弧度

# ---------- 安全边界（与 Safety Arbiter 一致的低速 ODD） ----------
class Calibration:
    """已审核的车型标定；所有执行器语义必须来自外部签名/受控配置。

    这里不放任何默认车辆参数。缺少标定时，适配器只能提供上行信号，
    任何下行控制和最小风险动作都会被拒绝。
    """

    def __init__(self, raw):
        required = {
            "version", "max_speed_mps", "max_steer_deg", "brake_threshold",
            "gear_to_platform", "gear_to_vehicle", "park_shift",
        }
        if set(raw) != required:
            raise ValueError("标定文件必须且只能包含规定字段")
        if not isinstance(raw["version"], str) or not raw["version"].strip():
            raise ValueError("标定 version 必须是非空字符串")
        self.version = raw["version"]
        self.max_speed_mps = _positive(raw["max_speed_mps"], "max_speed_mps")
        self.max_steer_deg = _positive(raw["max_steer_deg"], "max_steer_deg")
        self.brake_threshold = _fraction(raw["brake_threshold"], "brake_threshold")
        if not isinstance(raw["gear_to_platform"], dict) or not raw["gear_to_platform"]:
            raise ValueError("gear_to_platform 必须是非空对象")
        if not isinstance(raw["gear_to_vehicle"], dict) or not raw["gear_to_vehicle"]:
            raise ValueError("gear_to_vehicle 必须是非空对象")
        self.gear_to_platform = {int(k): _nonempty(v, "gear_to_platform")
                                 for k, v in raw["gear_to_platform"].items()}
        self.gear_to_vehicle = {str(k): _integer(v, "gear_to_vehicle")
                                for k, v in raw["gear_to_vehicle"].items()}
        self.park_shift = _integer(raw["park_shift"], "park_shift")


def _nonempty(value, field):
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{field} 的值必须是非空字符串")
    return value


def _integer(value, field):
    if isinstance(value, bool) or not isinstance(value, int):
        raise ValueError(f"{field} 的值必须是整数")
    return value


def _positive(value, field):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or value <= 0:
        raise ValueError(f"{field} 必须是正数")
    return float(value)


def _fraction(value, field):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not 0 <= value <= 1:
        raise ValueError(f"{field} 必须位于 0..1")
    return float(value)


def load_calibration(path):
    """加载严格的 JSON 标定包；生产启动时由受控安装流程提供。"""
    with open(path, "r", encoding="utf-8") as f:
        raw = json.load(f)
    if not isinstance(raw, dict):
        raise ValueError("标定文件根节点必须是对象")
    return Calibration(raw)


def clamp(v, lo, hi):
    return max(lo, min(hi, v))


def now_ns():
    return time.monotonic_ns()


def signal(path, value, ts_ns, quality="SIGNAL_QUALITY_GOOD"):
    """构造一条平台信号（对应 telemetry.proto 的 Signal）。"""
    return {"path": path, "value": value,
            "sample_monotonic_ns": ts_ns, "quality": quality}


# ================= 上行翻译：ROS 1 -> 平台 =================

def translate_vehicle_status(msg, ts_ns, calibration):
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
                          {"text": calibration.gear_to_platform.get(msg["shift_level"],
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

def control_to_ecu(cmd_payload, calibration):
    """ControlCommand payload -> can_msgs/ecu 字典。

    注意：只做“翻译 + 限幅”，是否执行由车端安全仲裁器裁决。
    """
    ecu = {"motor": 0.0, "steer": 0.0, "brake": False, "shift": 0}
    which = cmd_payload.get("command", {})

    if "motion" in which:
        speed = clamp(which["motion"].get("target_speed_mps", 0.0),
                      0.0, calibration.max_speed_mps)
        # 标定文件冻结 ecu.motor 的目标速度语义为 km/h。
        ecu["motor"] = round(speed / KMH_TO_MPS, 3)

    if "direct" in which:
        d = which["direct"]
        steer_deg = d.get("steering_angle_target_rad", 0.0) / DEG_TO_RAD
        ecu["steer"] = round(clamp(steer_deg, -calibration.max_steer_deg,
                                    calibration.max_steer_deg), 3)
        ecu["brake"] = d.get("brake_fraction", 0.0) >= calibration.brake_threshold
        ecu["shift"] = calibration.gear_to_vehicle.get(d.get("gear", "GEAR_UNSPECIFIED"),
                                                         calibration.park_shift)

    return ecu


def minimal_risk_ecu(calibration):
    """生成标定定义的最小风险 ECU 动作（零驱动、制动、驻车）。"""
    return {"motor": 0.0, "steer": 0.0, "brake": True, "shift": calibration.park_shift}


# ================= CI 翻译自检 =================

def _approx(a, b, eps=1e-6):
    return abs(a - b) <= eps


def run_self_test():
    """仅用于开发/CI 的确定性翻译自检，不属于任何生产运行路径。"""
    fails = 0
    ts = now_ns()
    calibration = Calibration({
        "version": "test-ros1-v1", "max_speed_mps": 5.0,
        "max_steer_deg": 30.0, "brake_threshold": 0.5,
        "gear_to_platform": {"0": "GEAR_UNSPECIFIED", "1": "GEAR_DRIVE",
                              "2": "GEAR_NEUTRAL", "3": "GEAR_REVERSE", "4": "GEAR_PARK"},
        "gear_to_vehicle": {"GEAR_UNSPECIFIED": 0, "GEAR_DRIVE": 1,
                             "GEAR_NEUTRAL": 2, "GEAR_REVERSE": 3, "GEAR_PARK": 4},
        "park_shift": 4,
    })

    def check(name, ok, detail):
        nonlocal fails
        print(f"  [{'PASS' if ok else 'FAIL'}] {name}  {detail}")
        if not ok:
            fails += 1

    print("=== 上行：ROS 1 -> 平台（VSS） ===")
    vs = {"cur_speed": 7.2, "cur_steer": 90.0, "shift_level": 1, "is_autodrive": True}
    sigs = {s["path"]: s["value"] for s in translate_vehicle_status(vs, ts, calibration)}
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
    ecu = control_to_ecu(cmd_motion, calibration)
    check("TargetMotion 2.0 m/s -> ecu.motor",
          _approx(ecu["motor"], 7.2, 1e-3),
          f"= {ecu['motor']}（km/h，来自测试标定）")

    cmd_over = {"command": {"motion": {"target_speed_mps": 20.0}}}
    ecu2 = control_to_ecu(cmd_over, calibration)
    check("超速 20 m/s -> 限幅到 5 m/s",
          _approx(ecu2["motor"], 18.0, 1e-3),
          f"= {ecu2['motor']}（= 5.0 m/s）")

    cmd_direct = {"command": {"direct": {
        "steering_angle_target_rad": 3.14, "brake_fraction": 0.9, "gear": "GEAR_DRIVE"}}}
    ecu3 = control_to_ecu(cmd_direct, calibration)
    check("DirectActuation 转向 3.14 rad -> 限幅 ±30°",
          _approx(ecu3["steer"], 30.0, 1e-3),
          f"= {ecu3['steer']}°")
    check("DirectActuation brake 0.9 -> ecu.brake",
          ecu3["brake"] is True, "= True（急停）")
    check("DirectActuation gear GEAR_DRIVE -> ecu.shift",
          ecu3["shift"] == 1, f"= {ecu3['shift']}（来自测试标定）")

    print(f"\n结果：{'全部通过 ✓' if fails == 0 else f'{fails} 项失败 ✗'}")
    return 0 if fails == 0 else 1


if __name__ == "__main__":
    if sys.argv[1:] == ["--self-test"]:
        sys.exit(run_self_test())
    print("拒绝启动：适配器不提供无参数运行路径。生产节点请运行 ros1_node.py 并提供 --calibration。",
          file=sys.stderr)
    sys.exit(2)
