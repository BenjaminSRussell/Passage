import { afterEach, describe, expect, it } from "vitest";
import { ROUTES } from "../src/routes.ts";
import { APP, CUSTOMER_A, CUSTOMER_B, ID_1, LE_1, LE_2, TENANT, api, signIn, startRig, tokenClaims, type Rig } from "./helpers.ts";

let rig: Rig;
afterEach(() => rig.close());

describe("session rules on /api/v1", () => {
  it("needs a session", async () => {
    rig = await startRig();
    expect((await api(rig, undefined, "GET", "/api/v1/invoices")).status).toBe(401);
    expect((await api(rig, { cookie: "__Host-passage_customer=forged", csrf: "x" }, "GET", "/api/v1/invoices")).status).toBe(401);
    expect(rig.calls).toHaveLength(0);
  });

  it("limits a session's request rate", async () => {
    rig = await startRig({ limit: 3 });
    const s = await signIn(rig);
    const statuses = [];
    for (let i = 0; i < 5; i++) statuses.push((await api(rig, s, "GET", "/api/v1/invoices")).status);
    expect(statuses).toEqual([200, 200, 200, 429, 429]);
  });

  it("is read-only: a write is refused without CSRF and unroutable with it", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    for (const [method, path] of [
      ["POST", "/api/v1/invoices"],
      ["POST", `/api/v1/orders/${ID_1}`],
      ["PUT", `/api/v1/open-items/${ID_1}`],
      ["DELETE", `/api/v1/returns/${ID_1}`],
      ["PATCH", "/api/v1/payments"],
    ] as const) {
      const bare = await fetch(`${rig.base}${path}`, { method, headers: { cookie: s.cookie, "content-type": "application/json" }, body: "{}" });
      expect(bare.status, `${method} ${path} without csrf`).toBe(403);
      const evil = await fetch(`${rig.base}${path}`, { method, headers: { cookie: s.cookie, "x-csrf-token": s.csrf, origin: "https://evil.example.invalid" }, body: "{}" });
      expect(evil.status, `${method} ${path} from another origin`).toBe(403);
      const withCsrf = await fetch(`${rig.base}${path}`, { method, headers: { cookie: s.cookie, "x-csrf-token": s.csrf, origin: APP }, body: "{}" });
      expect(withCsrf.status, `${method} ${path} with csrf`).toBe(404);
    }
    expect(rig.calls).toHaveLength(0);
  });
});

