import { afterEach, describe, expect, it } from "vitest";
import { ISSUER, api, oidc, signIn, startRig, type Rig } from "./helpers.ts";

let rig: Rig;
afterEach(() => rig.close());

const EVENT = "http://schemas.openid.net/event/backchannel-logout";

function token(r: Rig, over: Record<string, unknown> = {}): string {
  const iat = Math.floor(r.now.ms / 1000);
  return r.signToken({ iss: ISSUER, aud: oidc.clientId, iat, jti: "j-1", sid: "kc-sid-1", events: { [EVENT]: {} }, ...over });
}

function post(r: Rig, logoutToken: string): Promise<Response> {
  return fetch(`${r.base}/bff/backchannel-logout`, { method: "POST", headers: { "content-type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ logout_token: logoutToken }).toString() });
}

describe("back-channel logout", () => {
  it("ends the portal session of the Keycloak session named, and only that one", async () => {
    rig = await startRig();
    const a = await signIn(rig);
    rig.claims["sid"] = "kc-sid-9";
    rig.claims["sub"] = "user-2";
    const b = await signIn(rig);
    expect((await api(rig, a, "GET", "/api/v1/orders")).status).toBe(200);
    const res = await post(rig, token(rig));
    expect(res.status).toBe(200);
    expect((await fetch(`${rig.base}/bff/me`, { headers: { cookie: a.cookie } })).status).toBe(401);
    expect((await fetch(`${rig.base}/bff/me`, { headers: { cookie: b.cookie } })).status).toBe(200);
  });

  it("ends every session of a user when the token names the user and no session", async () => {
    rig = await startRig();
    const a = await signIn(rig);
    const a2 = await signIn(rig);
    rig.claims["sub"] = "user-2";
    rig.claims["sid"] = "kc-sid-9";
    const b = await signIn(rig);
    const res = await post(rig, token(rig, { sid: undefined, sub: "user-1" }));
    expect(res.status).toBe(200);
    for (const s of [a, a2]) expect((await fetch(`${rig.base}/bff/me`, { headers: { cookie: s.cookie } })).status).toBe(401);
    expect((await fetch(`${rig.base}/bff/me`, { headers: { cookie: b.cookie } })).status).toBe(200);
  });

  it.each([
    ["another issuer", { iss: "https://evil.example.invalid" }],
    ["another client", { aud: "passage-web" }],
    ["no logout event", { events: {} }],
    ["a nonce", { nonce: "n" }],
    ["no jti", { jti: undefined }],
    ["neither sid nor sub", { sid: undefined, sub: undefined }],
    ["an old iat", { iat: 1 }],
  ])("refuses a token with %s, and ends nothing", async (_name, over) => {
    rig = await startRig();
    const s = await signIn(rig);
    expect((await post(rig, token(rig, over))).status).toBe(400);
    expect((await fetch(`${rig.base}/bff/me`, { headers: { cookie: s.cookie } })).status).toBe(200);
  });

  it("refuses a token that is not signed by the realm, malformed, or missing", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const good = token(rig);
    const forged = good.split(".").slice(0, 2).concat("AAAA").join(".");
    expect((await post(rig, forged)).status).toBe(400);
    expect((await post(rig, "not.a.jwt")).status).toBe(400);
    expect((await post(rig, "")).status).toBe(400);
    expect((await fetch(`${rig.base}/bff/backchannel-logout`, { method: "POST", body: "x".repeat(20_000) })).status).toBe(400);
    expect((await fetch(`${rig.base}/bff/me`, { headers: { cookie: s.cookie } })).status).toBe(200);
  });
});
