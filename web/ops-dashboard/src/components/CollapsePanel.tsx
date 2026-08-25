// 可收起面板：标题栏右侧「收起/展开」按钮切换；状态写 localStorage，刷新后保持。
// 收起时内容隐藏但不卸载（终端/事件等子组件状态与连接不丢）。
import { useEffect, useState } from "react";
import type { ReactNode } from "react";

interface Props {
  id: string;
  title: string;
  hint?: string;
  right?: ReactNode;
  children: ReactNode;
  // 固定模式：恒展开、隐藏收起按钮（总览大屏四个固定区域用）
  fixed?: boolean;
}

export default function CollapsePanel({ id, title, hint, right, children, fixed }: Props) {
  const [open, setOpen] = useState<boolean>(() => {
    try {
      const v = window.localStorage.getItem("cp-open-" + id);
      return v === null ? true : v === "1";
    } catch {
      return true;
    }
  });
  useEffect(() => {
    try {
      window.localStorage.setItem("cp-open-" + id, open ? "1" : "0");
    } catch {
      /* 忽略 */
    }
  }, [open, id]);

  return (
    <div className={"panel" + (fixed || open ? "" : " cpanel-collapsed")}>
      <div className="panel-title">
        <span
          style={fixed ? undefined : { cursor: "pointer" }}
          title={fixed ? undefined : open ? "点击收起" : "点击展开"}
          onClick={fixed ? undefined : () => setOpen((o) => !o)}
        >
          {title}
          {hint ? <span className="hint"> {hint}</span> : null}
        </span>
        <span className="btn-row">
          {right}
          {!fixed && (
            <button className="btn small ghost" onClick={() => setOpen((o) => !o)}>
              {open ? "收起 ▾" : "展开 ▸"}
            </button>
          )}
        </span>
      </div>
      <div style={fixed || open ? undefined : { display: "none" }}>{children}</div>
    </div>
  );
}
