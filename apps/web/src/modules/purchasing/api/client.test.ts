import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ProcurementApiError,
  actOnPurchaseOrder,
  awardRFQ,
  changeRFQ,
  compareRFQ,
  changePurchaseAgreement,
  consolidateRequisitions,
  describeProblem,
  editPurchaseOrder,
  listOpenRequisitionLines,
  listPurchaseOrderVersions,
  listPurchaseOrders,
  listRFQs,
  lookupPrice,
  submitRequisition,
} from "./client";

function jsonResponse(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

describe("purchasing API client", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("submits with If-Match carrying the version and an Idempotency-Key", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: "r1", version: 4 }, 200, { ETag: '"4"' }));
    vi.stubGlobal("fetch", fetchMock);

    const out = await submitRequisition("r1", 3, "key-1");

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/procurement/requisitions/r1:submit");
    expect(init.method).toBe("POST");
    const headers = init.headers as Record<string, string>;
    expect(headers["If-Match"]).toBe('"3"');
    expect(headers["Idempotency-Key"]).toBe("key-1");
    expect(out.etag).toBe('"4"');
  });

  it("omits empty lookup parameters from the query string", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ winner: null, candidates: [] }));
    vi.stubGlobal("fetch", fetchMock);

    const out = await lookupPrice({
      vendor: "v1",
      product: "p1",
      qty: "10",
      date: "2026-03-01",
      currency: "USD",
      unit: " ",
    });

    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(url).toBe("/api/v1/procurement/pricing/lookup?vendor=v1&product=p1&qty=10&date=2026-03-01&currency=USD");
    expect(out.body.winner).toBeNull();
  });

  it("throws ProcurementApiError carrying the problem code", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(
          { type: "https://passage.dev/problems/PRC-PRICE-001", title: "Unprocessable Entity", status: 422, code: "PRC-PRICE-001", detail: "no price" },
          422,
        ),
      ),
    );

    const err = await lookupPrice({ vendor: "v", product: "p", qty: "1", date: "2026-01-01", currency: "USD", unit: "EA" }).catch(
      (e: unknown) => e,
    );

    expect(err).toBeInstanceOf(ProcurementApiError);
    expect((err as ProcurementApiError).problem.code).toBe("PRC-PRICE-001");
  });

  it("builds list queries from filters, limit and cursor and returns the page", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [{ id: "po-1" }], nextCursor: "po-1" }));
    vi.stubGlobal("fetch", fetchMock);

    const page = await listPurchaseOrders({ approvalStatus: "DRAFT", vendorId: "" }, { limit: 25, cursor: "c-9" });

    const [url] = fetchMock.mock.calls[0] as unknown as [string];
    expect(url).toBe("/api/v1/procurement/purchase-orders?approvalStatus=DRAFT&limit=25&cursor=c-9");
    expect(page.nextCursor).toBe("po-1");
  });

  it("keeps the HTTP status when the error body is not JSON", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("gateway down", { status: 502, statusText: "Bad Gateway" })));

    const err = await listPurchaseOrders().catch((e: unknown) => e);

    expect(describeProblem(err)).toEqual({ message: "Bad Gateway", code: "502", errors: undefined });
    expect(describeProblem(new TypeError("fetch failed")).code).toBe("NETWORK");
  });

  it("changes an agreement through its custom method with If-Match and an Idempotency-Key", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: "a1", status: "ON_HOLD", version: 3 }, 200, { ETag: '"3"' }));
    vi.stubGlobal("fetch", fetchMock);

    const out = await changePurchaseAgreement("a1", "hold", 2, "key-h");

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/procurement/agreements/a1:hold");
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
    expect(init.headers as Record<string, string>).toMatchObject({ "If-Match": '"2"', "Idempotency-Key": "key-h" });
    expect(out.body.status).toBe("ON_HOLD");
  });

  it("posts a consolidation and reads the queue with its sourcing filter", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ purchaseOrders: [], items: [] }, 201));
    vi.stubGlobal("fetch", fetchMock);

    await consolidateRequisitions({ windowDays: 7, lines: [{ requisitionLineId: "l1" }] }, "key-c");
    await listOpenRequisitionLines({ sourcing: "" }, { limit: 50 });

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/procurement/requisitions:consolidate");
    expect(JSON.parse(String(init.body))).toEqual({ windowDays: 7, lines: [{ requisitionLineId: "l1" }] });
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBe("key-c");
    expect((fetchMock.mock.calls[1] as unknown as [string])[0]).toBe("/api/v1/procurement/requisition-lines?limit=50");
  });
  it("runs a purchase order action with If-Match and an Idempotency-Key", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: "po1", rowVersion: 8 }, 200, { ETag: '"8"' }));
    vi.stubGlobal("fetch", fetchMock);

    await actOnPurchaseOrder("po1", "confirm", 7, "key-c");

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/procurement/purchase-orders/po1:confirm");
    expect(init.method).toBe("POST");
    expect(init.headers).toMatchObject({ "If-Match": '"7"', "Idempotency-Key": "key-c" });
  });

  it("edits a purchase order with PATCH and If-Match and no Idempotency-Key", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ id: "po1", rowVersion: 3 }));
    vi.stubGlobal("fetch", fetchMock);

    const input = { legalEntityId: "le", vendorId: "v", currency: "USD", lines: [{ lineNo: 1, qty: "5", unit: "EA", deliveryDate: "2026-12-01" }] };
    await editPurchaseOrder("po1", input, 2);

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/procurement/purchase-orders/po1");
    expect(init.method).toBe("PATCH");
    const headers = init.headers as Record<string, string>;
    expect(headers["If-Match"]).toBe('"2"');
    expect(headers["Idempotency-Key"]).toBeUndefined();
    expect(JSON.parse(String(init.body)).lines[0].lineNo).toBe(1);
  });

  it("unwraps the version list", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ items: [{ versionNo: 1, patch: [] }] })));
    const versions = await listPurchaseOrderVersions("po1");
    expect(versions.map((v) => v.versionNo)).toEqual([1]);
  });

  it("lists RFQs by a comma-joined status set and compares with an optional rule", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ items: [] }));
    vi.stubGlobal("fetch", fetchMock);
    await listRFQs({ statuses: ["SENT", "REPLIES_OPEN"] }, { limit: 200 });
    await listRFQs();
    await compareRFQ("q1");
    await compareRFQ("q1", "LEAD_TIME");
    const urls = fetchMock.mock.calls.map((c) => (c as unknown as [string])[0]);
    expect(urls).toEqual([
      "/api/v1/procurement/rfqs?status=SENT%2CREPLIES_OPEN&limit=200",
      "/api/v1/procurement/rfqs",
      "/api/v1/procurement/rfqs/q1/compare",
      "/api/v1/procurement/rfqs/q1/compare?rule=LEAD_TIME",
    ]);
  });

  it("awards and closes RFQs with If-Match and an Idempotency-Key", async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ rfq: { id: "q1" }, purchaseOrders: [] }));
    vi.stubGlobal("fetch", fetchMock);
    await awardRFQ("q1", 4, [{ lineId: "l1", replyId: "r1" }], "key-a");
    await changeRFQ("q1", "close", 5, "key-c");
    const [awardUrl, awardInit] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(awardUrl).toBe("/api/v1/procurement/rfqs/q1:award");
    expect(awardInit.headers).toMatchObject({ "If-Match": '"4"', "Idempotency-Key": "key-a" });
    expect(JSON.parse(String(awardInit.body))).toEqual({ awards: [{ lineId: "l1", replyId: "r1" }] });
    const [closeUrl, closeInit] = fetchMock.mock.calls[1] as unknown as [string, RequestInit];
    expect(closeUrl).toBe("/api/v1/procurement/rfqs/q1:close");
    expect(closeInit.body).toBeUndefined();
    expect(closeInit.headers).toMatchObject({ "If-Match": '"5"', "Idempotency-Key": "key-c" });
  });
});
