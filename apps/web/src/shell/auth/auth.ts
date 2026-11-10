import { authorizeUrl, CALLBACK_PATH, endpoints, LOGOUT_PATH, randomToken, sessionFrom, type OidcConfig, type Session, type TokenResponse } from "./oidc";

const PENDING_KEY = "passage.oidc.pending";
const REFRESH_MARGIN_MS = 30_000;

interface Pending {
  state: string;
  verifier: string;
  returnTo: string;
}

export interface AuthEnv {
  fetch: (input: string, init?: RequestInit) => Promise<Response>;
  storage: Pick<Storage, "getItem" | "setItem" | "removeItem">;
  location: { origin: string; pathname: string; search: string };
  assign: (url: string) => void;
  replaceUrl: (url: string) => void;
  now: () => number;
}

export function browserEnv(): AuthEnv {
  return {
    fetch: (input, init) => globalThis.fetch(input, init),
    storage: window.sessionStorage,
    location: window.location,
    assign: (url) => window.location.assign(url),
    replaceUrl: (url) => window.history.replaceState(null, "", url),
    now: () => Date.now(),
  };
}

export interface Auth {
  ensureSession(): Promise<Session | null>;
  accessToken(): Promise<string | null>;
  login(): Promise<void>;
  logout(): void;
}

// Tokens live in memory only. A reload sends the browser through Keycloak, whose SSO cookie returns it at once.
export function createAuth(config: OidcConfig, env: AuthEnv): Auth {
  let session: Session | null = null;
  let refreshing: Promise<Session | null> | null = null;

  async function requestToken(form: Record<string, string>): Promise<Session> {
    const res = await env.fetch(endpoints(config.issuer).token, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
      body: new URLSearchParams({ client_id: config.clientId, ...form }).toString(),
    });
    if (!res.ok) throw new Error(`token endpoint answered ${res.status}`);
    return sessionFrom((await res.json()) as TokenResponse, env.now());
  }

  async function login() {
    const pending: Pending = {
      state: randomToken(),
      verifier: randomToken(48),
      returnTo: env.location.pathname === CALLBACK_PATH || env.location.pathname === LOGOUT_PATH ? "/" : env.location.pathname + env.location.search,
    };
    env.storage.setItem(PENDING_KEY, JSON.stringify(pending));
    env.assign(await authorizeUrl(config, env.location.origin, pending.state, pending.verifier));
  }

  function takePending(): Pending | null {
    const raw = env.storage.getItem(PENDING_KEY);
    env.storage.removeItem(PENDING_KEY);
    if (raw === null) return null;
    try {
      return JSON.parse(raw) as Pending;
    } catch {
      return null;
    }
  }

  async function completeLogin(): Promise<Session> {
    const query = new URLSearchParams(env.location.search);
    const pending = takePending();
    const failure = query.get("error");
    if (failure !== null) throw new Error(`sign-in was refused: ${query.get("error_description") ?? failure}`);
    const code = query.get("code");
    if (pending === null || code === null || query.get("state") !== pending.state) {
      throw new Error("sign-in response did not match this browser's request");
    }
    const next = await requestToken({
      grant_type: "authorization_code",
      code,
      redirect_uri: env.location.origin + CALLBACK_PATH,
      code_verifier: pending.verifier,
    });
    env.replaceUrl(pending.returnTo);
    return next;
  }

  function refresh(current: Session): Promise<Session | null> {
    refreshing ??= requestToken({ grant_type: "refresh_token", refresh_token: current.refreshToken ?? "" })
      .catch(() => null)
      .finally(() => {
        refreshing = null;
      });
    return refreshing;
  }

  async function accessToken() {
    if (session === null) return null;
    if (session.expiresAt - env.now() > REFRESH_MARGIN_MS) return session.accessToken;
    session = session.refreshToken === undefined ? null : await refresh(session);
    return session?.accessToken ?? null;
  }

  function logout() {
    const idToken = session?.idToken;
    session = null;
    const query = new URLSearchParams({ client_id: config.clientId, post_logout_redirect_uri: `${env.location.origin}/` });
    if (idToken !== undefined) query.set("id_token_hint", idToken);
    env.assign(`${endpoints(config.issuer).logout}?${query.toString()}`);
  }

  async function start() {
    if (env.location.pathname === LOGOUT_PATH) {
      logout();
      return null;
    }
    if (session === null && env.location.pathname === CALLBACK_PATH) session = await completeLogin();
    return session;
  }

  let starting: Promise<Session | null> | null = null;
  const ensureSession = () => (starting ??= start());

  return { ensureSession, accessToken, login, logout };
}

const BEARER_INSTALLED = Symbol.for("passage.bearerFetch");

export function installBearerFetch(auth: Auth, origin: string, target: { fetch: typeof fetch } = globalThis): void {
  if (BEARER_INSTALLED in target.fetch) return;
  const original = target.fetch.bind(target);
  target.fetch = async (input, init) => {
    const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const url = new URL(raw, origin);
    if (url.origin !== origin || !url.pathname.startsWith("/api/")) return original(input, init);
    const token = await auth.accessToken();
    if (token === null) {
      await auth.login();
      return new Promise<Response>(() => undefined);
    }
    const headers = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined));
    headers.set("Authorization", `Bearer ${token}`);
    return original(input, { ...init, headers });
  };
  Object.defineProperty(target.fetch, BEARER_INSTALLED, { value: true });
}
