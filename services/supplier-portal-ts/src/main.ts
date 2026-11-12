// Entry point. Environment: HTTP_ADDR (default :8080); OIDC_ISSUER, OIDC_CLIENT_ID (default passage-portal-supplier),
// OIDC_CLIENT_SECRET, OIDC_REDIRECT_URI; optionally OIDC_AUTHORIZATION_ENDPOINT, OIDC_TOKEN_ENDPOINT and OIDC_JWKS_URI
// (default to the Keycloak paths under the issuer); OIDC_END_SESSION_ENDPOINT (default: the Keycloak logout path) and
// OIDC_POST_LOGOUT_REDIRECT_URI (default: APP_ORIGIN/); APP_ORIGIN; COOKIE_SECURE=false for local http;
// SESSION_STORE_URL (redis:// or rediss:// of a Valkey or Redis server shared by the pods; unset keeps sessions in memory) with
// SESSION_STORE_PASSWORD or SESSION_STORE_PASSWORD_FILE and SESSION_STORE_CA_FILE;
// KEYCLOAK_ADMIN_URL, KEYCLOAK_REALM, KEYCLOAK_ADMIN_CLIENT_ID, KEYCLOAK_ADMIN_CLIENT_SECRET(_FILE) for creating invited users;
// PURCHASING_URL, RECEIVING_URL, PARTY_URL, PAYABLES_URL; PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE.
// Virus scan of uploads: CLAMAV_ADDR (host:port of clamd; when set the scan is required and a failed scan refuses the upload),
// CLAMAV_TIMEOUT_MS, SCAN_ALLOW_UNSCANNED=true (accept uploads when no scanner is configured; default false).
// Public registration page: REGISTRATION_TENANT_ID and REGISTRATION_LEGAL_ENTITY_ID (both, or registration is closed),
// PUBLIC_RATE_PER_HOUR (per client address, default 10). PASSAGE_TRUSTED_PROXIES: comma-separated CIDRs or addresses of the
// proxies in front (the edge) whose X-Forwarded-For is believed; empty means none and the client address is the TCP peer.

import { readFileSync } from "node:fs";
import { parseTrustedProxies, TRUSTED_PROXIES_ENV } from "./clientAddress.ts";
import { Jwks, type OidcConfig } from "./oidc.ts";
import { KeycloakProvisioner, PendingProvisioner, type UserProvisioner } from "./provision.ts";
import { scanPolicyFromEnv } from "./scan.ts";
import { createHttpServer } from "./server.ts";
import { serviceTokenSignerFromEnv } from "./serviceToken.ts";
import { MemorySessionStore, RateLimiter, type SessionStore } from "./session.ts";
import { RedisSessionStore } from "./redisStore.ts";
import { parseStoreUrl, RespClient } from "./resp.ts";

function required(env: Record<string, string | undefined>, name: string): string {
  const v = env[name];
  if (!v) throw new Error(`${name} is required`);
  return v;
}

export function oidcFromEnv(env: Record<string, string | undefined>): OidcConfig {
  const issuer = required(env, "OIDC_ISSUER").replace(/\/$/, "");
  return {
    issuer,
    clientId: env["OIDC_CLIENT_ID"] ?? "passage-portal-supplier",
    clientSecret: required(env, "OIDC_CLIENT_SECRET"),
    redirectUri: required(env, "OIDC_REDIRECT_URI"),
    authorizationEndpoint: env["OIDC_AUTHORIZATION_ENDPOINT"] ?? `${issuer}/protocol/openid-connect/auth`,
    tokenEndpoint: env["OIDC_TOKEN_ENDPOINT"] ?? `${issuer}/protocol/openid-connect/token`,
    jwksUri: env["OIDC_JWKS_URI"] ?? `${issuer}/protocol/openid-connect/certs`,
    endSessionEndpoint: env["OIDC_END_SESSION_ENDPOINT"] ?? `${issuer}/protocol/openid-connect/logout`,
    postLogoutRedirectUri: env["OIDC_POST_LOGOUT_REDIRECT_URI"] ?? `${(env["APP_ORIGIN"] ?? "").replace(/\/$/, "")}/`,
  };
}

/** The shared store when SESSION_STORE_URL is set, else this pod's memory (with a warning: a restart signs everyone out). */
export function sessionStoreFromEnv(env: Record<string, string | undefined>, readFile: (path: string) => string, warn: (msg: string) => void = console.warn): SessionStore {
  const url = env["SESSION_STORE_URL"];
  if (!url) {
    warn("SESSION_STORE_URL is not set: sessions live in this pod's memory, a restart signs everyone out and a second replica would not know them");
    return new MemorySessionStore();
  }
  const file = env["SESSION_STORE_PASSWORD_FILE"];
  const password = (file ? readFile(file).trim() : env["SESSION_STORE_PASSWORD"]) || undefined;
  const caFile = env["SESSION_STORE_CA_FILE"];
  return new RedisSessionStore(new RespClient({ ...parseStoreUrl(url, password), ca: caFile ? readFile(caFile) : undefined }), "supplier-portal:");
}

