import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { describeChange, editOutcome, purchaseOrderActions } from "./PurchaseOrderPages";
import type { PurchaseOrder, PurchaseOrderVersion } from "./api/client";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const PRODUCT = "33333333-3333-4333-8333-333333333333";
const PO_ID = "55555555-5555-4555-8555-555555555555";
const SITE = "99999999-9999-4999-8999-999999999999";
const AGR_LINE = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";

afterEach(() => vi.unstubAllGlobals());

const draft: PurchaseOrder = {
  id: PO_ID,
  number: "PO-00000009",
  legalEntityId: LE,
  vendorId: VENDOR,
  siteId: SITE,
  currency: "USD",
  approvalStatus: "DRAFT",
  documentStatus: "NONE",
  versionNo: 0,
  changeManagement: true,
  subtotal: "1050",
  discountTotal: "0",
  chargesTotal: "0",
  taxTotal: "0",
  total: "1050",
  rowVersion: 1,
  lines: [
    { lineNo: 1, productId: PRODUCT, qty: "100", unit: "EA", unitPrice: "10", priceUnit: "1", discountPct: "0", grossAmount: "1000", discountAmount: "0", lineAmount: "1000", taxAmount: "0", deliveryDate: "2026-12-01", priceSource: "MANUAL", receivedQty: "0", invoicedQty: "0", status: "OPEN" },
    { lineNo: 2, description: "Setup", qty: "1", unit: "EA", unitPrice: "50", priceUnit: "1", discountPct: "0", grossAmount: "50", discountAmount: "0", lineAmount: "50", taxAmount: "0", deliveryDate: "2026-12-01", priceSource: "MANUAL", receivedQty: "0", invoicedQty: "0", status: "OPEN" },
  ],
  charges: [],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
};

const confirmed: PurchaseOrder = {
  ...draft,
  approvalStatus: "CONFIRMED",
  versionNo: 1,
  rowVersion: 4,
  lines: [
    { ...draft.lines[0], receivedQty: "20", agreementLineId: undefined },
    { ...draft.lines[1], status: "CANCELLED" },
    { lineNo: 3, productId: PRODUCT, qty: "5", unit: "EA", unitPrice: "2.5", priceUnit: "1", discountPct: "0", grossAmount: "12.5", discountAmount: "0", lineAmount: "12.5", taxAmount: "0", deliveryDate: "2026-12-02", priceSource: `AGREEMENT:${AGR_LINE}`, agreementLineId: AGR_LINE, receivedQty: "0", invoicedQty: "0", status: "OPEN" },
  ],
  charges: [{ seq: 1, calc: "FIXED", value: "25", amount: "25" }],
};

const v1: PurchaseOrderVersion = {
  versionNo: 1,
  confirmedAt: "2026-10-27T09:00:00Z",
  reapprovalRequired: false,
  createdAt: "2026-10-27T09:00:00Z",
  patch: [],
  snapshot: { currency: "USD", total: "1050", lines: [{ lineNo: 1, status: "OPEN" }, { lineNo: 2, status: "OPEN" }] },
};

const v2: PurchaseOrderVersion = {
  versionNo: 2,
  reapprovalRequired: true,
  createdAt: "2026-10-28T09:00:00Z",
  patch: [
    { op: "replace", path: "/lines/0/qty", value: "110" },
    { op: "replace", path: "/lines/1/status", value: "CANCELLED" },
    { op: "add", path: "/lines/-", value: { lineNo: 3, qty: "5" } },
    { op: "replace", path: "/total", value: "1112.5" },
  ],
  snapshot: { currency: "USD", total: "1112.5", lines: [{ lineNo: 1, status: "OPEN" }, { lineNo: 2, status: "CANCELLED" }, { lineNo: 3, status: "OPEN" }] },
};

type Init = RequestInit & { headers?: Record<string, string> };

/** Routes GET order, GET versions and the writes; writes answer with next. */
function serve(po: PurchaseOrder, versions: PurchaseOrderVersion[], write: (url: string, init: Init) => Response = () => json(po)) {
  return stubFetch((url, init) => {
    if (init.method === "POST" || init.method === "PATCH") return write(url, init as Init);
    if (url.endsWith("/versions")) return json({ items: versions });
    return json(po, 200, { ETag: `"${po.rowVersion}"` });
  });
}

