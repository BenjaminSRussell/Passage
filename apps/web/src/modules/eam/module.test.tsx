import { afterEach, describe, expect, it, vi } from "vitest";
import { loadModules } from "../registry";
import { assetKpis, convertRequest, EamApiError, transitionWorkOrder } from "./api/client";
import { addMoney, money, nextStatuses } from "./format";
import eam from "./module";

describe("eam module", () => {
  it("registers a list, a create form and a detail page per entity, gated by eam.inquire", () => {
    expect(eam.id).toBe("eam");
    const paths = eam.routes.map((r) => r.path);
    for (const path of [
      "/eam",
      "/eam/assets",
      "/eam/assets/new",
      "/eam/assets/:id",
      "/eam/plans",
      "/eam/plans/new",
      "/eam/requests",
      "/eam/requests/new",
      "/eam/work-orders",
      "/eam/work-orders/new",
      "/eam/work-orders/:id",
      "/eam/fault-catalogs",
    ]) {
      expect(paths).toContain(path);
    }
    expect(eam.routes.every((r) => r.element !== null && r.element !== undefined)).toBe(true);
    expect(eam.nav.every((n) => n.to.startsWith("/eam") && n.duty === "eam.inquire")).toBe(true);
    expect(loadModules().map((m) => m.id)).toContain("eam");
  });
});

describe("eam api client", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("sends the key, the quoted If-Match version and the body on status transitions", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ id: "wo", version: 2 }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    await transitionWorkOrder("wo-1", { to: "COMPLETED", downtimeMinutes: 30 }, 4, "key-9");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/eam/work-orders/wo-1/status");
    const headers = new Headers(init.headers);
    expect(headers.get("If-Match")).toBe('"4"');
    expect(headers.get("Idempotency-Key")).toBe("key-9");
    expect(JSON.parse(init.body as string)).toEqual({ to: "COMPLETED", downtimeMinutes: 30 });
  });

  it("posts the conversion currency with the caller's idempotency key", async () => {
    const fetchMock = vi.fn(async () => new Response("{}", { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);
    await convertRequest("req-1", "EUR", "key-1");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/eam/requests/req-1/convert");
    expect(JSON.parse(init.body as string)).toEqual({ currency: "EUR" });
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("key-1");
  });

  it("builds the KPI query and surfaces the problem code", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ code: "EAM-VAL-001", detail: "from and to" }), { status: 400 }));
    vi.stubGlobal("fetch", fetchMock);
    const failure = await assetKpis("a", "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z").catch((e: unknown) => e);
    expect(failure).toBeInstanceOf(EamApiError);
    expect((failure as EamApiError).code).toBe("EAM-VAL-001");
    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(new URL(url, "http://x").searchParams.get("from")).toBe("2026-01-01T00:00:00Z");
  });
});

describe("eam format", () => {
  it("offers the transitions the service allows (BR-44.3, BR-44.7)", () => {
    expect(nextStatuses("CREATED", 3)).toEqual(["SCHEDULED", "CANCELED"]);
    expect(nextStatuses("CREATED", 1)).toEqual(["RELEASED", "IN_PROGRESS", "SCHEDULED", "CANCELED"]);
    expect(nextStatuses("SCHEDULED", 1)).toContain("IN_PROGRESS");
    expect(nextStatuses("IN_PROGRESS", 3)).toEqual(["ON_HOLD", "COMPLETED", "CANCELED"]);
    expect(nextStatuses("COMPLETED", 3)).toEqual(["CLOSED"]);
    expect(nextStatuses("CANCELED", 1)).toEqual([]);
  });

  it("adds decimal money strings in cents, never floats", () => {
    expect(addMoney("350.000000", "360.000000")).toBe("710.00");
    expect(addMoney("0.10", "0.20")).toBe("0.30");
    expect(money("100.25")).toBe("100.25");
    expect(addMoney("-5.50", "2")).toBe("-3.50");
  });
});
