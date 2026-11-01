import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { draftFromSchedule, emptyScheduleDraft, fromMicro, toMicro, toScheduleInput, unscheduled } from "./DeliverySchedulePage";
import type { DeliveryScheduleResult, PurchaseOrder } from "./api/client";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const PO_ID = "66666666-6666-4666-8666-666666666666";
const SCHEDULE_URL = `/api/v1/procurement/purchase-orders/${PO_ID}/lines/1/delivery-schedule`;
const PAGE = `/purchasing/purchase-orders/${PO_ID}/lines/1/delivery-schedule`;

afterEach(() => vi.unstubAllGlobals());

const order = (approvalStatus: PurchaseOrder["approvalStatus"]): PurchaseOrder => ({
  id: PO_ID,
  number: "PO-00000021",
  legalEntityId: "11111111-1111-4111-8111-111111111111",
  vendorId: "22222222-2222-4222-8222-222222222222",
  currency: "EUR",
  approvalStatus,
  documentStatus: "NONE",
  versionNo: approvalStatus === "CONFIRMED" ? 1 : 0,
  changeManagement: true,
  subtotal: "1000.00",
  discountTotal: "0",
  chargesTotal: "0",
  taxTotal: "0",
  total: "1000.00",
  rowVersion: 6,
  lines: [
    { lineNo: 1, description: "Bolt M8", qty: "100", unit: "EA", unitPrice: "10", priceUnit: "1", discountPct: "0", grossAmount: "1000.00", discountAmount: "0", lineAmount: "1000.00", taxAmount: "0", deliveryDate: "2027-02-01", priceSource: "MANUAL", status: "OPEN" },
    { lineNo: 2, description: "Washer", qty: "1", unit: "EA", unitPrice: "1", priceUnit: "1", discountPct: "0", grossAmount: "1", discountAmount: "0", lineAmount: "1", taxAmount: "0", deliveryDate: "2027-02-01", priceSource: "MANUAL", status: "CANCELLED" },
  ],
  charges: [],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
});

const schedule: DeliveryScheduleResult = {
  purchaseOrderId: PO_ID,
  lineNo: 1,
  unit: "EA",
  lineQty: "100",
  rows: [
    { seq: 1, qty: "60", deliveryDate: "2027-02-01" },
    { seq: 2, qty: "40", deliveryDate: "2027-03-01" },
  ],
  poRowVersion: 7,
};

type Init = RequestInit & { headers?: Record<string, string> };

/** Serves the PO, the line's schedule (or 404 PRC-DS-NOT-FOUND), and hands writes to write. */
function serve(status: PurchaseOrder["approvalStatus"], current: DeliveryScheduleResult | null, write: (url: string, init: Init) => Response = () => json(schedule)) {
  return stubFetch((url, init) => {
    if (init.method === "PUT" || init.method === "DELETE") return write(url, init as Init);
    if (url === SCHEDULE_URL && current) return json(current, 200, { ETag: `"${current.poRowVersion}"` });
    if (url.endsWith("/delivery-schedule")) return problem(404, "PRC-DS-NOT-FOUND", "the line has no delivery schedule");
    if (url.endsWith("/delivery-schedules")) return json({ items: current ? [current] : [] });
    if (url.endsWith("/invoicing-plans")) return json({ items: [] });
    if (url.endsWith("/versions")) return json({ items: [] });
    return json(order(status), 200, { ETag: '"6"' });
  });
}

describe("delivery schedule form rules", () => {
  it("does exact decimal arithmetic in millionths", () => {
    expect(toMicro("0.1")).toBe(100000n);
    expect(toMicro("1.0000001")).toBeNull();
    expect(toMicro("-1")).toBeNull();
    expect(toMicro("ten")).toBeNull();
    expect(fromMicro(toMicro("12.340000")! + toMicro("0.66")!)).toBe("13");
    expect(unscheduled("0.3", [{ key: "a", qty: "0.1", date: "" }, { key: "b", qty: "0.2", date: "" }])).toBe("0");
    expect(unscheduled("100", [{ key: "a", qty: "60", date: "" }, { key: "b", qty: "x", date: "" }])).toBe("40");
  });

  it("builds the body from rows that add up to the line quantity", () => {
    const rows = [{ key: "a", qty: "60", date: "2027-02-01" }, { key: "b", qty: "40.0", date: "2027-03-01" }];
    expect(toScheduleInput(rows, "100")).toEqual({
      errors: {},
      input: { rows: [{ qty: "60", deliveryDate: "2027-02-01" }, { qty: "40.0", deliveryDate: "2027-03-01" }] },
    });
  });

  it("names each malformed field and the sum rule", () => {
    const bad = toScheduleInput([{ key: "a", qty: "0", date: "" }], "100");
    expect(bad.input).toBeUndefined();
    expect(Object.keys(bad.errors).sort()).toEqual(["rows[0].deliveryDate", "rows[0].qty"]);
    expect(toScheduleInput([{ key: "a", qty: "90", date: "2027-02-01" }], "100").errors.rows).toBe("The deliveries must add up to the line quantity, 100.");
    expect(toScheduleInput([], "100").errors.rows).toBe("Add at least one delivery.");
  });

  it("starts a replacement from the schedule that is there", () => {
    const d = draftFromSchedule(schedule);
    expect(d.map((r) => [r.qty, r.date])).toEqual([["60", "2027-02-01"], ["40", "2027-03-01"]]);
    expect(emptyScheduleDraft()).toHaveLength(1);
  });
});

