import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Order } from "./api/client";
import { emptyRunLine, validateRun } from "./BackorderRunPage";
import { buildChange, editsOf } from "./ChangeOrderPage";
import sales from "./module";
import { validateRelease } from "./ScheduleReleasePage";

const ORDER_ID = "66666666-0000-4000-8000-000000000006";
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {sales.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

function order(overrides: Partial<Order> = {}): Order {
  return {
    id: ORDER_ID,
    legalEntityId: "11111111-0000-4000-8000-000000000001",
    orderNumber: "SO-0000042",
    customerId: "22222222-0000-4000-8000-000000000002",
    invoiceAccountId: "22222222-0000-4000-8000-000000000002",
    orderType: "ORDER",
    status: "CONFIRMED",
    currency: "USD",
    requestedShipDate: "2027-03-04",
    channel: "UI",
    netTotal: "57",
    version: 3,
    lines: [
      { id: "l1", lineNo: 1, productId: "p1", quantity: "5", unit: "EA", siteId: "s", warehouseId: "w", unitPrice: "10", discPct: "0", lineNet: "50", requestedShipDate: "2027-03-04", confirmedShipDate: "2027-03-04", lineStatus: "OPEN" },
      { id: "l2", lineNo: 2, productId: "p2", quantity: "1", unit: "EA", siteId: "s", warehouseId: "w", unitPrice: "7", discPct: "0", lineNet: "7", requestedShipDate: "2027-03-04", confirmedShipDate: "2027-03-04", lineStatus: "OPEN" },
    ],
    holds: [],
    createdAt: "2027-03-01T09:00:00Z",
    updatedAt: "2027-03-01T09:00:00Z",
    ...overrides,
  };
}

describe("buildChange", () => {
  it("sends only the fields that changed and lists them as the diff (AC-27.7)", () => {
    const o = order();
    const values = editsOf(o);
    values.lines[0].quantity = "8";
    values.lines[1].unitPrice = "7.000"; // same value retyped: not a change
    const { errors, diff, input } = buildChange(o, { ...values, reason: " customer call " });
    expect(errors).toEqual({});
    expect(diff).toEqual([{ lineNo: 1, field: "quantity", old: "5", new: "8" }]);
    expect(input).toEqual({ reason: "customer call", lines: [{ lineNo: 1, quantity: "8" }] });
  });

  it("builds no request when nothing changed", () => {
    const o = order();
    const { diff, input } = buildChange(o, editsOf(o));
    expect(diff).toEqual([]);
    expect(input).toBeUndefined();
  });

  it("refuses values the service would refuse", () => {
    const o = order();
    const values = editsOf(o);
    values.lines[0] = { ...values.lines[0], quantity: "0", unitPrice: "-1", discPct: "101", requestedShipDate: "" };
    const { errors, input } = buildChange(o, { ...values, requestedShipDate: "", reason: "x".repeat(501) });
    expect(Object.keys(errors).sort()).toEqual(["line-1.discPct", "line-1.quantity", "line-1.requestedShipDate", "line-1.unitPrice", "reason", "requestedShipDate"]);
    expect(input).toBeUndefined();
  });

  it("offers only OPEN and RESERVED lines for editing", () => {
    const o = order();
    o.lines[1].lineStatus = "PICKED";
    expect(editsOf(o).lines.map((l) => l.lineNo)).toEqual([1]);
  });
});

describe("Change sales order page", () => {
  it("reports a line that allocation quota moved to a later period", async () => {
    const warnings = [{ code: "SLS-0013", lineNo: 1, message: "Allocation for product x in the period of 2027-03-04 is exhausted; line 1 is promised for 2027-04-01 (source ALLOCATION)" }];
    handler = (_url, init) =>
      init.method === "PATCH" ? { status: 200, body: { ...order({ version: 4 }), warnings }, etag: '"4"' } : { status: 200, body: order(), etag: '"3"' };
    renderAt(`/sales/orders/${ORDER_ID}/change`);
    const line1 = await screen.findByRole("group", { name: "Line 1" });
    fireEvent.change(within(line1).getByLabelText("Quantity"), { target: { value: "8" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/Saved as version 4\. Allocation for product x .* promised for 2027-04-01 \(source ALLOCATION\)/)).toBeInTheDocument();
  });

  it("shows the diff, saves with If-Match and reports the new version", async () => {
    handler = (_url, init) =>
      init.method === "PATCH" ? { status: 200, body: order({ version: 4, netTotal: "87" }), etag: '"4"' } : { status: 200, body: order(), etag: '"3"' };
    const { container } = renderAt(`/sales/orders/${ORDER_ID}/change`);
    expect(await screen.findByRole("heading", { name: "Change sales order SO-0000042" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "No changes yet" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
    expect(pageViolations(container)).toEqual([]);

    const line1 = screen.getByRole("group", { name: "Line 1" });
    fireEvent.change(within(line1).getByLabelText("Quantity"), { target: { value: "8" } });
    const diff = screen.getByRole("table", { name: "Changes to save" });
    expect(within(diff).getByText("Quantity")).toBeInTheDocument();
    expect(within(diff).getByText("8")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Saved as version 4.")).toBeInTheDocument();
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.url.pathname).toBe(`/api/v1/sales/orders/${ORDER_ID}`);
    expect(patch?.headers.get("If-Match")).toBe('"3"');
    expect(patch?.body).toEqual({ lines: [{ lineNo: 1, quantity: "8" }] });
  });

  it("warns when the higher total placed a credit hold", async () => {
    const hold = { id: "h1", holdType: "CREDIT", reasonCode: "CR-1", status: "ACTIVE", orderAmount: "3000", appliedBy: "u1", appliedAt: "2027-03-01T09:00:00Z" };
    handler = (_url, init) =>
      init.method === "PATCH" ? { status: 200, body: order({ version: 4, holds: [hold as Order["holds"][number]] }), etag: '"4"' } : { status: 200, body: order(), etag: '"3"' };
    renderAt(`/sales/orders/${ORDER_ID}/change`);
    fireEvent.change(within(await screen.findByRole("group", { name: "Line 1" })).getByLabelText("Unit price"), { target: { value: "600" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/failed the credit check, so a credit hold was placed/)).toBeInTheDocument();
  });

  it("offers a reload when someone else changed the order first", async () => {
    handler = (_url, init) =>
      init.method === "PATCH" ? { status: 412, body: { status: 412, code: "SLS-0006", detail: "the order changed; reload it" } } : { status: 200, body: order(), etag: '"3"' };
    renderAt(`/sales/orders/${ORDER_ID}/change`);
    fireEvent.change(await screen.findByLabelText("Requested ship date (order)"), { target: { value: "2027-03-09" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Someone else changed this order")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload order" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "GET")).toHaveLength(2));
  });

  it("cancels only after the user confirms, with the reason and If-Match", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 200, body: order({ status: "CANCELLED", version: 4 }), etag: '"4"' } : { status: 200, body: order(), etag: '"3"' };
    renderAt(`/sales/orders/${ORDER_ID}/change`);
    const cancel = await screen.findByRole("button", { name: "Cancel order" });
    expect(cancel).toBeDisabled();
    fireEvent.change(screen.getAllByLabelText(/Reason/)[1], { target: { value: "duplicate" } });
    fireEvent.click(screen.getByLabelText("Cancel every open line of this order and clear its holds"));
    fireEvent.click(cancel);
    expect(await screen.findByText("Order SO-0000042 is cancelled.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/orders/${ORDER_ID}:cancel`);
    expect(post?.headers.get("If-Match")).toBe('"3"');
    expect(post?.body).toEqual({ reason: "duplicate" });
    expect(screen.getByText("Only draft, open and confirmed orders can be changed or cancelled.")).toBeInTheDocument();
  });

  it("explains that a shipped order cannot change", async () => {
    handler = () => ({ status: 200, body: order({ status: "SHIPPED" }), etag: '"3"' });
    renderAt(`/sales/orders/${ORDER_ID}/change`);
    expect(await screen.findByText("Only draft, open and confirmed orders can be changed or cancelled.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });

  it("shows loading, then the error with a retry", async () => {
    handler = () => ({ status: 404, body: { status: 404, code: "SLS-0100", detail: "order not found" } });
    renderAt(`/sales/orders/${ORDER_ID}/change`);
    expect(screen.getByText("Loading the order…")).toBeInTheDocument();
    expect(await screen.findByText(/SLS-0100/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("Order change history on the order detail", () => {
  it("lists versions newest first with the newest diff", async () => {
    handler = (url) =>
      url.pathname.endsWith("/versions")
        ? {
            status: 200,
            body: { items: [{ version: 4, actor: "rep", reason: "customer call", changedAt: "2027-03-02T10:00:00Z", diff: [{ lineNo: 1, field: "quantity", old: "5", new: "8" }] }] },
          }
        : { status: 200, body: order({ version: 4 }), etag: '"4"' };
    renderAt(`/sales/orders/${ORDER_ID}`);
    const versions = await findTable({ name: "Versions" });
    expect(await within(versions).findByText("customer call")).toBeInTheDocument();
    const diff = screen.getByRole("table", { name: "Changes in version 4" });
    expect(within(diff).getByText("Quantity")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Change or cancel" })).toHaveAttribute("href", `/sales/orders/${ORDER_ID}/change`);
  });

  it("explains an order without changes, and a failed history load", async () => {
    handler = (url) => (url.pathname.endsWith("/versions") ? { status: 200, body: { items: [] } } : { status: 200, body: order(), etag: '"3"' });
    const first = renderAt(`/sales/orders/${ORDER_ID}`);
    expect(await screen.findByRole("heading", { name: "No changes yet" })).toBeInTheDocument();
    first.unmount();

    handler = (url) =>
      url.pathname.endsWith("/versions") ? { status: 500, body: { status: 500, code: "COM-0500", detail: "store unavailable" } } : { status: 200, body: order(), etag: '"3"' };
    renderAt(`/sales/orders/${ORDER_ID}`);
    expect(await screen.findByText("Could not load the change history")).toBeInTheDocument();
  });
});

describe("Scheduling release check", () => {
  it("validates like the service", () => {
    const { errors } = validateRelease({
      cumReceived: "x",
      cumShipped: "1200",
      tolerance: "-1",
      buckets: [
        { date: "2027-04-15", cumRequired: "1500" },
        { date: "2027-04-01", cumRequired: "1400" },
      ],
    });
    expect(Object.keys(errors).sort()).toEqual(["buckets[1].cumRequired", "buckets[1].date", "cumReceived", "tolerance"]);
  });

  it("shows the open lines of a clean release (AC-27.13)", async () => {
    handler = () => ({ status: 200, body: { held: false, difference: "0", openQuantity: "300", lines: [{ date: "2027-04-15", quantity: "300" }] } });
    const { container } = renderAt("/sales/schedule-releases");
    fireEvent.change(screen.getByLabelText("Cumulative received (customer)"), { target: { value: "1200" } });
    fireEvent.change(screen.getByLabelText("Cumulative shipped"), { target: { value: "1200" } });
    fireEvent.change(screen.getByLabelText("Date"), { target: { value: "2027-04-15" } });
    fireEvent.change(screen.getByLabelText("Cumulative required"), { target: { value: "1500" } });
    fireEvent.click(screen.getByRole("button", { name: "Check release" }));
    const lines = await findTable({ name: "Open schedule lines" });
    expect(within(lines).getByText("2027-04-15")).toBeInTheDocument();
    expect(screen.getByText("300 open over 1 schedule line.")).toBeInTheDocument();
    expect(calls[0].body).toEqual({ cumReceived: "1200", cumShipped: "1200", buckets: [{ date: "2027-04-15", cumRequired: "1500" }] });
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a held release and shows no lines", async () => {
    handler = () => ({ status: 200, body: { held: true, code: "CUM_QTY_MISMATCH", serviceCode: "SLS-0014", difference: "-50", openQuantity: "0", lines: [] } });
    renderAt("/sales/schedule-releases");
    fireEvent.change(screen.getByLabelText("Cumulative received (customer)"), { target: { value: "1150" } });
    fireEvent.change(screen.getByLabelText("Cumulative shipped"), { target: { value: "1200" } });
    fireEvent.change(screen.getByLabelText("Date"), { target: { value: "2027-04-15" } });
    fireEvent.change(screen.getByLabelText("Cumulative required"), { target: { value: "1500" } });
    fireEvent.click(screen.getByRole("button", { name: "Check release" }));
    expect(await screen.findByText("Release held: cumulative quantity mismatch")).toBeInTheDocument();
    expect(screen.getByText(/CUM_QTY_MISMATCH/)).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Open schedule lines" })).not.toBeInTheDocument();
  });
});

describe("Backorder run preview", () => {
  it("validates like the service", () => {
    const line = { ...emptyRunLine(1), requestedDate: "2027-03-01", demand: "10", confirmed: "11", priorityClass: "0" };
    const { errors, input } = validateRun({ strategy: "WIN", available: "", loseThreshold: "12", lines: [line, { ...line }] });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual([
      "available",
      "lines[0].confirmed",
      "lines[0].priorityClass",
      "lines[1].confirmed",
      "lines[1].id",
      "lines[1].priorityClass",
      "loseThreshold",
    ]);
  });

  it("previews a run and warns about deconfirmed lines (AC-27.12)", async () => {
    handler = () => ({
      status: 200,
      body: {
        preview: true,
        strategy: "REDISTRIBUTE",
        allocated: "40",
        deconfirmedCount: 1,
        warningCode: "SLS-0015",
        warning: "backorder run would deconfirm 1 lines",
        lines: [
          { id: "L1", before: "0", after: "20", changed: true, deconfirmed: false },
          { id: "L2", before: "30", after: "20", changed: true, deconfirmed: true },
        ],
      },
    });
    const { container } = renderAt("/sales/backorder-runs");
    fireEvent.change(screen.getByLabelText("Available supply"), { target: { value: "40" } });
    const line = screen.getByRole("group", { name: "Line 1" });
    fireEvent.change(within(line).getByLabelText("Requested date"), { target: { value: "2027-03-01" } });
    fireEvent.change(within(line).getByLabelText("Quantity"), { target: { value: "20" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview run" }));
    const table = await findTable({ name: "Preview by line" });
    expect(within(table).getByText("Loses confirmation")).toBeInTheDocument();
    expect(screen.getByText(/take confirmation away from 1 line \(SLS-0015\)/)).toBeInTheDocument();
    expect(calls[0].body).toEqual({
      strategy: "REDISTRIBUTE",
      available: "40",
      lines: [{ id: "L1", priorityClass: 3, requestedDate: "2027-03-01", orderValue: "0", demand: "20", confirmed: "0" }],
    });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows why the service refused a run", async () => {
    handler = () => ({ status: 422, body: { status: 422, code: "SLS-0027", detail: "WIN cannot keep the 20 confirmed on line L1; supply is short" } });
    renderAt("/sales/backorder-runs");
    fireEvent.change(screen.getByLabelText("Available supply"), { target: { value: "1" } });
    const line = screen.getByRole("group", { name: "Line 1" });
    fireEvent.change(within(line).getByLabelText("Requested date"), { target: { value: "2027-03-01" } });
    fireEvent.change(within(line).getByLabelText("Quantity"), { target: { value: "20" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview run" }));
    expect(await screen.findByText(/SLS-0027/)).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Preview by line" })).not.toBeInTheDocument();
  });
});
