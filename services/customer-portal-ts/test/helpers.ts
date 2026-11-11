import { generateKeyPairSync, sign, randomBytes, type KeyObject } from "node:crypto";
import type { AddressInfo } from "node:net";
import type { Server } from "node:http";
import { Jwks, type OidcConfig } from "../src/oidc.ts";
import { createHttpServer } from "../src/server.ts";
import { createServiceTokenSigner } from "../src/serviceToken.ts";
import { RateLimiter, SessionStore } from "../src/session.ts";

export const TENANT = "11111111-1111-4111-8111-111111111111";
export const CUSTOMER_A = "0c1d2e3f-0000-4000-8000-0000000000aa";
export const CUSTOMER_B = "0c1d2e3f-0000-4000-8000-0000000000bb";
export const LE_1 = "00000000-0000-4000-8000-0000000000a1";
export const LE_2 = "00000000-0000-4000-8000-0000000000b1";
export const ID_1 = "22222222-2222-4222-8222-222222222222";
export const ISSUER = "https://keycloak.example.invalid/realms/passage";
export const APP = "https://customer.example.invalid";

export const oidc: OidcConfig = {
  issuer: ISSUER,
  clientId: "passage-portal-customer",
  clientSecret: "secret",
  redirectUri: `${APP}/bff/callback`,
  authorizationEndpoint: `${ISSUER}/protocol/openid-connect/auth`,
  tokenEndpoint: `${ISSUER}/protocol/openid-connect/token`,
  jwksUri: `${ISSUER}/protocol/openid-connect/certs`,
};

export interface Call {
  url: string;
  method: string;
  headers: Headers;
  body: string;
}

export interface Rig {
  base: string;
  server: Server;
  calls: Call[];
  /** What the next upstream call (receivables, billing, sales) answers; default 200 {}. */
  answer: (r: Response) => void;
  /** Claims the fake identity provider puts in the next ID token. */
  claims: Record<string, unknown>;
  seedBase64: string;
  now: { ms: number };
  close: () => Promise<void>;
}

export function idToken(key: KeyObject, claims: Record<string, unknown>, kid = "k1", alg = "RS256"): string {
  const h = Buffer.from(JSON.stringify({ alg, kid })).toString("base64url");
  const p = Buffer.from(JSON.stringify(claims)).toString("base64url");
  return `${h}.${p}.${sign("RSA-SHA256", Buffer.from(`${h}.${p}`), key).toString("base64url")}`;
}

export async function startRig(opts: { limit?: number } = {}): Promise<Rig> {
  const { privateKey, publicKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
  const jwk = { ...publicKey.export({ format: "jwk" }), kid: "k1", use: "sig" };
  const seedBase64 = randomBytes(32).toString("base64");
  const now = { ms: Date.UTC(2026, 10, 10, 12) };
  const calls: Call[] = [];
  let next: Response | undefined;
  const rig = { calls, now, seedBase64, claims: {} as Record<string, unknown> } as Rig;
  rig.answer = (r) => {
    next = r;
  };
  let lastNonce = "";
  const fetcher = async (url: string, init?: RequestInit): Promise<Response> => {
    if (url === oidc.jwksUri) return Response.json({ keys: [jwk] });
    if (url === oidc.tokenEndpoint) {
      const claims = { iss: ISSUER, aud: oidc.clientId, exp: Math.floor(now.ms / 1000) + 300, iat: Math.floor(now.ms / 1000), nonce: lastNonce, ...rig.claims };
      return Response.json({ id_token: idToken(privateKey, claims), access_token: "never-used" });
    }
    calls.push({ url, method: init?.method ?? "GET", headers: new Headers(init?.headers), body: typeof init?.body === "string" ? init.body : "" });
    const r = next ?? Response.json({});
    next = undefined;
    return r;
  };
  const sessions = new SessionStore();
  const server = createHttpServer({
    oidc,
    jwks: new Jwks(oidc.jwksUri, fetcher),
    fetcher,
    sessions,
    signer: createServiceTokenSigner({ seedBase64, now: () => now.ms }),
    upstreams: { receivables: "http://receivables.test", billing: "http://billing.test", sales: "http://sales.test" },
    appOrigin: APP,
    cookieSecure: true,
    limiter: new RateLimiter(opts.limit ?? 1000, 60_000),
    now: () => now.ms,
    log: () => {},
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  rig.server = server;
  rig.base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  rig.claims = { sub: "user-1", tid: TENANT, customer_id: CUSTOMER_A, le: LE_1, roles: ["customer-user"], name: "Pat Buyer" };
  rig.close = () => new Promise((resolve) => server.close(() => resolve()));
  (rig as Rig & { setNonce: (n: string) => void }).setNonce = (n) => {
    lastNonce = n;
  };
  return rig;
}

export interface Signed {
  cookie: string;
  csrf: string;
}

/** Signs in through /bff/login and /bff/callback and returns the session cookie and CSRF token. */
export async function signIn(rig: Rig): Promise<Signed> {
  const login = await fetch(`${rig.base}/bff/login`, { redirect: "manual" });
  const url = new URL(login.headers.get("location") as string);
  (rig as Rig & { setNonce: (n: string) => void }).setNonce(url.searchParams.get("nonce") as string);
  const cb = await fetch(`${rig.base}/bff/callback?code=abc&state=${url.searchParams.get("state")}`, { redirect: "manual" });
  const cookie = (cb.headers.get("set-cookie") ?? "").split(";")[0] ?? "";
  if (cb.status !== 302 || !cookie) throw new Error(`sign-in failed: ${cb.status} ${await cb.text()}`);
  const me = await fetch(`${rig.base}/bff/me`, { headers: { cookie } });
  return { cookie, csrf: ((await me.json()) as { csrfToken: string }).csrfToken };
}

export function api(rig: Rig, s: Signed | undefined, method: string, path: string, body?: unknown, extra: Record<string, string> = {}): Promise<Response> {
  const headers: Record<string, string> = { ...extra };
  if (s) headers["cookie"] = s.cookie;
  if (s && method !== "GET") headers["x-csrf-token"] = s.csrf;
  if (body !== undefined) headers["content-type"] = "application/json";
  return fetch(`${rig.base}${path}`, { method, headers, ...(body !== undefined ? { body: JSON.stringify(body) } : {}) });
}

export function tokenClaims(token: string): Record<string, unknown> {
  return JSON.parse(Buffer.from(token.split(".")[0] as string, "base64url").toString("utf8")) as Record<string, unknown>;
}
