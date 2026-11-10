import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { findAlert, findStatus } from "../../test-utils";
import inventory from "./module";
import { buildTransferEdit, startingValues } from "./TransferEditForm";
import type { TransferOrder } from "./api/client";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ORDER_ID = "8a8a8a8a-0000-4000-8000-000000000008";
const LINE_ID = "1b1b1b1b-0000-4000-8000-000000000009";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];
const json = (status: number, body: unknown) => ({ status, body });

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
afterEach(() => vi.unstubAllGlobals());

const line = (o: Record<string, unknown> = {}) => ({
  id: LINE_ID, lineNo: 1, productId: PRODUCT_ID, dimensions: { site: "S1" }, unit: "EA", qty: "100", shippedQty: "0", receivedQty: "0",
  inTransit: "0", underdeliveryPct: "0", overdeliveryPct: "0", closed: false, ...o,
});
const order = (o: Record<string, unknown> = {}) => ({
  id: ORDER_ID, legalEntityId: "9e8d7c6b-0000-4000-8000-000000000003", number: "TO-ABCDEF123456", status: "CREATED", fromWarehouseId: "W1",
  toWarehouseId: "W2", transitWarehouseId: "T1", useTransit: true, shipDate: "2026-09-01", version: 1, createdAt: "2026-09-01T08:00:00Z",
  lines: [line()], ...o,
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {inventory.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

describe("buildTransferEdit", () => {
  const o = order() as unknown as TransferOrder;
  it("sends only what changed", () => {
    const v = startingValues(o);
    expect(buildTransferEdit(o, v).errors).toEqual({ form: "Nothing was changed." });
    const r = buildTransferEdit(o, { ...v, plannedReceiptDate: "2026-09-05", qty: { [LINE_ID]: "80" }, add: { productId: PRODUCT_ID, site: "S1", unit: "EA", qty: "2" } });
    expect(r.errors).toEqual({});
    expect(r.edit).toEqual({
      plannedReceiptDate: "2026-09-05",
      updateLines: [{ lineId: LINE_ID, qty: "80" }],
      addLines: [{ productId: PRODUCT_ID, dimensions: { site: "S1" }, unit: "EA", qty: "2" }],
    });
  });
  it("refuses bad values and removing every line", () => {
    const v = startingValues(o);
    expect(buildTransferEdit(o, { ...v, qty: { [LINE_ID]: "0" } }).errors).toEqual({ [`qty-${LINE_ID}`]: "Enter a quantity above zero." });
    expect(buildTransferEdit(o, { ...v, plannedReceiptDate: "2026-08-01" }).errors.plannedReceiptDate).toBeDefined();
    expect(buildTransferEdit(o, { ...v, remove: { [LINE_ID]: true } }).errors.form).toMatch(/at least one line/);
    expect(Object.keys(buildTransferEdit(o, { ...v, add: { productId: "x", site: "", unit: "", qty: "" } }).errors).sort()).toEqual(["addProduct", "addQty", "addSite", "addUnit"]);
  });
});

describe("Transfer order edit form", () => {
  it("patches the order at its version and shows the result", async () => {
    handler = (_u, init) => (init.method === "PATCH" ? json(200, order({ version: 2, lines: [line({ qty: "80" })] })) : json(200, order()));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    const qty = await screen.findByLabelText("Line 1: quantity (EA)");
    fireEvent.change(qty, { target: { value: "80" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await findStatus()).toHaveTextContent("Transfer order TO-ABCDEF123456 updated.");
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.url.pathname).toBe(`/api/v1/inventory/transfer-orders/${ORDER_ID}`);
    expect(new Headers(patch.init.headers).get("If-Match")).toBe('"1"');
    expect(JSON.parse(patch.init.body as string)).toEqual({ updateLines: [{ lineId: LINE_ID, qty: "80" }] });
  });

  it("refuses to remove every line without calling the service", async () => {
    handler = () => json(200, order());
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByLabelText("Remove line 1"));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Changes not saved");
    expect(alert).toHaveTextContent("An order needs at least one line");
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);
  });

  it("shows the service refusal and no form for a shipped order", async () => {
    handler = (_u, init) => (init.method === "PATCH" ? json(422, { code: "INV-TRF-006", detail: "cannot be edited" }) : json(200, order()));
    const first = renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    fireEvent.change(await screen.findByLabelText("Line 1: quantity (EA)"), { target: { value: "90" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await findAlert()).toHaveTextContent("INV-TRF-006");
    first.unmount();
    handler = () => json(200, order({ status: "SHIPPED", lines: [line({ shippedQty: "100", inTransit: "100" })] }));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    expect(await screen.findByRole("heading", { name: "Receive" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  });
});
