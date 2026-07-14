import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dev server proxies /api to the Go server on :8080; production builds are
// embedded into the Go binary and served from the same origin.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
  },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
    },
  },
});
