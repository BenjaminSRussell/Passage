import { findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import inventory from "./module";
import { validateTransfer, type TransferValues } from "./NewTransferPage";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const ORDER_ID = "8a8a8a8a-0000-4000-8000-000000000008";
const LINE_A = "1b1b1b1b-0000-4000-8000-000000000009";
const LINE_B = "1b1b1b1b-0000-4000-8000-00000000000a";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];

const json = (status: number, body: unknown) => ({ status, body });
const sent = (method: string) => calls.filter((c) => c.method === method);
const bodyOf = (call?: { init: RequestInit }) => JSON.parse((call?.init.body as string) ?? "null");

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
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
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const line = (id: string, lineNo: number, overrides: Record<string, unknown> = {}) => ({
  id,
  lineNo,
  productId: PRODUCT_ID,
  dimensions: { site: "S1", warehouse: "W1" },
  unit: "EA",
  qty: "100",
  shippedQty: "0",
  receivedQty: "0",
  inTransit: "0",
  underdeliveryPct: "10",
  overdeliveryPct: "0",
  unshippedQty: "100",
  closed: false,
  ...overrides,
});

const order = (overrides: Record<string, unknown> = {}) => ({
  id: ORDER_ID,
  legalEntityId: ENTITY_ID,
  number: "TO-ABCDEF123456",
  status: "CREATED",
  fromWarehouseId: "W1",
  toWarehouseId: "W2",
  transitWarehouseId: "T1",
  useTransit: true,
  shipDate: "2026-09-01",
  plannedReceiptDate: "2026-09-08",
  version: 1,
  createdAt: "2026-09-01T08:00:00Z",
  lines: [line(LINE_A, 1), line(LINE_B, 2, { qty: "50", unshippedQty: "50" })],
  ...overrides,
});

