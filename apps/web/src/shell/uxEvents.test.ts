import { afterEach, beforeEach, vi } from "vitest";
import type { PreferenceStorage } from "./preferenceStorage";
import { FUNNEL_RULES, UX_EVENTS_PATH, UX_OPT_OUT_KEY, UxEventClient, isOptedOut, matchFunnelRule, observeFetch, problemFields, routeTemplate, setOptOut } from "./uxEvents";

function memoryStorage(): PreferenceStorage & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return { data, read: (k) => data.get(k) ?? null, write: (k, v) => (data.set(k, v), true), remove: (k) => void data.delete(k) };
}

const DUTY = ["analytics.ux.record"];

function client(storage = memoryStorage()) {
  const fetchImpl = vi.fn<typeof fetch>(async () => new Response(null, { status: 202 }));
  const c = new UxEventClient({ fetchImpl, storage, now: () => new Date("2026-11-10T12:00:00Z") });
  c.configure({ duties: DUTY, identity: "t1.u1", release: "2026.11.1" });
  return { c, fetchImpl, storage };
}

const sent = (fetchImpl: ReturnType<typeof vi.fn>, call = 0) => JSON.parse((fetchImpl.mock.calls[call]?.[1] as RequestInit).body as string).events as Record<string, unknown>[];