describe("delivery schedule page", () => {
  it("explains a line without a schedule and saves a split with the PO row version", async () => {
    const fetchMock = serve("DRAFT", null);
    const { container } = renderAt(PAGE);
    expect(await screen.findByRole("heading", { name: "No delivery schedule" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Delivery schedule: PO-00000021 line 1" })).toBeInTheDocument();
    expect(screen.getByText("Still to schedule: 100 of 100 EA.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Quantity (EA)"), { target: { value: "60" } });
    fireEvent.change(screen.getByLabelText("Delivery date"), { target: { value: "2027-02-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Add delivery" }));
    const second = screen.getByRole("group", { name: "Delivery 2" });
    fireEvent.change(within(second).getByLabelText("Quantity (EA)"), { target: { value: "40" } });
    fireEvent.change(within(second).getByLabelText("Delivery date"), { target: { value: "2027-03-01" } });
    expect(screen.getByText("All 100 EA are scheduled.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save schedule" }));
    expect(await screen.findByText("Delivery schedule saved: 2 deliveries adding up to 100 EA.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "PUT") as [string, Init];
    expect(put[0]).toBe(SCHEDULE_URL);
    expect(put[1].headers?.["If-Match"]).toBe('"6"');
    expect(JSON.parse(String(put[1].body))).toEqual({ rows: [{ qty: "60", deliveryDate: "2027-02-01" }, { qty: "40", deliveryDate: "2027-03-01" }] });
    expect(await findTable({ name: "Delivery rows" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Replace schedule" })).toBeInTheDocument();
  });

  it("validates before calling the service", async () => {
    const fetchMock = serve("DRAFT", null);
    renderAt(PAGE);
    fireEvent.click(await screen.findByRole("button", { name: "Save schedule" }));
    expect(await screen.findByText("Enter a quantity above 0 with at most six decimal places.")).toBeInTheDocument();
    expect(screen.getByText("Enter a delivery date.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit).method === "PUT")).toBe(false);
  });

  it("shows the service's sum check with its code", async () => {
    serve("DRAFT", null, () => problem(422, "PRC-DS-001", "delivery schedule quantities add up to 90, not the line quantity 100"));
    renderAt(PAGE);
    fireEvent.change(await screen.findByLabelText("Quantity (EA)"), { target: { value: "100" } });
    fireEvent.change(screen.getByLabelText("Delivery date"), { target: { value: "2027-02-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Save schedule" }));
    expect(await screen.findByText("delivery schedule quantities add up to 90, not the line quantity 100 (PRC-DS-001)")).toBeInTheDocument();
  });

  it("shows the deliveries and removes the schedule after confirmation", async () => {
    const fetchMock = serve("DRAFT", schedule, () => new Response(null, { status: 204, headers: { ETag: '"8"' } }));
    const { container } = renderAt(PAGE);
    const rows = await findTable({ name: "Delivery rows" });
    expect(within(rows).getAllByRole("row")).toHaveLength(3);
    expect(within(rows).getByText("60 EA")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Remove schedule" }));
    fireEvent.click(screen.getByRole("button", { name: "Yes, remove it" }));
    expect(await screen.findByText("Delivery schedule removed.")).toBeInTheDocument();
    const del = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "DELETE") as [string, Init];
    expect(del[0]).toBe(SCHEDULE_URL);
    expect(del[1].headers?.["If-Match"]).toBe('"7"');
    expect(screen.getByRole("heading", { name: "No delivery schedule" })).toBeInTheDocument();
  });

  it("records the vendor's confirmed date on a confirmed order and offers no form", async () => {
    const confirmed: DeliveryScheduleResult = { ...schedule, rows: [{ ...schedule.rows[0], confirmedDate: "2027-02-05" }, schedule.rows[1]], poRowVersion: 8 };
    const fetchMock = serve("CONFIRMED", schedule, () => json(confirmed, 200, { ETag: '"8"' }));
    renderAt(PAGE);
    expect(await screen.findByText("The deliveries are fixed")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Replace schedule" })).toBeNull();
    const first = screen.getByRole("group", { name: "Delivery 1" });
    fireEvent.change(within(first).getByLabelText("Confirmed date"), { target: { value: "2027-02-05" } });
    fireEvent.click(screen.getByRole("button", { name: "Confirm delivery 1" }));
    expect(await screen.findByText("Delivery 1 confirmed for 2027-02-05.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "PUT") as [string, Init];
    expect(put[0]).toBe(`${SCHEDULE_URL}/rows/1/confirmation`);
    expect(put[1].headers?.["If-Match"]).toBe('"7"');
    expect(JSON.parse(String(put[1].body))).toEqual({ confirmedDate: "2027-02-05" });
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Delivery rows" })).getByText("2027-02-05")).toBeInTheDocument());
  });

  it("refuses a schedule on a cancelled line and a missing line", async () => {
    serve("DRAFT", null);
    const cancelled = renderAt(`/purchasing/purchase-orders/${PO_ID}/lines/2/delivery-schedule`);
    expect(await screen.findByText("The deliveries are fixed")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save schedule" })).toBeNull();
    cancelled.unmount();
    renderAt(`/purchasing/purchase-orders/${PO_ID}/lines/9/delivery-schedule`);
    expect(await screen.findByRole("heading", { name: "Line 9 not found" })).toBeInTheDocument();
  });

  it("reports a purchase order that cannot be loaded", async () => {
    stubFetch(() => problem(404, "PRC-PO-NOT-FOUND", "purchase order not found"));
    renderAt(PAGE);
    expect(await screen.findByRole("heading", { name: "Purchase order not found" })).toBeInTheDocument();
  });
});
