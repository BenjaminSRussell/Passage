import { afterEach, describe, expect, it } from "vitest";
import { APP, CUSTOMER_A, CUSTOMER_B, ID_1, LE_1, LE_2, TENANT, api, signIn, startRig, tokenClaims, type Rig, type Signed } from "./helpers.ts";

let rig: Rig;
afterEach(() => rig.close());

const KEY = "submit-0001-abcd";
const ORDER = { requestedShipDate: "2027-03-04", externalRef: "PO-77", lines: [{ productId: ID_1, quantity: "3" }] };
const RETURN = { originalInvoiceId: ID_1, returnReasonCode: "DAMAGED", lines: [{ productId: ID_1, quantity: "1" }] };

function post(s: Signed, path: string, body: unknown, extra: Record<string, string> = {}): Promise<Response> {
  return api(rig, s, "POST", path, body, { "idempotency-key": KEY, origin: APP, ...extra });
}

describe("placing an order", () => {
  it("is one call to sales holding only the order duty, for the session's customer, with the client's key and body", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    rig.answer(new Response(JSON.stringify({ id: "o1", orderNumber: "SO-1" }), { status: 201, headers: { "content-type": "application/json" } }));
    const res = await post(s, "/api/v1/orders", ORDER);
    expect(res.status).toBe(201);
    expect(((await res.json()) as { orderNumber: string }).orderNumber).toBe("SO-1");
    expect(rig.calls).toHaveLength(1);
    const call = rig.calls[0]!;
    expect(call.method).toBe("POST");
    expect(call.url).toBe("http://sales.test/v1/sales/customer-portal/orders");
    expect(call.headers.get("idempotency-key")).toBe(KEY);
    expect(JSON.parse(call.body)).toEqual(ORDER);
    expect(tokenClaims(call.headers.get("x-passage-service-token") as string)).toMatchObject({
      iss: "customer-portal", aud: "sales", tid: TENANT, le: LE_1, act: "user-1", cus: CUSTOMER_A, duties: ["sales.customer-portal.order"],
    });
    for (const h of ["cookie", "x-csrf-token", "origin", "x-passage-duties"]) expect(call.headers.get(h), h).toBeNull();
  });

  it("is a return of one's own invoice, holding only the return duty", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const res = await post(s, "/api/v1/returns", RETURN);
    expect(res.status).toBe(200);
    const call = rig.calls[0]!;
    expect(call.url).toBe("http://sales.test/v1/sales/customer-portal/returns");
    expect(tokenClaims(call.headers.get("x-passage-service-token") as string)).toMatchObject({ cus: CUSTOMER_A, duties: ["sales.customer-portal.return"] });
  });

  it("is refused without a session, CSRF token, same-origin request, key or JSON body, before sales is called", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const headers = { "content-type": "application/json", "idempotency-key": KEY };
    expect((await fetch(`${rig.base}/api/v1/orders`, { method: "POST", headers, body: JSON.stringify(ORDER) })).status).toBe(401);
    expect((await fetch(`${rig.base}/api/v1/orders`, { method: "POST", headers: { ...headers, cookie: s.cookie }, body: JSON.stringify(ORDER) })).status).toBe(403);
    expect((await post(s, "/api/v1/orders", ORDER, { origin: "https://evil.example.invalid" })).status).toBe(403);
    expect((await post(s, "/api/v1/orders", ORDER, { "idempotency-key": "short" })).status).toBe(400);
    expect((await post(s, "/api/v1/orders", ORDER, { "idempotency-key": "has spaces in it!" })).status).toBe(400);
    const noKey = await fetch(`${rig.base}/api/v1/orders`, { method: "POST", headers: { cookie: s.cookie, "x-csrf-token": s.csrf, "content-type": "application/json" }, body: JSON.stringify(ORDER) });
    expect(noKey.status).toBe(400);
    const text = await fetch(`${rig.base}/api/v1/orders`, { method: "POST", headers: { cookie: s.cookie, "x-csrf-token": s.csrf, "content-type": "text/plain", "idempotency-key": KEY }, body: JSON.stringify(ORDER) });
    expect(text.status).toBe(415);
    for (const bad of ["not json", "[]", "null", "\"x\""]) {
      const r = await fetch(`${rig.base}/api/v1/orders`, { method: "POST", headers: { cookie: s.cookie, "x-csrf-token": s.csrf, "content-type": "application/json", "idempotency-key": KEY }, body: bad });
      expect(r.status, bad).toBe(400);
    }
    expect(rig.calls).toHaveLength(0);
  });

  it("refuses a body that names a customer, a legal entity, a tenant or a price", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    for (const field of ["customerId", "customer", "customer_id", "legalEntityId", "tenantId", "invoiceAccountId", "unitPrice"]) {
      const res = await post(s, "/api/v1/orders", { ...ORDER, [field]: CUSTOMER_B });
      expect(res.status, field).toBe(400);
      expect(await res.text()).toContain(field);
    }
    expect(rig.calls).toHaveLength(0);
  });

  it("refuses a body over 64 KiB", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    const res = await post(s, "/api/v1/orders", { ...ORDER, externalRef: "x".repeat(70_000) });
    expect(res.status).toBe(413);
    expect(rig.calls).toHaveLength(0);
  });

  it("passes sales' refusals through and never reads a service 401 as the contact being signed out", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    rig.answer(new Response(JSON.stringify({ code: "SAL-CUS-002", status: 422 }), { status: 422, headers: { "content-type": "application/problem+json" } }));
    const refused = await post(s, "/api/v1/orders", ORDER);
    expect(refused.status).toBe(422);
    expect(((await refused.json()) as { code: string }).code).toBe("SAL-CUS-002");
    rig.answer(new Response("{}", { status: 401 }));
    expect((await post(s, "/api/v1/orders", ORDER)).status).toBe(502);
  });

  it("needs a legal entity of the user's own when there are several", async () => {
    rig = await startRig();
    rig.claims["le"] = `${LE_1},${LE_2}`;
    const s = await signIn(rig);
    expect((await post(s, "/api/v1/orders", ORDER)).status).toBe(400);
    expect((await post(s, "/api/v1/orders", ORDER, { "x-legal-entity": LE_2 })).status).toBe(200);
    expect(tokenClaims(rig.calls[0]!.headers.get("x-passage-service-token") as string)["le"]).toBe(LE_2);
  });

  it("counts toward the session's rate limit", async () => {
    rig = await startRig({ limit: 2 });
    const s = await signIn(rig);
    expect([(await post(s, "/api/v1/orders", ORDER)).status, (await post(s, "/api/v1/orders", ORDER)).status, (await post(s, "/api/v1/orders", ORDER)).status]).toEqual([200, 200, 429]);
  });
});