/**
 * The Keycloak admin client that creates an accepted invitation's user, when KEYCLOAK_ADMIN_URL is set (with KEYCLOAK_REALM,
 * KEYCLOAK_ADMIN_CLIENT_ID and KEYCLOAK_ADMIN_CLIENT_SECRET or _FILE); else the pending adapter that leaves it to an administrator.
 */
export function provisionerFromEnv(env: Record<string, string | undefined>, readFile: (path: string) => string, log: (entry: Record<string, unknown>) => void): UserProvisioner {
  const base = env["KEYCLOAK_ADMIN_URL"];
  if (!base) return new PendingProvisioner(log);
  const file = env["KEYCLOAK_ADMIN_CLIENT_SECRET_FILE"];
  return new KeycloakProvisioner({
    baseUrl: base,
    realm: env["KEYCLOAK_REALM"] ?? "passage",
    clientId: required(env, "KEYCLOAK_ADMIN_CLIENT_ID"),
    clientSecret: file ? readFile(file).trim() : required(env, "KEYCLOAK_ADMIN_CLIENT_SECRET"),
    portalClientId: env["OIDC_CLIENT_ID"] ?? "passage-portal-supplier",
    redirectUri: `${(env["APP_ORIGIN"] ?? "").replace(/\/$/, "")}/`,
    fetcher: fetch,
    log,
  });
}

export function listenAddress(raw: string | undefined): { host?: string; port: number } {
  const addr = raw ?? ":8080";
  const i = addr.lastIndexOf(":");
  const port = Number(addr.slice(i + 1));
  if (i < 0 || !Number.isInteger(port) || port < 0 || port > 65535) throw new TypeError(`HTTP_ADDR ${JSON.stringify(addr)} is not [host]:port`);
  const host = addr.slice(0, i);
  return host ? { host, port } : { port };
}

export function registrationFromEnv(env: Record<string, string | undefined>): { tenantId: string; legalEntityId: string } | undefined {
  const tenantId = env["REGISTRATION_TENANT_ID"];
  const legalEntityId = env["REGISTRATION_LEGAL_ENTITY_ID"];
  if (!tenantId && !legalEntityId) return undefined;
  if (!tenantId || !legalEntityId) throw new Error("REGISTRATION_TENANT_ID and REGISTRATION_LEGAL_ENTITY_ID are set together or not at all");
  return { tenantId, legalEntityId };
}

function main(): void {
  const env = process.env;
  const oidc = oidcFromEnv(env);
  const registration = registrationFromEnv(env);
  const trustedProxies = parseTrustedProxies(env[TRUSTED_PROXIES_ENV]);
  if (trustedProxies.size === 0) {
    console.log(JSON.stringify({ msg: `${TRUSTED_PROXIES_ENV} is empty: the public rate limit counts by the TCP peer, so behind a proxy every client shares one allowance` }));
  }
  const server = createHttpServer({
    oidc,
    jwks: new Jwks(oidc.jwksUri, fetch),
    fetcher: fetch,
    sessions: sessionStoreFromEnv(env, (p) => readFileSync(p, "utf8")),
    signer: serviceTokenSignerFromEnv(env, (p) => readFileSync(p, "utf8")),
    upstreams: { purchasing: required(env, "PURCHASING_URL"), receiving: required(env, "RECEIVING_URL"), party: required(env, "PARTY_URL"), payables: required(env, "PAYABLES_URL") },
    appOrigin: required(env, "APP_ORIGIN"),
    cookieSecure: env["COOKIE_SECURE"] !== "false",
    limiter: new RateLimiter(300, 60_000),
    scan: scanPolicyFromEnv(env),
    provisioner: provisionerFromEnv(env, (p) => readFileSync(p, "utf8"), (entry) => console.log(JSON.stringify(entry))),
    ...(registration ? { registration } : {}),
    publicLimiter: new RateLimiter(Number(env["PUBLIC_RATE_PER_HOUR"] ?? 10), 3_600_000),
    trustedProxies,
  });
  const { host, port } = listenAddress(env["HTTP_ADDR"]);
  server.listen(port, host, () => console.log(JSON.stringify({ msg: "supplier-portal listening", host: host ?? "", port })));
  const stop = (): void => {
    server.close(() => process.exit(0));
  };
  process.on("SIGTERM", stop);
  process.on("SIGINT", stop);
}

if (import.meta.url === `file://${process.argv[1]}`) main();
