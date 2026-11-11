// Entry point. Environment: HTTP_ADDR (default :8080); OIDC_ISSUER, OIDC_CLIENT_ID (default passage-portal-customer),
// OIDC_CLIENT_SECRET, OIDC_REDIRECT_URI; optionally OIDC_AUTHORIZATION_ENDPOINT, OIDC_TOKEN_ENDPOINT and OIDC_JWKS_URI
// (default to the Keycloak paths under the issuer); APP_ORIGIN; COOKIE_SECURE=false for local http;
// RECEIVABLES_URL, BILLING_URL, SALES_URL; PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE.

import { readFileSync } from "node:fs";
import { Jwks, type OidcConfig } from "./oidc.ts";
import { createHttpServer } from "./server.ts";
import { serviceTokenSignerFromEnv } from "./serviceToken.ts";
import { RateLimiter, SessionStore } from "./session.ts";

function required(env: Record<string, string | undefined>, name: string): string {
  const v = env[name];
  if (!v) throw new Error(`${name} is required`);
  return v;
}

export function oidcFromEnv(env: Record<string, string | undefined>): OidcConfig {
  const issuer = required(env, "OIDC_ISSUER").replace(/\/$/, "");
  return {
    issuer,
    clientId: env["OIDC_CLIENT_ID"] ?? "passage-portal-customer",
    clientSecret: required(env, "OIDC_CLIENT_SECRET"),
    redirectUri: required(env, "OIDC_REDIRECT_URI"),
    authorizationEndpoint: env["OIDC_AUTHORIZATION_ENDPOINT"] ?? `${issuer}/protocol/openid-connect/auth`,
    tokenEndpoint: env["OIDC_TOKEN_ENDPOINT"] ?? `${issuer}/protocol/openid-connect/token`,
    jwksUri: env["OIDC_JWKS_URI"] ?? `${issuer}/protocol/openid-connect/certs`,
  };
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
  const server = createHttpServer({
    oidc,
    jwks: new Jwks(oidc.jwksUri, fetch),
    fetcher: fetch,
    sessions: new SessionStore(),
    signer: serviceTokenSignerFromEnv(env, (p) => readFileSync(p, "utf8")),
    upstreams: { receivables: required(env, "RECEIVABLES_URL"), billing: required(env, "BILLING_URL"), sales: required(env, "SALES_URL") },
    appOrigin: required(env, "APP_ORIGIN"),
    cookieSecure: env["COOKIE_SECURE"] !== "false",
    limiter: new RateLimiter(300, 60_000),
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
