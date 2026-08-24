// 侧栏图标：手绘线性 SVG（16x16，描边跟随文字颜色），替换旧字符图标。
export default function NavIcon({ name }: { name: string }) {
  const common = {
    width: 16,
    height: 16,
    viewBox: "0 0 16 16",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.4,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
  };
  switch (name) {
    case "grid": // 总览大屏
      return (
        <svg {...common}>
          <rect x="2" y="2" width="5" height="5" rx="1" />
          <rect x="9" y="2" width="5" height="5" rx="1" />
          <rect x="2" y="9" width="5" height="5" rx="1" />
          <path d="M9 11.5h5M11.5 9v5" />
        </svg>
      );
    case "truck": // 车辆
      return (
        <svg {...common}>
          <path d="M1.5 4h8v7h-8z" />
          <path d="M9.5 6.5h2.8l2.2 2.4V11h-5" />
          <circle cx="4.5" cy="11.8" r="1.4" />
          <circle cx="11.5" cy="11.8" r="1.4" />
        </svg>
      );
    case "map": // 地图中心
      return (
        <svg {...common}>
          <path d="M5.5 2 2 3.4v10.2l3.5-1.4 3 1.4 3.5-1.4V2.9L8.5 4.3l-3-1.4Z" />
          <path d="M5.5 2v10.2M8.5 4.3v10.2" />
        </svg>
      );
    case "route": // 循迹导航
      return (
        <svg {...common}>
          <circle cx="3.5" cy="12.5" r="1.6" />
          <path d="M5.1 12.5h5.4a3 3 0 0 0 0-6H6" />
          <circle cx="12.5" cy="3.5" r="1.6" />
        </svg>
      );
    case "joystick": // 远程接管
      return (
        <svg {...common}>
          <circle cx="8" cy="5" r="2.2" />
          <path d="M8 7.2V11" />
          <path d="M3.5 13.5h9M5 11h6" />
        </svg>
      );
    case "camera": // 视频监控
      return (
        <svg {...common}>
          <rect x="1.5" y="4.5" width="9" height="7" rx="1.5" />
          <path d="M10.5 7.5l4-2.3v5.6l-4-2.3" />
        </svg>
      );
    case "bell": // 告警
      return (
        <svg {...common}>
          <path d="M8 2.2a4 4 0 0 0-4 4v3l-1.5 2.6h11L12 9.2v-3a4 4 0 0 0-4-4Z" />
          <path d="M6.6 13.6a1.5 1.5 0 0 0 2.8 0" />
        </svg>
      );
    case "terminal": // 远程终端
      return (
        <svg {...common}>
          <rect x="1.8" y="2.8" width="12.4" height="10.4" rx="1.6" />
          <path d="M4.5 6.5l2.2 2-2.2 2M8.5 10.5h3" />
        </svg>
      );
    case "plug": // API 平台
      return (
        <svg {...common}>
          <path d="M6 2.5v3M10 2.5v3" />
          <path d="M4.5 5.5h7v2.6a3.5 3.5 0 0 1-3.5 3.5 3.5 3.5 0 0 1-3.5-3.5V5.5Z" />
          <path d="M8 11.6v2" />
        </svg>
      );
    case "users": // 组织管理
      return (
        <svg {...common}>
          <circle cx="6" cy="5.5" r="2.2" />
          <path d="M2.5 13.2a3.5 3.5 0 0 1 7 0" />
          <path d="M10.5 3.6a2.2 2.2 0 1 1 0 3.9M11.5 9.9a3.5 3.5 0 0 1 2 3.3" />
        </svg>
      );
    default:
      return (
        <svg {...common}>
          <circle cx="8" cy="8" r="5.5" />
        </svg>
      );
  }
}
