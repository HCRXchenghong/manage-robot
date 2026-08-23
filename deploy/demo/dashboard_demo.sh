#!/bin/zsh
# 第 10 步任务 7：运营大屏端到端演示编排。
#
# 一键拉起：postgres(ra-pg) + nginx(ra-ingress) + authority + gateway(MQTT 上行)
#          + vehicle_side(遥测) + fleet-hub(9800)。
# mosquitto(ra-mqtt) 为前置基础设施（第 5b 步已部署），这里只做检查。
#
# 打开：
#   https://127.0.0.1/       ingress（自签证书，浏览器需信任或 curl -k）
#   http://127.0.0.1:9800/   fleet-hub 直连（含内嵌前端）
# 日志：/tmp/ra-logs/*.log

set -e
cd "$(dirname "$0")/../.."   # Robot-agent 根目录

echo "== 1/4 基础容器检查 =="
if docker ps --filter name=ra-mqtt --format "{{.Names}}" | grep -q ra-mqtt; then
  echo "  mosquitto(ra-mqtt) 在跑"
else
  echo "  !! ra-mqtt 未运行：请先按 deploy/compose/mqtt 的说明启动 mosquitto"
fi
docker compose -f deploy/compose/postgres.yml up -d --wait >/dev/null 2>&1 +  && echo "  postgres(ra-pg) 在跑"

echo "== 2/4 入口层（nginx TLS + 限流 + 安全头） =="
docker compose -f deploy/compose/ingress.yml up -d >/dev/null 2>&1 +  && echo "  ra-ingress 在跑（https://127.0.0.1/）"

echo "== 3/4 车端 + 云端进程 =="
pkill -f "fleet-hub -addr" 2>/dev/null || true
pkill -f authority_service 2>/dev/null || true
pkill -f vehicle_side.py 2>/dev/null || true
pkill -f "gateway.py --uplink" 2>/dev/null || true
sleep 1
mkdir -p /tmp/ra-logs
export DYLD_LIBRARY_PATH=/opt/homebrew/opt/expat/lib   # .venv python 的 expat 坑
PY=.venv/bin/python
nohup $PY server/control-authority/authority_service.py > /tmp/ra-logs/authority.log 2>&1 & disown
sleep 1
nohup $PY vehicle/gateway/gateway.py --uplink mqtt > /tmp/ra-logs/gateway.log 2>&1 & disown
sleep 2
nohup $PY client/simulator/vehicle_side.py > /tmp/ra-logs/vehicle.log 2>&1 & disown
nohup ./server/fleet/fleet-hub -addr :9800 > /tmp/ra-logs/fleet.log 2>&1 & disown
echo "  authority / gateway / vehicle_side / fleet-hub 已后台启动"

echo "== 4/4 验证 =="
sleep 5
curl -s http://127.0.0.1:9800/api/fleet | python3 -c "
import sys, json
d = json.load(sys.stdin)
for v in d['vehicles']:
    print('  车辆', v['vehicle_id'], 'online=', v['online'], 'mode=', v['mode'], 'speed=', v['speed_mps'])
print('  接管:', d['takeover'])
"
echo
echo "演示剧本（人工，边做边看）："
echo "  a) 当前即为「只起基础链路」：在线 1 辆、autonomous、速度曲线走、地图见车"
echo "  b) .venv/bin/python client/simulator/takeover_demo.py   -> 接管面板倒计时"
echo "  c) .venv/bin/python client/simulator/minimum_risk_demo.py -> critical + 曲线归零"
echo "  d) pkill -f vehicle_side.py                              -> 6s 内离线事件"
echo "  e) 大屏地图：2D/3D 切换、缩放旋转、配置点云加载自定义、多车高亮"
echo
echo "打开 https://127.0.0.1/ （或 http://127.0.0.1:9800/）"
echo "日志：/tmp/ra-logs/*.log"
