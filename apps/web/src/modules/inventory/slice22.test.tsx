import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { findAlert, findStatus } from "../../test-utils";
import inventory from "./module";
import { buildTransferEdit, startingValues } from "./TransferEditForm";
import type { TransferOrder } from "./api/client";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const ORDER_ID = "8a8a8a8a-0000-4000-8000-000000000008";
const LINE_ID = "1b1b1b1b-0000-4000-8000-000000000009";

type Handler = (url: URL, init: RequestInit) => { status: number; body?: unknown };
let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];
const json = (status: number, body?: unknown) => ({ status, body });

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const { status, body } = handler(url, init);
      if (status === 204) return new Response(null, { status });
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
  id: ORDER_ID, legalEntityId: ENTITY_ID, number: "TO-ABCDEF123456", status: "DRAFT", fromWarehouseId: "W1", toWarehouseId: "W2",
  transitWarehouseId: "T1", useTransit: true, shipDate: "2026-09-01", version: 1, createdAt: "2026-09-01T08:00:00Z", lines: [line()], ...o,
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

describe("Transfer order draft", () => {
  it("shows the draft, confirms it at its version and offers shipping afterwards", async () => {
    handler = (_u, init) => (init.method === "POST" ? json(200, order({ status: "CREATED", version: 2 })) : json(200, order()));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    expect(await screen.findByText("This order is a working copy. Nothing sees it until you confirm it; edit it below, or delete it.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Ship" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Cancel order" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Confirm order" }));
    expect(await findStatus()).toHaveTextContent("Transfer order TO-ABCDEF123456 confirmed. It can now be shipped.");
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe(`/api/v1/inventory/transfer-orders/${ORDER_ID}:confirm`);
    expect(new Headers(post.init.headers).get("If-Match")).toBe('"1"');
    expect(await screen.findByRole("heading", { name: "Ship" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Confirm order" })).toBeNull();
  });

  it("shows the service refusal of a confirm", async () => {
    handler = (_u, init) => (init.method === "POST" ? json(412, { code: "conflict_version", detail: "changed since it was read" }) : json(200, order()));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Confirm order" }));
    expect(await findAlert()).toHaveTextContent("(conflict_version)");
    expect(screen.getByRole("button", { name: "Reload order" })).toBeInTheDocument();
  });
});

describe("Delete a transfer order", () => {
  it("asks first, then deletes at the version and returns to the list", async () => {
    handler = (u, init) => {
      if (init.method === "DELETE") return json(204);
      if (u.pathname.endsWith("/transfer-orders")) return json(200, { items: [] });
      return json(200, order({ status: "CREATED", version: 4 }));
    };
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Delete order" }));
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    expect(await screen.findByRole("alertdialog", { name: "Delete transfer order TO-ABCDEF123456?" })).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "Delete order" }).at(-1)!);
    expect(await screen.findByRole("heading", { name: "Transfer orders" })).toBeInTheDocument();
    const del = calls.find((c) => c.method === "DELETE")!;
    expect(del.url.pathname).toBe(`/api/v1/inventory/transfer-orders/${ORDER_ID}`);
    expect(new Headers(del.init.headers).get("If-Match")).toBe('"4"');
  });

  it("keeps the order when the question is declined or the service refuses", async () => {
    handler = (_u, init) => (init.method === "DELETE" ? json(422, { code: "INV-TRF-006", detail: "has shipped quantity" }) : json(200, order({ status: "CREATED" })));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Delete order" }));
    fireEvent.click(screen.getByRole("button", { name: "Keep order" }));
    await waitFor(() => expect(screen.queryByText("Delete transfer order TO-ABCDEF123456?")).toBeNull());
    fireEvent.click(screen.getByRole("button", { name: "Delete order" }));
    fireEvent.click(screen.getAllByRole("button", { name: "Delete order" }).at(-1)!);
    expect(await screen.findByText(/has shipped quantity \(INV-TRF-006\)/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Transfer order TO-ABCDEF123456" })).toBeInTheDocument();
  });

  it("offers no delete once the order has shipped", async () => {
    handler = () => json(200, order({ status: "SHIPPED", lines: [line({ shippedQty: "100", inTransit: "100" })] }));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    expect(await screen.findByRole("heading", { name: "Receive" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete order" })).toBeNull();
  });
});

describe("Delivery terms", () => {
  const o = order({ status: "CREATED", deliveryTerms: "FCA" }) as unknown as TransferOrder;

  it("sends only a changed code, and an empty one clears it", () => {
    const v = startingValues(o);
    expect(v.deliveryTerms).toBe("FCA");
    expect(buildTransferEdit(o, v).errors).toEqual({ form: "Nothing was changed." });
    expect(buildTransferEdit(o, { ...v, deliveryTerms: "", modeOfDelivery: " ROAD " }).edit).toEqual({ deliveryTerms: "", modeOfDelivery: "ROAD" });
    expect(buildTransferEdit(o, { ...v, modeOfDelivery: "X".repeat(21) }).errors.modeOfDelivery).toMatch(/at most 20/i);
  });

  it("shows the codes on the order", async () => {
    handler = () => json(200, order({ status: "CREATED", deliveryTerms: "FCA", modeOfDelivery: "ROAD" }));
    renderAt(`/inventory/transfer-orders/${ORDER_ID}`);
    expect(await screen.findByText("FCA", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByText("ROAD", { selector: "dd" })).toBeInTheDocument();
  });
});

describe("New transfer order as a draft", () => {
  function fill() {
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Shipping warehouse"), { target: { value: "W1" } });
    fireEvent.change(screen.getByLabelText(/Receiving warehouse/), { target: { value: "W2" } });
    fireEvent.change(screen.getByLabelText("Transit warehouse"), { target: { value: "T1" } });
    fireEvent.change(screen.getByLabelText(/Delivery terms/), { target: { value: "FCA" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "S1" } });
  }

  it("saves a draft with its delivery terms and opens it", async () => {
    handler = (_u, init) => (init.method === "POST" ? json(201, order({ deliveryTerms: "FCA" })) : json(200, order({ deliveryTerms: "FCA" })));
    renderAt("/inventory/transfer-orders/new");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Save as draft" }));
    expect(await screen.findByRole("heading", { name: "Transfer order TO-ABCDEF123456" })).toBeInTheDocument();
    const body = JSON.parse(calls.find((c) => c.method === "POST")!.init.body as string);
    expect(body.draft).toBe(true);
    expect(body.deliveryTerms).toBe("FCA");
    expect(body).not.toHaveProperty("modeOfDelivery");
  });

  it("creates a normal order without the draft flag and checks the codes first", async () => {
    handler = () => json(201, order({ status: "CREATED" }));
    renderAt("/inventory/transfer-orders/new");
    fill();
    fireEvent.change(screen.getByLabelText(/Delivery terms/), { target: { value: "X".repeat(21) } });
    fireEvent.click(screen.getByRole("button", { name: "Save as draft" }));
    expect(await screen.findByText("At most 20 characters.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText(/Delivery terms/), { target: { value: "FCA" } });
    fireEvent.click(screen.getByRole("button", { name: "Create transfer order" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === "POST")!.init.body as string)).not.toHaveProperty("draft");
  });
});

describe("Transfer order list", () => {
  it("filters by draft", async () => {
    handler = (u) => json(200, { items: u.searchParams.get("status") === "DRAFT" ? [order()] : [] });
    renderAt("/inventory/transfer-orders");
    fireEvent.change(await screen.findByLabelText("Status"), { target: { value: "DRAFT" } });
    expect(await screen.findByRole("link", { name: "TO-ABCDEF123456" })).toBeInTheDocument();
    expect(screen.getAllByText("Draft")).toHaveLength(2);
  });
});
