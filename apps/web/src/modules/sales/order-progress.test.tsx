import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import type { Order, OrderProgress } from "./api/client";
import sales from "./module";

const ORDER_ID = "44444444-0000-4000-8000-000000000004";
const LE = "11111111-0000-4000-8000-000000000001";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
let handler: (url: URL, init: RequestInit) => Reply;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = ["sales.order.read", "sales.order.record-delivery", "sales.order.record-invoice"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {sales.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

function order(overrides: Partial<Order> = {}): Order {
  return {
    id: ORDER_ID,
    legalEntityId: LE,
    orderNumber: "SO-0000042",
    customerId: "22222222-0000-4000-8000-000000000002",
    invoiceAccountId: "22222222-0000-4000-8000-000000000002",
    orderType: "ORDER",
    status: "PARTIALLY_SHIPPED",
    currency: "USD",
    requestedShipDate: "2027-03-04",
    channel: "UI",
    netTotal: "57",
    version: 4,
    lines: [
      { id: "l1", lineNo: 1, productId: "p1", quantity: "10", unit: "EA", siteId: "s", warehouseId: "w", unitPrice: "5", discPct: "0", lineNet: "50", requestedShipDate: "2027-03-04", lineStatus: "PICKED", deliveredQty: "4", invoicedQty: "0" },
      { id: "l2", lineNo: 2, productId: "p2", quantity: "1", unit: "EA", siteId: "s", warehouseId: "w", unitPrice: "7", discPct: "0", lineNet: "7", requestedShipDate: "2027-03-04", lineStatus: "OPEN", deliveredQty: "0", invoicedQty: "0" },
    ],
    holds: [],
    createdAt: "2027-03-01T09:00:00Z",
    updatedAt: "2027-03-01T09:00:00Z",
    ...overrides,
  };
}

function report(overrides: Partial<OrderProgress> = {}): OrderProgress {
  return {
    id: "66666666-0000-4000-8000-000000000006",
    legalEntityId: LE,
    orderId: ORDER_ID,
    orderNumber: "SO-0000042",
    kind: "DELIVERED",
    sourceRef: "PS-1001",
    lines: [{ lineId: "l1", lineNo: 1, quantity: "4", deliveredQty: "4", invoicedQty: "0", lineStatus: "PICKED" }],
    orderStatus: "PARTIALLY_SHIPPED",
    orderVersion: 4,
    recordedBy: "fulfillment",
    recordedAt: "2027-03-02T10:00:00Z",
    ...overrides,
  };
}

describe("Deliveries and invoices list", () => {
  it("loads the reports and filters by kind", async () => {
    handler = () => ({ status: 200, body: { items: [report()] } });
    const { container } = renderAt("/sales/order-progress");
    expect(screen.getByText("Loading…")).toBeTruthy();
    expect(await screen.findByRole("link", { name: "SO-0000042" })).toHaveAttribute("href", `/sales/orders/${ORDER_ID}`);
    expect(screen.getByText("PS-1001")).toBeInTheDocument();
    expect(screen.getByText("1: 4")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Report"), { target: { value: "INVOICED" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1].url.searchParams.get("kind")).toBe("INVOICED");
  });

  it("shows the empty state, the error with a retry, and refuses a malformed order id", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const first = renderAt("/sales/order-progress");
    expect(await screen.findByText("Nothing recorded")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Order/), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(await screen.findByText("Enter an order UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(1);
    first.unmount();
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "down" } });
    renderAt("/sales/order-progress");
    expect(await screen.findByText(/down/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("Delivery tolerance on reports and on the form", () => {
  it("says what the tolerance did to a line in the reports list", async () => {
    handler = () => ({
      status: 200,
      body: {
        items: [
          report({ sourceRef: "PS-OVER", lines: [{ lineId: "l1", lineNo: 1, quantity: "105", deliveredQty: "105", invoicedQty: "0", lineStatus: "DELIVERED", lineQuantity: "105", tolerance: "OVER" }] }),
          report({ id: "66666666-0000-4000-8000-000000000007", sourceRef: "PS-UNDER", lines: [{ lineId: "l2", lineNo: 2, quantity: "96", deliveredQty: "96", invoicedQty: "0", lineStatus: "DELIVERED", lineQuantity: "96", tolerance: "UNDER" }] }),
        ],
      },
    });
    renderAt("/sales/order-progress");
    expect(await screen.findByText("1: 105 (over-delivery accepted, line now 105)")).toBeInTheDocument();
    expect(screen.getByText("2: 96 (completed short, remainder closed at 96)")).toBeInTheDocument();
  });

  it("accepts a delivery up to the over-delivery tolerance of the product and customer", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: report({ sourceRef: "PS-1003" }) };
      if (url.pathname.endsWith("/delivery-tolerances:resolve")) {
        return url.searchParams.get("productId") === "p1"
          ? { status: 200, body: { source: "CUSTOMER", toleranceId: "t1", overdeliveryPct: "5", underdeliveryPct: "0", partialDelivery: "ALLOWED", orderedQty: "10", minQty: "10", maxQty: "10.5" } }
          : { status: 200, body: { source: "DEFAULT", overdeliveryPct: "0", underdeliveryPct: "0", partialDelivery: "ALLOWED" } };
      }
      return { status: 200, body: order(), etag: '"4"' };
    };
    const { container } = renderAt(`/sales/orders/${ORDER_ID}/progress/new`);
    expect(await screen.findByText("up to 6.5 (over 5 %)")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    const resolve = calls.find((c) => c.url.pathname.endsWith("/delivery-tolerances:resolve") && c.url.searchParams.get("productId") === "p1");
    expect(resolve?.url.searchParams.get("customerId")).toBe("22222222-0000-4000-8000-000000000002");
    expect(resolve?.url.searchParams.get("orderedQty")).toBe("10");
    fireEvent.change(screen.getByLabelText("Reference"), { target: { value: "PS-1003" } });
    fireEvent.change(screen.getByLabelText("Quantity on line 1"), { target: { value: "7" } });
    fireEvent.click(screen.getByRole("button", { name: "Record delivery" }));
    expect(await screen.findByText("At most 6.5 can still be delivered within the over-delivery tolerance.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Quantity on line 1"), { target: { value: "6.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Record delivery" }));
    await vi.waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ kind: "DELIVERED", sourceRef: "PS-1003", lines: [{ lineNo: 1, quantity: "6.5" }] });
  });

  it("names the under-delivery bound and keeps the exact limit when the tolerance cannot be read", async () => {
    handler = (url) =>
      url.pathname.endsWith("/delivery-tolerances:resolve")
        ? { status: 403, body: { code: "COM-0403", detail: "forbidden" } }
        : { status: 200, body: order(), etag: '"4"' };
    const first = renderAt(`/sales/orders/${ORDER_ID}/progress/new`);
    await screen.findByText("Record delivery or invoice on SO-0000042");
    fireEvent.change(screen.getByLabelText("Quantity on line 1"), { target: { value: "7" } });
    fireEvent.change(screen.getByLabelText("Reference"), { target: { value: "PS-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Record delivery" }));
    expect(await screen.findByText("At most 6 can still be delivered.")).toBeInTheDocument();
    first.unmount();
    handler = (url) =>
      url.pathname.endsWith("/delivery-tolerances:resolve")
        ? { status: 200, body: { source: "ALL_CUSTOMERS", overdeliveryPct: "0", underdeliveryPct: "5", partialDelivery: "ALLOWED", minQty: "9.5", maxQty: "10" } }
        : { status: 200, body: order(), etag: '"4"' };
    renderAt(`/sales/orders/${ORDER_ID}/progress/new`);
    expect((await screen.findAllByText("a last delivery completes from 9.5 in all (under 5 %)")).length).toBeGreaterThan(0);
  });

  it("marks a short delivery as the last one only within the under-delivery tolerance", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: report({ sourceRef: "PS-2001" }) };
      if (url.pathname.endsWith("/delivery-tolerances:resolve")) {
        return { status: 200, body: { source: "ALL_CUSTOMERS", overdeliveryPct: "0", underdeliveryPct: "5", partialDelivery: "ALLOWED", orderedQty: "10", minQty: "9.5", maxQty: "10" } };
      }
      return { status: 200, body: order(), etag: '"4"' };
    };
    const { container } = renderAt(`/sales/orders/${ORDER_ID}/progress/new`);
    await screen.findAllByText("a last delivery completes from 9.5 in all (under 5 %)");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Reference"), { target: { value: "PS-2001" } });
    fireEvent.click(screen.getByLabelText("Close line 1 after this delivery"));
    fireEvent.change(screen.getByLabelText("Quantity on line 1"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Record delivery" }));
    expect(await screen.findByText(/A last delivery must bring the line to at least 9.5/)).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Quantity on line 1"), { target: { value: "5.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Record delivery" }));
    await vi.waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ kind: "DELIVERED", sourceRef: "PS-2001", lines: [{ lineNo: 1, quantity: "5.5", final: true }] });
  });

  it("asks no tolerance for a catch-weight line and does not widen a line delivered in full", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/delivery-tolerances:resolve")) {
        return { status: 200, body: { source: "ALL_CUSTOMERS", overdeliveryPct: "10", underdeliveryPct: "0", partialDelivery: "ALLOWED", minQty: "10", maxQty: "11" } };
      }
      const base = order();
      return {
        status: 200,
        etag: '"4"',
        body: order({
          lines: [
            { ...base.lines[0], deliveredQty: "10", lineStatus: "DELIVERED" },
            { ...base.lines[1], catchWeight: { nominal: "2", min: "1.8", max: "2.2" } },
          ],
        }),
      };
    };
    renderAt(`/sales/orders/${ORDER_ID}/progress/new`);
    await screen.findByText("Record delivery or invoice on SO-0000042");
    await vi.waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith("/delivery-tolerances:resolve"))).toBe(true));
    expect(calls.filter((c) => c.url.pathname.endsWith("/delivery-tolerances:resolve")).map((c) => c.url.searchParams.get("productId"))).toEqual(["p1"]);
    expect(screen.queryByLabelText("Close line 1 after this delivery")).toBeNull();
    expect(screen.getByLabelText("Close line 2 after this delivery")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Reference"), { target: { value: "PS-3001" } });
    fireEvent.change(screen.getByLabelText("Quantity on line 1"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Record delivery" }));
    expect(await screen.findByText("At most 0 can still be delivered.")).toBeInTheDocument();
  });
});

