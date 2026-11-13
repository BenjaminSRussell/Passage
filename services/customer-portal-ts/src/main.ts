// Entry point. Environment: HTTP_ADDR (default :8080); OIDC_ISSUER, OIDC_CLIENT_ID (default passage-portal-customer),
// OIDC_CLIENT_SECRET, OIDC_REDIRECT_URI; optionally OIDC_AUTHORIZATION_ENDPOINT, OIDC_TOKEN_ENDPOINT and OIDC_JWKS_URI
// (default to the Keycloak paths under the issuer); OIDC_END_SESSION_ENDPOINT (default: the Keycloak logout path) and
// OIDC_POST_LOGOUT_REDIRECT_URI (default: APP_ORIGIN/); APP_ORIGIN; COOKIE_SECURE=false for local http;
// RECEIVABLES_URL, BILLING_URL, SALES_URL, PARTY_URL, IDENTITY_URL; PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE;
// SESSION_STORE_URL (redis:// or rediss:// of a Valkey or Redis server shared by the pods; unset keeps sessions in this pod's memory,
// which suits development and a single pod only; set, it also holds the per-session rate limit for every pod) with SESSION_STORE_PASSWORD or SESSION_STORE_PASSWORD_FILE and, for rediss:// with a private CA, SESSION_STORE_CA_FILE (PEM).

import { readFileSync } from "node:fs";
import { Jwks, type OidcConfig } from "./oidc.ts";
import { RedisRateLimiter, RedisSessionStore } from "./redisStore.ts";
import { parseStoreUrl, RespClient } from "./resp.ts";
import { createHttpServer } from "./server.ts";
import { serviceTokenSignerFromEnv } from "./serviceToken.ts";
import { MemorySessionStore, RateLimiter, type Limiter, type SessionStore } from "./session.ts";

function required(env: Record<string, string | undefined>, name: string): string {
  const v = env[name];
  if (!v) throw new Error(`${name} is required`);
  return v;
}

export function oidcFromEnv(env: Record<string, string | undefined>): OidcConfig {
  const issuer = required(env, "OIDC_ISSUER").replace(/\/$/, "");
  const appOrigin = required(env, "APP_ORIGIN").replace(/\/$/, "");
  return {
    issuer,
    clientId: env["OIDC_CLIENT_ID"] ?? "passage-portal-customer",
    clientSecret: required(env, "OIDC_CLIENT_SECRET"),
    redirectUri: required(env, "OIDC_REDIRECT_URI"),
    authorizationEndpoint: env["OIDC_AUTHORIZATION_ENDPOINT"] ?? `${issuer}/protocol/openid-connect/auth`,
    tokenEndpoint: env["OIDC_TOKEN_ENDPOINT"] ?? `${issuer}/protocol/openid-connect/token`,
    jwksUri: env["OIDC_JWKS_URI"] ?? `${issuer}/protocol/openid-connect/certs`,
    endSessionEndpoint: env["OIDC_END_SESSION_ENDPOINT"] ?? `${issuer}/protocol/openid-connect/logout`,
    postLogoutRedirectUri: env["OIDC_POST_LOGOUT_REDIRECT_URI"] ?? `${appOrigin}/`,
  };
}

/** Calls per session and minute through /api/v1. */
export const RATE_LIMIT = 300;
const RATE_WINDOW_MS = 60_000;

/**
 * The shared store when SESSION_STORE_URL is set, holding the sessions and the per-session rate limit for every pod; else this pod's
 * memory for both (with a warning: a restart signs everyone out, and each pod would count on its own).
 */
export function storesFromEnv(env: Record<string, string | undefined>, readFile: (path: string) => string, warn: (msg: string) => void = console.warn): { sessions: SessionStore; limiter: Limiter } {
  const url = env["SESSION_STORE_URL"];
  if (!url) {
    warn("SESSION_STORE_URL is not set: sessions live in this pod's memory, a restart signs everyone out and a second replica would not know them");
    return { sessions: new MemorySessionStore(), limiter: new RateLimiter(RATE_LIMIT, RATE_WINDOW_MS) };
  }
  const file = env["SESSION_STORE_PASSWORD_FILE"];
  const password = (file ? readFile(file).trim() : env["SESSION_STORE_PASSWORD"]) || undefined;
  const caFile = env["SESSION_STORE_CA_FILE"];
  const client = new RespClient({ ...parseStoreUrl(url, password), ca: caFile ? readFile(caFile) : undefined });
  return { sessions: new RedisSessionStore(client), limiter: new RedisRateLimiter(client, RATE_LIMIT, RATE_WINDOW_MS) };
}

/** The session store of storesFromEnv. */
export function sessionStoreFromEnv(env: Record<string, string | undefined>, readFile: (path: string) => string, warn: (msg: string) => void = console.warn): SessionStore {
  return storesFromEnv(env, readFile, warn).sessions;
}

export function listenAddress(raw: string | undefined): { host?: string; port: number } {
  const addr = raw ?? ":8080";
  const i = addr.lastIndexOf(":");
  const port = Number(addr.slice(i + 1));
  if (i < 0 || !Number.isInteger(port) || port < 0 || port > 65535) throw new TypeError(`HTTP_ADDR ${JSON.stringify(addr)} is not [host]:port`);
  const host = addr.slice(0, i);
  return host ? { host, port } : { port };
}

function main(): void {
  const env = process.env;
  const oidc = oidcFromEnv(env);
  const { sessions, limiter } = storesFromEnv(env, (p) => readFileSync(p, "utf8"));
  const server = createHttpServer({
    oidc,
    jwks: new Jwks(oidc.jwksUri, fetch),
    fetcher: fetch,
    sessions,
    signer: serviceTokenSignerFromEnv(env, (p) => readFileSync(p, "utf8")),
    upstreams: {
      receivables: required(env, "RECEIVABLES_URL"),
      billing: required(env, "BILLING_URL"),
      sales: required(env, "SALES_URL"),
      party: required(env, "PARTY_URL"),
      identity: required(env, "IDENTITY_URL"),
    },
    appOrigin: required(env, "APP_ORIGIN"),
    cookieSecure: env["COOKIE_SECURE"] !== "false",
    limiter,
  });
  const { host, port } = listenAddress(env["HTTP_ADDR"]);
  server.listen(port, host, () => console.log(JSON.stringify({ msg: "customer-portal listening", host: host ?? "", port })));
  const stop = (): void => {
    server.close(() => process.exit(0));
  };
  process.on("SIGTERM", stop);
  process.on("SIGINT", stop);
}

if (import.meta.url === `file://${process.argv[1]}`) main();
