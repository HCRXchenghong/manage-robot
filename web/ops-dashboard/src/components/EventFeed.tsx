import { useMemo, useState } from "react";
import CollapsePanel from "./CollapsePanel";
import type { EventSnap } from "../types";

interface Props {
  events: EventSnap[];
  compact?: boolean;
}

function fmtTime(tsNs: number): string {
  const d = new Date(tsNs / 1e6);
  const pad = (n: number) => (n < 10 ? "0" + n : String(n));
  return pad(d.getHours()) + ":" + pad(d.getMinutes()) + ":" + pad(d.getSeconds());
}

export function downloadEventsCSV(events: EventSnap[], filename: string) {
  const lines = ["time,level,vehicle,text"];
  for (const e of events) {
    const text = '"' + e.text.replace(/"/g, '""') + '"';
    lines.push([new Date(e.ts_ns / 1e6).toISOString(), e.level, e.vehicle_id || "", text].join(","));
  }
  const blob = new Blob(["\uFEFF" + lines.join("\r\n")], { type: "text/csv;charset=utf-8" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = filename;
  a.click();
  URL.revokeObjectURL(a.href);
}

export default function EventFeed({ events, compact }: Props) {
  const [readSet, setReadSet] = useState<Set<number>>(new Set());
  const [rangeMin, setRangeMin] = useState<number>(0); // 0 = 全部

  const filtered = useMemo(() => {
    if (rangeMin <= 0) return events;
    const cutoff = Date.now() * 1e6 - rangeMin * 60 * 1e9;
    return events.filter((e) => e.ts_ns >= cutoff);
  }, [events, rangeMin]);

  const markAllRead = () => {
    setReadSet(new Set(events.map((e) => e.ts_ns)));
  };

  return (
    <CollapsePanel
      id="ov-events"
      title={"告警与事件" + (compact ? "" : "（最新在前）")}
      right={
        <span className="btn-row">
          <select className="input small" value={rangeMin} onChange={(e) => setRangeMin(Number(e.target.value))}>
            <option value={0}>全部时间</option>
            <option value={5}>近 5 分钟</option>
            <option value={30}>近 30 分钟</option>
          </select>
          <button className="btn small" onClick={markAllRead}>标记已读</button>
          {!compact && (
            <button className="btn small" onClick={() => downloadEventsCSV(filtered, "events.csv")}>
              导出日志
            </button>
          )}
        </span>
      }
    >
      <div className="scroll">
        {filtered.length === 0 && <div className="muted" style={{ padding: 10 }}>暂无事件</div>}
        {filtered.map((e) => (
          <div key={e.ts_ns + e.text} className={"event" + (readSet.has(e.ts_ns) ? " read" : "")}>
            <span className={"event-time lvl-" + e.level}>{fmtTime(e.ts_ns)}</span>
            <span className="event-text">
              <span className={"lvl-" + e.level}>[{e.level}]</span>{" "}
              {e.vehicle_id && <span className="event-veh">{e.vehicle_id} </span>}
              {e.text}
            </span>
          </div>
        ))}
      </div>
    </CollapsePanel>
  );
}