describe("Order detail", () => {
  it("shows the delivered and invoiced quantities and the reports of the order", async () => {
    handler = (url) => (url.pathname.endsWith("/order-progress") ? { status: 200, body: { items: [report()] } } : { status: 200, body: order(), etag: '"4"' });
    renderAt(`/sales/orders/${ORDER_ID}`);
    expect(await screen.findByText("PS-1001")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Delivered" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Record delivery or invoice" })).toHaveAttribute("href", `/sales/orders/${ORDER_ID}/progress/new`);
  });

  it("hides the record link on a journal order, whatever the duties", async () => {
    handler = (url) => (url.pathname.endsWith("/order-progress") ? { status: 200, body: { items: [] } } : { status: 200, body: order({ orderType: "JOURNAL" }), etag: '"4"' });
    renderAt(`/sales/orders/${ORDER_ID}`);
    expect(await screen.findByText("Nothing delivered or invoiced yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Record delivery or invoice" })).toBeNull();
  });

  it("hides the record link without the duty and shows the empty state", async () => {
    handler = (url) => (url.pathname.endsWith("/order-progress") ? { status: 200, body: { items: [] } } : { status: 200, body: order(), etag: '"4"' });
    renderAt(`/sales/orders/${ORDER_ID}`, ["sales.order.read"]);
    expect(await screen.findByText("Nothing delivered or invoiced yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Record delivery or invoice" })).toBeNull();
  });
});

