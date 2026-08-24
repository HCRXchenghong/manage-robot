# web/ops-dashboard —— 运营大屏前端（第 10 步）

React 19 + TypeScript + Vite；three + @react-three/fiber 做 3D 点云；
xterm 做远程终端。运行时依赖严格白名单：
react / react-dom / three / @react-three/fiber / @xterm/* / leaflet / vite / typescript。
无 CDN、无外链运行时资源；build 产物被 Go embed（server/fleet），单二进制交付。

## 运行

    npm install
    npm run dev        # :5173，代理 /api、/ws -> 127.0.0.1:9800（ws: true）
    npm run build      # 产物输出到 ../../server/fleet/web/dist
    npm run typecheck

## 结构

- src/App.tsx —— 侧栏 7 分类路由（总览大屏 / 车辆列表 / 车辆详情 /
  远程驾驶·接管 / 视频监控 / 告警与事件 / 远程终端）+ 底部 admin
- src/api.ts —— REST + WebSocket（指数退避重连，重连后先拉全量对齐）；
  后端不可达自动降级 src/mock.ts 演示数据，10s 探测恢复
- src/components/LidarView.tsx —— 激光雷达点云地图（重点）
- src/components/ —— TopBar / StatCards / VehicleTable / EventFeed /
  TakeoverPanel / TerminalPanel / VideoPanel / Sparkline
- src/pages/ —— 七个页面

## 激光雷达点云地图

1. 2D 鸟瞰 / 3D 轨道一键切换；拖拽旋转(3D)/平移(2D)、滚轮缩放、点大小可调；
2. 多车同屏：每车点云按 pose 放入统一世界系，颜色按状态
   （绿=行驶、黄=空闲、灰=离线、红=告警）；静态基础设施为暗蓝；
3. 点车辆锥体或表格行 -> 高亮该车 + 可勾选视角跟随；
4. 「配置点云」本地加载 JSON/CSV（每行 x,y,z[,intensity]），
   替换静态场景、保留车辆；可一键恢复默认场景；
   自定义地图加载后相机按包围盒自动取景，静态点按高度渐变着色；
5. 超 10 万点自动降采样；THREE.Points + BufferGeometry 一次上传。

## 真实激光雷达地图（PCD）

前端只收 JSON/CSV 文本格式；二进制点云（如 PCD）先用随仓工具转换：

    python3 deploy/demo/pcd_to_csv.py <你的.pcd文件> /tmp/map.csv

再「配置点云」加载生成的 CSV 即可（相机自动取景）。
效果见 docs/screenshots/real-ndt-map-2d.png / real-ndt-map-3d.png。

## 自动 3D→2D（BEV 网格）

2D 鸟瞰默认显示由 3D 点云自动投影的占据网格（高度切片做法，对齐 ROS octomap_server）；
格宽 0.1/0.2/0.5m 可选，可勾回原始点云；「导出 2D 地图」下载 PNG（人看）+
PGM + YAML（ROS map_server 格式，车端导航栈可直接用）。实施计划见 docs/plan-map-engine.md。

## 控制按钮

## 远程接管驾驶舱

左上视频墙（1/2/4 画面切换，含前/后/左/右机位 + 融合鸟瞰 BEV + 360° 环视；
阶段 1 模拟画面，阶段 2 换 WebRTC 真流）；左中点云；底部地图走天地图 WMTS
（配置 tk 后启用，未配置降级 OSM 演示），车辆按遥测坐标落图留轨迹；
右上接管卡（状态/驾驶员/租约/fencing）+ 底盘卡（轮速/转向/电量/底盘类型：
阿克曼 / 四轮四转 / 差速AGV）；最右上切换控制车辆。

申请接管 / 续租 / 交还控制权 走 fleet-hub -> control-authority（租约 + fencing）；
紧急停车为红色二次确认（3 秒内再点执行），指令被车端接受即刻出 critical 事件。

## 阶段划分

- 阶段 1：视频为模拟画面；终端为 hub 模拟回显；登录 /login 占位；
- 阶段 2：WebRTC 双路视频、workspace-agent 真 PTY、OIDC 登录（nginx auth_request）。
