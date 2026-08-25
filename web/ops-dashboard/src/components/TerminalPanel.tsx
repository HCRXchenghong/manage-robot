import { useEffect, useRef, useState } from "react";
import CollapsePanel from "./CollapsePanel";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import type { VehicleSnap } from "../types";
import { wsURL } from "../api";

interface Props {
  vehicle: VehicleSnap | null;
  fixed?: boolean;
}

type ConnState = "closed" | "connecting" | "open";

export default function TerminalPanel({ vehicle, fixed }: Props) {
  const boxRef = useRef<HTMLDivElement | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [conn, setConn] = useState<ConnState>("closed");
  const vid = vehicle ? vehicle.vehicle_id : "sim-veh-001";

  useEffect(() => {
    const term = new Terminal({
      fontSize: 12,
      fontFamily: "Menlo, Consolas, monospace",
      theme: { background: "#05080f", foreground: "#dce6f7", cursor: "#38bdf8" },
      convertEol: true,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    termRef.current = term;
    fitRef.current = fit;
    if (boxRef.current) {
      term.open(boxRef.current);
      fit.fit();
    }
    term.writeln("终端就绪。点击「打开终端」连接 " + vid + "。");

    const onResize = () => fitRef.current?.fit();
    window.addEventListener("resize", onResize);
    const ro = new ResizeObserver(() => fitRef.current?.fit());
    if (boxRef.current) ro.observe(boxRef.current);

    return () => {
      window.removeEventListener("resize", onResize);
      ro.disconnect();
      wsRef.current?.close();
      term.dispose();
    };
    // vid 变化时重建终端会话
  }, [vid]);

  const connect = () => {
    const term = termRef.current;
    if (!term || wsRef.current) return;
    setConn("connecting");
    term.writeln("正在连接 " + vid + " …");
    const ws = new WebSocket(wsURL("/ws/terminal?vehicle_id=" + encodeURIComponent(vid)));
    wsRef.current = ws;
    ws.onopen = () => {
      setConn("open");
      term.focus();
    };
    ws.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data as string) as { type: string; data: string };
        if (msg.type === "output") term.write(msg.data);
      } catch {
        term.write(String(ev.data));
      }
    };
    ws.onclose = () => {
      setConn("closed");
      wsRef.current = null;
      term.writeln("（连接已断开）");
    };
    ws.onerror = () => {
      term.writeln("（连接失败：后端 /ws/terminal 不可达）");
      ws.close();
    };
    const disp = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "input", data }));
      }
    });
    // 关闭时注销输入监听
    ws.addEventListener("close", () => disp.dispose());
  };

  const disconnect = () => {
    wsRef.current?.close();
  };

  return (
    <CollapsePanel
      id="ov-terminal"
      fixed={fixed}
      title={"远程终端 · " + vid}
      hint={conn === "open" ? "已连接" : conn === "connecting" ? "连接中…" : "未连接"}
    >
      <div className="term-box" ref={boxRef} />
      <div className="btn-row mt">
        <button className="btn small primary" disabled={conn !== "closed"} onClick={connect}>打开终端</button>
        <button className="btn small" disabled={conn === "closed"} onClick={disconnect}>断开</button>
        <button className="btn small" onClick={() => { disconnect(); window.setTimeout(connect, 200); }}>重连</button>
      </div>
    </CollapsePanel>
  );
}
