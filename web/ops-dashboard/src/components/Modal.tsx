// 通用弹窗：页面只留「看」的内容，所有「配置/新建/编辑/文档」都收进弹窗。
// 点遮罩空白处或右上角 ✕ 关闭。
// 用 portal 挂到 body：避免祖先 backdrop-filter/transform 把 fixed 弹窗困在面板里。
import type { MouseEvent as ReactMouseEvent, ReactNode } from "react";
import { createPortal } from "react-dom";

interface Props {
  title: string;
  onClose: () => void;
  width?: number | string;
  children: ReactNode;
}

// 「双击空白区域放大」统一守卫：总览/数字孪生等页面共用。
// 命中交互控件、弹窗/浮层内部，或当前页面已有任何弹窗时，一律不触发放大。
// （React portal 事件沿 React 树冒泡，弹窗内点击可能触发父面板 onClick，
//   造成「点急停弹窗却弹出车辆详情」这类串弹问题。）
export function openIfPlain(fn: () => void) {
  return (e: ReactMouseEvent) => {
    const t = e.target as HTMLElement;
    if (t.closest("button, select, input, a, textarea, tr, .term-box")) return;
    // 地图、点云和视频画面保留自己的拖动/缩放/双击行为；放大弹窗从卡片标题或空白处双击打开。
    if (t.closest(".amap-container, .gps-map, canvas, video")) return;
    if (t.closest(".modal-overlay, .pop-mask, .ctx-menu, .veh-search-pop")) return;
    if (document.querySelector(".modal-overlay")) return;
    fn();
  };
}

export default function Modal({ title, onClose, width, children }: Props) {
  return createPortal(
    <div
      className="modal-overlay"
      onClick={(e) => {
        e.stopPropagation();
        if (e.target === e.currentTarget) onClose();
      }}
      onMouseDown={(e) => e.stopPropagation()}
      onMouseUp={(e) => e.stopPropagation()}
      onDoubleClick={(e) => e.stopPropagation()}
      onContextMenu={(e) => e.stopPropagation()}
    >
      <div
        className="modal"
        style={{
          width: width || "min(560px, 92vw)",
          maxHeight: "86vh",
          display: "flex",
          flexDirection: "column",
        }}
      >
        <div className="modal-head">
          <div style={{ fontWeight: 700, fontSize: 14 }}>{title}</div>
          <button className="btn small ghost" onClick={onClose} title="关闭">✕</button>
        </div>
        <div style={{ padding: 16, overflow: "auto" }}>{children}</div>
      </div>
    </div>
    ,
    document.body
  );
}
