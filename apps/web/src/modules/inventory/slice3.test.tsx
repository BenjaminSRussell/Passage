import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import inventory from "./module";
import { validateAbc } from "./NewAbcRunPage";
import { validateTransfer, type TransferValues } from "./NewTransferPage";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const ORDER_ID = "8a8a8a8a-0000-4000-8000-000000000008";
const LINE_ID = "1b1b1b1b-0000-4000-8000-000000000009";
const RUN_ID = "2c2c2c2c-0000-4000-8000-000000000010";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];

const json = (status: number, body: unknown) => ({ status, body });
const sent = (method: string) => calls.filter((c) => c.method === method);
const bodyOf = (call?: { init: RequestInit }) => JSON.parse((call?.init.body as string) ?? "null");
const header = (call: { init: RequestInit } | undefined, name: string) => new Headers(call?.init.headers).get(name);

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

const line = (overrides: Record<string, unknown> = {}) => ({
  id: LINE_ID,
  lineNo: 1,
  productId: PRODUCT_ID,
  dimensions: { site: "S1", warehouse: "W1", batch: "B1" },
  unit: "EA",
  qty: "100",
  shippedQty: "0",
  receivedQty: "0",
  inTransit: "0",
  underdeliveryPct: "10",
  overdeliveryPct: "0",
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
  version: 1,
  createdAt: "2026-09-01T08:00:00Z",
  lines: [line()],
  ...overrides,
});

const run = (overrides: Record<string, unknown> = {}) => ({
  id: RUN_ID,
  legalEntityId: ENTITY_ID,
  criterion: "VALUE",
  thresholds: { a: "80", b: "15", c: "5" },
  zeroClass: "C",
  createdAt: "2026-09-01T08:00:00Z",
  classCounts: { A: 1, B: 1, C: 2, N: 0 },
  ...overrides,
});

describe("Transfer orders", () => {
  it("lists orders with status and progress and filters by status", async () => {
    handler = () =>
      json(200, {
        items: [order(), order({ id: "o2", number: "TO-2", status: "PARTIALLY_RECEIVED", lines: [line({ shippedQty: "100", receivedQty: "60", inTransit: "40" })] })],
      });
    const { container } = renderAt("/inventory/transfer-orders");
    expect(screen.getByRole("table", { name: "Transfer orders" })).toHaveAttribute("aria-busy", "true");
    const table = await screen.findByRole("table", { name: "Transfer orders" });
    await waitFor(() => expect(table).toHaveTextContent("Partially received"));
    expect(table).toHaveTextContent("60 of 100 received");
    expect(within(table).getByRole("link", { name: "TO-ABCDEF123456" })).toHaveAttribute("href", `/inventory/transfer-orders/${ORDER_ID}`);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "SHIPPED" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("SHIPPED"));
  });

  it("explains an empty list and shows failures", async () => {
    handler = () => json(200, { items: [] });
    const first = renderAt("/inventory/transfer-orders");
    expect(await screen.findByText("No transfer orders yet")).toBeInTheDocument();
    first.unmount();
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty inventory.read" });
    renderAt("/inventory/transfer-orders");
    expect(await screen.findByRole("alert")).toHaveTextContent("missing duty inventory.read (AUTH-0003)");
  });
});