describe("UX event client", () => {
  it("sends a batch with a session id and the release, and no user identity", async () => {
    const { c, fetchImpl } = client();
    c.funnelStep("procure_to_pay", "requisition_created");
    await c.flush();
    expect(fetchImpl).toHaveBeenCalledWith(UX_EVENTS_PATH, expect.objectContaining({ method: "POST" }));
    const [event] = sent(fetchImpl);
    expect(event).toMatchObject({ type: "funnel_step", flow: "procure_to_pay", step: "requisition_created", release: "2026.11.1", occurredAt: "2026-11-10T12:00:00.000Z" });
    expect(String(event?.sessionId)).toMatch(/^[A-Za-z0-9-]{8,64}$/);
    expect(JSON.stringify(event)).not.toContain("t1.u1");
  });

  it("sends nothing without the record duty", async () => {
    const { c, fetchImpl } = client();
    c.configure({ duties: ["analytics.view"], identity: "t1.u1" });
    c.funnelStep("procure_to_pay", "requisition_created");
    await c.flush();
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("sends nothing after the person opts out, and again after opting back in", async () => {
    const { c, fetchImpl, storage } = client();
    setOptOut(true, storage);
    expect(storage.data.get(UX_OPT_OUT_KEY)).toBe("1");
    expect(isOptedOut(storage)).toBe(true);
    c.funnelStep("onboarding", "site_created");
    await c.flush();
    expect(fetchImpl).not.toHaveBeenCalled();
    setOptOut(false, storage);
    c.funnelStep("onboarding", "site_created");
    await c.flush();
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it("honours the browser's do-not-track signal", () => {
    Object.defineProperty(navigator, "doNotTrack", { value: "1", configurable: true });
    try {
      expect(isOptedOut(memoryStorage())).toBe(true);
    } finally {
      Reflect.deleteProperty(navigator, "doNotTrack");
    }
  });

  it("drops validation errors whose field is not a field name", async () => {
    const { c, fetchImpl } = client();
    c.track({ type: "validation_error", route: "/x", field: "john smith@example.com" });
    c.track({ type: "validation_error", route: "/x", field: "lines[0].qty" });
    await c.flush();
    expect(sent(fetchImpl).map((e) => e.field)).toEqual(["lines[0].qty"]);
  });

  it("flushes when a batch is full and survives a failed send", async () => {
    const { c, fetchImpl } = client();
    fetchImpl.mockRejectedValueOnce(new Error("offline"));
    for (let i = 0; i < 20; i++) c.funnelStep("onboarding", "site_created");
    await vi.waitFor(() => expect(fetchImpl).toHaveBeenCalled());
    await expect(c.flush()).resolves.toBeUndefined();
  });

  it("reports the first transaction once per browser", async () => {
    const { c, fetchImpl } = client();
    c.firstTransaction();
    c.firstTransaction();
    await c.flush();
    expect(sent(fetchImpl)).toHaveLength(1);
    expect(sent(fetchImpl)[0]).toMatchObject({ flow: "onboarding", step: "first_transaction" });
  });
});

describe("funnel rules", () => {
  it("maps the procure-to-pay and onboarding writes to their steps", () => {
    const step = (method: string, path: string) => matchFunnelRule(method, path)?.step;
    expect(step("POST", "/api/v1/procurement/requisitions")).toBe("requisition_created");
    expect(step("POST", "/api/v1/procurement/requisitions/abc:submit")).toBe("requisition_submitted");
    expect(step("POST", "/api/v1/procurement/purchase-orders")).toBe("purchase_order_created");
    expect(step("POST", "/api/v1/receiving/invoices:post")).toBe("vendor_invoice_posted");
    expect(step("POST", "/api/v1/payables/payment-proposals")).toBe("payment_proposal_created");
    expect(step("POST", "/api/v1/org/legal-entities")).toBe("legal_entity_created");
    expect(step("POST", "/api/v1/product/products")).toBe("product_created");
    expect(step("GET", "/api/v1/procurement/requisitions")).toBeUndefined();
    expect(step("POST", "/api/v1/procurement/requisitions/abc:cancel")).toBeUndefined();
  });

  it("only names steps the analytics service knows", () => {
    const known: Record<string, string[]> = {
      procure_to_pay: ["requisition_created", "requisition_submitted", "purchase_order_created", "receipt_posted", "vendor_invoice_posted", "payment_proposal_created"],
      onboarding: ["legal_entity_created", "fiscal_calendar_set", "site_created", "party_added", "product_created", "first_transaction"],
    };
    for (const rule of FUNNEL_RULES) expect(known[rule.flow], `${rule.flow}/${rule.step}`).toContain(rule.step);
  });
});

describe("observeFetch", () => {
  const original = window.fetch;
  let stop: () => void;
  beforeEach(() => {
    window.fetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/refused")) {
        return new Response(JSON.stringify({ code: "COM-0001", errors: { "lines[0].qty": "Enter a quantity", description: "Required" } }), { status: 400 });
      }
      return new Response("{}", { status: 201 });
    }) as typeof fetch;
  });
  afterEach(() => {
    stop?.();
    window.fetch = original;
  });

  it("reports a successful funnel write and returns the response untouched", async () => {
    const { c, fetchImpl } = client();
    stop = observeFetch(c, () => "/purchasing/requisitions/new");
    const res = await window.fetch("/api/v1/procurement/requisitions", { method: "POST", body: "{}" });
    expect(res.status).toBe(201);
    await c.flush();
    expect(sent(fetchImpl)[0]).toMatchObject({ step: "requisition_created" });
  });

  it("reports refused fields with the page template, never the values", async () => {
    const { c, fetchImpl } = client();
    stop = observeFetch(c, () => "/purchasing/requisitions/new");
    const res = await window.fetch("/api/v1/procurement/refused", { method: "POST", body: JSON.stringify({ description: "secret" }) });
    expect(res.status).toBe(400);
    expect(await res.json()).toMatchObject({ code: "COM-0001" });
    await c.flush();
    const events = sent(fetchImpl);
    expect(events.map((e) => e.field)).toEqual(["lines[0].qty", "description"]);
    expect(events.every((e) => e.route === "/purchasing/requisitions/new")).toBe(true);
    expect(JSON.stringify(events)).not.toContain("secret");
  });

  it("does not report its own sends", async () => {
    const { c, fetchImpl } = client();
    stop = observeFetch(c);
    await window.fetch(UX_EVENTS_PATH, { method: "POST", body: "{}" });
    await c.flush();
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});

describe("helpers", () => {
  it("names a page by template, replacing ids when the page has no help entry", () => {
    expect(routeTemplate("/purchasing/requisitions/new")).toBe("/purchasing/requisitions/new");
    expect(routeTemplate("/purchasing/requisitions/0193d6a0-0000-7000-8000-000000000001")).toBe("/purchasing/requisitions/:id");
    expect(routeTemplate("/nowhere/PO-1234")).toBe("/nowhere/:id");
  });

  it("reads field names from a problem body", () => {
    expect(problemFields({ errors: { a: "x", b: "y" } })).toEqual(["a", "b"]);
    expect(problemFields({ errors: ["a"] })).toEqual([]);
    expect(problemFields(null)).toEqual([]);
  });
});
