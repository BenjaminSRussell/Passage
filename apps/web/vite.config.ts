/// <reference types="vitest" />
import { configDefaults } from "vitest/config";
import { defineConfig, loadEnv } from "vite";
import react from "@vitejs/plugin-react";
import { mobileDevStub } from "./src/modules/mobile-flows/api/devStub";
import { federationRemotes } from "./src/shell/federation";
import { devIdentityHeaders, devProxy } from "./src/shell/devProxy";
import authzCatalog from "../../policies/authz/data.json";

/** Every duty in the authorization catalog: the demo build's single user holds them all. */
function allDuties(): string {
  return Object.keys(authzCatalog.duties).join(",");
}

/**
 * Wave 5 owns the shell host. Module federation remotes are declared in
 * src/shell/federation.ts. The @module-federation/vite plugin is a lockfile
 * Need; discovery in tests and `vite` uses import.meta.glob.
 */
/** PASSAGE_IDLE_TIMEOUT_MINUTES: a whole number of minutes, 1 to 480; anything else (or unset) is off. A bad value stops the build. */
function idleMinutes(raw: string | undefined): number {
  if (raw === undefined || raw === "") return 0;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 0 || n > 480) throw new Error(`PASSAGE_IDLE_TIMEOUT_MINUTES must be a whole number of minutes from 0 to 480, got ${raw}`);
  return n;
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
        __PASSAGE_OIDC__: JSON.stringify(null),
        __PASSAGE_IDLE_TIMEOUT_MINUTES__: JSON.stringify(0),
        __PASSAGE_DEV_DUTIES__: JSON.stringify(allDuties()),
        __PASSAGE_DEV_IDENTITY__: JSON.stringify(`${devIdentityHeaders({})["X-Passage-Tenant"]}.${devIdentityHeaders({})["X-Passage-User"]}`),
        __PASSAGE_DEV_SHOW_ALL_NAV__: JSON.stringify(true),
      },
      server: { port: 5174 },
    };
  }
  return {
    plugins: [react(), ...(command === "serve" && env.PASSAGE_MOBILE_STUB === "1" ? [mobileDevStub()] : [])],
    define: {
      __PASSAGE_REMOTES__: JSON.stringify(federationRemotes),
      __PASSAGE_OIDC__: JSON.stringify(
        command === "serve" && mode !== "test" && env.PASSAGE_OIDC_ISSUER
          ? { issuer: env.PASSAGE_OIDC_ISSUER, clientId: env.PASSAGE_OIDC_CLIENT_ID ?? "passage-web" }
          : null,
      ),
      // Idle sign-out in minutes (#606): the cloud profile's idleTimeoutMinutes, 15 under the hipaa overlay. 0 turns it off.
      // Build with PASSAGE_IDLE_TIMEOUT_MINUTES=$(python3 deploy/cloud-profiles/cloudprofile.py idle-timeout <profile>).
      __PASSAGE_IDLE_TIMEOUT_MINUTES__: JSON.stringify(mode === "test" ? 0 : idleMinutes(env.PASSAGE_IDLE_TIMEOUT_MINUTES)),
      __PASSAGE_DEV_DUTIES__: JSON.stringify(command === "serve" && mode !== "test" ? (env.PASSAGE_DEV_DUTIES ?? "") : ""),
      __PASSAGE_DEV_IDENTITY__: JSON.stringify(command === "serve" && mode !== "test" ? `${devIdentityHeaders(env)["X-Passage-Tenant"]}.${devIdentityHeaders(env)["X-Passage-User"]}` : ""),
      __PASSAGE_DEV_SHOW_ALL_NAV__: JSON.stringify(command === "serve" && mode !== "test" && env.PASSAGE_DEV_SHOW_ALL_NAV === "1"),
    },
    server: {
      port: 5173,
      proxy: devProxy(env),
      fs: { allow: ["../warehouse-mobile/public", "../technician/public", ...(mode === "test" ? ["../../policies/authz", "../../services", "../../docs/user"] : []), "."] },
    },
    test: { environment: "jsdom", globals: true, setupFiles: ["./src/test-setup.ts"], exclude: [...configDefaults.exclude, "e2e/**"] },
  };
});
