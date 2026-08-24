# 点云地图引擎（map-engine）实施计划 · v1

> 对应架构文档中的 map-engine 模块（转换、分块、版本、发布）。
> 程序员按阶段实施，Codex 审核。原则：不闭门造车，每层都采用 GitHub 生态的成熟做法。

## 1. 调研结论：成熟项目怎么处理点云地图

| 层 | 代表项目（stars / 最近更新） | 成熟做法 |
|---|---|---|
| 服务端转换/分块 | PDAL/PDAL 1.4k · hobuinc/untwine · potree/PotreeConverter 815 | LAS/LAZ/PCD 转成多级分辨率八叉树（Entwine/COPC/Potree 格式） |
| 格式标准 | COPC（copc.io）· OGC 3D Tiles（pnts） | 单文件 + 八叉树索引，HTTP Range 按需取块 |
| 浏览器查看 | potree/potree 5.6k · CesiumGS/cesium 15.6k · verma/plasio（plas.io）537 · connormanning/copc.js | Potree/Cesium 独立成套；copc.js 可直接嵌进 React+three 栈 |
| 机器人可视化 | foxglove（sdk 299 / ros-bridge 287 / 开源末版 149） | ROS1/ROS2 实时 PointCloud2 的事实标准 |
| 浏览器编辑/标注 | cvat-ai/cvat 16.6k · naurril/SUSTechPOINTS 1.1k · xtreme1-io/xtreme1 1.2k · walzimmer/3d-bat 818 | 多边形圈选 + 拉伸体 + 分类/_bbox |
| 运营侧编辑 | CyberAgentAILab/nav2-keepout-zone-map-creator | 不改原始点云，在 2D 地图上画矢量区域（禁行/缓行） |
| 3D→2D | OctoMap/octomap 2.4k · OctoMap/octomap_mapping 442 · ANYbotics/grid_map 3.2k | 高度切片/射线投影 → 占据网格，导出 ROS 地图 |

一句话共识：**服务端转八叉树、浏览器按 Range 流式加载；编辑分「点级标注」与「矢量区域」两条路；3D→2D 用高度切片投影、导出 ROS 地图格式。**

## 2. 我们的选型

- 保留 React + three 现有栈；CSV/JSON 轻量通道保留为演示通道（已实现）。
- 阶段 3 引入 COPC：服务端 untwine/PDAL 转换作业，前端 copc.js 按需流式加载；REST/WS 接口不变。
- 编辑 = SUSTechPOINTS 式多边形拉伸体点删除/分类（存差异、不动原始数据）+ nav2 式矢量区域 + 版本化（PG）。
- 3D→2D = BEV 高度切片投影（octomap_server 做法，bev.ts 已实现），导出 PNG+PGM+YAML（map_server 兼容）。

## 3. 阶段任务与验收

### 阶段 1（已完成）自动 3D→2D + 导出
- bev.ts：地面估计（直方图求模）+ 高度切片 [g+0.15, g+1.4] 投影；格宽 0.1/0.2/0.5 可选，超上限自动加倍
- LidarView：2D 默认显示网格（可勾回点云）；3D 叠加网格地贴；「导出 2D 地图」下载 PNG/PGM/YAML
- 验收：加载真实 PCD CSV → 2D 网格自动生成且与点云形状一致；导出三件套格式符合 map_server 约定

### 阶段 2 浏览器编辑地图
- 2.1 多边形套索（2D 视图点选、双击/点回起点闭合）→ 圈内擦除/恢复 ✅（编辑独立页：地图中心 → 卡片「编辑」）
- 2.2 撤销/重做；「保存为新版本」✅（/api/maps/{id}/edit：版本号、作者、时间戳、操作记录全留存；原始只读）
  - 地图中心：每车一卡片，车端 15s 实时上报（sha256 去重），服务器版本化存储（车上原件不动）✅
  - 一行命令 3D→2D：deploy/demo/map_convert.py（PCD/CSV → PNG/PGM/YAML），
    fleet-hub POST /api/maps/{id}/convert 已封装 ✅
- 2.3 矢量区域：禁行/缓行多边形绘制，2D/3D 叠加渲染，与接管页 fencing 提示联动
- 验收（已过）：车端上报真实地图 → 一键 3D→2D → 套索圈删一块 → 保存为 v3 →
  版本链 v1(原始)/v2(转换)/v3(编辑) 完整可追溯；篡改/重放调用被拒

### 阶段 3 大地图流式（COPC）
- 3.1 deploy 增加转换作业（Docker：untwine/PDAL）PCD/LAS → COPC；fleet-hub 增加 /api/maps/:id/copc（Range 透传）
- 3.2 前端引入 copc.js 按需加载 LOD；CSV 通道保留
- 3.3 车端实时点云 topic（ROS gateway 阶段 2）同视图叠加渲染
- 验收：1 亿点 LAZ 首屏 < 3s、缩放流畅；实时点云与底图对齐

## 4. 风险与边界
- COPC 转换依赖 PDAL/untwine 二进制 → 用 Docker 镜像交付，不污染宿主机
- 编辑差异存「索引 bitmap + 区域 GeoJSON」，原始点云永远只读
- 2D 网格目前是「占据/未知」两态（无射线自由判定）；导航若要 free 态，阶段 3 升 octomap 射线版
