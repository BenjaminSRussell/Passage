/// <reference types="vitest" />
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The portal talks only to its BFF (services/customer-portal-ts): /bff/* for the session, /api/v1/* for data. In development the BFF
// runs on :8080 (COOKIE_SECURE=false); in production the edge routes both to it.
export default defineConfig({
  plugins: [react()],
  server: { port: 5176, proxy: { "/bff": "http://localhost:8080", "/api": "http://localhost:8080" } },
  build: { outDir: "dist", sourcemap: true },
  test: { environment: "jsdom", globals: true, setupFiles: ["./test/setup.ts"], include: ["test/**/*.test.{ts,tsx}"] },
});
