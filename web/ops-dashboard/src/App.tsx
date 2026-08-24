import { useState } from "react";
import { useFleet } from "./api";
import TopBar from "./components/TopBar";
import Overview from "./pages/Overview";
import Vehicles from "./pages/Vehicles";
import VehicleDetail from "./pages/VehicleDetail";
import Drive from "./pages/Drive";
import Video from "./pages/Video";
import Alerts from "./pages/Alerts";
import Terminal from "./pages/Terminal";

export type PageId =
  | "overview"
  | "vehicles"
  | "detail"
  | "drive"
  | "video"
  | "alerts"
  | "terminal";

const NAV: { id: PageId; icon: string; label: string }[] = [
  { id: "overview", icon: "▦", label: "总览大屏" },
  { id: "vehicles", icon: "▤", label: "车辆列表" },
  { id: "drive", icon: "✥", label: "远程接管" },
  { id: "video", icon: "▶", label: "视频监控" },
  { id: "alerts", icon: "⚠", label: "告警与事件" },
  { id: "terminal", icon: ">_", label: "远程终端" },
];

function LoginPlaceholder() {
  return (
    <div style={{ height: "100%", display: "flex", alignItems: "center", justifyContent: "center" }}>
      <div className="panel" style={{ width: 380, padding: 24 }}>
        <div className="brand-name" style={{ marginBottom: 8 }}>燃石创想 · 数字孪生运维平台</div>
        <div className="muted" style={{ lineHeight: 1.8, fontSize: 12 }}>
          登录入口（阶段 2 OIDC 占位）。<br />
          本期使用本地演示身份 admin；启用 OIDC 后由 nginx auth_request 统一鉴权。
        </div>
        <button className="btn primary mt" onClick={() => { window.location.href = "/"; }}>
          以演示身份进入
        </button>
      </div>
    </div>
  );
}

export default function App() {
  if (window.location.pathname === "/login") return <LoginPlaceholder />;
  const fleet = useFleet();
  const [page, setPage] = useState<PageId>("overview");
  const [selected, setSelected] = useState<string>("sim-veh-001");

  const selVehicle =
    fleet.snap.vehicles.find((v) => v.vehicle_id === selected) ||
    fleet.snap.vehicles[0] ||
    null;

  const selectVehicle = (id: string, goDetail = false) => {
    setSelected(id);
    if (goDetail) setPage("detail");
  };

  return (
    <div className="app">
      <TopBar snap={fleet.snap} source={fleet.source} wsConnected={fleet.wsConnected} />
      <div className="app-body">
        <aside className="sidebar">
          <nav>
            {NAV.map((n) => (
              <button
                key={n.id}
                className={"nav-item" + (page === n.id ? " active" : "")}
                onClick={() => setPage(n.id)}
              >
                <span className="nav-icon">{n.icon}</span>
                <span>{n.label}</span>
              </button>
            ))}
          </nav>
          <div className="sidebar-user">
            <span className="avatar">A</span>
            <div>
              <div className="user-name">管理员</div>
              <div className="user-role">admin</div>
            </div>
          </div>
        </aside>
        <main className="content">
          {page === "overview" && (
            <Overview fleet={fleet} selected={selVehicle} onSelect={selectVehicle} />
          )}
          {page === "vehicles" && (
            <Vehicles fleet={fleet} onSelect={selectVehicle} />
          )}
          {page === "detail" && (
            <VehicleDetail fleet={fleet} vehicle={selVehicle} onSelect={selectVehicle} />
          )}
          {page === "drive" && (
            <Drive fleet={fleet} vehicle={selVehicle} onSelect={selectVehicle} />
          )}
          {page === "video" && <Video fleet={fleet} vehicle={selVehicle} />}
          {page === "alerts" && <Alerts fleet={fleet} />}
          {page === "terminal" && <Terminal fleet={fleet} vehicle={selVehicle} />}
        </main>
      </div>
    </div>
  );
}
