/// <reference types="vitest" />
import { configDefaults } from "vitest/config";
import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import { mobileDevStub } from "./src/modules/mobile-flows/api/devStub";
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
    plugins: [react(), ...(command === "serve" && env.PASSAGE_MOBILE_STUB === "1" ? [mobileDevStub()] : [])],
    define: {
      __PASSAGE_REMOTES__: JSON.stringify(federationRemotes),
      __PASSAGE_OIDC__: JSON.stringify(
        command === "serve" && mode !== "test" && env.PASSAGE_OIDC_ISSUER
          ? { issuer: env.PASSAGE_OIDC_ISSUER, clientId: env.PASSAGE_OIDC_CLIENT_ID ?? "passage-web" }
          : null,
      ),
      __PASSAGE_DEV_DUTIES__: JSON.stringify(command === "serve" && mode !== "test" ? (env.PASSAGE_DEV_DUTIES ?? "") : ""),
      __PASSAGE_DEV_SHOW_ALL_NAV__: JSON.stringify(command === "serve" && mode !== "test" && env.PASSAGE_DEV_SHOW_ALL_NAV === "1"),
    },
    server: {
      port: 5173,
      proxy: devProxy(env),
      fs: { allow: ["../warehouse-mobile/public", ...(mode === "test" ? ["../../policies/authz", "../../services"] : []), "."] },
    },
    test: { environment: "jsdom", globals: true, setupFiles: ["./src/test-setup.ts"], exclude: [...configDefaults.exclude, "e2e/**"] },
  };
});
