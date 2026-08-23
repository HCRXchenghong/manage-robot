import { useEffect, useRef, useState } from "react";
import type { FleetSnap } from "../types";
import { emergencyStop, takeoverRelease, takeoverRenew, takeoverRequest } from "../api";

interface Props {
  snap: FleetSnap;
  big?: boolean;
}

export default function TakeoverPanel({ snap, big }: Props) {
  const [driver, setDriver] = useState("admin");
  const [busy, setBusy] = useState(false);
  const [armEstop, setArmEstop] = useState(false);
  const [msg, setMsg] = useState("");
  const serverTimeRef = useRef<number>(snap.server_time_ns);
  const localRef = useRef<number>(Date.now());
  serverTimeRef.current = snap.server_time_ns;
  localRef.current = Date.now();

  // 倒计时：以 server_time_ns 为基准（不用浏览器墙钟，计划坑速查第 7 条）
  const [, force] = useState(0);
  useEffect(() => {
    const t = window.setInterval(() => force((x) => x + 1), 200);
    return () => window.clearInterval(t);
  }, []);

  const secondsLeft = (() => {
    if (!snap.takeover.active) return 0;
    const elapsedSinceSnap = (Date.now() - localRef.current) / 1000;
    const base = snap.takeover.seconds_left || 0;
    return Math.max(0, base - elapsedSinceSnap);
  })();

  const run = async (label: string, fn: () => Promise<Record<string, unknown>>) => {
    setBusy(true);
    setMsg("");
    try {
      const resp = await fn();
      setMsg(label + (resp.ok ? " 成功" : " 失败：" + String(resp.error || resp.ack || "")));
    } catch (e) {
      setMsg(label + " 失败：" + String(e));
    } finally {
      setBusy(false);
    }
  };

  const onEstop = () => {
    if (!armEstop) {
      setArmEstop(true);
      window.setTimeout(() => setArmEstop(false), 3000);
      return;
    }
    setArmEstop(false);
    void run("紧急停车", emergencyStop);
  };

  const tk = snap.takeover;

  return (
    <div style={{ display: "flex", flexDirection: "column", minHeight: 0, flex: 1 }}>
      <div className="panel-title">
        <span>远程驾驶 · 接管</span>
        <span className="hint">{tk.active ? "接管进行中" : "当前无接管"}</span>
      </div>
      <div className="kv">
        <span className="k">状态</span>
        <span>{tk.active ? <span className="badge warn">接管中</span> : <span className="badge dim">自动驾驶</span>}</span>
        <span className="k">驾驶员</span>
        <span>{tk.active ? tk.driver || "-" : "-"}</span>
        <span className="k">租约</span>
        <span className="mono">{tk.active ? tk.lease_id || "-" : "-"}</span>
        <span className="k">fencing</span>
        <span className="mono">{tk.active ? tk.fencing ?? "-" : "-"}</span>
      </div>
      {tk.active && (
        <div style={{ marginBottom: 10 }}>
          <div className="muted" style={{ fontSize: 11 }}>租约剩余（服务器时间基准）</div>
          <div className={"countdown" + (secondsLeft < 2 ? " low" : "")}>{secondsLeft.toFixed(1)} s</div>
        </div>
      )}
      <div className="row" style={{ marginBottom: 8 }}>
        <input className="input" style={{ width: 110 }} value={driver} onChange={(e) => setDriver(e.target.value)} placeholder="驾驶员" />
      </div>
      <div className="btn-row">
        <button className="btn primary" disabled={busy || tk.active} onClick={() => void run("申请接管", () => takeoverRequest(driver))}>
          申请接管
        </button>
        <button className="btn" disabled={busy || !tk.active} onClick={() => void run("续租", () => takeoverRenew(driver))}>
          续租
        </button>
        <button className="btn" disabled={busy || !tk.active} onClick={() => void run("交还控制权", () => takeoverRelease(driver))}>
          交还控制权
        </button>
      </div>
      <div className="btn-row mt">
        <button className={"btn danger" + (big ? "" : " small")} disabled={busy} onClick={onEstop}>
          {armEstop ? "再次点击确认！" : "紧急停车"}
        </button>
        {armEstop && <span className="confirm-inline">3 秒内再次点击执行紧急停车</span>}
      </div>
      {msg && <div className="muted mt" style={{ fontSize: 11 }}>{msg}</div>}
    </div>
  );
}
