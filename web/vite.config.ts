import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In development the UI runs on Vite and proxies the API to `make run-server`.
// In production it is embedded in the firstlume binary and served on the same origin.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": "http://127.0.0.1:8787" },
  },
  build: { outDir: "dist", emptyOutDir: true },
});
