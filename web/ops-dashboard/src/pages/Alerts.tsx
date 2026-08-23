import { useMemo, useState } from "react";
import type { FleetState } from "../api";
import EventFeed, { downloadEventsCSV } from "../components/EventFeed";

export default function Alerts({ fleet }: { fleet: FleetState }) {
  const [level, setLevel] = useState<"all" | "critical" | "warn" | "info">("all");
  const [veh, setVeh] = useState("all");

  const filtered = useMemo(() => {
    let list = fleet.snap.events;
    if (level !== "all") list = list.filter((e) => e.level === level);
    if (veh !== "all") list = list.filter((e) => e.vehicle_id === veh);
    return list;
  }, [fleet.snap.events, level, veh]);

  const vehicles = fleet.snap.vehicles;

  return (
    <div className="panel" style={{ height: "100%", display: "flex", flexDirection: "column" }}>
      <div className="alerts-toolbar">
        <div className="tabs">
          {([["all", "全部级别"], ["critical", "critical"], ["warn", "warn"], ["info", "info"]] as const).map(([id, label]) => (
            <button key={id} className={"tab" + (level === id ? " active" : "")} onClick={() => setLevel(id)}>
              {label}
            </button>
          ))}
        </div>
        <select className="input" value={veh} onChange={(e) => setVeh(e.target.value)}>
          <option value="all">全部车辆</option>
          {vehicles.map((v) => (
            <option key={v.vehicle_id} value={v.vehicle_id}>{v.vehicle_id}</option>
          ))}
        </select>
        <span className="spacer" />
        <button className="btn small" onClick={() => downloadEventsCSV(filtered, "alerts.csv")}>导出日志</button>
      </div>
      <EventFeed events={filtered} />
    </div>
  );
}
