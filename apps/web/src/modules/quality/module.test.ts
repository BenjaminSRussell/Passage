import { afterEach, describe, expect, it, vi } from "vitest";
import { loadModules } from "../registry";
import { describeProblem, getOrder, listOrders, postDecision, QualityApiError, recordResults, sizeSample } from "./api/client";
import quality from "./module";

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("quality module", () => {
  it("uses its folder name as id and declares routes and nav under /quality", () => {
    expect(quality.id).toBe("quality");
    expect(quality.nav.every((n) => n.to.startsWith("/quality"))).toBe(true);
    const paths = quality.routes.map((r) => r.path);
    for (const path of ["/quality/orders", "/quality/orders/new", "/quality/orders/:id", "/quality/test-groups", "/quality/test-groups/new", "/quality/instruments", "/quality/instruments/new", "/quality/associations", "/quality/associations/new", "/quality/sampling", "/quality/samples", "/quality/receiving", "/quality/compliance/lists", "/quality/compliance/lists/new", "/quality/compliance/items", "/quality/compliance/items/:id", "/quality/stability", "/quality/stability/new", "/quality/stability/:id", "/quality/spc", "/quality/nonconformances", "/quality/nonconformances/new", "/quality/nonconformances/:id"]) {
      expect(paths).toContain(path);
    }
    expect(quality.routes.every((r) => r.element !== null && r.element !== undefined)).toBe(true);
  });

  it("gates every nav entry by a quality duty", () => {
    expect(quality.nav.every((n) => n.duty?.startsWith("QM_"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("quality");
  });
});

describe("quality API client", () => {
  it("posts the lot and plan to sampling-plans:size and returns the draw", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { codeLetter: "E", sampleQty: "20", acceptNo: 1, rejectNo: 2, severity: "NORMAL", inspectionLevel: "II" }));
    vi.stubGlobal("fetch", fetchMock);

    const draw = await sizeSample("51", { method: "AQL", aql: "2.5", inspectionLevel: "II" });

    expect(draw.sampleQty).toBe("20");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/quality/sampling-plans:size");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ lotQty: "51", method: "AQL", aql: "2.5", inspectionLevel: "II" });
  });

  it("sends the order filter as query parameters and unwraps items", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { items: [] }));
    vi.stubGlobal("fetch", fetchMock);
    await listOrders({ status: "PENDING_DECISION", siteId: "" });
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/quality/quality-orders?status=PENDING_DECISION");
  });

  it("sends Idempotency-Key on results and Idempotency-Key plus If-Match on the decision", async () => {
    const fetchMock = vi.fn().mockImplementation(async () => jsonResponse(201, {}));
    vi.stubGlobal("fetch", fetchMock);
    await recordResults("o-1", [{ testId: "t", sampleNo: 1, value: "1.5" }], "k-1");
    await postDecision("o-1", 4, [{ decision: "ACCEPT", quantity: "10" }], "k-2");
    const results = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(results[0]).toBe("/api/v1/quality/quality-orders/o-1/results");
    expect(new Headers(results[1].headers).get("Idempotency-Key")).toBe("k-1");
    const decision = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(decision[0]).toBe("/api/v1/quality/quality-orders/o-1:usage-decision");
    expect(new Headers(decision[1].headers).get("If-Match")).toBe('"4"');
    expect(new Headers(decision[1].headers).get("Idempotency-Key")).toBe("k-2");
  });

  it("maps a problem response to QualityApiError carrying the stable code and field errors", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(400, { code: "COM-0001", detail: "request failed validation", errors: { lotQty: "must be positive" } })));

    const err = await getOrder("00000000-0000-4000-8000-000000000001").catch((e: unknown) => e);

    expect(err).toBeInstanceOf(QualityApiError);
    expect(describeProblem(err)).toEqual({ code: "COM-0001", message: "request failed validation", fields: { lotQty: "must be positive" } });
  });

  it("keeps a code for non-JSON failures", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("bad gateway", { status: 502 })));
    const err = await getOrder("x").catch((e: unknown) => e);
    expect(describeProblem(err).code).toBe("HTTP-502");
  });
});
