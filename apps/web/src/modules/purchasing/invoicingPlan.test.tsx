import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { draftFromPlan, emptyPlanDraft, toPlanInput } from "./InvoicingPlanPage";
import type { InvoicingPlanResult, PurchaseOrder } from "./api/client";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const PO_ID = "55555555-5555-4555-8555-555555555555";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const PLAN_URL = `/api/v1/procurement/purchase-orders/${PO_ID}/lines/1/invoicing-plan`;
const PAGE = `/purchasing/purchase-orders/${PO_ID}/lines/1/invoicing-plan`;

afterEach(() => vi.unstubAllGlobals());

const order: PurchaseOrder = {
  id: PO_ID,
  number: "PO-00000012",
  legalEntityId: "11111111-1111-4111-8111-111111111111",
  vendorId: VENDOR,
  currency: "EUR",
  approvalStatus: "CONFIRMED",
  documentStatus: "NONE",
  versionNo: 1,
  changeManagement: true,
  subtotal: "12000.00",
  discountTotal: "0",
  chargesTotal: "0",
  taxTotal: "0",
  total: "12000.00",
  rowVersion: 6,
  lines: [
    { lineNo: 1, description: "Maintenance contract", qty: "12", unit: "MO", unitPrice: "1000", priceUnit: "1", discountPct: "0", grossAmount: "12000.00", discountAmount: "0", lineAmount: "12000.00", taxAmount: "0", deliveryDate: "2027-01-01", priceSource: "MANUAL", status: "OPEN" },
    { lineNo: 2, description: "Setup", qty: "1", unit: "EA", unitPrice: "50", priceUnit: "1", discountPct: "0", grossAmount: "50", discountAmount: "0", lineAmount: "50", taxAmount: "0", deliveryDate: "2027-01-01", priceSource: "MANUAL", status: "CANCELLED" },
  ],
  charges: [],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
};

const monthly = (amount: string, status: "OPEN" | "INVOICED" = "OPEN") =>
  Array.from({ length: 12 }, (_, i) => ({ seq: i + 1, dueDate: `2027-${String(i + 1).padStart(2, "0")}-01`, amount, status: i === 0 ? status : "OPEN" }));

const plan: InvoicingPlanResult = {
  purchaseOrderId: PO_ID,
  lineNo: 1,
  kind: "PERIODIC",
  frequency: "MONTHLY",
  billingDay: 1,
  settleWithoutReceipt: true,
  lineAmount: "12000.00",
  currency: "EUR",
  rows: monthly("1000.00"),
  updatedAt: "2026-10-26T12:00:00Z",
  poRowVersion: 7,
};

type Init = RequestInit & { headers?: Record<string, string> };

/** Serves the PO, the line's plan (or 404 PRC-IP-NOT-FOUND), and hands writes to write. */
function serve(current: InvoicingPlanResult | null, write: (url: string, init: Init) => Response = () => json(plan)) {
  return stubFetch((url, init) => {
    if (init.method === "PUT" || init.method === "DELETE") return write(url, init as Init);
    if (url === PLAN_URL && current) return json(current, 200, { ETag: `"${current.poRowVersion}"` });
    if (url.endsWith("/invoicing-plan")) return problem(404, "PRC-IP-NOT-FOUND", "the line has no invoicing plan");
    if (url.endsWith("/invoicing-plans")) return json({ items: current ? [current] : [] });
    if (url.endsWith("/versions")) return json({ items: [] });
    return json(order, 200, { ETag: `"${order.rowVersion}"` });
  });
}

