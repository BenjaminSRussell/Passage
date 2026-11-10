import { afterEach, describe, expect, it } from "vitest";
import { ROUTES } from "../src/routes.ts";
import { APP, LE_1, LE_2, PO, TENANT, VENDOR_A, api, signIn, startRig, tokenClaims, type Rig } from "./helpers.ts";

let rig: Rig;
afterEach(() => rig.close());

describe("session and CSRF rules on /api/v1", () => {
  it("needs a session", async () => {
    rig = await startRig();
    expect((await api(rig, undefined, "GET", "/api/v1/purchase-orders")).status).toBe(401);
    expect((await api(rig, { cookie: "__Host-passage_supplier=forged", csrf: "x" }, "GET", "/api/v1/purchase-orders")).status).toBe(401);
    expect(rig.calls).toHaveLength(0);
  });

  it("refuses a write without the CSRF token or from another origin", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const body = { outcome: "ACCEPTED", versionNo: 1 };
    const post = (headers: Record<string, string>) =>
      fetch(`${rig.base}/api/v1/purchase-orders/${PO}/acknowledgements`, { method: "POST", headers: { cookie: s.cookie, "content-type": "application/json", ...headers }, body: JSON.stringify(body) });
    expect((await post({})).status).toBe(403);
    expect((await post({ "x-csrf-token": "wrong" })).status).toBe(403);
    expect((await post({ "x-csrf-token": s.csrf, origin: "https://evil.example.invalid" })).status).toBe(403);
    expect(rig.calls).toHaveLength(0);
    expect((await post({ "x-csrf-token": s.csrf, origin: APP })).status).toBe(200);
    expect(rig.calls).toHaveLength(1);
  });

  it("limits a session's request rate", async () => {
    rig = await startRig({ limit: 3 });
    const s = await signIn(rig);
    const statuses = [];
    for (let i = 0; i < 5; i++) statuses.push((await api(rig, s, "GET", "/api/v1/purchase-orders")).status);
    expect(statuses).toEqual([200, 200, 200, 429, 429]);
  });
});

describe("vendor scoping", () => {
  it("signs the session's vendor, tenant, user and legal entity, whatever the client sends", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const res = await api(rig, s, "GET", `/api/v1/purchase-orders?limit=5&vendorId=${VENDOR_A.replace("aa", "bb")}&legalEntityId=${LE_2}&tenantId=x`, undefined, { "x-passage-tenant": "evil", "x-passage-vendor": "evil" });
    expect(res.status).toBe(200);
    const call = rig.calls[0]!;
    const url = new URL(call.url);
    expect(url.origin + url.pathname).toBe("http://purchasing.test/v1/procurement/vendor/purchase-orders");
    expect([...url.searchParams.keys()]).toEqual(["limit"]);
    expect(call.headers.get("x-passage-tenant")).toBeNull();
    expect(call.headers.get("x-passage-vendor")).toBeNull();
    expect(call.headers.get("cookie")).toBeNull();
    expect(tokenClaims(call.headers.get("x-passage-service-token") as string)).toMatchObject({ iss: "supplier-portal", aud: "purchasing", tid: TENANT, le: LE_1, act: "user-1", vnd: VENDOR_A });
  });

  it("strips vendor and tenant from bodies and sets the legal entity from the session", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const res = await api(rig, s, "POST", "/api/v1/asns", { vendorId: "evil", legalEntityId: LE_2, tenantId: "evil", purchaseOrderId: PO, trackingNumber: "T1", lines: [{ poLineNo: 1, qty: "5" }] });
    expect(res.status).toBe(200);
    const call = rig.calls[0]!;
    expect(call.url).toBe("http://receiving.test/v1/receiving/vendor/asns");
    expect(JSON.parse(call.body)).toEqual({ legalEntityId: LE_1, purchaseOrderId: PO, trackingNumber: "T1", lines: [{ poLineNo: 1, qty: "5" }] });
    expect(call.headers.get("idempotency-key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(tokenClaims(call.headers.get("x-passage-service-token") as string)).toMatchObject({ aud: "receiving", vnd: VENDOR_A });
  });

  it("keeps the client's Idempotency-Key", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    await api(rig, s, "POST", "/api/v1/requests", { kind: "OTHER", subject: "s", details: "d" }, { "idempotency-key": "my-key" });
    expect(rig.calls[0]!.headers.get("idempotency-key")).toBe("my-key");
    expect(rig.calls[0]!.url).toBe("http://party.test/v1/party/vendor-portal/requests");
  });

  it("asks a user with several legal entities to name one of their own", async () => {
    rig = await startRig();
    rig.claims["le"] = `${LE_1},${LE_2}`;
    const s = await signIn(rig);
    expect((await api(rig, s, "GET", "/api/v1/asns")).status).toBe(400);
    expect((await api(rig, s, "GET", "/api/v1/asns", undefined, { "x-legal-entity": "00000000-0000-4000-8000-0000000000c1" })).status).toBe(400);
    expect((await api(rig, s, "GET", "/api/v1/asns", undefined, { "x-legal-entity": "nope" })).status).toBe(400);
    expect(rig.calls).toHaveLength(0);
    expect((await api(rig, s, "GET", "/api/v1/asns", undefined, { "x-legal-entity": LE_2.toUpperCase() })).status).toBe(200);
    expect(tokenClaims(rig.calls[0]!.headers.get("x-passage-service-token") as string)["le"]).toBe(LE_2);
  });
});

