import type { ProxyOptions } from "vite";

type Env = Record<string, string | undefined>;

const DEFAULT_TARGET = "http://localhost:8080";
const SERVICE_URL = /^PASSAGE_SVC_(?!DEFAULT_URL$)([A-Z0-9_]+)_URL$/;

export function stripApi(path: string): string {
  return path.replace(/^\/api(?=\/)/, "");
}

export function devIdentityHeaders(env: Env): Record<string, string> {
  return {
    "X-Passage-Tenant": env.PASSAGE_DEV_TENANT ?? "00000000-0000-0000-0000-000000000001",
    "X-Passage-Legal-Entity": env.PASSAGE_DEV_LEGAL_ENTITY ?? "00000000-0000-0000-0000-0000000000e1",
    "X-Passage-User": env.PASSAGE_DEV_USER ?? "dev",
    "X-Passage-Duties": env.PASSAGE_DEV_DUTIES ?? "",
  };
}

export function devProxy(env: Env): Record<string, ProxyOptions> {
  if (env.PASSAGE_OIDC_ISSUER) return { "/api": { target: env.PASSAGE_GATEWAY_URL ?? "http://localhost:8088" } };
  const headers = devIdentityHeaders(env);
  const entry = (target: string): ProxyOptions => ({ target, headers, rewrite: stripApi });
  const proxy: Record<string, ProxyOptions> = {};
  for (const [key, url] of Object.entries(env)) {
    const match = SERVICE_URL.exec(key);
    if (match && url) proxy[`^/api/v1/${match[1].toLowerCase().replaceAll("_", "-")}(/|\\?|$)`] = entry(url);
  }
  proxy["/api"] = entry(env.PASSAGE_SVC_DEFAULT_URL ?? DEFAULT_TARGET);
  return proxy;
}
