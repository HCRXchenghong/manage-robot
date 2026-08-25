import { useEffect, useMemo, useState } from "react";
import { fetchMe, logout, useFleet } from "./api";
import TopBar from "./components/TopBar";
import SettingsModal from "./components/SettingsModal";
import PasswordModal from "./components/PasswordModal";
import NavIcon from "./components/NavIcons";
import Login from "./pages/Login";
import Overview from "./pages/Overview";
import Vehicles from "./pages/Vehicles";
import Drive from "./pages/Drive";
import Video from "./pages/Video";
import Alerts from "./pages/Alerts";
import Terminal from "./pages/Terminal";
import Maps from "./pages/Maps";
import MapEdit from "./pages/MapEdit";
import NavRoutePage from "./pages/NavRoute";
import ApiPortal from "./pages/ApiPortal";
import AdminPage from "./pages/Admin";
import Twin from "./pages/Twin";
import type { Me } from "./types";

export type PageId =
  | "overview"
  | "vehicles"
  | "drive"
  | "video"
  | "alerts"
  | "terminal"
  | "maps"
  | "mapedit"
  | "navroute"
  | "apiportal"
  | "admin"
  | "twin";

const NAV: { id: PageId; icon: string; label: string; adminOnly?: boolean }[] = [
  { id: "overview", icon: "grid", label: "总览大屏" },
  { id: "vehicles", icon: "truck", label: "车辆列表" },
  { id: "maps", icon: "map", label: "地图中心" },
  { id: "navroute", icon: "route", label: "循迹导航" },
  { id: "drive", icon: "joystick", label: "远程接管" },
  { id: "video", icon: "camera", label: "视频监控" },
  { id: "alerts", icon: "bell", label: "告警与事件" },
  { id: "terminal", icon: "terminal", label: "远程终端" },
  { id: "apiportal", icon: "plug", label: "API 平台", adminOnly: true },
  { id: "admin", icon: "users", label: "组织管理", adminOnly: true },
];

const PAGE_IDS: PageId[] = ["overview","vehicles","drive","video","alerts","terminal","maps","mapedit","navroute","apiportal","admin","twin"];

