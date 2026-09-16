import { createRoot } from "react-dom/client";
import App from "./App";
import "./styles.css";

// 防止浏览器级捏合缩放破坏控制台固定精度的交互；这不是访问控制。
// 访问权限只由服务端会话、角色、资源范围和控制权租约判定，绝不能依赖 UA 或视口宽度。
document.addEventListener(
  "wheel",
  (e) => {
    if (e.ctrlKey) e.preventDefault();
  },
  { passive: false },
);
document.addEventListener("gesturestart", (e) => e.preventDefault());
document.addEventListener("gesturechange", (e) => e.preventDefault());

createRoot(document.getElementById("root")!).render(<App />);