describe("invoicing plan form rules", () => {
  it("builds a periodic body and leaves the arithmetic to the service", () => {
    const d = { ...emptyPlanDraft(), start: "2027-01-01", periods: "12", billingDay: "1", amountPerPeriod: "1000.00" };
    expect(toPlanInput(d)).toEqual({
      errors: {},
      input: { kind: "PERIODIC", start: "2027-01-01", frequency: "MONTHLY", periods: 12, billingDay: 1, amountPerPeriod: "1000.00", settleWithoutReceipt: true },
    });
    const blankDay = toPlanInput({ ...d, billingDay: "" });
    expect(blankDay.input && "billingDay" in blankDay.input ? blankDay.input.billingDay : "absent").toBeUndefined();
  });

  it("names each malformed periodic field", () => {
    const { input, errors } = toPlanInput({ ...emptyPlanDraft(), start: "", periods: "0", billingDay: "32", amountPerPeriod: "0.00" });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["amountPerPeriod", "billingDay", "periods", "start"]);
  });

  it("builds milestones by percent or by amount and refuses dates going back", () => {
    const base = { ...emptyPlanDraft(), kind: "PARTIAL" as const };
    const byPercent = toPlanInput({ ...base, milestones: [{ key: "a", date: "2027-02-01", value: "30" }, { key: "b", date: "2027-03-01", value: "70" }] });
    expect(byPercent.input).toEqual({
      kind: "PARTIAL",
      milestones: [{ date: "2027-02-01", percent: "30" }, { date: "2027-03-01", percent: "70" }],
      settleWithoutReceipt: true,
    });
    const byAmount = toPlanInput({ ...base, by: "amount", milestones: [{ key: "a", date: "2027-02-01", value: "400" }] });
    expect(byAmount.input).toEqual({ kind: "PARTIAL", milestones: [{ date: "2027-02-01", amount: "400" }], settleWithoutReceipt: true });
    const backwards = toPlanInput({ ...base, milestones: [{ key: "a", date: "2027-03-01", value: "50" }, { key: "b", date: "2027-02-01", value: "x" }] });
    expect(backwards.errors).toEqual({ "milestones[1].date": "Dates must not go back in time.", "milestones[1].percent": "Enter a percent above 0." });
  });

  it("starts a replacement from the plan that is there", () => {
    const d = draftFromPlan(plan);
    expect([d.kind, d.start, d.frequency, d.periods, d.billingDay, d.amountPerPeriod]).toEqual(["PERIODIC", "2027-01-01", "MONTHLY", "12", "1", "1000.00"]);
    const partial = draftFromPlan({ ...plan, kind: "PARTIAL", frequency: undefined, rows: [{ seq: 1, dueDate: "2027-02-01", amount: "3600.00", percent: "30", status: "OPEN" }] });
    expect([partial.kind, partial.by, partial.milestones[0].value]).toEqual(["PARTIAL", "percent", "30"]);
  });
});

