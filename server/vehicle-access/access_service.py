#!/usr/bin/env python3
# vehicle-access 云端接入服务骨架（第 5b 步）
#
# 职责（架构文档 §4.2）：
#   - 订阅 vehicle/#：注册、遥测、状态
#   - 注册时核对白名单（生产：数据库 + 兼容矩阵，见介绍 §6.4）
#   - 在线状态维护（生产：Redis；这里用内存演示）
#
# 车辆身份由 Broker 的 mTLS 保证（证书即身份，无账号密码）。
#
# 用法：
#   .venv/bin/python server/vehicle-access/access_service.py

import argparse
import json

import paho.mqtt.client as mqtt


class AccessService:
    def __init__(self, args):
        self.known = {"sim-veh-001"}   # 演示白名单；生产为车辆数据库
        self.online = {}               # vid -> 注册信息
        self.telemetry_count = {}
        self.heartbeat_count = {}

        self.client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2,
                                  client_id="vehicle-access",
                                  protocol=mqtt.MQTTv5)
        self.client.tls_set(ca_certs=args.ca, certfile=args.cert,
                            keyfile=args.key)
        self.client.on_connect = self.on_connect
        self.client.on_message = self.on_message
        self.client.connect(args.host, args.port, keepalive=15)

    def on_connect(self, client, userdata, flags, reason_code, properties=None):
        client.subscribe("vehicle/#", qos=1)
        print(f"[vehicle-access] 已连接 Broker（{reason_code}），订阅 vehicle/#")

    def on_message(self, client, userdata, msg):
        try:
            env = json.loads(msg.payload.decode("utf-8"))
        except (ValueError, UnicodeDecodeError):
            return
        parts = msg.topic.split("/")
        vid = parts[1] if len(parts) > 1 else "?"
        kind = parts[2] if len(parts) > 2 else "?"

        if kind == "register":
            payload = env.get("payload", {})
            accepted = vid in self.known
            self.online[vid] = payload
            print(f"[vehicle-access] 注册请求 vid={vid} "
                  f"栈={payload.get('stack')} 映射={payload.get('topic_mapping_version')} "
                  f"-> {'接受（进入在线监控）' if accepted else '拒绝：不在白名单'}")
        elif kind == "telemetry":
            n = len(env.get("payload", {}).get("signals", []))
            c = self.telemetry_count.get(vid, 0) + 1
            self.telemetry_count[vid] = c
            if vid not in self.online:
                print(f"[vehicle-access] 警告：未注册车辆 {vid} 上报遥测，仅记录不控制")
            if c == 1 or c % 10 == 0:
                print(f"[vehicle-access] 遥测 vid={vid} 第 {c} 批（{n} 个信号，"
                      f"seq={env.get('sequence')}）")
        elif kind == "status":
            c = self.heartbeat_count.get(vid, 0) + 1
            self.heartbeat_count[vid] = c
            if c == 1 or c % 10 == 0:
                print(f"[vehicle-access] 心跳 vid={vid} 第 {c} 次")

    def run(self):
        self.client.loop_forever()


def main():
    ap = argparse.ArgumentParser(description="vehicle-access 接入服务（演示版）")
    ap.add_argument("--host", default="localhost")
    ap.add_argument("--port", type=int, default=8883)
    ap.add_argument("--ca", default="deploy/pki/dev/ca.crt")
    ap.add_argument("--cert", default="deploy/pki/dev/access.crt")
    ap.add_argument("--key", default="deploy/pki/dev/access.key")
    args = ap.parse_args()
    AccessService(args).run()


if __name__ == "__main__":
    main()