describe("purchase order lifecycle actions", () => {
  it("offers the transitions each approval status allows", () => {
    const labels = (s: Parameters<typeof purchaseOrderActions>[0]) => purchaseOrderActions(s).map((a) => a.action);
    expect(labels("DRAFT")).toEqual(["submit", "cancel"]);
    expect(labels("REJECTED")).toEqual(["submit", "cancel"]);
    expect(labels("IN_REVIEW")).toEqual(["approve", "reject", "cancel"]);
    expect(labels("APPROVED")).toEqual(["confirm", "cancel"]);
    expect(labels("CONFIRMED")).toEqual(["cancel"]);
    expect(labels("FINALIZED")).toEqual([]);
    expect(labels("CANCELLED")).toEqual([]);
  });

  it("submits a draft with If-Match and an Idempotency-Key and then offers approval", async () => {
    const fetchMock = serve(draft, [], () => json({ ...draft, approvalStatus: "IN_REVIEW", rowVersion: 2 }));
    const { container } = renderAt(`/purchasing/purchase-orders/${PO_ID}`);
    expect(await screen.findByRole("heading", { name: "Not confirmed yet" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Edit" })).toHaveAttribute("href", `/purchasing/purchase-orders/${PO_ID}/edit`);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Submit for approval" }));
    expect(await screen.findByText("Submitted for approval.")).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([, init]) => (init as Init).method === "POST") as [string, Init];
    expect(post[0]).toBe(`/api/v1/procurement/purchase-orders/${PO_ID}:submit`);
    expect(post[1].headers?.["If-Match"]).toBe('"1"');
    expect(post[1].headers?.["Idempotency-Key"]).toMatch(/-submit$/);
    expect(screen.getByRole("button", { name: "Approve" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reject" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Submit for approval" })).not.toBeInTheDocument();
  });

  it("asks before cancelling and sends nothing when the buyer keeps the order", async () => {
    const fetchMock = serve(confirmed, [v1], () => json({ ...confirmed, approvalStatus: "CANCELLED", rowVersion: 5 }));
    renderAt(`/purchasing/purchase-orders/${PO_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel order" }));
    fireEvent.click(screen.getByRole("button", { name: "Keep the order" }));
    expect(screen.queryByRole("button", { name: "Yes, cancel the order" })).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => (init as Init).method === "POST")).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Cancel order" }));
    fireEvent.click(screen.getByRole("button", { name: "Yes, cancel the order" }));
    expect(await screen.findByText(/^Cancelled\./)).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([, init]) => (init as Init).method === "POST") as [string, Init];
    expect(post[0]).toBe(`/api/v1/procurement/purchase-orders/${PO_ID}:cancel`);
    expect(screen.queryByRole("button", { name: "Cancel order" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Change order" })).not.toBeInTheDocument();
  });

  it("explains a stale version and shows other refusals with their code", async () => {
    let answer = problem(412, "PRC-PO-009", "changed");
    serve({ ...draft, approvalStatus: "APPROVED" }, [], () => answer);
    renderAt(`/purchasing/purchase-orders/${PO_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Confirm and send" }));
    expect(await screen.findByText(/Someone else changed this order/)).toBeInTheDocument();
    answer = problem(403, "AUTH-0003", "missing duty procurement.purchase-order.confirm");
    fireEvent.click(screen.getByRole("button", { name: "Confirm and send" }));
    expect(await screen.findByText("missing duty procurement.purchase-order.confirm")).toBeInTheDocument();
  });
});

describe("purchase order versions", () => {
  it("lists versions with each change in business terms and marks the pending one", async () => {
    serve({ ...confirmed, approvalStatus: "IN_REVIEW" }, [v1, v2]);
    const { container } = renderAt(`/purchasing/purchase-orders/${PO_ID}`);
    const table = await findTable({ name: "Versions" });
    await within(table).findByText("Line 1 quantity → 110");
    expect(table).toHaveTextContent("First confirmed version");
    expect(table).toHaveTextContent("Line 2 status → CANCELLED");
    expect(table).toHaveTextContent("Line 3 added");
    expect(table).toHaveTextContent("Total → 1112.5 USD");
    expect(within(table).getByText("Awaiting re-approval")).toBeInTheDocument();
    expect(within(table).getByText("Required")).toBeInTheDocument();
    const lines = screen.getByRole("table", { name: "Purchase order lines" });
    expect(within(lines).getByText("Cancelled")).toBeInTheDocument();
    expect(lines).toHaveTextContent("20");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the history's own error with its code", async () => {
    stubFetch((url) => (url.endsWith("/versions") ? problem(503, "COM-0503", "versions unavailable") : json(confirmed)));
    renderAt(`/purchasing/purchase-orders/${PO_ID}`);
    expect(await screen.findByText(/versions unavailable/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Purchase order PO-00000009" })).toBeInTheDocument();
  });

  it("names a change the way a buyer reads it", () => {
    expect(describeChange({ op: "replace", path: "/lines/2/deliveryDate", value: "2026-12-09" }, v2)).toBe("Line 3 delivery date → 2026-12-09");
    expect(describeChange({ op: "replace", path: "/charges/0/value", value: "30" }, v2)).toBe("Charges changed");
  });
});

describe("change a purchase order", () => {
  it("keeps prices of existing lines, cancels a left-out line, adds a new one and reports re-approval", async () => {
    const pending = { ...confirmed, approvalStatus: "IN_REVIEW" as const, rowVersion: 5 };
    const fetchMock = serve(confirmed, [v1], () => json(pending));
    const { router, container } = renderAt(`/purchasing/purchase-orders/${PO_ID}/edit`);
    expect(await screen.findByRole("heading", { name: "Change purchase order PO-00000009" })).toBeInTheDocument();
    // The cancelled line 2 is not offered; lines 1 and 3 are, with their price kept.
    const line1 = screen.getByRole("group", { name: "Line 1" });
    expect(screen.queryByRole("group", { name: "Line 2" })).not.toBeInTheDocument();
    expect(within(line1).getByLabelText("Unit price")).toHaveValue("");
    expect(within(line1).getByText("Blank keeps 10 / 1 while product and unit stay the same")).toBeInTheDocument();
    expect(within(line1).getByText("20 received; cannot go lower")).toBeInTheDocument();
    expect(within(screen.getByRole("group", { name: "Line 3" })).queryByLabelText("Agreement line id")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(within(line1).getByLabelText("Quantity"), { target: { value: "110" } });
    fireEvent.click(screen.getByRole("button", { name: "Cancel line 3" }));
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    const added = screen.getByRole("group", { name: "New line 1" });
    fireEvent.change(within(added).getByLabelText("Description"), { target: { value: "Freight box" } });
    fireEvent.change(within(added).getByLabelText("Unit price"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Save change" }));

    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/purchase-orders/${PO_ID}`));
    const patch = fetchMock.mock.calls.find(([, init]) => (init as Init).method === "PATCH") as [string, Init];
    expect(patch[0]).toBe(`/api/v1/procurement/purchase-orders/${PO_ID}`);
    expect(patch[1].headers?.["If-Match"]).toBe('"4"');
    const body = JSON.parse(String(patch[1].body));
    expect(body).toMatchObject({ legalEntityId: LE, vendorId: VENDOR, currency: "USD", siteId: SITE, charges: [{ calc: "FIXED", value: "25" }] });
    expect(body.lines).toHaveLength(2);
    expect(body.lines[0]).toMatchObject({ lineNo: 1, productId: PRODUCT, qty: "110", unit: "EA" });
    expect(body.lines[0].unitPrice).toBeUndefined();
    // An unchanged destination is not sent, so the line keeps following the order.
    expect(body.lines[0].warehouseId).toBeUndefined();
    expect(body.lines[1].lineNo).toBeUndefined();
    expect(body.lines[1]).toMatchObject({ description: "Freight box", unitPrice: "4" });
    expect(await screen.findByText("Change saved as version 2. It needs re-approval before the vendor sees it.")).toBeInTheDocument();
  });

  it("sends a changed delivery warehouse of an existing line and of a new line", async () => {
    const WH_OLD = "88888888-8888-4888-8888-888888888881";
    const WH_NEW = "88888888-8888-4888-8888-888888888882";
    const withWarehouse = { ...confirmed, lines: confirmed.lines.map((l) => (l.lineNo === 1 ? { ...l, warehouseId: WH_OLD } : l)) };
    const fetchMock = serve(withWarehouse, [v1], () => json({ ...withWarehouse, rowVersion: 5 }));
    renderAt(`/purchasing/purchase-orders/${PO_ID}/edit`);
    const line1 = await screen.findByRole("group", { name: "Line 1" });
    expect(within(line1).getByLabelText(/Deliver to warehouse/)).toHaveAttribute("data-value", WH_OLD);
    expect(within(line1).getByLabelText(/Deliver to warehouse/)).toHaveAttribute("readonly");
    expect(within(line1).getByLabelText(/Deliver to site/)).toHaveAttribute("readonly");
    // Line 3 has no receipts: its warehouse can be changed and the change is sent.
    const line3 = screen.getByRole("group", { name: "Line 3" });
    expect(within(line3).getByLabelText(/Deliver to warehouse/)).not.toHaveAttribute("readonly");
    fireEvent.change(within(line3).getByLabelText(/Deliver to warehouse/), { target: { value: WH_NEW } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    const added = screen.getByRole("group", { name: "New line 1" });
    fireEvent.change(within(added).getByLabelText("Description"), { target: { value: "Pallet" } });
    fireEvent.change(within(added).getByLabelText("Unit price"), { target: { value: "4" } });
    fireEvent.change(within(added).getByLabelText(/Deliver to warehouse/), { target: { value: WH_NEW } });
    fireEvent.click(screen.getByRole("button", { name: "Save change" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([, init]) => (init as Init).method === "PATCH")).toBe(true));
    const patch = fetchMock.mock.calls.find(([, init]) => (init as Init).method === "PATCH") as [string, Init];
    const body = JSON.parse(String(patch[1].body));
    expect(body.lines[0].warehouseId).toBeUndefined();
    expect(body.lines.find((l: { lineNo?: number }) => l.lineNo === 3)).toMatchObject({ warehouseId: WH_NEW });
    expect(body.lines.at(-1)).toMatchObject({ description: "Pallet", warehouseId: WH_NEW });
  });

  it("keeps an existing line's category, sends a new line's category and shows category warnings", async () => {
    const withCategory = { ...confirmed, lines: confirmed.lines.map((l) => (l.lineNo === 1 ? { ...l, category: "IT hardware" } : l)) };
    const saved = {
      ...withCategory,
      rowVersion: 5,
      warnings: [{ code: "PRC-CAT-010", lineNo: 4, detail: "vendor is only provisionally assigned to category Docks" }],
    };
    const fetchMock = serve(withCategory, [v1], () => json(saved));
    const { router } = renderAt(`/purchasing/purchase-orders/${PO_ID}/edit`);
    const line1 = await screen.findByRole("group", { name: "Line 1" });
    const kept = within(line1).getByLabelText("Procurement category");
    expect(kept).toHaveValue("IT hardware");
    expect(kept).toHaveAttribute("readonly");
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    const added = screen.getByRole("group", { name: "New line 1" });
    fireEvent.change(within(added).getByLabelText("Description"), { target: { value: "Dock" } });
    fireEvent.change(within(added).getByLabelText("Procurement category"), { target: { value: " Docks " } });
    fireEvent.change(within(added).getByLabelText("Unit price"), { target: { value: "80" } });
    fireEvent.click(screen.getByRole("button", { name: "Save change" }));

    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/purchase-orders/${PO_ID}`));
    const patch = fetchMock.mock.calls.find(([, init]) => (init as Init).method === "PATCH") as [string, Init];
    const body = JSON.parse(String(patch[1].body));
    expect(body.lines[0].category).toBeUndefined();
    expect(body.lines[2]).toMatchObject({ description: "Dock", category: "Docks" });
    expect(await screen.findByText(/Line 4: vendor is only provisionally assigned to category Docks \(PRC-CAT-010\)/)).toBeInTheDocument();
  });

  it("keeps the quantity and unit of a line ordered from a requisition line", async () => {
    const REQ_LINE = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    const fromRequisition = { ...draft, lines: draft.lines.map((l) => (l.lineNo === 1 ? { ...l, requisitionLineId: REQ_LINE } : l)) };
    serve(fromRequisition, []);
    renderAt(`/purchasing/purchase-orders/${PO_ID}/edit`);
    const line1 = await screen.findByRole("group", { name: "Line 1" });
    expect(within(line1).getByLabelText("Quantity")).toHaveAttribute("readonly");
    expect(within(line1).getByLabelText("Unit")).toHaveAttribute("readonly");
    expect(within(line1).getByText(/Orders its whole requisition line/)).toBeInTheDocument();
    const line2 = screen.getByRole("group", { name: "Line 2" });
    expect(within(line2).getByLabelText("Quantity")).not.toHaveAttribute("readonly");
  });

  it("does not offer an edit of an approved order", async () => {
    const fetchMock = serve({ ...draft, approvalStatus: "APPROVED" }, []);
    renderAt(`/purchasing/purchase-orders/${PO_ID}/edit`);
    expect(await screen.findByRole("heading", { name: "This order is approved and cannot be edited" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save purchase order" })).not.toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("validates before calling the service and shows a refusal with its code", async () => {
    const fetchMock = serve(confirmed, [], () => problem(422, "PRC-PO-007", "line 1 quantity 10 cannot be below received 20 / invoiced 0"));
    renderAt(`/purchasing/purchase-orders/${PO_ID}/edit`);
    const line1 = await screen.findByRole("group", { name: "Line 1" });
    fireEvent.change(within(line1).getByLabelText("Quantity"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Save change" }));
    expect(await within(line1).findByText("Enter a positive quantity.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => (init as Init).method === "PATCH")).toBe(false);

    fireEvent.change(within(line1).getByLabelText("Quantity"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save change" }));
    expect(await screen.findByText(/cannot be below received 20 /)).toBeInTheDocument();
  });

  it("says what a saved edit did", () => {
    expect(editOutcome(draft, { ...draft, rowVersion: 2 })).toMatch(/draft again/);
    expect(editOutcome(confirmed, { ...confirmed, versionNo: 2 })).toBe("Version 2 is confirmed and goes to the vendor.");
    expect(editOutcome({ ...confirmed, approvalStatus: "REJECTED" }, confirmed)).toMatch(/withdrawn; version 1 stands/);
    expect(editOutcome(confirmed, confirmed)).toBe("Nothing changed.");
  });
});
