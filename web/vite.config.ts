import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In dev, run the Go server with FIN_TRACKER_DEV_USER set (see Makefile `dev`)
// and Vite proxies API calls to it.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true, chunkSizeWarningLimit: 800 },
  server: { proxy: { "/api": "http://localhost:8080" } },
});
