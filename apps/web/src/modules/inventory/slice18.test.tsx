import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { PrincipalProvider } from "../../shell/principal";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import inventory from "./module";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const ORDER_ID = "8c8c8c8c-0000-4000-8000-000000000021";
const LINE_ID = "8d8d8d8d-0000-4000-8000-000000000022";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
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

function renderAt(path: string, duties: string[] = ["inventory.read", "inventory.consignment.write"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {inventory.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const order = (overrides: Record<string, unknown> = {}, line: Record<string, unknown> = {}) => ({
  id: ORDER_ID,
  legalEntityId: ENTITY_ID,
  number: "CRO-00000000A1B2",
  vendorId: "V1",
  siteId: "S1",
  warehouseId: "W1",
  requestedDate: "2026-10-01",
  currency: "USD",
  status: "CREATED",
  version: 1,
  createdAt: "2026-10-01T08:00:00Z",
  closedAt: null,
  lines: [
    {
      id: LINE_ID,
      lineNo: 1,
      productId: PRODUCT_ID,
      dimensions: { site: "S1", warehouse: "W1", owner: "V1" },
      unit: "EA",
      qty: "10",
      receivedQty: "0",
      openQty: "10",
      givenUpQty: "0",
      consignmentPrice: "4",
      ...line,
    },
  ],
  ...overrides,
});

const partly = () => order({ status: "PARTIALLY_RECEIVED", version: 2 }, { receivedQty: "4", openQty: "6" });

describe("Closing and cancelling consignment orders", () => {
  it("closes a partly received order after confirmation and shows what was given up", async () => {
    const closed = order(
      { status: "CLOSED", version: 3, closedAt: "2026-10-05T09:00:00Z", closedBy: "buyer", closeReason: "vendor stopped supplying" },
      { receivedQty: "4", openQty: "0", givenUpQty: "6" },
    );
    handler = (_url, init) => json(200, init.method === "POST" ? closed : partly());
    renderAt(`/inventory/consignment-orders/${ORDER_ID}`);
    fireEvent.change(await screen.findByLabelText("Reason"), { target: { value: "  vendor stopped supplying " } });
    fireEvent.click(screen.getByRole("button", { name: "Close order" }));
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("stop expecting the 6 EA still open");
    expect(sent("POST")).toHaveLength(0);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Close order" }));
    expect(await screen.findByText("Consignment order CRO-00000000A1B2 closed; the open quantity is given up.")).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/consignment-orders/${ORDER_ID}:close`);
    expect(header(post, "If-Match")).toBe('"2"');
    expect(bodyOf(post)).toEqual({ reason: "vendor stopped supplying" });
    expect(screen.getByText("vendor stopped supplying")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Consignment lines" })).toHaveTextContent("6");
    // An ended order takes no receipt and offers no further ending.
    expect(screen.queryByRole("button", { name: "Book receipt" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Close order" })).toBeNull();
  });

  it("cancels an order nothing was received against", async () => {
    const cancelled = order({ status: "CANCELLED", version: 2, closedAt: "2026-10-05T09:00:00Z" }, { openQty: "0", givenUpQty: "10" });
    handler = (_url, init) => json(200, init.method === "POST" ? cancelled : order());
    renderAt(`/inventory/consignment-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel order" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Cancel order" }));
    expect(await screen.findByText("Consignment order CRO-00000000A1B2 cancelled.")).toBeInTheDocument();
    const post = sent("POST")[0];
    expect(post.url.pathname).toBe(`/api/v1/inventory/consignment-orders/${ORDER_ID}:cancel`);
    expect(header(post, "If-Match")).toBe('"1"');
    expect(bodyOf(post)).toEqual({});
  });

  it("sends nothing when the confirmation is declined", async () => {
    handler = () => json(200, partly());
    renderAt(`/inventory/consignment-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Close order" }));
    fireEvent.click(await screen.findByRole("button", { name: "Keep order" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(sent("POST")).toHaveLength(0);
  });

  it("refuses a reason that is too long without calling the service", async () => {
    handler = () => json(200, partly());
    renderAt(`/inventory/consignment-orders/${ORDER_ID}`);
    fireEvent.change(await screen.findByLabelText("Reason"), { target: { value: "x".repeat(201) } });
    fireEvent.click(screen.getByRole("button", { name: "Close order" }));
    expect(await screen.findByText("Use at most 200 characters.")).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(sent("POST")).toHaveLength(0);
  });

  it("shows a refusal from the service", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(412, { code: "conflict_version", detail: "replenishment order changed since it was read" }) : json(200, partly()));
    renderAt(`/inventory/consignment-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Close order" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Close order" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The order was not closed");
    expect(alert).toHaveTextContent("(conflict_version)");
  });

  it("sends one close when the confirmation is clicked twice", async () => {
    const closed = order({ status: "CLOSED", version: 3, closedAt: "2026-10-05T09:00:00Z" }, { receivedQty: "4", openQty: "0", givenUpQty: "6" });
    handler = (_url, init) => json(200, init.method === "POST" ? closed : partly());
    renderAt(`/inventory/consignment-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Close order" }));
    const confirm = within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Close order" });
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    expect(await screen.findByText("Consignment order CRO-00000000A1B2 closed; the open quantity is given up.")).toBeInTheDocument();
    expect(sent("POST")).toHaveLength(1);
  });

  it("names the receipt when a receipt is refused because the order was closed meanwhile", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(422, { code: "INV-CNS-003", detail: "replenishment order CRO-00000000A1B2 takes no receipt (CLOSED)" }) : json(200, partly());
    renderAt(`/inventory/consignment-orders/${ORDER_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Book receipt" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The receipt was not booked");
    expect(alert).toHaveTextContent("(INV-CNS-003)");
    expect(sent("POST")[0].url.pathname).toBe(`/api/v1/inventory/consignment-orders/${ORDER_ID}:receive`);
  });

  it("offers neither close nor cancel to a user without the consignment duty", async () => {
    handler = () => json(200, partly());
    renderAt(`/inventory/consignment-orders/${ORDER_ID}`, ["inventory.read"]);
    await screen.findByRole("button", { name: "Book receipt" });
    expect(screen.queryByRole("button", { name: "Close order" })).toBeNull();
  });

  it("does not offer to end a fully received order", async () => {
    handler = () => json(200, order({ status: "RECEIVED", version: 3 }, { receivedQty: "10", openQty: "0" }));
    renderAt(`/inventory/consignment-orders/${ORDER_ID}`);
    await screen.findByRole("table", { name: "Consignment lines" });
    expect(screen.queryByRole("button", { name: "Close order" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Cancel order" })).toBeNull();
  });

  it("lists closed and cancelled orders and filters by them", async () => {
    handler = (url) => json(200, { items: url.searchParams.get("status") === "CLOSED" ? [order({ status: "CLOSED", closedAt: "2026-10-05T09:00:00Z" })] : [order()] });
    renderAt("/inventory/consignment-orders");
    await findTable({ name: "Consignment orders" });
    const select = screen.getByLabelText("Status");
    expect(within(select).getByRole("option", { name: "Closed" })).toBeInTheDocument();
    expect(within(select).getByRole("option", { name: "Cancelled" })).toBeInTheDocument();
    fireEvent.change(select, { target: { value: "CLOSED" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("status") === "CLOSED")).toBe(true));
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Consignment orders" })).getByText("Closed")).toBeInTheDocument());
  });
});
