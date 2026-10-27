import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MAX_FIRM_BATCH, type FirmBatch, type Message, type PlannedOrder } from "./api/client";
import { validateFirmSelection } from "./Firming";
import planning from "./module";

const base: PlannedOrder = {
  id: "00000000-0000-0000-0000-0000000000a2",
  versionId: "00000000-0000-0000-0000-0000000000b1",
  number: "PLN-000002",
  itemId: "00000000-0000-0000-0000-0000000000c1",
  orderType: "purchase",
  qty: "100",
  orderDate: "2026-11-08",
  deliveryDate: "2026-11-10",
  futuresDate: null,
  delayDays: 0,
  lotRule: "lot-for-lot",
  status: "approved",
  version: 2,
};
const second: PlannedOrder = { ...base, id: "00000000-0000-0000-0000-0000000000a1", number: "PLN-000001", qty: "50", status: "unprocessed", version: 1 };
const firmed: PlannedOrder = {
  ...base,
  id: "00000000-0000-0000-0000-0000000000a3",
  number: "PLN-000003",
  status: "firmed",
  firmedTargetType: "purchase-order",
  firmedTargetId: "00000000-0000-0000-0000-0000000000d1",
};
const vendor = "00000000-0000-0000-0000-0000000000e1";

const batch: FirmBatch = {
  id: "00000000-0000-0000-0000-0000000000f1",
  grouping: "vendor-date",
  firmed: 2,
  groups: [
    {
      targetType: "purchase-order",
      targetId: "00000000-0000-0000-0000-0000000000d4",
      orderType: "purchase",
      siteId: "00000000-0000-0000-0000-0000000000f9",
      vendorId: vendor,
      deliveryDate: "2026-11-10",
      qty: "150",
      plannedOrderIds: [second.id, base.id],
    },
  ],
};

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

type Handler = (url: string, init?: RequestInit) => Response;

/** Routes fetch by URL substring, first match wins. */
function stubApi(...routes: [string, Handler][]) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    for (const [match, respond] of routes) {
      if (url.includes(match)) return respond(url, init);
    }
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const posts = (fetchMock: ReturnType<typeof stubApi>) => fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");
const lists = (fetchMock: ReturnType<typeof stubApi>) => fetchMock.mock.calls.filter(([url, init]) => url.includes("/planned-orders?") && !init?.method);
const keyOf = (init?: RequestInit) => (init?.headers as Record<string, string>)["Idempotency-Key"];

function Location() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname + location.search}</div>;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
      <Location />
    </MemoryRouter>,
  );
}

async function ordersTable() {
  const table = await screen.findByRole("table", { name: "Planned orders" });
  await waitFor(() => expect(within(table).getByText("PLN-000002")).toBeInTheDocument());
  return table;
}

afterEach(() => vi.unstubAllGlobals());

describe("validateFirmSelection", () => {
  it("needs one to 1,000 planned orders", () => {
    expect(validateFirmSelection([])).toMatch(/at least one/);
    expect(validateFirmSelection(new Array<string>(MAX_FIRM_BATCH + 1).fill(base.id))).toMatch(/at most 1000/);
    expect(validateFirmSelection([base.id, second.id])).toBeNull();
  });
});

