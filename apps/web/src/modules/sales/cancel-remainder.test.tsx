import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import type { Order } from "./api/client";
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

function renderAt(path: string, duties: string[] = ["sales.order.read", "sales.order.write"]) {
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


const reportsOrOrder = (current: Order) => (url: URL) => (url.pathname.endsWith("/order-progress") ? { status: 200, body: { items: [] } } : { status: 200, body: current, etag: '"4"' });

describe("Cancel the remainder", () => {
  it("is offered on a partly shipped order to a user with the order write duty, and posts with the ETag", async () => {
    const shipped = order({ status: "SHIPPED", version: 5, netTotal: "20" });
    handler = (url, init) => (init.method === "POST" ? { status: 200, body: shipped, etag: '"5"' } : reportsOrOrder(order())(url));
    const { container } = renderAt(`/sales/orders/${ORDER_ID}`);
    expect(await screen.findByText("Cancel the remainder")).toBeInTheDocument();
    expect(screen.getByText(/Line 1, 2 still have quantity to deliver/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    const button = screen.getByRole("button", { name: "Cancel remainder" });
    expect(button).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Cancel everything not yet delivered on this order"));
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "customer takes 4" } });
    fireEvent.click(button);
    await vi.waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/orders/${ORDER_ID}:cancel-remainder`);
    expect(post?.headers.get("If-Match")).toBe('"4"');
    expect(post?.body).toEqual({ reason: "customer takes 4" });
    expect(await screen.findByText(/The remainder of order SO-0000042 is cancelled; it is shipped with a total of 20 USD\./)).toBeInTheDocument();
    expect(screen.queryByText("Cancel the remainder")).toBeNull();
  });

  it("is hidden without the duty and on an order that is not partly shipped", async () => {
    handler = (url) => reportsOrOrder(order())(url);
    const first = renderAt(`/sales/orders/${ORDER_ID}`, ["sales.order.read", "sales.order.confirm"]);
    expect(await screen.findByText("Deliveries and invoices")).toBeInTheDocument();
    expect(screen.queryByText("Cancel the remainder")).toBeNull();
    first.unmount();
    handler = (url) => reportsOrOrder(order({ status: "CONFIRMED" }))(url);
    renderAt(`/sales/orders/${ORDER_ID}`);
    expect(await screen.findByText("Deliveries and invoices")).toBeInTheDocument();
    expect(screen.queryByText("Cancel the remainder")).toBeNull();
  });

  it("says that active holds stay on the order", async () => {
    const held = order({ holds: [{ id: "h1", holdType: "FRAUD", reasonCode: "REVIEW", status: "ACTIVE", orderAmount: "57", appliedBy: "analyst", appliedAt: "2027-03-01T09:00:00Z" }] });
    handler = (url) => reportsOrOrder(held)(url);
    renderAt(`/sales/orders/${ORDER_ID}`);
    expect(await screen.findByText(/Its active holds stay until they are released\./)).toBeInTheDocument();
  });

  it("shows the service's refusal and keeps the form", async () => {
    handler = (url, init) =>
      init.method === "POST" ? { status: 412, body: { code: "COM-0412", detail: "the order changed" } } : reportsOrOrder(order())(url);
    renderAt(`/sales/orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByLabelText("Cancel everything not yet delivered on this order"));
    fireEvent.click(screen.getByRole("button", { name: "Cancel remainder" }));
    expect(await screen.findByText(/The remainder was not cancelled/)).toBeInTheDocument();
    expect(screen.getByText(/the order changed/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel remainder" })).toBeInTheDocument();
  });

  it("refuses a reason over 500 characters before sending", async () => {
    handler = (url) => reportsOrOrder(order())(url);
    renderAt(`/sales/orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByLabelText("Cancel everything not yet delivered on this order"));
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "x".repeat(501) } });
    fireEvent.click(screen.getByRole("button", { name: "Cancel remainder" }));
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });
});
