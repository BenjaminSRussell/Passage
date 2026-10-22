import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    proxy: {
      "/api": {
        target: "http://127.0.0.1:8080",
        rewrite: (path) => path.replace(/^\/api/, ""),
        configure: (proxy) => {
          proxy.on("proxyReq", (proxyReq) => {
            if (!proxyReq.getHeader("authorization")) {
              const token = process.env.LEDGER_TOKEN || "local-dev-token";
              proxyReq.setHeader("authorization", `Bearer ${token}`);
            }
          });
        },
      },
    },
  },
});
