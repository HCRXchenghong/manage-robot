// 侧翼列：整列可收起（收起后变成细竖条），状态写 localStorage。
// 总览大屏用：左翼=车辆列表/告警与事件，右翼=详情/接管/终端；收起后空间让给中间地图。
import { useEffect, useState } from "react";
import type { ReactNode } from "react";

interface Props {
  id: string;
  label: string;
  children: ReactNode;
  // 可选受控模式：父级传 open 时开合由父级决定（总览大屏用来联动地图控制按钮列位置）
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}

export default function SideCol({ id, label, children, open: openProp, onOpenChange }: Props) {
  const [inner, setInner] = useState<boolean>(() => {
    try {
      const v = window.localStorage.getItem("ov-col-" + id);
      return v === null ? true : v === "1";
    } catch {
      return true;
    }
  });
  const open = openProp ?? inner;
  const setOpen = (v: boolean) => {
    setInner(v);
    if (onOpenChange) onOpenChange(v);
  };
  useEffect(() => {
    try {
      window.localStorage.setItem("ov-col-" + id, open ? "1" : "0");
    } catch {
      /* 忽略 */
    }
  }, [open, id]);

  if (!open) {
    return (
      <div className="side-rail" onClick={() => setOpen(true)} title={"展开 " + label}>
        <span className="vtext">⟨ {label}</span>
      </div>
    );
  }
  return (
    <div className="side-col">
      <div className="side-head">
        <span className="muted" style={{ fontSize: 11 }}>{label}</span>
        <button className="btn small ghost" onClick={() => setOpen(false)} title="收起本列">⟩</button>
      </div>
      {children}
    </div>
  );
}