describe("Firming planned orders (FR-22.10)", () => {
  it("firms the selected open orders with the grouping and an Idempotency-Key, shows the orders created and reloads", async () => {
    let listed = [base, second, firmed];
    const fetchMock = stubApi(
      ["/planned-orders:firm", () => {
        listed = listed.map((o) => (o.id === firmed.id ? o : { ...o, status: "firmed" as const }));
        return json(batch, 201);
      }],
      ["/planned-orders", () => json({ items: listed })],
    );
    renderAt("/planning/planned-orders");
    const table = await ordersTable();
    const firm = screen.getByRole("button", { name: "Firm selected" });
    expect(firm).toBeDisabled();
    // A firmed order cannot be selected again.
    expect(within(table).queryByRole("checkbox", { name: "Select PLN-000003 for firming" })).not.toBeInTheDocument();

    fireEvent.click(within(table).getByRole("checkbox", { name: "Select PLN-000002 for firming" }));
    fireEvent.click(within(table).getByRole("checkbox", { name: "Select PLN-000001 for firming" }));
    // Ticking a box selects; it does not open the order.
    expect(screen.getByTestId("location")).toHaveTextContent("/planning/planned-orders");
    expect(screen.getByText("2 planned orders selected.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Purchase order grouping"), { target: { value: "vendor-week" } });
    fireEvent.click(firm);

    expect(await screen.findByText("Firmed 2 planned orders into 1 order")).toBeInTheDocument();
    const [[url, init]] = posts(fetchMock);
    expect(url).toBe("/api/v1/planning/planned-orders:firm");
    expect(keyOf(init)).toMatch(/.+/);
    expect(JSON.parse(String(init?.body))).toEqual({ plannedOrderIds: [second.id, base.id], grouping: "vendor-week" });

    const created = screen.getByRole("table", { name: "Orders created by firming" });
    expect(within(created).getByText("Purchase order")).toBeInTheDocument();
    expect(within(created).getByText(batch.groups[0].targetId)).toBeInTheDocument();
    expect(within(created).getByText("150")).toBeInTheDocument();

    await waitFor(() => expect(lists(fetchMock)).toHaveLength(2));
    await waitFor(() => expect(screen.getByRole("button", { name: "Firm selected" })).toBeDisabled());
    expect(screen.queryByRole("checkbox", { name: /for firming/ })).not.toBeInTheDocument();
  });

  it("selects every open order at once and clears the selection", async () => {
    stubApi(["/planned-orders", () => json({ items: [base, second, firmed] })]);
    renderAt("/planning/planned-orders");
    await ordersTable();
    fireEvent.click(screen.getByRole("button", { name: "Select all open" }));
    expect(screen.getByText("2 planned orders selected.")).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Select PLN-000001 for firming" })).toBeChecked();
    fireEvent.click(screen.getByRole("button", { name: "Clear selection" }));
    expect(screen.getByRole("button", { name: "Firm selected" })).toBeDisabled();
  });

  it("shows the conflict and retries with the same Idempotency-Key", async () => {
    const fetchMock = stubApi(
      ["/planned-orders:firm", () => problem(409, "PLAN-0002", `Planned order ${base.id} is already firmed.`)],
      ["/planned-orders", () => json({ items: [base, second] })],
    );
    renderAt("/planning/planned-orders");
    const table = await ordersTable();
    fireEvent.click(within(table).getByRole("checkbox", { name: "Select PLN-000002 for firming" }));
    fireEvent.click(screen.getByRole("button", { name: "Firm selected" }));
    expect(await screen.findByText("The orders were not firmed")).toBeInTheDocument();
    expect(screen.getByText(/is already firmed\. \(PLAN-0002\)/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Firm selected" }));
    await waitFor(() => expect(posts(fetchMock)).toHaveLength(2));
    const [first, retry] = posts(fetchMock);
    expect(keyOf(retry[1])).toBe(keyOf(first[1]));
    expect(screen.getByRole("checkbox", { name: "Select PLN-000002 for firming" })).toBeChecked();
  });

  it("uses a new Idempotency-Key when the selection changes", async () => {
    const fetchMock = stubApi(
      ["/planned-orders:firm", () => problem(503, "PLAN-UNKNOWN", "unavailable")],
      ["/planned-orders", () => json({ items: [base, second] })],
    );
    renderAt("/planning/planned-orders");
    const table = await ordersTable();
    fireEvent.click(within(table).getByRole("checkbox", { name: "Select PLN-000002 for firming" }));
    fireEvent.click(screen.getByRole("button", { name: "Firm selected" }));
    await screen.findByText("The orders were not firmed");
    fireEvent.click(within(table).getByRole("checkbox", { name: "Select PLN-000001 for firming" }));
    fireEvent.click(screen.getByRole("button", { name: "Firm selected" }));
    await waitFor(() => expect(posts(fetchMock)).toHaveLength(2));
    const [first, second_] = posts(fetchMock);
    expect(keyOf(second_[1])).not.toBe(keyOf(first[1]));
  });
});

describe("Firmed planned order", () => {
  it("shows the order it became and no maintenance form", async () => {
    stubApi(
      ["/pegging", () => json({ orderId: firmed.id, number: firmed.number, qty: "100", tree: { ref: firmed.number, refType: "planned-order", qty: "100", children: [] } })],
      ["/planned-orders/", () => json(firmed)],
    );
    renderAt(`/planning/planned-orders/${firmed.id}`);
    expect(await screen.findByRole("heading", { level: 2, name: "Planned order PLN-000003" })).toBeInTheDocument();
    expect(screen.getByText("Became")).toBeInTheDocument();
    expect(screen.getByText(firmed.firmedTargetId as string)).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Change planned order" })).not.toBeInTheDocument();
  });
});

describe("Cancel and decrease messages (FR-22.7)", () => {
  it("lists them and filters by cancel", async () => {
    const cancel: Message = { action: "cancel", itemId: base.itemId, supplyRef: "PO-9", demandRef: "", suggestedDate: "2026-12-20", delayDays: 0, qty: "50" };
    const decrease: Message = { ...cancel, action: "decrease", supplyRef: "PO-1", demandRef: "SO-1", qty: "200" };
    const fetchMock = stubApi(["/messages", (url) => json({ items: url.includes("action=cancel") ? [cancel] : [cancel, decrease] })]);
    renderAt("/planning/messages");
    const table = await screen.findByRole("table", { name: "Messages" });
    await waitFor(() => expect(within(table).getByText("Decrease")).toBeInTheDocument());
    expect(within(table).getByText("Cancel")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Action"), { target: { value: "cancel" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("action=cancel"))).toBe(true));
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Messages" })).queryByText("Decrease")).not.toBeInTheDocument());
  });
});
