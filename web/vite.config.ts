import { resolve } from "node:path";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";

/**
 * Two entry points, two audiences.
 *
 *   /        the subscriber's portal
 *   /admin/  the operator's console
 *
 * The split buys bundle size and URL ownership, not security — the boundary
 * that matters is on the server (see core/internal/api/portal.go). What it does
 * buy is that a customer never downloads the console, which drags in Monaco and
 * the whole fleet-management client.
 *
 * That only holds while src/portal/ imports neither src/api.ts nor src/pages/.
 * It is discipline rather than structure; if it ever needs to be structural,
 * the honest version asserts on the built module graph, not on a grep.
 *
 * appType "mpa" because both interfaces use hash routing and neither wants an
 * SPA fallback. The cost is that /admin without a trailing slash 404s in dev.
 */
export default defineConfig({
  plugins: [react(), tailwindcss()],
  appType: "mpa",
  build: {
    // Leave dist/ in place rather than wiping it.
    //
    // The directory holds a committed placeholder that keeps
    // `//go:embed all:dist` matching, so `go build ./...` works in a checkout
    // that never ran this. Emptying the directory deletes that file, and the
    // Go build then fails with "contains no embeddable files" — on a machine
    // whose only mistake was building the frontend.
    emptyOutDir: false,
    rollupOptions: {
      input: {
        portal: resolve(__dirname, "index.html"),
        admin: resolve(__dirname, "admin/index.html"),
      },
    },
  },
  server: {
    port: 5173,
    // Dev-only: forward API calls to a locally running core. The portal's
    // calls all sit under /api/portal/, so this one rule covers both.
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/sub": "http://127.0.0.1:8080",
    },
  },
});
