import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    // Dev-only: forward API calls to a locally running core.
    proxy: {
      "/api": "http://127.0.0.1:8080",
    },
  },
});