describe("routing", () => {
  it("proxies nothing outside the table and nothing with a malformed id", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    for (const [method, path] of [
      ["GET", "/api/v1/ledger/entries"],
      ["GET", "/api/v1/purchase-orders/not-a-uuid"],
      ["GET", `/api/v1/purchase-orders/${PO}/../../x`],
      ["POST", `/api/v1/purchase-orders/${PO}`],
      ["GET", "/v1/procurement/purchase-orders"],
    ] as const) {
      expect((await api(rig, s, method, path, method === "POST" ? {} : undefined)).status, `${method} ${path}`).toBe(404);
    }
    expect(rig.calls).toHaveLength(0);
  });

  it("covers every route with a distinct upstream and a service-only vendor-portal path", () => {
    for (const r of ROUTES) expect(r.upstream, r.path).toMatch(/\/vendor(-portal)?\//);
    expect(new Set(ROUTES.map((r) => `${r.method} ${r.path}`)).size).toBe(ROUTES.length);
  });

  it("rejects bodies that are not JSON objects", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const send = (body: string) => fetch(`${rig.base}/api/v1/requests`, { method: "POST", headers: { cookie: s.cookie, "x-csrf-token": s.csrf, "content-type": "application/json" }, body });
    expect((await send("{not json")).status).toBe(400);
    expect((await send("[1]")).status).toBe(400);
    expect(rig.calls).toHaveLength(0);
  });
});

describe("answers", () => {
  it("passes problems through, but never tells the user to sign in again when a service refuses this portal", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    rig.answer(new Response(JSON.stringify({ code: "PRC-NF-001", status: 404 }), { status: 404, headers: { "content-type": "application/problem+json" } }));
    const nf = await api(rig, s, "GET", `/api/v1/purchase-orders/${PO}`);
    expect(nf.status).toBe(404);
    expect(((await nf.json()) as { code: string }).code).toBe("PRC-NF-001");
    rig.answer(new Response(JSON.stringify({ code: "AUTH-0001" }), { status: 401 }));
    const refused = await api(rig, s, "GET", "/api/v1/purchase-orders");
    expect(refused.status).toBe(502);
    expect(await refused.text()).not.toContain("AUTH-0001");
  });

  it("streams a document with its type, name and digest", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    rig.answer(new Response(Buffer.from("%PDF-1.4"), { headers: { "content-type": "application/pdf", "content-disposition": 'attachment; filename="a.pdf"', "x-content-sha256": "ab", "set-cookie": "x=1" } }));
    const res = await api(rig, s, "GET", `/api/v1/documents/${PO}/content`);
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("application/pdf");
    expect(res.headers.get("content-disposition")).toContain("a.pdf");
    expect(res.headers.get("x-content-sha256")).toBe("ab");
    expect(res.headers.get("set-cookie")).toBeNull();
    expect(res.headers.get("x-content-type-options")).toBe("nosniff");
    expect(Buffer.from(await res.arrayBuffer()).toString()).toBe("%PDF-1.4");
  });
});
