import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { repriceSummary, signedMoney, toRunInput } from "./RepricePages";
import type { PurchaseOrder, RepriceResult, RepriceRunResult } from "./api/client";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const PO_ID = "55555555-5555-4555-8555-555555555555";
const PO_B = "66666666-6666-4666-8666-666666666666";
const T1 = "77777777-7777-4777-8777-777777777777";
const T2 = "88888888-8888-4888-8888-888888888888";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const PAGE = `/purchasing/purchase-orders/${PO_ID}/reprice`;
const REPRICE_URL = `/api/v1/procurement/purchase-orders/${PO_ID}:reprice`;
const RUN_URL = "/api/v1/procurement/purchase-orders:reprice";

afterEach(() => vi.unstubAllGlobals());

const order: PurchaseOrder = {
  id: PO_ID,
  number: "PO-00000042",
  legalEntityId: "11111111-1111-4111-8111-111111111111",
  vendorId: VENDOR,
  currency: "USD",
  approvalStatus: "CONFIRMED",
  documentStatus: "NONE",
  versionNo: 1,
  changeManagement: true,
  subtotal: "3100",
  discountTotal: "0",
  chargesTotal: "0",
  taxTotal: "0",
  total: "3100",
  rowVersion: 7,
  lines: [],
  charges: [],
  createdAt: "2026-02-10T09:30:00Z",
  updatedAt: "2026-02-10T09:30:00Z",
};

const change = (lineNo: number, qty: string, delta: string, oldAmount: string, newAmount: string) => ({
  lineNo,
  qty,
  oldPrice: "10",
  oldPriceUnit: "1",
  newPrice: "9.5",
  newPriceUnit: "1",
  oldAmount,
  newAmount,
  delta,
  oldPriceSource: `TRADE_AGREEMENT:${T1}`,
  newPriceSource: `TRADE_AGREEMENT:${T2}`,
});

const preview: RepriceResult = {
  purchaseOrderId: PO_ID,
  number: "PO-00000042",
  currency: "USD",
  applied: false,
  versionNo: 1,
  reapprovalRequired: false,
  pricingDate: "2026-02-10",
  totalDelta: "-50.00",
  changes: [change(1, "100", "-50.00", "1000.00", "950.00")],
  invoiced: [change(3, "40", "-20.00", "400.00", "380.00")],
  skipped: [
    { lineNo: 2, reason: "MANUAL_PRICE" },
    { lineNo: 3, reason: "INVOICED" },
  ],
};

type Init = RequestInit & { headers?: Record<string, string> };

/** Serves the order and its preview; apply goes to onApply. */
function serve(po: PurchaseOrder, onApply: (init: Init) => Response = () => json({ ...preview, applied: true, versionNo: 2 }, 200, { ETag: '"8"' })) {
  return stubFetch((url, init) => {
    if (url === REPRICE_URL) {
      const body = JSON.parse(String(init.body ?? "{}")) as { apply?: boolean };
      return body.apply ? onApply(init as Init) : json(preview, 200, { ETag: '"7"' });
    }
    return json(po, 200, { ETag: `"${po.rowVersion}"` });
  });
}

describe("price change helpers", () => {
  it("signs deltas and summarises a result", () => {
    expect(signedMoney("-50.00", "USD")).toBe("−50.00 USD");
    expect(signedMoney("12.5", "EUR")).toBe("+12.5 EUR");
    expect(signedMoney("0", "USD")).toBe("0 USD");
    expect(repriceSummary(preview)).toBe("1 line move, −50.00 USD in total");
    expect(repriceSummary({ ...preview, changes: [] })).toBe("No line moves");
  });

  it("checks the run filters and drops blank ones", () => {
    expect(toRunInput({ legalEntityId: "", vendorId: ` ${VENDOR} `, tradeAgreementId: "" })).toEqual({
      errors: {},
      input: { legalEntityId: undefined, vendorId: VENDOR, tradeAgreementId: undefined },
    });
    const bad = toRunInput({ legalEntityId: "x", vendorId: "", tradeAgreementId: "nope" });
    expect(bad.input).toBeUndefined();
    expect(Object.keys(bad.errors).sort()).toEqual(["legalEntityId", "tradeAgreementId"]);
  });
});