describe("New transfer order", () => {
  const values: TransferValues = {
    legalEntityId: ENTITY_ID,
    fromWarehouseId: "W1",
    toWarehouseId: "W2",
    toSiteId: "",
    useTransit: true,
    transitWarehouseId: "T1",
    shipDate: "2026-09-01",
    lines: [{ productId: PRODUCT_ID, qty: "100", unit: "EA", underdeliveryPct: "10", overdeliveryPct: "", dimensions: { site: "S1", location: "", batch: "", serial: "" } }],
  };

  it("validates the route and every line", () => {
    expect(validateTransfer(values)).toEqual({ header: {}, lines: [{}] });
    const bad = validateTransfer({
      ...values,
      toWarehouseId: "W1",
      transitWarehouseId: "",
      lines: [{ ...values.lines[0], qty: "2", underdeliveryPct: "150", dimensions: { site: "", location: "", batch: "", serial: "SN1" } }],
    });
    expect(bad.header).toEqual({
      toWarehouseId: "The receiving warehouse must differ from the shipping warehouse.",
      transitWarehouseId: "Enter the transit warehouse, or ship in one step.",
    });
    expect(bad.lines[0]).toEqual({
      qty: "A serial number moves one unit at a time.",
      site: "Site is required.",
      underdeliveryPct: "Enter a percentage from 0 to 100.",
    });
    expect(validateTransfer({ ...values, useTransit: false, transitWarehouseId: "" }).header).toEqual({});
  });

  it("creates a two-line order and opens it", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, order()) : json(200, order()));
    const { container } = renderAt("/inventory/transfer-orders/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Shipping warehouse"), { target: { value: "W1" } });
    fireEvent.change(screen.getByLabelText("Receiving warehouse"), { target: { value: "W2" } });
    fireEvent.change(screen.getByLabelText("Transit warehouse"), { target: { value: "T1" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "100" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText("Batch"), { target: { value: "B1" } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    expect(screen.getByRole("heading", { name: "Line 2" })).toBeInTheDocument();
    expect(screen.getAllByLabelText("Site")[1]).toHaveValue("S1");
    fireEvent.change(screen.getAllByLabelText("Product")[1], { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getAllByLabelText("Quantity")[1], { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Create transfer order" }));
    expect(await screen.findByRole("heading", { name: "Transfer order TO-ABCDEF123456" })).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe("/api/v1/inventory/transfer-orders");
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    const body = bodyOf(post);
    expect(body).toMatchObject({ legalEntityId: ENTITY_ID, fromWarehouseId: "W1", toWarehouseId: "W2", transitWarehouseId: "T1", useTransit: true });
    expect(body.lines).toEqual([
      { productId: PRODUCT_ID, qty: "100", unit: "EA", dimensions: { site: "S1", batch: "B1" } },
      { productId: PRODUCT_ID, qty: "5", unit: "EA", dimensions: { site: "S1" } },
    ]);
  });

  it("drops the transit warehouse for a one-step transfer and shows service errors", async () => {
    handler = () => json(400, { code: "COM-0001", detail: "one or more fields are invalid", errors: { "lines[0].unit": "is required (1 to 12 characters of A-Z, 0-9, _ or -)" } });
    renderAt("/inventory/transfer-orders/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Shipping warehouse"), { target: { value: "W1" } });
    fireEvent.change(screen.getByLabelText("Receiving warehouse"), { target: { value: "W2" } });
    fireEvent.click(screen.getByLabelText("Ship through a transit warehouse"));
    expect(screen.queryByLabelText("Transit warehouse")).toBeNull();
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
    fireEvent.click(screen.getByRole("button", { name: "Create transfer order" }));
    expect(await screen.findByText("is required (1 to 12 characters of A-Z, 0-9, _ or -)")).toBeInTheDocument();
    const body = bodyOf(sent("POST")[0]);
    expect(body.useTransit).toBe(false);
    expect(body.transitWarehouseId).toBeUndefined();
  });

  it("refuses to send an invalid form", async () => {
    handler = () => json(500, {});
    renderAt("/inventory/transfer-orders/new");
    fireEvent.click(screen.getByRole("button", { name: "Create transfer order" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Site is required.")).toBeInTheDocument();
    expect(calls.length).toBe(0);
  });
});

describe("Transfer order detail", () => {
  it("ships a created order at its version and date", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(200, order({ status: "SHIPPED", version: 2, lines: [line({ shippedQty: "100", inTransit: "100" })] })) : json(200, order());
    const { container } = renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    expect(await screen.findByRole("heading", { name: "Transfer order TO-ABCDEF123456" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Ship date"), { target: { value: "2026-09-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Ship order" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Shipped: 1 line is now in transit at T1.");
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/transfer-orders/${ORDER_ID}:ship`);
    expect(header(post, "If-Match")).toBe('"1"');
    expect(bodyOf(post)).toEqual({ shipDate: "2026-09-02" });
    expect(screen.getByRole("heading", { name: "Receive" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel order" })).toBeNull();
  });

  it("books a short-closed receipt", async () => {
    const shipped = order({ status: "SHIPPED", version: 2, lines: [line({ shippedQty: "100", inTransit: "100" })] });
    handler = (_url, init) =>
      init.method === "POST"
        ? json(200, order({ status: "RECEIVED", version: 3, receiptDate: "2026-09-05", lines: [line({ shippedQty: "100", receivedQty: "95", inTransit: "5", closed: true })] }))
        : json(200, shipped);
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    const qty = await screen.findByLabelText("Line 1: received EA");
    expect(qty).toHaveValue("100");
    fireEvent.change(qty, { target: { value: "95" } });
    fireEvent.click(screen.getByLabelText("Close line 1 short (within 10%)"));
    fireEvent.change(screen.getByLabelText("Receipt date"), { target: { value: "2026-09-05" } });
    fireEvent.click(screen.getByRole("button", { name: "Book receipt" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Transfer order TO-ABCDEF123456 fully received.");
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/transfer-orders/${ORDER_ID}:receive`);
    expect(header(post, "If-Match")).toBe('"2"');
    expect(bodyOf(post)).toEqual({ receiptDate: "2026-09-05", lines: [{ lineId: LINE_ID, qty: "95", closeShort: true }] });
    expect(screen.getByRole("table", { name: "Transfer lines" })).toHaveTextContent("Closed short");
  });

  it("shows an over-receipt refusal with its code and cancels a created order", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(422, { code: "INV-TRF-002", detail: "received quantity 120 exceeds shipped quantity 100 plus tolerance 0 %" }) : json(200, order({ status: "SHIPPED", lines: [line({ shippedQty: "100", inTransit: "100" })] }));
    const first = renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    fireEvent.change(await screen.findByLabelText("Line 1: received EA"), { target: { value: "120" } });
    fireEvent.click(screen.getByRole("button", { name: "Book receipt" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("plus tolerance 0 % (INV-TRF-002)");
    first.unmount();
    calls = [];
    handler = (_url, init) => (init.method === "POST" ? json(200, order({ status: "CANCELLED", version: 2 })) : json(200, order()));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel order" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Transfer order TO-ABCDEF123456 cancelled.");
    expect(sent("POST")[0].url.pathname).toBe(`/api/v1/inventory/transfer-orders/${ORDER_ID}:cancel`);
  });

  it("says when the order does not exist", async () => {
    handler = () => json(404, { code: "INV-TRF-004", detail: "transfer order not found" });
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    expect(await screen.findByRole("heading", { name: "Transfer order not found" })).toBeInTheDocument();
  });
});

describe("Stock in transit", () => {
  it("lists lines in transit and flags short closes", async () => {
    handler = () =>
      json(200, {
        items: [
          { orderId: ORDER_ID, number: "TO-1", lineId: "l1", productId: PRODUCT_ID, dimensions: { site: "S1", warehouse: "W1" }, unit: "EA", fromWarehouseId: "W1", toWarehouseId: "W2", transitWarehouseId: "T1", shipDate: "2026-09-01", daysInTransit: 4, inTransitQty: "5", closedShort: true },
        ],
      });
    const { container } = renderAt("/inventory/in-transit");
    const table = await screen.findByRole("table", { name: "Stock in transit" });
    await waitFor(() => expect(table).toHaveTextContent("W1 → T1 → W2"));
    expect(table).toHaveTextContent("5 EA");
    expect(table).toHaveTextContent("Closed short, investigate");
    expect(screen.getByText("One line was closed short: the missing quantity is still booked in transit.")).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: "TO-1" })).toHaveAttribute("href", `/inventory/transfer-orders/${ORDER_ID}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains an empty result", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/in-transit");
    expect(await screen.findByText("Nothing in transit")).toBeInTheDocument();
  });
});

describe("ABC classification", () => {
  it("lists runs with class counts", async () => {
    handler = () => json(200, { items: [run()] });
    const { container } = renderAt("/inventory/abc-runs");
    const table = await screen.findByRole("table", { name: "ABC runs" });
    await waitFor(() => expect(table).toHaveTextContent("80 / 15 / 5"));
    expect(within(table).getByRole("link", { name: "2026-09-01 08:00 UTC" })).toHaveAttribute("href", `/inventory/abc-runs/${RUN_ID}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("validates thresholds before running", () => {
    expect(validateAbc({ legalEntityId: ENTITY_ID, a: "80", b: "15", c: "5", zeroClass: "C" })).toEqual({});
    expect(validateAbc({ legalEntityId: ENTITY_ID, a: "80", b: "15", c: "4", zeroClass: "C" })).toEqual({ thresholds: "The three percentages must add up to 100 (they add up to 99)." });
    expect(validateAbc({ legalEntityId: "x", a: "120", b: "15", c: "5", zeroClass: "C" })).toEqual({
      legalEntityId: "Enter the legal entity id (a UUID).",
      a: "Enter a percentage from 0 to 100.",
    });
  });

  it("runs a classification and shows the report", async () => {
    handler = (_url, init) =>
      json(init.method === "POST" ? 201 : 200, run({ zeroClass: "N", items: [{ productId: PRODUCT_ID, site: "S1", value: "800.00", cumPct: "80.00", class: "A" }] }));
    const { container } = renderAt("/inventory/abc-runs/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Products with no value"), { target: { value: "N" } });
    fireEvent.click(screen.getByRole("button", { name: "Run classification" }));
    const table = await screen.findByRole("table", { name: "Classified items" });
    expect(table).toHaveTextContent("Class A");
    expect(table).toHaveTextContent("800.00");
    const post = sent("POST")[0];
    expect(bodyOf(post)).toEqual({ legalEntityId: ENTITY_ID, zeroClass: "N", thresholds: { a: "80", b: "15", c: "5" }, criterion: "VALUE" });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
  });

  it("shows a threshold rejection from the service", async () => {
    handler = () => json(422, { code: "INV-ABC-001", detail: "ABC thresholds must sum to 100 (got 99)" });
    renderAt("/inventory/abc-runs/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.click(screen.getByRole("button", { name: "Run classification" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("ABC thresholds must sum to 100 (got 99)");
  });

  it("says when a run does not exist", async () => {
    handler = () => json(404, { code: "INV-ABC-002", detail: "ABC run not found" });
    renderAt(`/inventory/abc-runs/${RUN_ID}`);
    expect(await screen.findByRole("heading", { name: "ABC run not found" })).toBeInTheDocument();
  });
});