describe("Partial shipments on the transfer order", () => {
  it("ships less than the full quantity, naming its lines, and offers the rest afterwards", async () => {
    const after = order({
      status: "SHIPPED",
      version: 2,
      lines: [line(LINE_A, 1, { shippedQty: "60", inTransit: "60", unshippedQty: "40" }), line(LINE_B, 2, { qty: "50", unshippedQty: "50" })],
    });
    handler = (_url, init) => (init.method === "POST" ? json(200, after) : json(200, order()));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    expect(await screen.findByRole("heading", { name: "Transfer order TO-ABCDEF123456" })).toBeInTheDocument();
    expect(screen.getByText("2026-09-08")).toBeInTheDocument();
    const qtyA = screen.getByLabelText("Line 1: ship EA");
    expect(qtyA).toHaveValue("100");
    fireEvent.change(qtyA, { target: { value: "60" } });
    fireEvent.change(screen.getByLabelText("Line 2: ship EA"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Ship order" }));
    expect(await findStatus()).toHaveTextContent("Partial shipment booked. 2 line(s) still have quantity left to ship.");
    expect(bodyOf(sent("POST")[0]).lines).toEqual([{ lineId: LINE_A, qty: "60" }]);
    // The order is shipped but a line has quantity left, so the ship form is offered again with the remainder.
    expect(await screen.findByLabelText("Line 1: ship EA")).toHaveValue("40");
    expect(screen.getByRole("heading", { name: "Receive" })).toBeInTheDocument();
  });

  it("shipping everything left sends no lines", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(200, order({ status: "SHIPPED", version: 2, lines: [line(LINE_A, 1, { shippedQty: "100", unshippedQty: "0" })] })) : json(200, order()));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Ship order" }));
    await findStatus();
    expect(Object.keys(bodyOf(sent("POST")[0]))).toEqual(["shipDate"]);
  });

  it("refuses a quantity above what is left, or no quantity at all, without calling the service", async () => {
    handler = () => json(200, order());
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    const qtyA = await screen.findByLabelText("Line 1: ship EA");
    fireEvent.change(qtyA, { target: { value: "101" } });
    fireEvent.click(screen.getByRole("button", { name: "Ship order" }));
    expect(await screen.findByText("Only 100 EA is left to ship.")).toBeInTheDocument();
    fireEvent.change(qtyA, { target: { value: "0" } });
    fireEvent.change(screen.getByLabelText("Line 2: ship EA"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Ship order" }));
    expect(await screen.findByText("Enter a quantity to ship on at least one line.")).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(0);
  });

  it("shows the service refusal with its code", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(422, { code: "INV-TRF-007", detail: "line 1 ships 100 but only 40 is left to ship" }) : json(200, order()));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Ship order" }));
    expect(await screen.findByText(/only 40 is left to ship/)).toBeInTheDocument();
    expect(screen.getByText(/INV-TRF-007/)).toBeInTheDocument();
  });

  it("offers receipt only on lines with shipped quantity, and warns that closing short gives up the rest", async () => {
    handler = () =>
      json(200, order({ status: "SHIPPED", version: 2, lines: [line(LINE_A, 1, { shippedQty: "60", inTransit: "60", unshippedQty: "40" }), line(LINE_B, 2, { qty: "50", unshippedQty: "50" })] }));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    expect(await screen.findByLabelText("Line 1: received EA")).toHaveValue("60");
    expect(screen.queryByLabelText("Line 2: received EA")).toBeNull();
    expect(screen.getByLabelText(/Close line 1 short/)).toBeInTheDocument();
    expect(screen.getByText(/give up the 40 EA not shipped/)).toBeInTheDocument();
  });

  it("offers no ship form once every line is fully shipped", async () => {
    handler = () =>
      json(200, order({ status: "SHIPPED", version: 2, lines: [line(LINE_A, 1, { shippedQty: "100", inTransit: "100", unshippedQty: "0" })] }));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    expect(await screen.findByRole("heading", { name: "Receive" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Ship order" })).toBeNull();
  });
});

describe("Planned receipt date", () => {
  const values: TransferValues = {
    legalEntityId: ENTITY_ID,
    fromWarehouseId: "W1",
    toWarehouseId: "W2",
    toSiteId: "",
    useTransit: true,
    transitWarehouseId: "T1",
    shipDate: "2026-09-10",
    plannedReceiptDate: "",
    deliveryTerms: "",
    modeOfDelivery: "",
    lines: [{ productId: PRODUCT_ID, qty: "5", unit: "EA", underdeliveryPct: "", overdeliveryPct: "", dimensions: { site: "S1", location: "", batch: "", serial: "" } }],
  };

  it("is optional and must not precede the ship date", () => {
    expect(validateTransfer(values).header.plannedReceiptDate).toBeUndefined();
    expect(validateTransfer({ ...values, plannedReceiptDate: "2026-09-09" }).header.plannedReceiptDate).toMatch(/must not be before the ship date/);
    expect(validateTransfer({ ...values, plannedReceiptDate: "2026-09-12" }).header.plannedReceiptDate).toBeUndefined();
  });

  function fillOrder() {
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Shipping warehouse"), { target: { value: "W1" } });
    fireEvent.change(screen.getByLabelText("Receiving warehouse"), { target: { value: "W2" } });
    fireEvent.change(screen.getByLabelText("Transit warehouse"), { target: { value: "T1" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText("Planned ship date"), { target: { value: "2026-09-10" } });
  }

  it("refuses a planned receipt date before the ship date without calling the service", async () => {
    handler = () => json(201, order());
    renderAt("/inventory/transfer-orders/new");
    fillOrder();
    fireEvent.change(screen.getByLabelText(/Planned receipt date/), { target: { value: "2026-09-09" } });
    fireEvent.click(screen.getByRole("button", { name: "Create transfer order" }));
    expect(await screen.findByText("The planned receipt date must not be before the ship date.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("is sent on create when given", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, order()) : json(200, order()));
    renderAt("/inventory/transfer-orders/new");
    fillOrder();
    fireEvent.change(screen.getByLabelText(/Planned receipt date/), { target: { value: "2026-09-12" } });
    fireEvent.click(screen.getByRole("button", { name: "Create transfer order" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(1));
    expect(bodyOf(sent("POST")[0]).plannedReceiptDate).toBe("2026-09-12");
  });

  it("is left out of the request when empty", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, order()) : json(200, order()));
    renderAt("/inventory/transfer-orders/new");
    fillOrder();
    fireEvent.click(screen.getByRole("button", { name: "Create transfer order" }));
    await waitFor(() => expect(sent("POST")).toHaveLength(1));
    expect(bodyOf(sent("POST")[0])).not.toHaveProperty("plannedReceiptDate");
  });
});

describe("Transfer line inquiry", () => {
  it("shows the planned receipt date of each order", async () => {
    handler = () =>
      json(200, {
        items: [
          {
            orderId: ORDER_ID, legalEntityId: ENTITY_ID, number: "TO-1", orderStatus: "SHIPPED", plannedReceiptDate: "2026-09-08", fromWarehouseId: "W1",
            toWarehouseId: "W2", shipDate: "2026-09-01", lineId: LINE_A, lineNo: 1, productId: PRODUCT_ID, dimensions: { site: "S1" }, unit: "EA", qty: "10",
            shippedQty: "10", receivedQty: "0", remainingQty: "10", inTransit: "10", daysInTransit: 12, closed: false, late: "LATE_RECEIPT", daysLate: 3,
          },
        ],
        truncated: false,
      });
    renderAt("/inventory/transfer-lines");
    const table = await findTable();
    await waitFor(() => expect(table).toHaveTextContent("2026-09-08"));
    expect(table).toHaveTextContent("Late receipt, 3 days overdue");
  });
});
