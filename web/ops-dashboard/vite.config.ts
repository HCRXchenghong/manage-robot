import { defineConfig } from "vite";

// 不用 @vitejs/plugin-react：esbuild 原生 JSX（automatic）即可构建，
// 让运行时依赖严格落在计划白名单内（react/three/@react-three/fiber/xterm）。
export default defineConfig({
  esbuild: { jsx: "automatic" },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://127.0.0.1:9800",
      "/ws": { target: "ws://127.0.0.1:9800", ws: true },
    },
  },
  build: {
    outDir: "../../server/fleet/web/dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 1600,
  },
});