describe("customer scoping", () => {
  it("signs the session's customer, tenant, user and legal entity, whatever the client sends", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const evil = `customer=${CUSTOMER_B}&customerId=${CUSTOMER_B}&customer_id=${CUSTOMER_B}&legalEntityId=${LE_2}&tenantId=x`;
    const res = await api(rig, s, "GET", `/api/v1/invoices?limit=5&status=POSTED&${evil}`, undefined, {
      "x-passage-tenant": "evil", "x-passage-customer": CUSTOMER_B, "x-customer-id": CUSTOMER_B, "x-passage-duties": "billing.invoice.view",
    });
    expect(res.status).toBe(200);
    const call = rig.calls[0]!;
    const url = new URL(call.url);
    expect(url.origin + url.pathname).toBe("http://billing.test/v1/billing/customer-portal/invoices");
    expect([...url.searchParams.keys()].sort()).toEqual(["limit", "status"]);
    for (const h of ["x-passage-tenant", "x-passage-customer", "x-customer-id", "x-passage-duties", "cookie"]) expect(call.headers.get(h), h).toBeNull();
    expect(tokenClaims(call.headers.get("x-passage-service-token") as string)).toMatchObject({
      iss: "customer-portal", aud: "billing", tid: TENANT, le: LE_1, act: "user-1", cus: CUSTOMER_A, duties: ["billing.customer-portal.read"],
    });
  });

  it("never lets a client name another customer on any route", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    for (const r of ROUTES) {
      const path = r.path.replace(":id", ID_1);
      const before = rig.calls.length;
      expect((await api(rig, s, "GET", `${path}?customer=${CUSTOMER_B}&customerId=${CUSTOMER_B}&legalEntityId=${LE_2}`, undefined, { "x-customer-id": CUSTOMER_B })).status, path).toBe(200);
      const call = rig.calls[before]!;
      const url = new URL(call.url);
      for (const forbidden of ["customer", "customerId", "customer_id", "legalEntityId", "tenantId"]) expect(url.searchParams.has(forbidden), `${path} ${forbidden}`).toBe(false);
      expect(JSON.stringify([...call.headers])).not.toContain(CUSTOMER_B);
      expect(tokenClaims(call.headers.get("x-passage-service-token") as string)).toMatchObject({ cus: CUSTOMER_A, le: LE_1 });
    }
  });

  it("gives two signed-in customers each their own token, never each other's", async () => {
    rig = await startRig();
    const a = await signIn(rig);
    rig.claims["customer_id"] = CUSTOMER_B;
    rig.claims["sub"] = "user-2";
    const b = await signIn(rig);
    await api(rig, a, "GET", "/api/v1/open-items");
    await api(rig, b, "GET", "/api/v1/open-items");
    await api(rig, a, "GET", `/api/v1/orders/${ID_1}`);
    const cus = rig.calls.map((c) => (tokenClaims(c.headers.get("x-passage-service-token") as string) as { cus: string }).cus);
    expect(cus).toEqual([CUSTOMER_A, CUSTOMER_B, CUSTOMER_A]);
  });

  it("maps each route to the one customer-portal path of one owning service", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const expected: Record<string, string> = {
      "/api/v1/invoices": "http://billing.test/v1/billing/customer-portal/invoices",
      [`/api/v1/invoices/${ID_1}`]: `http://billing.test/v1/billing/customer-portal/invoices/${ID_1}`,
      "/api/v1/open-items": "http://receivables.test/v1/receivables/customer-portal/open-items",
      [`/api/v1/open-items/${ID_1}`]: `http://receivables.test/v1/receivables/customer-portal/open-items/${ID_1}`,
      "/api/v1/payments": "http://receivables.test/v1/receivables/customer-portal/payments",
      "/api/v1/statement?asOf=2027-03-31": "http://receivables.test/v1/receivables/customer-portal/statement?asOf=2027-03-31",
      "/api/v1/orders": "http://sales.test/v1/sales/customer-portal/orders",
      [`/api/v1/orders/${ID_1}`]: `http://sales.test/v1/sales/customer-portal/orders/${ID_1}`,
      "/api/v1/returns": "http://sales.test/v1/sales/customer-portal/returns",
      [`/api/v1/returns/${ID_1}`]: `http://sales.test/v1/sales/customer-portal/returns/${ID_1}`,
    };
    for (const [path, upstream] of Object.entries(expected)) {
      const before = rig.calls.length;
      expect((await api(rig, s, "GET", path)).status, path).toBe(200);
      expect(rig.calls[before]!.url, path).toBe(upstream);
      expect(rig.calls[before]!.method).toBe("GET");
    }
  });

  it("asks a user with several legal entities to name one of their own", async () => {
    rig = await startRig();
    rig.claims["le"] = `${LE_1},${LE_2}`;
    const s = await signIn(rig);
    expect((await api(rig, s, "GET", "/api/v1/orders")).status).toBe(400);
    expect((await api(rig, s, "GET", "/api/v1/orders", undefined, { "x-legal-entity": "00000000-0000-4000-8000-0000000000c1" })).status).toBe(400);
    expect((await api(rig, s, "GET", "/api/v1/orders", undefined, { "x-legal-entity": "nope" })).status).toBe(400);
    expect(rig.calls).toHaveLength(0);
    expect((await api(rig, s, "GET", "/api/v1/orders", undefined, { "x-legal-entity": LE_2.toUpperCase() })).status).toBe(200);
    expect(tokenClaims(rig.calls[0]!.headers.get("x-passage-service-token") as string)["le"]).toBe(LE_2);
  });
});

describe("routing", () => {
  it("proxies nothing outside the table and nothing with a malformed id", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    for (const path of [
      "/api/v1/ledger/entries",
      "/api/v1/invoices/not-a-uuid",
      `/api/v1/invoices/${ID_1}/../../x`,
      `/api/v1/orders/${ID_1}/holds`,
      "/api/v1/purchase-orders",
      "/v1/billing/invoices",
      "/v1/receivables/open-items",
    ]) {
      expect((await api(rig, s, "GET", path)).status, path).toBe(404);
    }
    expect(rig.calls).toHaveLength(0);
  });

  it("covers every route with a distinct upstream under a customer-portal path, and only reads", () => {
    for (const r of ROUTES) {
      expect(r.upstream, r.path).toContain("/customer-portal/");
      expect(r.method).toBe("GET");
    }
    expect(new Set(ROUTES.map((r) => `${r.method} ${r.path}`)).size).toBe(ROUTES.length);
  });
});

describe("answers", () => {
  it("passes problems through, but never tells the user to sign in again when a service refuses this portal", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    rig.answer(new Response(JSON.stringify({ code: "BIL-0100", status: 404 }), { status: 404, headers: { "content-type": "application/problem+json" } }));
    const nf = await api(rig, s, "GET", `/api/v1/invoices/${ID_1}`);
    expect(nf.status).toBe(404);
    expect(((await nf.json()) as { code: string }).code).toBe("BIL-0100");
    rig.answer(new Response(JSON.stringify({ code: "AUTH-0001" }), { status: 401 }));
    const refused = await api(rig, s, "GET", "/api/v1/invoices");
    expect(refused.status).toBe(502);
    expect(await refused.text()).not.toContain("AUTH-0001");
  });

  it("does not forward a service's cookies and sets nosniff and no-store", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    rig.answer(new Response(JSON.stringify({ items: [] }), { headers: { "content-type": "application/json", "set-cookie": "x=1" } }));
    const res = await api(rig, s, "GET", "/api/v1/payments");
    expect(res.headers.get("set-cookie")).toBeNull();
    expect(res.headers.get("x-content-type-options")).toBe("nosniff");
    expect(res.headers.get("cache-control")).toBe("no-store");
  });
});