// hash 路由：#/页面?参数 —— 刷新不回首页、筛选状态可进 URL（根源修复）。
export function parseHash(): { page: PageId; params: URLSearchParams } {
  const h = window.location.hash.replace(/^#\/?/, "");
  const qi = h.indexOf("?");
  const path = qi >= 0 ? h.slice(0, qi) : h;
  const query = qi >= 0 ? h.slice(qi + 1) : "";
  const segs = path.split("/").filter(Boolean);
  const first = segs[0] || "overview";
  const page = (PAGE_IDS as string[]).includes(first) ? (first as PageId) : "overview";
  const params = new URLSearchParams(query);
  if (segs[1]) params.set("id", segs.slice(1).join("/"));
  return { page, params };
}

export function navTo(page: PageId, params?: Record<string, string>) {
  const q = params && Object.keys(params).length ? "?" + new URLSearchParams(params).toString() : "";
  window.location.hash = "#/" + page + q;
}

const roleLabel = (r: string) =>
  r === "super" ? "超级管理员" : r === "group_admin" ? "管理员" : "用户";

export default function App() {
  const fleet = useFleet(); // hooks 必须无条件调用（登录态判断在其后）
  const [me, setMe] = useState<Me | null>(null);
  const [checking, setChecking] = useState(true);
  const [route, setRoute] = useState(parseHash);
  const [selected, setSelected] = useState<string>("");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [pwdOpen, setPwdOpen] = useState(false);
  const [userMenu, setUserMenu] = useState(false);
  const [navCollapsed, setNavCollapsed] = useState<boolean>(() => {
    try {
      return window.localStorage.getItem("nav-collapsed") === "1";
    } catch {
      return false;
    }
  });
  useEffect(() => {
    try {
      window.localStorage.setItem("nav-collapsed", navCollapsed ? "1" : "0");
    } catch {
      /* 忽略 */
    }
  }, [navCollapsed]);

  useEffect(() => {
    const onHash = () => setRoute(parseHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  useEffect(() => {
    void fetchMe().then((m) => {
      setMe(m);
      setChecking(false);
    });
  }, []);

  const isAdmin = me != null && me.role !== "user";
  const page = route.page;

  // 分组过滤：超管全量；其他角色只见本分组车辆（服务端快照同样已过滤，双保险）
  const scopedFleet = useMemo(() => {
    if (!me || me.role === "super") return fleet;
    return {
      ...fleet,
      snap: {
        ...fleet.snap,
        vehicles: fleet.snap.vehicles.filter((v) => me.groups.includes(v.group)),
      },
    };
  }, [fleet, me]);

  const selVehicle =
    scopedFleet.snap.vehicles.find((v) => v.vehicle_id === selected) ||
    scopedFleet.snap.vehicles[0] ||
    null;

  const selectVehicle = (id: string, goDetail = false) => {
    setSelected(id);
    if (goDetail) {
      // 车辆详情 = 数字孪生独立页，新标签页打开
      window.open("#/twin/" + encodeURIComponent(id), "_blank");
    }
  };

  if (checking) {
    return (
      <div className="login-bg">
        <div className="muted">正在校验会话…</div>
      </div>
    );
  }

  if (!me) {
    return (
      <Login
        onLoggedIn={() => {
          void fetchMe().then((m) => setMe(m));
        }}
      />
    );
  }

  // 数字孪生详情页：独立页面（无侧栏/顶栏），等保鉴权在页内二次校验
  if (page === "twin") {
    return <Twin vehicleId={route.params.get("id") || ""} />;
  }

  const doLogout = async () => {
    await logout();
    window.location.href = "/";
  };

  return (
    <div className="app">
      <TopBar snap={scopedFleet.snap} source={scopedFleet.source} wsConnected={scopedFleet.wsConnected} />
      <div className="app-body">
        <aside className={"sidebar" + (navCollapsed ? " collapsed" : "")}>
          <nav>
            <button
              className="nav-collapse-btn"
              title={navCollapsed ? "展开导航" : "收起导航"}
              onClick={() => setNavCollapsed((v) => !v)}
            >
              {navCollapsed ? "»" : "«"}
            </button>
            {NAV.filter((n) => !n.adminOnly || isAdmin).map((n) => (
              <button
                key={n.id}
                className={"nav-item" + (page === n.id ? " active" : "")}
                onClick={() => navTo(n.id)}
                title={n.label}
              >
                <span className="nav-icon"><NavIcon name={n.icon} /></span>
                <span className="nav-label">
                  {n.id === "admin" && me.role === "group_admin" ? "成员管理" : n.label}
                </span>
              </button>
            ))}
          </nav>
          <div
            className="sidebar-user"
            style={{ cursor: "pointer", position: "relative" }}
            onClick={() => setUserMenu((v) => !v)}
          >
            <span className="avatar">{(me.display_name || me.username || "U").slice(0, 1)}</span>
            <div className="user-txt">
              <div className="user-name">{me.display_name || me.username}</div>
              <div className="user-role">{roleLabel(me.role)}</div>
            </div>
            {userMenu && (
              <div className="user-menu" onClick={(e) => e.stopPropagation()}>
                {isAdmin && (
                  <button
                    onClick={() => {
                      setUserMenu(false);
                      setSettingsOpen(true);
                    }}
                  >
                    设置
                  </button>
                )}
                <button
                  onClick={() => {
                    setUserMenu(false);
                    setPwdOpen(true);
                  }}
                >
                  修改密码
                </button>
                <button onClick={() => void doLogout()}>退出登录</button>
              </div>
            )}
          </div>
        </aside>
        <main className="content">
          {page === "overview" && (
            <Overview fleet={scopedFleet} selected={selVehicle} onSelect={selectVehicle} />
          )}
          {page === "vehicles" && (
            <Vehicles fleet={scopedFleet} me={me} onSelect={selectVehicle} />
          )}
          {page === "drive" && (
            <Drive fleet={scopedFleet} vehicle={selVehicle} onSelect={selectVehicle} />
          )}
          {page === "video" && <Video fleet={scopedFleet} vehicle={selVehicle} />}
          {page === "alerts" && <Alerts me={me} />}
          {page === "terminal" && <Terminal fleet={scopedFleet} vehicle={selVehicle} />}
          {page === "maps" && (
            <Maps
              fleet={scopedFleet}
              me={me}
              onEdit={(mapId) => navTo("mapedit", { id: mapId })}
            />
          )}
          {page === "mapedit" && (
            <MapEdit mapId={route.params.get("id") || ""} onBack={() => navTo("maps")} />
          )}
          {page === "navroute" && <NavRoutePage fleet={scopedFleet} />}
          {page === "apiportal" && isAdmin && <ApiPortal />}
          {page === "admin" && isAdmin && <AdminPage me={me} />}
        </main>
      </div>
      {settingsOpen && <SettingsModal onClose={() => setSettingsOpen(false)} />}
      {pwdOpen && <PasswordModal onClose={() => setPwdOpen(false)} />}
    </div>
  );
}
