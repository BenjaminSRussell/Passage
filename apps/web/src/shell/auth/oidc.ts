import { parseDuties } from "../principal";

export interface OidcConfig {
  issuer: string;
  clientId: string;
}

export interface Session {
  accessToken: string;
  refreshToken: string | undefined;
  idToken: string | undefined;
  expiresAt: number;
  subject: string;
  name?: string;
  tenantId: string;
  duties: readonly string[];
}

export interface TokenResponse {
  access_token: string;
  refresh_token?: string;
  id_token?: string;
  expires_in: number;
}

declare const __PASSAGE_OIDC__: OidcConfig | null | undefined;

export const CALLBACK_PATH = "/auth/callback";
export const LOGOUT_PATH = "/auth/logout";

export function configuredOidc(): OidcConfig | null {
  return typeof __PASSAGE_OIDC__ === "object" && __PASSAGE_OIDC__ !== null ? __PASSAGE_OIDC__ : null;
}

export function endpoints(issuer: string) {
  const base = `${issuer.replace(/\/+$/, "")}/protocol/openid-connect`;
  return { authorize: `${base}/auth`, token: `${base}/token`, logout: `${base}/logout` };
}

function base64Url(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export function randomToken(bytes = 32): string {
  return base64Url(crypto.getRandomValues(new Uint8Array(bytes)));
}

export async function challengeS256(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
  return base64Url(new Uint8Array(digest));
}

export async function authorizeUrl(config: OidcConfig, origin: string, state: string, verifier: string): Promise<string> {
  const query = new URLSearchParams({
    response_type: "code",
    client_id: config.clientId,
    redirect_uri: origin + CALLBACK_PATH,
    scope: "openid",
    state,
    code_challenge: await challengeS256(verifier),
    code_challenge_method: "S256",
  });
  return `${endpoints(config.issuer).authorize}?${query.toString()}`;
}

function decodeClaims(token: string): Record<string, unknown> {
  const payload = token.split(".")[1];
  if (payload === undefined) throw new Error("access token is not a JWT");
  const padded = payload.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(payload.length / 4) * 4, "=");
  const bytes = Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
  return JSON.parse(new TextDecoder().decode(bytes)) as Record<string, unknown>;
}

// Display and nav gating only: the gateway verifies the signature and OPA decides what the API allows.
export function sessionFrom(response: TokenResponse, now: number): Session {
  const claims = decodeClaims(response.access_token);
  const text = (key: string) => (typeof claims[key] === "string" ? (claims[key] as string) : "");
  return {
    accessToken: response.access_token,
    refreshToken: response.refresh_token,
    idToken: response.id_token,
    expiresAt: now + response.expires_in * 1000,
    subject: text("sub"),
    name: text("name") || text("preferred_username") || text("email") || undefined,
    tenantId: text("tid"),
    duties: parseDuties(text("duties")),
  };
}
