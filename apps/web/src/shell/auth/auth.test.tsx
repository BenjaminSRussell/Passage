import { StrictMode } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import admin from "../../modules/admin/module";
import { visibleModules } from "../dutyGate";
import { AllFeaturesPage } from "../pages/AllFeaturesPage";
import { PrincipalProvider } from "../principal";
import { WorkspaceNavProvider } from "../workspaces/WorkspaceNav";
import { loadWorkspaceConfig, resolveNavEntries } from "../workspaces/model";
import { AuthGate } from "./AuthGate";
import { AuthRoot } from "./AuthRoot";
import { bootstrapAuth, createAuth, installBearerFetch, type AuthEnv } from "./auth";
import { checkAxeAA } from "../a11y";
import { authorizeUrl, challengeS256, endpoints, sessionFrom, type OidcConfig } from "./oidc";

const config: OidcConfig = { issuer: "http://localhost:8080/realms/passage", clientId: "passage-web" };
const origin = "http://localhost:5173";

function jwt(claims: Record<string, unknown>): string {
  const encode = (value: unknown) => btoa(JSON.stringify(value)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return `${encode({ alg: "RS256", kid: "k" })}.${encode(claims)}.signature`;
}

const ADMIN_DUTIES = "audit.read,numbering.write,identity.user.maintain,identity.effective-permission.view,identity.security-role.maintain,identity.legal-entity.maintain";
const CLERK_DUTIES = "receiving.receipt.maintain,ledger.journal.read";

function tokenFor(sub: string, duties: string): string {
  return jwt({ sub, tid: "tenant-1", duties });
}

function envAt(path: string, search: string, tokenResponse?: () => Response) {
  const store = new Map<string, string>();
  const assign = vi.fn();
  const replaceUrl = vi.fn();
  const fetcher = vi.fn(async () => tokenResponse?.() ?? new Response("{}", { status: 500 }));
  const env: AuthEnv = {
    fetch: fetcher,
    storage: { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => void store.set(k, v), removeItem: (k) => void store.delete(k) },
    location: { origin, pathname: path, search },
    assign,
    replaceUrl,
    now: () => 1_000_000,
  };
  return { env, store, assign, replaceUrl, fetcher };
}

describe("PKCE", () => {
  it("derives the S256 challenge from the RFC 7636 appendix B verifier", async () => {
    expect(await challengeS256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")).toBe("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
  });

  it("builds an authorization-code request with a challenge and no secret", async () => {
    const url = new URL(await authorizeUrl(config, origin, "state-1", "verifier-1"));
    expect(url.origin + url.pathname).toBe(endpoints(config.issuer).authorize);
    expect(Object.fromEntries(url.searchParams)).toMatchObject({
      response_type: "code",
      client_id: "passage-web",
      redirect_uri: `${origin}/auth/callback`,
      code_challenge_method: "S256",
      state: "state-1",
    });
    expect(url.searchParams.get("code_challenge")).toBe(await challengeS256("verifier-1"));
    expect(url.searchParams.has("client_secret")).toBe(false);
  });
});

describe("session", () => {
  it("reads subject, tenant and duties from the access token", () => {
    const session = sessionFrom({ access_token: tokenFor("user-1", ADMIN_DUTIES), expires_in: 300 }, 1000);
    expect(session.subject).toBe("user-1");
    expect(session.tenantId).toBe("tenant-1");
    expect(session.duties).toContain("numbering.write");
    expect(session.expiresAt).toBe(301_000);
  });

  it("has no duties when the token carries none", () => {
    expect(sessionFrom({ access_token: jwt({ sub: "u" }), expires_in: 60 }, 0).duties).toEqual([]);
  });
});

describe("login flow", () => {
  it("sends an anonymous visitor to Keycloak and remembers where they were", async () => {
    const { env, assign, store } = envAt("/ledger/accounts", "?q=1");
    const auth = createAuth(config, env);
    expect(await auth.ensureSession()).toBeNull();
    await auth.login();
    expect(assign).toHaveBeenCalledOnce();
    expect(JSON.parse(store.get("passage.oidc.pending") ?? "{}").returnTo).toBe("/ledger/accounts?q=1");
  });

  it("exchanges the code with the stored verifier and returns to the saved page", async () => {
    const start = envAt("/ledger/accounts", "");
    await createAuth(config, start.env).login();
    const pending = JSON.parse(start.store.get("passage.oidc.pending") ?? "{}") as { state: string; verifier: string };

    const callback = envAt("/auth/callback", `?code=abc&state=${pending.state}`, () =>
      new Response(JSON.stringify({ access_token: tokenFor("user-1", ADMIN_DUTIES), refresh_token: "r", expires_in: 300 })),
    );
    callback.store.set("passage.oidc.pending", start.store.get("passage.oidc.pending") ?? "");
    const session = await createAuth(config, callback.env).ensureSession();

    expect(session?.duties).toContain("audit.read");
    const [url, init] = callback.fetcher.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(endpoints(config.issuer).token);
    const form = new URLSearchParams(String(init.body));
    expect(form.get("code")).toBe("abc");
    expect(form.get("code_verifier")).toBe(pending.verifier);
    expect(form.get("grant_type")).toBe("authorization_code");
    expect(callback.replaceUrl).toHaveBeenCalledWith("/ledger/accounts");
    expect(callback.store.has("passage.oidc.pending")).toBe(false);
  });

  it("refuses a callback whose state this browser did not issue", async () => {
    const { env, store, fetcher } = envAt("/auth/callback", "?code=abc&state=forged");
    store.set("passage.oidc.pending", JSON.stringify({ state: "real", verifier: "v", returnTo: "/" }));
    await expect(createAuth(config, env).ensureSession()).rejects.toThrow(/did not match/);
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("refuses a callback with no pending request", async () => {
    const { env } = envAt("/auth/callback", "?code=abc&state=x");
    await expect(createAuth(config, env).ensureSession()).rejects.toThrow(/did not match/);
  });

  it("surfaces an error from Keycloak", async () => {
    const { env, store } = envAt("/auth/callback", "?error=access_denied&error_description=nope");
    store.set("passage.oidc.pending", JSON.stringify({ state: "s", verifier: "v", returnTo: "/" }));
    await expect(createAuth(config, env).ensureSession()).rejects.toThrow(/nope/);
  });

  it("completes the callback once even when the gate mounts twice", async () => {
    const { env, store, fetcher } = envAt("/auth/callback", "?code=abc&state=s", () =>
      new Response(JSON.stringify({ access_token: tokenFor("u", CLERK_DUTIES), expires_in: 300 })),
    );
    store.set("passage.oidc.pending", JSON.stringify({ state: "s", verifier: "v", returnTo: "/" }));
    const auth = createAuth(config, env);
    const [first, second] = await Promise.all([auth.ensureSession(), auth.ensureSession()]);
    expect(first).toBe(second);
    expect(fetcher).toHaveBeenCalledOnce();
  });
});

describe("logout", () => {
  it("sends the id token as a hint when this tab signed in", async () => {
    const { env, store, assign } = envAt("/auth/callback", "?code=c&state=s", () =>
      new Response(JSON.stringify({ access_token: tokenFor("u", ADMIN_DUTIES), id_token: "id.token.value", expires_in: 300 })),
    );
    store.set("passage.oidc.pending", JSON.stringify({ state: "s", verifier: "v", returnTo: "/" }));
    const auth = createAuth(config, env);
    await auth.ensureSession();
    auth.logout();
    const url = new URL(assign.mock.calls[0]?.[0] as string);
    expect(url.searchParams.get("id_token_hint")).toBe("id.token.value");
    expect(url.searchParams.get("post_logout_redirect_uri")).toBe(`${origin}/`);
  });
});

describe("bearer fetch", () => {
  it("adds the token to same-origin API calls only", async () => {
    const seen: Array<{ url: string; auth: string | null }> = [];
    const target = {
      fetch: (async (input: RequestInfo | URL, init?: RequestInit) => {
        seen.push({ url: String(input), auth: new Headers(init?.headers).get("Authorization") });
        return new Response("{}");
      }) as typeof fetch,
    };
    const { env, store } = envAt("/auth/callback", "?code=c&state=s", () =>
      new Response(JSON.stringify({ access_token: tokenFor("u", ADMIN_DUTIES), expires_in: 300 })),
    );
    store.set("passage.oidc.pending", JSON.stringify({ state: "s", verifier: "v", returnTo: "/" }));
    const auth = createAuth(config, env);
    await auth.ensureSession();
    installBearerFetch(auth, origin, target);
    await target.fetch("/api/v1/ledger/ledgers");
    await target.fetch("https://elsewhere.example/api/v1/x");
    await target.fetch("/assets/app.js");
    expect(seen.map((s) => s.auth !== null)).toEqual([true, false, false]);
    expect(seen[0]?.auth).toMatch(/^Bearer .+\..+\..+$/);
  });

  it("starts a login instead of calling the API without a token", async () => {
    const original = vi.fn(async () => new Response("{}"));
    const target = { fetch: original as unknown as typeof fetch };
    const { env, assign } = envAt("/", "");
    installBearerFetch(createAuth(config, env), origin, target);
    void target.fetch("/api/v1/ledger/ledgers");
    await waitFor(() => expect(assign).toHaveBeenCalledOnce());
    expect(original).not.toHaveBeenCalled();
  });
});

function adminNavAs(duties: readonly string[]) {
  const config = loadWorkspaceConfig();
  const entries = resolveNavEntries(visibleModules([admin], duties), config.areas);
  return render(
    <MemoryRouter>
      <PrincipalProvider duties={duties}>
        <WorkspaceNavProvider entries={entries} config={config}>
          <AllFeaturesPage />
        </WorkspaceNavProvider>
      </PrincipalProvider>
    </MemoryRouter>,
  );
}

function navLink(label: string) {
  return { name: new RegExp(`^${label} — `) };
}

describe("admin navigation follows the token's duties", () => {
  const gated = ["Audit log", "Sequences", "Alerts", "Check what a user can do", "Assign roles", "Company access", "Users", "Roles"];

  it("renders every duty-gated admin entry for an admin token", () => {
    adminNavAs(sessionFrom({ access_token: tokenFor("admin", ADMIN_DUTIES), expires_in: 300 }, 0).duties);
    for (const label of gated) expect(screen.getByRole("link", navLink(label))).toBeInTheDocument();
  });

  it("renders no duty-gated admin entry for a clerk token", () => {
    adminNavAs(sessionFrom({ access_token: tokenFor("clerk", CLERK_DUTIES), expires_in: 300 }, 0).duties);
    for (const label of gated) expect(screen.queryByRole("link", navLink(label))).not.toBeInTheDocument();
  });
});

describe("AuthGate", () => {
  it("shows nothing of the app until a session exists, then hands over the session", async () => {
    const session = sessionFrom({ access_token: tokenFor("clerk", CLERK_DUTIES), expires_in: 300 }, 0);
    const auth = { ensureSession: async () => session, accessToken: async () => "t", login: vi.fn(), logout: vi.fn() };
    render(<AuthGate auth={auth}>{(s) => <p>signed in with {s.duties.length} duties</p>}</AuthGate>);
    expect(screen.getByRole("status")).toBeInTheDocument();
    expect(await screen.findByText("signed in with 2 duties")).toBeInTheDocument();
  });

  it("starts a login when there is no session", async () => {
    const login = vi.fn(async () => undefined);
    render(<AuthGate auth={{ ensureSession: async () => null, accessToken: async () => null, login, logout: vi.fn() }}>{() => <p>app</p>}</AuthGate>);
    await waitFor(() => expect(login).toHaveBeenCalledOnce());
    expect(screen.queryByText("app")).not.toBeInTheDocument();
  });

  it("starts the login once when StrictMode mounts the gate twice", async () => {
    const login = vi.fn(async () => undefined);
    const auth = { ensureSession: async () => null, accessToken: async () => null, login, logout: vi.fn() };
    render(
      <StrictMode>
        <AuthGate auth={auth}>{() => <p>app</p>}</AuthGate>
      </StrictMode>,
    );
    await waitFor(() => expect(login).toHaveBeenCalled());
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(login).toHaveBeenCalledOnce();
  });

  it("reports a failed sign-in instead of rendering the app", async () => {
    const auth = { ensureSession: async () => Promise.reject(new Error("state mismatch")), accessToken: async () => null, login: vi.fn(), logout: vi.fn() };
    render(<AuthGate auth={auth}>{() => <p>app</p>}</AuthGate>);
    expect(await screen.findByRole("alert")).toHaveTextContent("state mismatch");
    expect(screen.queryByText("app")).not.toBeInTheDocument();
  });

  it("shows the error with a retry when starting the login rejects", async () => {
    const login = vi.fn().mockRejectedValueOnce(new Error("discovery unreachable")).mockResolvedValue(undefined);
    render(<AuthGate auth={{ ensureSession: async () => null, accessToken: async () => null, login, logout: vi.fn() }}>{() => <p>app</p>}</AuthGate>);
    expect(await screen.findByRole("alert")).toHaveTextContent("discovery unreachable");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(login).toHaveBeenCalledTimes(2));
  });
});

describe("main.tsx wiring under StrictMode", () => {
  it("creates one auth, wraps fetch once and sends the bearer token on API calls", async () => {
    const seen: Array<string | null> = [];
    const target = {
      fetch: (async (_input: RequestInfo | URL, init?: RequestInit) => {
        seen.push(new Headers(init?.headers).get("Authorization"));
        return new Response("{}");
      }) as typeof fetch,
    };
    const { env, store, assign, fetcher } = envAt("/auth/callback", "?code=c&state=s", () =>
      new Response(JSON.stringify({ access_token: tokenFor("u", CLERK_DUTIES), expires_in: 300 })),
    );
    store.set("passage.oidc.pending", JSON.stringify({ state: "s", verifier: "v", returnTo: "/" }));
    const auth = bootstrapAuth(config, env, target);
    const wrapped = target.fetch;
    bootstrapAuth(config, envAt("/", "").env, target);
    expect(target.fetch).toBe(wrapped);

    render(
      <StrictMode>
        <AuthRoot auth={auth} requireAuth>
          {(duties) => <p>app with {duties?.length} duties</p>}
        </AuthRoot>
      </StrictMode>,
    );
    expect(await screen.findByText("app with 2 duties")).toBeInTheDocument();
    await target.fetch(`${origin}/api/v1/ledger/ledgers`);

    expect(fetcher).toHaveBeenCalledOnce();
    expect(seen).toHaveLength(1);
    expect(seen[0]).toMatch(/^Bearer .+\..+\..+$/);
    expect(assign).not.toHaveBeenCalled();
  });

  it("refuses to render the app without sign-in settings when auth is required", () => {
    const { container } = render(<AuthRoot auth={null} requireAuth>{() => <p>app</p>}</AuthRoot>);
    expect(screen.getByRole("heading", { name: /not available yet/i })).toBeInTheDocument();
    expect(screen.getByText(/not available yet in this version/i)).toBeInTheDocument();
    expect(screen.queryByText("app")).not.toBeInTheDocument();
    const shellOnly = ["landmark-unique", "bypass"];
    expect(checkAxeAA(container).filter((v) => !shellOnly.includes(v.id))).toEqual([]);
  });

  it("renders the app ungated when auth is optional and unset", () => {
    render(<AuthRoot auth={null} requireAuth={false}>{() => <p>app</p>}</AuthRoot>);
    expect(screen.getByText("app")).toBeInTheDocument();
  });
});