describe("invoicing plan page", () => {
  it("explains a line without a plan and saves a periodic plan with the PO row version", async () => {
    const fetchMock = serve(null);
    const { container } = renderAt(PAGE);
    expect(await screen.findByRole("heading", { name: "No invoicing plan" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Invoicing plan: PO-00000012 line 1" })).toBeInTheDocument();
    expect(screen.getByText("12000.00 EUR")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("First period"), { target: { value: "2027-01-01" } });
    fireEvent.change(screen.getByLabelText("Billing day"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Amount per period"), { target: { value: "1000.00" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save plan" }));
    expect(await screen.findByText("Invoicing plan saved: 12 rows adding up to 12000.00 EUR.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "PUT") as [string, Init];
    expect(put[0]).toBe(PLAN_URL);
    expect(put[1].headers?.["If-Match"]).toBe('"6"');
    expect(JSON.parse(String(put[1].body))).toEqual({
      kind: "PERIODIC",
      start: "2027-01-01",
      frequency: "MONTHLY",
      periods: 12,
      billingDay: 1,
      amountPerPeriod: "1000.00",
      settleWithoutReceipt: true,
    });
    const rows = screen.getByRole("table", { name: "Plan rows" });
    expect(within(rows).getAllByText("1000.00 EUR")).toHaveLength(12);
    expect(screen.getByRole("button", { name: "Replace plan" })).toBeInTheDocument();
  });

  it("validates before calling the service", async () => {
    const fetchMock = serve(null);
    renderAt(PAGE);
    fireEvent.click(await screen.findByRole("button", { name: "Save plan" }));
    expect(await screen.findByText("Enter the date of the first billing period.")).toBeInTheDocument();
    expect(screen.getByText("Enter a positive amount.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit).method === "PUT")).toBe(false);
  });

  it("sends milestones and shows the service's total check with its code", async () => {
    const fetchMock = serve(null, () => problem(422, "PRC-IP-001", "milestone percents sum to 90, not 100"));
    renderAt(PAGE);
    fireEvent.change(await screen.findByLabelText("Plan type"), { target: { value: "PARTIAL" } });
    const first = screen.getByRole("group", { name: "Milestone 1" });
    fireEvent.change(within(first).getByLabelText("Date"), { target: { value: "2027-02-01" } });
    fireEvent.change(within(first).getByLabelText("Percent"), { target: { value: "30" } });
    const second = screen.getByRole("group", { name: "Milestone 2" });
    fireEvent.change(within(second).getByLabelText("Date"), { target: { value: "2027-03-01" } });
    fireEvent.change(within(second).getByLabelText("Percent"), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("checkbox", { name: /without waiting for a receipt/ }));
    fireEvent.click(screen.getByRole("button", { name: "Save plan" }));
    expect(await screen.findByText("milestone percents sum to 90, not 100 (PRC-IP-001)")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "PUT") as [string, Init];
    expect(JSON.parse(String(put[1].body))).toEqual({
      kind: "PARTIAL",
      milestones: [{ date: "2027-02-01", percent: "30" }, { date: "2027-03-01", percent: "60" }],
      settleWithoutReceipt: false,
    });
  });

  it("shows the schedule and removes the plan after confirmation", async () => {
    const fetchMock = serve(plan, () => new Response(null, { status: 204, headers: { ETag: '"8"' } }));
    const { container } = renderAt(PAGE);
    const rows = await findTable({ name: "Plan rows" });
    expect(within(rows).getAllByRole("row")).toHaveLength(13);
    expect(screen.getByText("Periodic")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Remove plan" }));
    fireEvent.click(screen.getByRole("button", { name: "Yes, remove it" }));
    expect(await screen.findByText("Invoicing plan removed.")).toBeInTheDocument();
    const del = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "DELETE") as [string, Init];
    expect(del[0]).toBe(PLAN_URL);
    expect(del[1].headers?.["If-Match"]).toBe('"7"');
    expect(screen.getByRole("heading", { name: "No invoicing plan" })).toBeInTheDocument();
  });

  it("offers no form once a row is invoiced", async () => {
    serve({ ...plan, rows: monthly("1000.00", "INVOICED") });
    renderAt(PAGE);
    expect(await screen.findByText("Part of this plan is invoiced")).toBeInTheDocument();
    expect(screen.getByText("Invoiced")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Replace plan" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove plan" })).toBeNull();
  });

  it("refuses a plan on a cancelled line", async () => {
    serve(null);
    renderAt(`/purchasing/purchase-orders/${PO_ID}/lines/2/invoicing-plan`);
    expect(await screen.findByText("This line cannot take a plan")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save plan" })).toBeNull();
  });

  it("says when the order or the line does not exist, and retries a failed load", async () => {
    stubFetch(() => problem(404, "PRC-PO-NOT-FOUND", "purchase order not found"));
    const first = renderAt(PAGE);
    expect(await screen.findByRole("heading", { name: "Purchase order not found" })).toBeInTheDocument();
    first.unmount();

    serve(null);
    const second = renderAt(`/purchasing/purchase-orders/${PO_ID}/lines/9/invoicing-plan`);
    expect(await screen.findByRole("heading", { name: "Line 9 not found" })).toBeInTheDocument();
    second.unmount();

    const fetchMock = stubFetch(() => problem(500, "COM-0500", "internal error"));
    renderAt(PAGE);
    expect(await screen.findByText(/internal error \(COM-0500\)/)).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "Try again" })[0]);
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(2));
  });
});

describe("purchase order detail", () => {
  it("links each open line to its invoicing plan", async () => {
    serve(plan);
    renderAt(`/purchasing/purchase-orders/${PO_ID}`);
    const lines = await findTable({ name: "Purchase order lines" });
    const link = await within(lines).findByRole("link", { name: "Invoicing plan of line 1" });
    expect(link).toHaveAttribute("href", PAGE);
    expect(link).toHaveTextContent("Periodic, 12 rows");
    expect(within(lines).queryByRole("link", { name: "Add an invoicing plan to line 2" })).toBeNull();
  });

  it("offers to add a plan to an open line without one", async () => {
    serve(null);
    renderAt(`/purchasing/purchase-orders/${PO_ID}`);
    const lines = await findTable({ name: "Purchase order lines" });
    expect(await within(lines).findByRole("link", { name: "Add an invoicing plan to line 1" })).toHaveAttribute("href", PAGE);
  });
});
