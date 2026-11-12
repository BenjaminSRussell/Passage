import { afterEach, describe, expect, it } from "vitest";
import { APP, CUSTOMER_A, CUSTOMER_B, ID_1, LE_1, LE_2, api, signIn, startRig, tokenClaims, type Rig } from "./helpers.ts";

let rig: Rig;
afterEach(() => rig.close());

const SHIP_TO = "0c1d2e3f-0000-4000-8000-0000000000cc";

function partyAnswers(): void {
  rig.on("http://party.test/", () =>
    Response.json({ items: [{ id: CUSTOMER_A, accountNumber: "C-1", name: "Head office", relation: "self" }, { id: SHIP_TO, accountNumber: "C-2", name: "Store two", relation: "ship-to" }] }),
  );
}

describe("acting for a bill-to or ship-to account", () => {
  it("lists the accounts party names for the contact's own customer, signed with the party duty only", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    partyAnswers();
    const res = await api(rig, s, "GET", "/api/v1/accounts", undefined, { "x-customer": CUSTOMER_B });
    expect(res.status).toBe(200);
    expect(((await res.json()) as { items: unknown[] }).items).toHaveLength(2);
    const call = rig.calls[0]!;
    expect(call.url).toBe("http://party.test/v1/party/customer-portal/accounts");
    expect(tokenClaims(call.headers.get("x-passage-service-token") as string)).toMatchObject({ aud: "party", cus: CUSTOMER_A, duties: ["party.customer-portal.read"] });
  });

  it("signs the named account when party lists it, and the contact's own otherwise", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    partyAnswers();
    expect((await api(rig, s, "GET", "/api/v1/orders", undefined, { "x-customer": SHIP_TO.toUpperCase() })).status).toBe(200);
    const accounts = rig.calls.filter((c) => c.url.startsWith("http://party.test"));
    const order = rig.calls.find((c) => c.url.startsWith("http://sales.test"))!;
    expect(accounts).toHaveLength(1);
    expect(tokenClaims(accounts[0]!.headers.get("x-passage-service-token") as string)["cus"]).toBe(CUSTOMER_A);
    expect(tokenClaims(order.headers.get("x-passage-service-token") as string)).toMatchObject({ aud: "sales", cus: SHIP_TO, duties: ["sales.customer-portal.read"] });
    // The list is kept for a minute: a second call does not ask party again.
    await api(rig, s, "GET", "/api/v1/open-items", undefined, { "x-customer": SHIP_TO });
    expect(rig.calls.filter((c) => c.url.startsWith("http://party.test"))).toHaveLength(1);
    // Naming the contact's own customer, or none, never asks party.
    const before = rig.calls.length;
    await api(rig, s, "GET", "/api/v1/orders", undefined, { "x-customer": CUSTOMER_A });
    await api(rig, s, "GET", "/api/v1/orders");
    expect(rig.calls.slice(before).every((c) => c.url.startsWith("http://sales.test"))).toBe(true);
    expect(rig.calls.slice(before).map((c) => tokenClaims(c.headers.get("x-passage-service-token") as string)["cus"])).toEqual([CUSTOMER_A, CUSTOMER_A]);
  });

  it("refuses an account party does not list, and never signs it", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    partyAnswers();
    for (const bad of [CUSTOMER_B, "not-an-id"]) {
      const res = await api(rig, s, "GET", "/api/v1/orders", undefined, { "x-customer": bad });
      expect(res.status, bad).toBe(403);
    }
    expect(rig.calls.some((c) => c.url.startsWith("http://sales.test"))).toBe(false);
  });

  it("refuses an account when party cannot answer", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    rig.on("http://party.test/", () => new Response("{}", { status: 503 }));
    expect((await api(rig, s, "GET", "/api/v1/orders", undefined, { "x-customer": SHIP_TO })).status).toBe(403);
    expect(rig.calls.some((c) => c.url.startsWith("http://sales.test"))).toBe(false);
  });

  it("applies to writes: an order for a ship-to account is signed for it, and for no unlisted one", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    partyAnswers();
    const body = { requestedShipDate: "2027-03-04", lines: [{ productId: ID_1, quantity: "1" }] };
    const ok = await api(rig, s, "POST", "/api/v1/orders", body, { "idempotency-key": "submit-0001-abcd", origin: APP, "x-customer": SHIP_TO });
    expect(ok.status).toBe(200);
    const sales = rig.calls.find((c) => c.url.startsWith("http://sales.test"))!;
    expect(tokenClaims(sales.headers.get("x-passage-service-token") as string)).toMatchObject({ cus: SHIP_TO, duties: ["sales.customer-portal.order"] });
    const before = rig.calls.length;
    const bad = await api(rig, s, "POST", "/api/v1/orders", body, { "idempotency-key": "submit-0002-abcd", origin: APP, "x-customer": CUSTOMER_B });
    expect(bad.status).toBe(403);
    expect(rig.calls.slice(before).some((c) => c.url.startsWith("http://sales.test"))).toBe(false);
  });

  it("keeps each session's accounts to itself", async () => {
    rig = await startRig();
    const a = await signIn(rig);
    partyAnswers();
    expect((await api(rig, a, "GET", "/api/v1/orders", undefined, { "x-customer": SHIP_TO })).status).toBe(200);
    rig.claims["customer_id"] = CUSTOMER_B;
    rig.claims["sub"] = "user-2";
    const b = await signIn(rig);
    rig.on("http://party.test/", () => Response.json({ items: [{ id: CUSTOMER_B, relation: "self" }] }));
    expect((await api(rig, b, "GET", "/api/v1/orders", undefined, { "x-customer": SHIP_TO })).status).toBe(403);
  });
});

describe("company names", () => {
  it("reads each legal entity's display name from identity with the identity duty only, and keeps it for a while", async () => {
    rig = await startRig();
    rig.claims["le"] = `${LE_1},${LE_2}`;
    const s = await signIn(rig);
    rig.on("http://identity.test/", (c) => {
      const le = tokenClaims(c.headers.get("x-passage-service-token") as string)["le"];
      return Response.json({ legalEntityId: le, displayName: le === LE_1 ? "Cedar Ridge Pumps Inc." : "Cedar Ridge Pumps GmbH" });
    });
    const res = await api(rig, s, "GET", "/api/v1/companies");
    expect(await res.json()).toEqual({ items: [{ legalEntityId: LE_1, name: "Cedar Ridge Pumps Inc." }, { legalEntityId: LE_2, name: "Cedar Ridge Pumps GmbH" }] });
    for (const c of rig.calls) {
      expect(c.url).toBe("http://identity.test/v1/identity/customer-portal/legal-entity");
      expect(tokenClaims(c.headers.get("x-passage-service-token") as string)).toMatchObject({ aud: "identity", cus: CUSTOMER_A, duties: ["identity.customer-portal.read"] });
    }
    await api(rig, s, "GET", "/api/v1/companies");
    expect(rig.calls).toHaveLength(2);
  });

  it("answers a null name when identity cannot, so the app falls back to a numbered name", async () => {
    rig = await startRig();
    const s = await signIn(rig);
    rig.on("http://identity.test/", () => new Response("{}", { status: 503 }));
    expect(await (await api(rig, s, "GET", "/api/v1/companies")).json()).toEqual({ items: [{ legalEntityId: LE_1, name: null }] });
  });

  it("needs a session", async () => {
    rig = await startRig();
    expect((await api(rig, undefined, "GET", "/api/v1/companies")).status).toBe(401);
  });
});