describe("Record delivery or invoice", () => {
  it("validates against what is still open, then posts the covered quantities with an Idempotency-Key", async () => {
    handler = (url, init) =>
      init.method === "POST"
        ? { status: 201, body: report({ sourceRef: "PS-1002" }) }
        : url.pathname.endsWith("/order-progress")
          ? { status: 200, body: { items: [] } }
          : { status: 200, body: order(), etag: '"4"' };
    const { container } = renderAt(`/sales/orders/${ORDER_ID}/progress/new`);
    expect(await screen.findByText("Record delivery or invoice on SO-0000042")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Record delivery" }));
    expect(await screen.findByText("Enter the packing slip or invoice number (up to 100 characters).")).toBeInTheDocument();
    expect(screen.getByText("Enter a quantity on at least one line.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Reference"), { target: { value: "PS-1002" } });
    fireEvent.change(screen.getByLabelText("Quantity on line 1"), { target: { value: "7" } });
    fireEvent.click(screen.getByRole("button", { name: "Record delivery" }));
    expect(await screen.findByText("At most 6 can still be delivered.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Quantity on line 1"), { target: { value: "6" } });
    fireEvent.click(screen.getByRole("button", { name: "Record delivery" }));
    await vi.waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/orders/${ORDER_ID}:record-progress`);
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ kind: "DELIVERED", sourceRef: "PS-1002", lines: [{ lineNo: 1, quantity: "6" }] });
  });

  it("limits an invoice to the delivered quantity and explains a refusal", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 422, body: { code: "SLS-0059", detail: "line 1: invoicing would exceed the delivered quantity" } } : { status: 200, body: order(), etag: '"4"' });
    renderAt(`/sales/orders/${ORDER_ID}/progress/new`);
    await screen.findByText("Record delivery or invoice on SO-0000042");
    fireEvent.change(screen.getByLabelText("Report"), { target: { value: "INVOICED" } });
    fireEvent.change(screen.getByLabelText("Reference"), { target: { value: "INV-9" } });
    fireEvent.change(screen.getByLabelText("Quantity on line 1"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Record invoice" }));
    expect(await screen.findByText("At most 4 can still be invoiced.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Quantity on line 1"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Record invoice" }));
    expect(await screen.findByText("The quantity is too high")).toBeInTheDocument();
  });

  it("offers only the report kinds the user holds a duty for and the order takes", async () => {
    handler = () => ({ status: 200, body: order(), etag: '"4"' });
    const billing = renderAt(`/sales/orders/${ORDER_ID}/progress/new`, ["sales.order.read", "sales.order.record-invoice"]);
    await screen.findByText("Record delivery or invoice on SO-0000042");
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["Invoice"]);
    expect(screen.getByRole("button", { name: "Record invoice" })).toBeEnabled();
    billing.unmount();
    handler = () => ({ status: 200, body: order({ orderType: "SUBSCRIPTION" }), etag: '"4"' });
    renderAt(`/sales/orders/${ORDER_ID}/progress/new`);
    await screen.findByText("Record delivery or invoice on SO-0000042");
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["Delivery (packing slip)"]);
  });

  it("shows a field refusal of the service on the line it names", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 400, body: { code: "COM-0001", detail: "validation failed", errors: { "lines[0].quantity": "must be a positive decimal with at most 6 places" } } }
        : { status: 200, body: order(), etag: '"4"' };
    renderAt(`/sales/orders/${ORDER_ID}/progress/new`);
    await screen.findByText("Record delivery or invoice on SO-0000042");
    fireEvent.change(screen.getByLabelText("Reference"), { target: { value: "PS-7" } });
    fireEvent.change(screen.getByLabelText("Quantity on line 2"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Record delivery" }));
    expect(await screen.findByText("must be a positive decimal with at most 6 places")).toBeInTheDocument();
    expect(screen.getByLabelText("Quantity on line 2")).toHaveAttribute("aria-invalid", "true");
  });

  it("warns and disables the form without the duty, and reports a load failure", async () => {
    handler = () => ({ status: 200, body: order(), etag: '"4"' });
    const first = renderAt(`/sales/orders/${ORDER_ID}/progress/new`, ["sales.order.read"]);
    expect(await screen.findByText(/needs the sales.order.record-delivery duty/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Record delivery" })).toBeDisabled();
    first.unmount();
    handler = () => ({ status: 404, body: { code: "SLS-0100", detail: "order not found" } });
    renderAt(`/sales/orders/${ORDER_ID}/progress/new`);
    expect(await screen.findByText(/order not found/)).toBeInTheDocument();
  });
});
