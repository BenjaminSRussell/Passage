import { findTable } from "../../test-utils";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Order } from "./api/client";
import { buildChange, editsOf } from "./ChangeOrderPage";
import { supplementaryNote } from "./format";
import sales from "./module";

const ORDER_ID = "77777777-0000-4000-8000-000000000007";
const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const FG100 = "33333333-0000-4000-8000-000000000003";
import { nameLookups } from "./testLookup";

const MANUAL ="44444444-0000-4000-8000-000000000004";
const SITE = "55555555-0000-4000-8000-000000000005";
const WAREHOUSE = "66666666-0000-4000-8000-000000000006";
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

/** The AC-27.10 order: 25 FG-100 with the 2 free MANUAL-EN its mandatory rule added. */
function order(overrides: Partial<Order> = {}): Order {
  return {
    id: ORDER_ID,
    legalEntityId: LE,
    orderNumber: "SO-0000077",
    customerId: CUSTOMER,
    invoiceAccountId: CUSTOMER,
    orderType: "ORDER",
    status: "DRAFT",
    currency: "USD",
    requestedShipDate: "2027-03-04",
    channel: "UI",
    netTotal: "250",
    version: 1,
    lines: [
      { id: "parent", lineNo: 1, productId: FG100, quantity: "25", unit: "EA", siteId: SITE, warehouseId: WAREHOUSE, unitPrice: "10", discPct: "0", lineNet: "250", requestedShipDate: "2027-03-04", lineStatus: "OPEN" },
      {
        id: "manual",
        lineNo: 2,
        productId: MANUAL,
        quantity: "2",
        unit: "EA",
        siteId: SITE,
        warehouseId: WAREHOUSE,
        unitPrice: "0",
        discPct: "0",
        lineNet: "0",
        requestedShipDate: "2027-03-04",
        lineStatus: "OPEN",
        supplementaryOf: "parent",
        parentLineNo: 1,
        supplementaryRuleId: "rule-1",
      },
    ],
    holds: [],
    createdAt: "2027-03-01T09:00:00Z",
    updatedAt: "2027-03-01T09:00:00Z",
    ...overrides,
  };
}

describe("Mandatory supplementary lines on a new order (FR-27.14, AC-27.10)", () => {
  it("opens the created order and says which supplementary items were added", async () => {
    const warnings = [{ code: "SLS-0012", lineNo: 2, message: `Mandatory supplementary item ${MANUAL} added (2 EA)` }];
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: order({ warnings }), etag: '"1"' } : { status: 200, body: order(), etag: '"1"' });
    nameLookups({ [MANUAL]: "Manual kit" });
    renderAt("/sales/orders/new");
    const set = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
    set("Legal entity", LE);
    set("Customer", CUSTOMER);
    set("Product", FG100);
    set("Quantity", "25");
    set("Unit price", "10");
    set("Site", SITE);
    set("Warehouse", WAREHOUSE);
    fireEvent.click(screen.getByRole("button", { name: "Save draft order" }));

    expect(await screen.findByRole("heading", { name: "Sales order SO-0000077" })).toBeInTheDocument();
    expect(screen.getByText("Supplementary items added")).toBeInTheDocument();
    expect(screen.getByText(new RegExp(`${MANUAL} added \\(2 EA\\)`))).toBeInTheDocument();
    const lines = screen.getByRole("table", { name: "Order lines" });
    const row = (await within(lines).findByText("Manual kit")).closest("tr") as HTMLElement;
    expect(within(row).getByText("For line 1")).toBeInTheDocument();
  });

  it("marks supplementary lines on the order without the creation notice when opened directly", async () => {
    handler = (url) => (url.pathname.endsWith("/versions") ? { status: 200, body: { items: [] } } : { status: 200, body: order(), etag: '"1"' });
    const { container } = renderAt(`/sales/orders/${ORDER_ID}`);
    const lines = await findTable({ name: "Order lines" });
    expect(within(lines).getByText("For line 1")).toBeInTheDocument();
    expect(screen.queryByText("Supplementary items added")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("Changing an order with supplementary lines (BR-27.14)", () => {
  it("refuses the supplementary line's own quantity and date but allows its price", () => {
    const o = order();
    const values = editsOf(o);
    expect(values.lines[1].parentLineNo).toBe(1);
    values.lines[1] = { ...values.lines[1], quantity: "5", requestedShipDate: "2027-04-01" };
    const refused = buildChange(o, values);
    expect(refused.errors).toEqual({
      "line-2.quantity": "This supplementary line follows line 1; change that line instead.",
      "line-2.requestedShipDate": "This supplementary line follows line 1; change that line instead.",
    });
    expect(refused.input).toBeUndefined();

    const priced = editsOf(o);
    priced.lines[1].unitPrice = "1.50";
    expect(buildChange(o, priced).input).toEqual({ lines: [{ lineNo: 2, unitPrice: "1.50" }] });
  });

  it("locks the follow-on fields and shows the service's follow-on changes after saving", async () => {
    const changed = order({
      version: 2,
      netTotal: "90",
      lines: [
        { ...order().lines[0], quantity: "9", lineNet: "90" },
        { ...order().lines[1], lineStatus: "CANCELLED" },
      ],
    });
    handler = (_url, init) =>
      init.method === "PATCH" ? { status: 200, body: changed, etag: '"2"' } : { status: 200, body: order(), etag: '"1"' };
    const { container } = renderAt(`/sales/orders/${ORDER_ID}/change`);
    const child = await screen.findByRole("group", { name: "Line 2 · supplementary item for line 1" });
    expect(within(child).getByLabelText("Quantity")).toHaveAttribute("readonly");
    expect(within(child).getByLabelText("Requested ship date")).toHaveAttribute("readonly");
    expect(within(child).getByLabelText("Unit price")).not.toHaveAttribute("readonly");
    expect(within(child).getAllByText("Follows line 1.")).toHaveLength(2);
    expect(screen.getByText(/Supplementary lines follow their parent line/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(within(screen.getByRole("group", { name: "Line 1" })).getByLabelText("Quantity"), { target: { value: "9" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Saved as version 2.")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ lines: [{ lineNo: 1, quantity: "9" }] });
    // The cancelled supplementary line is no longer offered for editing.
    expect(screen.queryByRole("group", { name: /supplementary item for line 1/ })).not.toBeInTheDocument();
    expect(screen.getByText(/Line 2 is cancelled/)).toBeInTheDocument();
  });
});

describe("supplementaryNote", () => {
  it("counts the supplementary lines of a created order", () => {
    expect(supplementaryNote(order().lines)).toBe(" It also has 1 mandatory supplementary line.");
    expect(supplementaryNote([order().lines[0]])).toBe("");
    expect(supplementaryNote([order().lines[1], order().lines[1]])).toBe(" It also has 2 mandatory supplementary lines.");
  });
});
