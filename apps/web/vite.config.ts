/// <reference types="vitest" />
import { configDefaults } from "vitest/config";
import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import { mobileDevStub } from "./src/modules/mobile-flows/api/devStub";
import { federationRemotes } from "./src/shell/federation";
import { devProxy } from "./src/shell/devProxy";
import authzCatalog from "../../policies/authz/data.json";

/**
 * Wave 5 owns the shell host. Module federation remotes are declared in
 * src/shell/federation.ts. The @module-federation/vite plugin is a lockfile
 * Need; discovery in tests and `vite` uses import.meta.glob.
 */
/** Every duty in the authorization catalog: the demo build's single user holds them all. */
function allDuties(): string {
  return Object.keys(authzCatalog.duties).join(",");
}

export default defineConfig(({ mode, command }) => {
  const env = loadEnv(mode, decodeURIComponent(new URL(".", import.meta.url).pathname), "PASSAGE_");
  // `vite build --mode demo` is the GitHub Pages demo: an in-browser fake backend (src/demo) and every screen visible.
  if (mode === "demo") {
    return {
      base: env.PASSAGE_DEMO_BASE ?? "/Passage/",
      plugins: [react()],
      define: {
        __PASSAGE_REMOTES__: JSON.stringify(federationRemotes),
        __PASSAGE_DEV_DUTIES__: JSON.stringify(allDuties()),
        __PASSAGE_DEV_SHOW_ALL_NAV__: JSON.stringify(true),
      },
      server: { port: 5174 },
    };
  }
  return {
    plugins: [react(), ...(command === "serve" && env.PASSAGE_MOBILE_STUB === "1" ? [mobileDevStub()] : [])],
    define: {
      __PASSAGE_REMOTES__: JSON.stringify(federationRemotes),
      __PASSAGE_DEV_DUTIES__: JSON.stringify(command === "serve" && mode !== "test" ? (env.PASSAGE_DEV_DUTIES ?? "") : ""),
      __PASSAGE_DEV_SHOW_ALL_NAV__: JSON.stringify(command === "serve" && mode !== "test" && env.PASSAGE_DEV_SHOW_ALL_NAV === "1"),
    },
    server: {
      port: 5173,
      proxy: devProxy(env),
      fs: { allow: ["../warehouse-mobile/public", ...(mode === "test" ? ["../../policies/authz"] : []), "."] },
    },
    test: { environment: "jsdom", globals: true, setupFiles: ["./src/test-setup.ts"], exclude: [...configDefaults.exclude, "e2e/**"] },
  };
});
