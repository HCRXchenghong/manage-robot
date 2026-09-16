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
  后端不可达时只显示 unavailable/degraded 状态，不生成车辆、媒体或地图数据
- src/components/LidarView.tsx —— 激光雷达点云地图（重点）
- src/components/ —— TopBar / StatCards / VehicleTable / EventFeed /
  TakeoverPanel / TerminalPanel / VideoPanel / Sparkline
- src/pages/ —— 七个页面

## 远程终端与 RViz 可视化（新标签页）

- 远程终端页顶部可设置远程车辆（分组下拉 + 在线/模式徽标）；「新标签页打开车端终端」
  打开独立页 `#/termwin/:id`（无侧栏，进入自动连接，等保会话校验）。
- 终端输入可视化命令（rviz / rviz2 / webviz / foxglove）：回车手势内同步新开
  `#/rviz?vehicle_id=…` RViz 风格页（Displays 树 / 3D 视口 / Views / Time 状态栏；
  点云 /api/pointcloud、位姿 /ws/fleet、Path /api/nav/routes）；
  hub 同时下发 `{"type":"app"}` 事件，车端真实启动可视化时可据此补开标签；
  被浏览器拦截时面板下方出现兜底链接条（对齐架构规范「由用户点击打开」）。
- `/ws/terminal` 只连接受授权的 workspace-agent；车辆未注册或通道不可用时拒绝连接，
  不提供 hub 回显替代。

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

    python3 map-engine/tools/pcd_to_csv.py <你的.pcd文件> /tmp/map.csv

再「配置点云」加载生成的 CSV 即可（相机自动取景）。
效果见 docs/screenshots/real-ndt-map-2d.png / real-ndt-map-3d.png。

## 自动 3D→2D（BEV 网格）

2D 鸟瞰默认显示由 3D 点云自动投影的占据网格（高度切片做法，对齐 ROS octomap_server）；
格宽 0.1/0.2/0.5m 可选，可勾回原始点云；「导出 2D 地图」下载 PNG（人看）+
PGM + YAML（ROS map_server 格式，车端导航栈可直接用）。实施计划见 docs/plan-map-engine.md。

## 控制按钮

## 远程接管驾驶舱

左上视频墙（1/2/4 画面切换，含前/后/左/右机位 + 融合鸟瞰 BEV + 360° 环视；
只显示真实 WebRTC 媒体会话）；左中点云；底部地图走已配置的天地图 WMTS，
未配置或不可用时显示明确不可用状态；车辆按真实遥测坐标落图留轨迹；
右上接管卡（状态/驾驶员/租约/fencing）+ 底盘卡（轮速/转向/电量/底盘类型：
阿克曼 / 四轮四转 / 差速AGV）；最右上切换控制车辆。

申请接管 / 续租 / 交还控制权 走 fleet-hub -> control-authority（租约 + fencing）；
紧急停车为红色二次确认（3 秒内再点执行），指令被车端接受即刻出 critical 事件。

## 运行边界

视频、终端、地图和车辆状态都必须来自已授权的真实服务。依赖不可用时页面保留
操作上下文并显示原因，不以固定图像、回显文本、内置车辆或浏览器本地状态替代。