describe("price change for one order", () => {
  it("previews at once: lines that move, invoiced lines for payables and lines left alone", async () => {
    const fetchMock = serve(order);
    const { container } = renderAt(PAGE);
    expect(await screen.findByRole("heading", { name: "Price change for PO-00000042" })).toBeInTheDocument();
    const moving = await screen.findByRole("table", { name: "Lines that move" });
    expect(within(moving).getByText("−50.00 USD")).toBeInTheDocument();
    expect(within(moving).getByText("950.00 USD")).toBeInTheDocument();
    const invoiced = screen.getByRole("table", { name: "Invoiced lines sent to payables" });
    expect(within(invoiced).getByText("−20.00 USD")).toBeInTheDocument();
    expect(within(invoiced).getByText("40")).toBeInTheDocument();
    const skipped = screen.getByRole("table", { name: "Lines left alone" });
    expect(within(skipped).getByText("Priced by hand; a price change never moves it")).toBeInTheDocument();
    expect(screen.getByText("2026-02-10")).toBeInTheDocument();
    // Only the read-only preview was sent: no Idempotency-Key, apply false.
    const call = fetchMock.mock.calls.find(([url]) => url === REPRICE_URL);
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ apply: false });
    expect(pageViolations(container)).toEqual([]);
  });

  it("applies under the previewed version with an idempotency key and says what was written", async () => {
    let sent: Init | undefined;
    serve(order, (init) => {
      sent = init;
      return json({ ...preview, applied: true, versionNo: 2, reapprovalRequired: true }, 200, { ETag: '"8"' });
    });
    renderAt(PAGE);
    fireEvent.click(await screen.findByRole("button", { name: "Apply price change" }));
    expect(await screen.findByText("1 line move, −50.00 USD in total. Version 2 waits for re-approval before the vendor receives it.")).toBeInTheDocument();
    expect(sent?.headers?.["If-Match"]).toBe('"7"');
    expect(sent?.headers?.["Idempotency-Key"]).toMatch(/.+/);
    expect(screen.queryByRole("button", { name: "Apply price change" })).not.toBeInTheDocument();
  });

  it("explains a stale preview and previews again", async () => {
    const fetchMock = serve(order, () => problem(412, "PRC-PO-009", "purchase order changed"));
    renderAt(PAGE);
    fireEvent.click(await screen.findByRole("button", { name: "Apply price change" }));
    expect(await screen.findByText(/The order changed after this preview/)).toBeInTheDocument();
    const before = fetchMock.mock.calls.filter(([url]) => url === REPRICE_URL).length;
    fireEvent.click(screen.getByRole("button", { name: "Preview again" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([url]) => url === REPRICE_URL).length).toBe(before + 1));
  });

  it("does not preview an order that is not confirmed", async () => {
    const fetchMock = serve({ ...order, approvalStatus: "IN_REVIEW" });
    renderAt(PAGE);
    expect(await screen.findByText("Only confirmed orders can be repriced")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => url === REPRICE_URL)).toBe(false);
    expect(screen.queryByRole("button", { name: "Apply price change" })).not.toBeInTheDocument();
  });

  it("shows a not-found state and a load error with its code", async () => {
    stubFetch(() => problem(404, "PRC-PO-NOT-FOUND", "purchase order not found"));
    const first = renderAt(PAGE);
    expect(await screen.findByText("Purchase order not found")).toBeInTheDocument();
    first.unmount();
    stubFetch((url) => (url === REPRICE_URL ? problem(503, "UNAVAILABLE", "pricing is down") : json(order)));
    renderAt(PAGE);
    expect(await screen.findByText("pricing is down (UNAVAILABLE)")).toBeInTheDocument();
  });

  it("is linked from a confirmed order", async () => {
    stubFetch((url) => (url.endsWith("/versions") || url.endsWith("/invoicing-plans") ? json({ items: [] }) : json(order, 200, { ETag: '"7"' })));
    renderAt(`/purchasing/purchase-orders/${PO_ID}`);
    expect(await screen.findByRole("link", { name: "Update prices" })).toHaveAttribute("href", PAGE);
  });
});

