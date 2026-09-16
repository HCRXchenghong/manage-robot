// A terminal is intentionally unavailable until the vehicle workspace agent
// establishes its authenticated reverse channel. No shell is emulated here.
import CollapsePanel from "./CollapsePanel";
import type { VehicleSnap } from "../types";

interface Props {
  vehicle: VehicleSnap | null;
  fixed?: boolean;
  onExpand?: () => void;
  autoConnect?: boolean;
  panelId?: string;
  vehicleId?: string;
}

export default function TerminalPanel({ vehicle, fixed, onExpand, panelId, vehicleId }: Props) {
  const id = vehicleId || vehicle?.vehicle_id;
  return (
    <CollapsePanel
      id={panelId || "ov-terminal"}
      fixed={fixed}
      onTitleClick={onExpand}
      title={"远程终端" + (id ? " · " + id : "")}
      hint="车端 Agent 未注册"
    >
      <div className="term-box" style={{ padding: 18, display: "grid", placeItems: "center", textAlign: "center" }}>
        <div>
          <div style={{ fontWeight: 650, marginBottom: 8 }}>真实终端通道未建立</div>
          <div className="muted" style={{ fontSize: 12, lineHeight: 1.7 }}>
            {id ? "车辆 " + id + " 尚未注册 workspace-agent。" : "请先选择已登记车辆。"}<br />
            车端 Agent 必须通过 Gateway 建立受令牌保护的反向通道；平台不会模拟 Shell 或执行命令。
          </div>
        </div>
      </div>
    </CollapsePanel>
  );
}
