import { afterEach, describe, expect, it } from "vitest";
import { LE_1, LE_2, signIn, startRig, type Rig } from "./helpers.ts";

let rig: Rig;
afterEach(() => rig.close());

type WithNonce = Rig & { setNonce: (n: string) => void };

async function begin(r: Rig): Promise<URL> {
  const login = await fetch(`${r.base}/bff/login`, { redirect: "manual" });
  expect(login.status).toBe(302);
  const url = new URL(login.headers.get("location") as string);
  (r as WithNonce).setNonce(url.searchParams.get("nonce") as string);
  return url;
}

describe("sign-in", () => {
  it("redirects to the identity provider with PKCE S256, a state and a nonce", async () => {
    rig = await startRig();
    const url = await begin(rig);
    expect(url.origin + url.pathname).toBe("https://keycloak.example.invalid/realms/passage/protocol/openid-connect/auth");
    expect(url.searchParams.get("response_type")).toBe("code");
    expect(url.searchParams.get("client_id")).toBe("passage-portal-customer");
    expect(url.searchParams.get("code_challenge_method")).toBe("S256");
    expect(url.searchParams.get("code_challenge")).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(url.searchParams.get("state")?.length).toBeGreaterThan(20);
    expect(url.searchParams.get("redirect_uri")).toBe("https://customer.example.invalid/bff/callback");
  });

  it("creates an HttpOnly Secure session cookie and shows who is signed in, with no token anywhere", async () => {
    rig = await startRig();
    const url = await begin(rig);
    const cb = await fetch(`${rig.base}/bff/callback?code=abc&state=${url.searchParams.get("state")}`, { redirect: "manual" });
    expect(cb.status).toBe(302);
    const cookie = cb.headers.get("set-cookie") ?? "";
    expect(cookie).toMatch(/^__Host-passage_customer=[A-Za-z0-9_-]{40,};/);
    expect(cookie).toMatch(/HttpOnly/);
    expect(cookie).toMatch(/Secure/);
    expect(cookie).toMatch(/SameSite=Lax/);
    const me = await fetch(`${rig.base}/bff/me`, { headers: { cookie: cookie.split(";")[0] as string } });
    const body = (await me.json()) as Record<string, unknown>;
    expect(body).toMatchObject({ userId: "user-1", legalEntityIds: [LE_1] });
    expect(JSON.stringify(body)).not.toContain("never-used");
    expect(body).not.toHaveProperty("customerId");
  });

  it("uses a state once", async () => {
    rig = await startRig();
    const url = await begin(rig);
    const state = url.searchParams.get("state");
    expect((await fetch(`${rig.base}/bff/callback?code=abc&state=${state}`, { redirect: "manual" })).status).toBe(302);
    expect((await fetch(`${rig.base}/bff/callback?code=abc&state=${state}`, { redirect: "manual" })).status).toBe(400);
    expect((await fetch(`${rig.base}/bff/callback?code=abc&state=unknown`, { redirect: "manual" })).status).toBe(400);
    expect((await fetch(`${rig.base}/bff/callback?state=${state}`, { redirect: "manual" })).status).toBe(400);
  });

  it("refuses an expired sign-in", async () => {
    rig = await startRig();
    const url = await begin(rig);
    rig.now.ms += 11 * 60 * 1000;
    expect((await fetch(`${rig.base}/bff/callback?code=abc&state=${url.searchParams.get("state")}`, { redirect: "manual" })).status).toBe(400);
  });

  it.each([
    ["a buyer with no customer role", { roles: ["buyer"] }],
    ["a supplier user", { roles: ["supplier-user"], vendor_id: "0b1c2d3e-0000-4000-8000-0000000000aa", customer_id: undefined }],
    ["no customer", { customer_id: undefined }],
    ["a nil customer", { customer_id: "00000000-0000-0000-0000-000000000000" }],
    ["a customer that is not an id", { customer_id: "acme" }],
    ["no tenant", { tid: undefined }],
    ["no legal entity", { le: "" }],
    ["another client", { aud: "passage-web" }],
    ["another issuer", { iss: "https://evil.example.invalid" }],
    ["an expired token", { exp: 1 }],
    ["another nonce", { nonce: "someone-elses" }],
  ])("refuses %s", async (_name, change) => {
    rig = await startRig();
    Object.assign(rig.claims, change);
    const url = await begin(rig);
    const cb = await fetch(`${rig.base}/bff/callback?code=abc&state=${url.searchParams.get("state")}`, { redirect: "manual" });
    expect(cb.status).toBe(403);
    expect(cb.headers.get("set-cookie")).toBeNull();
  });

  it("accepts a customer contact with several legal entities", async () => {
    rig = await startRig();
    rig.claims["le"] = `${LE_1}, ${LE_2}`;
    const s = await signIn(rig);
    const me = (await (await fetch(`${rig.base}/bff/me`, { headers: { cookie: s.cookie } })).json()) as { legalEntityIds: string[] };
    expect(me.legalEntityIds).toEqual([LE_1, LE_2]);
  });

  it("ends a session on logout and when it has been idle too long", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    expect((await fetch(`${rig.base}/bff/me`, { headers: { cookie: s.cookie } })).status).toBe(200);
    expect((await fetch(`${rig.base}/bff/logout`, { method: "POST", headers: { cookie: s.cookie } })).status).toBe(403);
    expect((await fetch(`${rig.base}/bff/logout`, { method: "POST", headers: { cookie: s.cookie, "x-csrf-token": s.csrf } })).status).toBe(204);
    expect((await fetch(`${rig.base}/bff/me`, { headers: { cookie: s.cookie } })).status).toBe(401);

    const again = await signIn(rig);
    rig.now.ms += 31 * 60 * 1000;
    expect((await fetch(`${rig.base}/bff/me`, { headers: { cookie: again.cookie } })).status).toBe(401);
  });
});
