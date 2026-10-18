import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Dev only: the browser's Origin is the Vite page (5173), which the Go server
// rejects on writes. Rewrite it to the Go default APP_ORIGIN so
// `APP_ORIGIN` need not be loosened. Production serves one origin and needs no proxy.
const backend = process.env.BACKEND_ORIGIN ?? "http://localhost:8080";

export default defineConfig({
  root: "web",
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: backend,
        configure: (proxy) => {
          proxy.on("proxyReq", (req) => req.setHeader("origin", backend));
        },
      },
    },
  },
});
