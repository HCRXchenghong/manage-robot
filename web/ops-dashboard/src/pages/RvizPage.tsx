// Web visualisation is not fabricated by the dashboard. It will be enabled
// when a vehicle exposes an authenticated Webviz/Foxglove endpoint through the
// media/workspace control plane.
export default function RvizPage({ vehicleId, app }: { vehicleId: string; app: string }) {
  return (
    <div className="rviz-root">
      <div className="rvz-view" style={{ display: "grid", placeItems: "center", textAlign: "center", padding: 24 }}>
        <div>
          <h2 style={{ marginBottom: 10 }}>车端可视化未注册</h2>
          <div className="muted" style={{ lineHeight: 1.7 }}>
            {vehicleId || "未指定车辆"}{app ? " · " + app : ""}<br />
            请由真实车端 Agent 注册经过身份验证的可视化端点；平台不会生成 RViz、路径或点云画面。
          </div>
          <button className="btn small" style={{ marginTop: 16 }} onClick={() => window.history.back()}>← 返回</button>
        </div>
      </div>
    </div>
  );
}
