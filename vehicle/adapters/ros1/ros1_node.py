#!/usr/bin/env python3
# ROS 1 Adapter 车端真身（rospy 版）——只在小车上运行，需要 ROS 1 Noetic 环境
#
# 结构：
#   订阅：/vehicle_status（can_msgs/VehicleStatus）、/battery（can_msgs/Battery）
#   发布：/ecu（can_msgs/Ecu）
#   翻译：全部复用 adapter.py 的翻译核心，本文件只做“接线”。
#   与 Gateway 通信：第 5 步接入 Unix Domain Socket + Protobuf；
#                    当前版本先把平台消息打印出来，便于在车上先验证订阅链路。
#
# 在车上运行：
#   source /opt/ros/noetic/setup.bash
#   source <小车工作空间>/devel/setup.bash
#   python3 ros1_node.py

try:
    import rospy
    from can_msgs.msg import Ecu  # noqa: F401（下行发布用，第 5 步启用）
    from can_msgs.msg import VehicleStatus, Battery  # 实际消息名以车上包为准
except ImportError as e:
    raise SystemExit("缺少 ROS 1 环境或 can_msgs：请先 source ROS 1 与小车工作空间。 " + str(e))

import adapter


def emit(signals):
    """第 5 步：这里改为写入 Gateway 的 Unix Domain Socket（Protobuf）。"""
    for s in signals:
        print(f"[ros1-adapter] {s['path']} = {s['value']}")


def on_vehicle_status(msg):
    d = {"cur_speed": msg.cur_speed,
         "cur_steer": msg.cur_steer,
         "shift_level": msg.shift_level,
         "is_autodrive": msg.is_autodrive}
    emit(adapter.translate_vehicle_status(d, adapter.now_ns()))


def on_battery(msg):
    d = {"capacity": msg.capacity, "voltage": msg.voltage}
    emit(adapter.translate_battery(d, adapter.now_ns()))


def main():
    rospy.init_node("platform_ros1_adapter")
    rospy.Subscriber("/vehicle_status", VehicleStatus, on_vehicle_status)
    rospy.Subscriber("/battery", Battery, on_battery)
    # TODO(第 5 步)：订阅 Gateway 下发的 ControlCommand，
    #   调 adapter.control_to_ecu() 翻译后发布 /ecu，
    #   并先经车端安全仲裁器校验。
    rospy.spin()


if __name__ == "__main__":
    main()