describe("price change run", () => {
  const runPreview: RepriceRunResult = {
    applied: false,
    visited: 3,
    items: [preview, { ...preview, purchaseOrderId: PO_B, number: "PO-00000043", invoiced: [], skipped: [] }],
    nextCursor: PO_B,
  };

  it("starts empty, then previews with the filters and applies that page with one key", async () => {
    const bodies: Record<string, unknown>[] = [];
    const keys: (string | undefined)[] = [];
    stubFetch((url, init) => {
      if (url !== RUN_URL) return json({});
      const body = JSON.parse(String(init.body)) as Record<string, unknown>;
      bodies.push(body);
      keys.push((init as Init).headers?.["Idempotency-Key"]);
      if (!body.apply) return json(runPreview);
      return json({
        applied: true,
        visited: 3,
        items: [
          { ...preview, applied: true, versionNo: 2 },
          { ...preview, purchaseOrderId: PO_B, number: "PO-00000043", error: { code: "PRC-PO-009", detail: "changed" } },
        ],
      });
    });
    const { container } = renderAt("/purchasing/reprice");
    expect(screen.getByText("Nothing previewed yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Trade agreement row/), { target: { value: T1 } });
    fireEvent.click(screen.getByRole("button", { name: "Preview price changes" }));
    const table = await screen.findByRole("table", { name: "Orders with price changes" });
    expect(within(table).getByRole("link", { name: "PO-00000042" })).toHaveAttribute("href", PAGE);
    expect(screen.getByText("3 confirmed orders checked on this page.")).toBeInTheDocument();
    expect(bodies[0]).toEqual({ apply: false, tradeAgreementId: T1 });
    expect(keys[0]).toBeUndefined();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Apply to these orders" }));
    expect(await screen.findByText("1 order repriced, 1 failed; open an order to see why.")).toBeInTheDocument();
    expect(screen.getByText("Failed: PRC-PO-009")).toBeInTheDocument();
    expect(bodies[1]).toEqual({ apply: true, tradeAgreementId: T1 });
    expect(keys[1]).toMatch(/.+/);
    expect(screen.queryByRole("button", { name: "Apply to these orders" })).not.toBeInTheDocument();
  });

  it("refuses malformed filters without calling the service and pages with the cursor", async () => {
    const fetchMock = stubFetch(() => json(runPreview));
    renderAt("/purchasing/reprice");
    fireEvent.change(screen.getByLabelText(/Vendor/), { target: { value: "not-a-uuid" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview price changes" }));
    expect(await screen.findByText("Enter a UUID, or leave it blank.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText(/Vendor/), { target: { value: VENDOR } });
    fireEvent.click(screen.getByRole("button", { name: "Preview price changes" }));
    fireEvent.click(await screen.findByRole("button", { name: "Next page" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(JSON.parse(String(fetchMock.mock.calls[1]?.[1]?.body))).toEqual({ apply: false, vendorId: VENDOR, cursor: PO_B });
  });

  it("shows the empty result and a service failure", async () => {
    stubFetch(() => json({ applied: false, visited: 4, items: [] }));
    const first = renderAt("/purchasing/reprice");
    fireEvent.click(screen.getByRole("button", { name: "Preview price changes" }));
    expect(await screen.findByText("No price changes")).toBeInTheDocument();
    first.unmount();
    stubFetch(() => problem(403, "AUTH-0003", "missing duty PurchaseOrderInquire"));
    renderAt("/purchasing/reprice");
    fireEvent.click(screen.getByRole("button", { name: "Preview price changes" }));
    expect(await screen.findByText("missing duty PurchaseOrderInquire (AUTH-0003)")).toBeInTheDocument();
  });
});
