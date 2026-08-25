// 侧翼列：整列可收起（左翼向左收成细竖条、右翼向右收），状态写 localStorage。
// 列体固定大小、不撑出画面：内容显示不完时底部出现「点击展开更多」，
// 点击后弹窗显示完整内容（同一份子组件移入弹窗，避免双实例）。
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import Modal from "./Modal";

interface Props {
  id: string;
  label: string;
  children: ReactNode;
  side?: "left" | "right";
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}

export default function SideCol({ id, label, children, side = "left", open: openProp, onOpenChange }: Props) {
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

  // 内容是否显示不完（固定列高，超出则提示展开更多）
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const [overflowing, setOverflowing] = useState(false);
  const [expanded, setExpanded] = useState(false);
  useLayoutEffect(() => {
    if (expanded) return;
    const el = bodyRef.current;
    if (!el) return;
    const check = () => setOverflowing(el.scrollHeight > el.clientHeight + 4);
    check();
    const t = window.setInterval(check, 1000);
    return () => window.clearInterval(t);
  }, [open, expanded]);

  if (!open) {
    return (
      <div className="side-rail-mini" onClick={() => setOpen(true)} title={"展开 " + label}>
        <span className="vtext">{side === "left" ? "⟩ " : "⟨ "}{label}</span>
      </div>
    );
  }
  return (
    <div className="side-col">
      <div className="side-head">
        {side === "left" && (
          <button className="btn small ghost" onClick={() => setOpen(false)} title="向左收起（外侧）">
            ⟨
          </button>
        )}
        <span className="muted" style={{ fontSize: 11 }}>{label}</span>
        {side === "right" && (
          <button className="btn small ghost" onClick={() => setOpen(false)} title="向右收起（外侧）">
            ⟩
          </button>
        )}
      </div>
      {expanded ? (
        <Modal title={label} onClose={() => setExpanded(false)} width="min(720px, 94vw)">
          <div className="side-modal-body">{children}</div>
        </Modal>
      ) : (
        <>
          <div className="side-body" ref={bodyRef}>{children}</div>
          {overflowing && (
            <button className="side-more" onClick={() => setExpanded(true)}>
              点击展开更多 ▴
            </button>
          )}
        </>
      )}
    </div>
  );
}
