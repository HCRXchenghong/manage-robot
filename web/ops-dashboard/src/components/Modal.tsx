// 通用弹窗：页面只留「看」的内容，所有「配置/新建/编辑/文档」都收进弹窗。
// 点遮罩空白处或右上角 ✕ 关闭。
import type { ReactNode } from "react";

interface Props {
  title: string;
  onClose: () => void;
  width?: number | string;
  children: ReactNode;
}

export default function Modal({ title, onClose, width, children }: Props) {
  return (
    <div
      className="modal-overlay"
      onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}
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
  );
}
