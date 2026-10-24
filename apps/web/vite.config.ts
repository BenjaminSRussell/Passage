/// <reference types="vitest" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { federationRemotes } from "./src/shell/federation";

/**
 * Wave 5 owns the shell host. Module federation remotes are declared in
 * src/shell/federation.ts. The @module-federation/vite plugin is a lockfile
 * Need; discovery in tests and `vite` uses import.meta.glob.
 */
export default defineConfig({
  plugins: [react()],
  define: { __PASSAGE_REMOTES__: JSON.stringify(federationRemotes) },
  server: { port: 5173, proxy: { "/api": "http://localhost:8080" } },
  test: { environment: "jsdom", globals: true, setupFiles: ["./src/test-setup.ts"] },
});
