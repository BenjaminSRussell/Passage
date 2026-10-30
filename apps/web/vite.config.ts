/// <reference types="vitest" />
import { configDefaults } from "vitest/config";
import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import { federationRemotes } from "./src/shell/federation";
import { devProxy } from "./src/shell/devProxy";

/**
 * Wave 5 owns the shell host. Module federation remotes are declared in
 * src/shell/federation.ts. The @module-federation/vite plugin is a lockfile
 * Need; discovery in tests and `vite` uses import.meta.glob.
 */
export default defineConfig(({ mode, command }) => {
  const env = loadEnv(mode, decodeURIComponent(new URL(".", import.meta.url).pathname), "PASSAGE_");
  return {
    plugins: [react()],
    define: {
      __PASSAGE_REMOTES__: JSON.stringify(federationRemotes),
      __PASSAGE_DEV_DUTIES__: JSON.stringify(command === "serve" && mode !== "test" ? (env.PASSAGE_DEV_DUTIES ?? "") : ""),
    },
    server: { port: 5173, proxy: devProxy(env), fs: { allow: ["../warehouse-mobile/public", "."] } },
    test: { environment: "jsdom", globals: true, setupFiles: ["./src/test-setup.ts"], exclude: [...configDefaults.exclude, "e2e/**"] },
  };
});
