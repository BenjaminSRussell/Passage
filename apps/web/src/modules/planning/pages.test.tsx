import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Message, PlannedOrder, PlanVersion } from "./api/client";
import planning from "./module";
import { validateRun } from "./RunPlanPage";

const order: PlannedOrder = {
  id: "00000000-0000-0000-0000-0000000000a1",
  versionId: "00000000-0000-0000-0000-0000000000b1",
  number: "PLN-000001",
  itemId: "00000000-0000-0000-0000-0000000000c1",
  orderType: "production",
  qty: "110",
  orderDate: "2026-11-21",
  deliveryDate: "2026-11-28",
  futuresDate: "2026-12-05",
  delayDays: 7,
  lotRule: "lot-for-lot",
  status: "unprocessed",
  version: 1,
};

const purchase: PlannedOrder = { ...order, id: "00000000-0000-0000-0000-0000000000a2", number: "PLN-000005", orderType: "purchase", qty: "500", futuresDate: null, delayDays: 0 };

const delay: Message = {
  id: "00000000-0000-0000-0000-00000000f102", status: "open", version: 1,
  action: "delay",
  itemId: order.itemId,
  supplyRef: "PLN-000001",
  demandRef: "SO-1",
  suggestedDate: "2026-12-05",
  delayDays: 7,
  qty: "110",
};

const version: PlanVersion = {
  id: "00000000-0000-0000-0000-0000000000b1",
  planCode: "MAIN",
  number: 3,
  status: "current",
  asOf: "2026-11-02",
  itemsPlanned: 3,
  plannedOrders: 6,
  messages: 4,
  durationMs: 12,
  planHash: "0f3a9c1d2e4b5a6978877665544332211",
};

const pegging = {
  orderId: order.id,
  number: order.number,
  qty: "110",
  tree: {
    ref: "PLN-000001",
    refType: "planned-order",
    qty: "110",
    children: [{ ref: "SO-1", refType: "sales-line", qty: "110", children: [] }],
  },
};

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

type Route = [match: string, respond: (url: string, init?: RequestInit) => Response];

/** Routes fetch calls by URL substring (first match wins) and records them. */
function stubApi(...routes: Route[]) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    for (const [match, respond] of routes) {
      if (url.includes(match)) return respond(url, init);
    }
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function Location() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname + location.search}</div>;
}

// MemoryRouter rather than a data router: jsdom's AbortSignal is not the one
// undici's Request accepts, which breaks data-router navigations in tests.
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

const location = () => screen.getByTestId("location").textContent;

afterEach(() => vi.unstubAllGlobals());

describe("Master planning workspace", () => {
  it("shows tiles for the current version and the last run", async () => {
    stubApi(
      ["/planned-orders", () => json({ items: [order, purchase] })],
      ["/messages", () => json({ items: [delay] })],
      ["/versions", () => json({ items: [version] })],
    );
    renderAt("/planning");
    expect(screen.getByRole("heading", { level: 2, name: "Master planning" })).toBeInTheDocument();
    const tiles = screen.getByRole("list", { name: "Plan summary" });
    await waitFor(() => expect(tiles).toHaveTextContent("Delayed orders 1"));
    const tile = (label: string) => within(tiles).getAllByRole("listitem").find((li) => li.textContent?.startsWith(label));
    expect(tile("Planned purchase orders")).toHaveTextContent("Planned purchase orders 1");
    expect(tile("Planned production orders")).toHaveTextContent("Planned production orders 1");
    expect(tile("Planned transfer orders")).toHaveTextContent("Planned transfer orders 0");
    expect(tile("Messages")).toHaveTextContent("Messages 1");
    expect(screen.getByText("0f3a9c1d2e4b5a69")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Run plan" })).toHaveAttribute("href", "/planning/runs/new?plan=MAIN");
  });

  it("invites a first run when the plan has no versions", async () => {
    stubApi(["/planned-orders", () => json({ items: [] })], ["/messages", () => json({ items: [] })], ["/versions", () => json({ items: [] })]);
    renderAt("/planning?plan=NIGHTLY");
    expect(await screen.findByRole("heading", { name: "Plan NIGHTLY has not run yet" })).toBeInTheDocument();
  });

  it("shows the problem code when the service fails", async () => {
    stubApi(["/", () => problem(500, "COM-0500", "internal error")]);
    renderAt("/planning");
    const alert = await findAlert();
    await waitFor(() => expect(alert).toHaveTextContent("COM-0500"));
    expect(alert).toHaveTextContent("Could not load plan MAIN");
  });

  it("switches plan through the picker and rejects a malformed code", async () => {
    const fetchMock = stubApi(["/", () => json({ items: [] })]);
    renderAt("/planning");
    const input = screen.getByLabelText("Plan");
    fireEvent.change(input, { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Show plan" }));
    expect(await screen.findByText("Use 2 to 20 capital letters, digits, _ or -.")).toBeInTheDocument();

    fireEvent.change(input, { target: { value: "nightly" } });
    fireEvent.click(screen.getByRole("button", { name: "Show plan" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("planCode=NIGHTLY"))).toBe(true));
  });
});

describe("Planned orders page", () => {
  it("lists the current version's orders and opens one", async () => {
    stubApi(
      ["/pegging", () => json(pegging)],
      [`/planned-orders/${order.id}`, () => json(order)],
      ["/planned-orders", () => json({ items: [order, purchase] })],
    );
    renderAt("/planning/planned-orders");
    const table = await findTable({ name: "Planned orders" });
    await waitFor(() => expect(within(table).getByText("PLN-000005")).toBeInTheDocument());
    expect(within(table).getByText("7 days")).toBeInTheDocument();
    expect(within(table).getByText("Production")).toBeInTheDocument();

    fireEvent.click(within(table).getByRole("button", { name: "PLN-000001" }));
    await waitFor(() => expect(location()).toBe(`/planning/planned-orders/${order.id}?plan=MAIN`));
    expect(await screen.findByRole("heading", { level: 2, name: "Planned order PLN-000001" })).toBeInTheDocument();
  });

  it("filters by status through the query", async () => {
    const fetchMock = stubApi(["/planned-orders", () => json({ items: [] })]);
    renderAt("/planning/planned-orders");
    expect(await screen.findByRole("heading", { name: "No planned orders" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "firmed" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("status=firmed"))).toBe(true));
  });

  it("shows a loading table while the request is pending", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => undefined)));
    renderAt("/planning/planned-orders");
    expect(await screen.findByRole("table", { name: "Planned orders" })).toHaveAttribute("aria-busy", "true");
  });

  it("shows the problem and no table on error", async () => {
    stubApi(["/planned-orders", () => problem(400, "COM-0001", "request failed validation")]);
    renderAt("/planning/planned-orders");
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("COM-0001"));
    expect(screen.queryByRole("table", { name: "Planned orders" })).not.toBeInTheDocument();
  });
});

describe("DDMRP buffer orders (FR-22.16, AC-22.12)", () => {
  const buffered: PlannedOrder = { ...purchase, id: "00000000-0000-0000-0000-0000000000a3", number: "PLN-000007", qty: "115", planningPriority: "0.5306", bufferZone: "yellow" };

  it("shows the buffer priority and zone in the list, a dash on other orders", async () => {
    stubApi(["/planned-orders", () => json({ items: [order, buffered] })]);
    renderAt("/planning/planned-orders");
    const table = await findTable({ name: "Planned orders" });
    await waitFor(() => expect(within(table).getByText("53.06% Yellow")).toBeInTheDocument());
    const plain = within(table).getByRole("button", { name: "PLN-000001" }).closest("tr")!;
    expect(within(plain).queryByText(/%/)).not.toBeInTheDocument();
  });

  it("sorts the orders by planning priority on request", async () => {
    const fetchMock = stubApi(["/planned-orders", () => json({ items: [buffered, order] })]);
    renderAt("/planning/planned-orders");
    await findTable({ name: "Planned orders" });
    expect(fetchMock.mock.calls.some(([url]) => url.includes("sort=priority"))).toBe(false);
    fireEvent.change(screen.getByLabelText("Sort by"), { target: { value: "priority" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("sort=priority"))).toBe(true));
  });

  it("shows the buffer priority and zone on a message and sorts messages by priority", async () => {
    const red: Message = { id: "00000000-0000-0000-0000-00000000f001", status: "open", version: 1, action: "red-zone", itemId: buffered.itemId, supplyRef: "PLN-000007", demandRef: "ddmrp:red:item", suggestedDate: "2026-11-12", delayDays: 0, qty: "80", planningPriority: "0.2449", bufferZone: "red" };
    const fetchMock = stubApi(["/messages", () => json({ items: [red] })]);
    renderAt("/planning/messages");
    const table = await findTable({ name: "Messages" });
    await waitFor(() => expect(within(table).getByText("24.49% Red")).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText("Sort by"), { target: { value: "priority" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("sort=priority"))).toBe(true));
  });

  it("lists a PLAN-0015 sales-on-top message with its excess and filters by it", async () => {
    const top: Message = { id: "00000000-0000-0000-0000-00000000f002", status: "open", version: 1, action: "sales-on-top", itemId: buffered.itemId, supplyRef: "", demandRef: "SO-1", suggestedDate: "2026-11-20", delayDays: 0, qty: "30", planningPriority: null, bufferZone: null };
    const fetchMock = stubApi(["/messages", () => json({ items: [top] })]);
    renderAt("/planning/messages");
    const table = await findTable({ name: "Messages" });
    await waitFor(() => expect(within(table).getByText("Sales on top")).toBeInTheDocument());
    expect(within(table).getByText("SO-1")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Action"), { target: { value: "sales-on-top" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("action=sales-on-top"))).toBe(true));
  });

  it("shows a PLAN-0005 lot-split message with its catalog text and filters by it", async () => {
    const split: Message = { id: "00000000-0000-0000-0000-00000000f004", status: "open", version: 1, action: "lot-split", code: "PLAN-0005", text: "Requirement for item-9 splits into 3 orders (max 100).", itemId: buffered.itemId, supplyRef: "PLN-000001", demandRef: "", suggestedDate: "2026-11-20", delayDays: 3, qty: "100", planningPriority: null, bufferZone: null };
    const fetchMock = stubApi(["/messages", () => json({ items: [split] })]);
    renderAt("/planning/messages");
    const table = await findTable({ name: "Messages" });
    await waitFor(() => expect(within(table).getByText("Lot split")).toBeInTheDocument());
    expect(within(table).getByText("PLAN-0005 Requirement for item-9 splits into 3 orders (max 100).")).toBeInTheDocument();
    // delayDays carries the number of orders, which is not a delay: the Delay column stays empty.
    const [, row] = within(table).getAllByRole("row");
    expect(within(row).queryByRole("cell", { name: "3" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Action"), { target: { value: "lot-split" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("action=lot-split"))).toBe(true));
  });

  it("shows a PLAN-0004 bom-cycle message with its catalog text and filters by it", async () => {
    const cycle: Message = { id: "00000000-0000-0000-0000-00000000f005", status: "open", version: 1, action: "bom-cycle", code: "PLAN-0004", text: "BOM cycle detected for item-9; low-level codes not updated.", itemId: buffered.itemId, supplyRef: "", demandRef: "item-8", suggestedDate: "2026-11-02", delayDays: 0, qty: "1", planningPriority: null, bufferZone: null };
    const fetchMock = stubApi(["/messages", () => json({ items: [cycle] })]);
    renderAt("/planning/messages");
    const table = await findTable({ name: "Messages" });
    await waitFor(() => expect(within(table).getByText("BOM cycle")).toBeInTheDocument());
    expect(within(table).getByText("PLAN-0004 BOM cycle detected for item-9; low-level codes not updated.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Action"), { target: { value: "bom-cycle" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("action=bom-cycle"))).toBe(true));
  });

  it("shows a PLAN-0009 no-quota-source message with its catalog text and filters by it", async () => {
    const none: Message = { id: "00000000-0000-0000-0000-00000000f006", status: "open", version: 1, action: "no-quota-source", code: "PLAN-0009", text: "Quota arrangement arr-1 has no usable source in 2026-11-11.", itemId: buffered.itemId, supplyRef: "PLN-000004", demandRef: "arr-1", suggestedDate: "2026-11-11", delayDays: 0, qty: "40", planningPriority: null, bufferZone: null };
    const fetchMock = stubApi(["/messages", () => json({ items: [none] })]);
    renderAt("/planning/messages");
    const table = await findTable({ name: "Messages" });
    await waitFor(() => expect(within(table).getByText("No quota source")).toBeInTheDocument());
    expect(within(table).getByText("PLAN-0009 Quota arrangement arr-1 has no usable source in 2026-11-11.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Action"), { target: { value: "no-quota-source" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("action=no-quota-source"))).toBe(true));
  });

  it("shows a PLAN-0008 no-vendor message with its catalog text and filters by it", async () => {
    const none: Message = { id: "00000000-0000-0000-0000-00000000f003", status: "open", version: 1, action: "no-vendor", code: "PLAN-0008", text: "No vendor for planned purchase of item-9; using none.", itemId: buffered.itemId, supplyRef: "PLN-000003", demandRef: "", suggestedDate: "2026-11-04", delayDays: 0, qty: "17", planningPriority: null, bufferZone: null };
    const fetchMock = stubApi(["/messages", () => json({ items: [none] })]);
    renderAt("/planning/messages");
    const table = await findTable({ name: "Messages" });
    await waitFor(() => expect(within(table).getByText("No vendor")).toBeInTheDocument());
    expect(within(table).getByText("PLAN-0008 No vendor for planned purchase of item-9; using none.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Action"), { target: { value: "no-vendor" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("action=no-vendor"))).toBe(true));
  });

  it("shows the buffer priority on the order page", async () => {
    stubApi(["/pegging", () => json({ ...pegging, orderId: buffered.id, number: buffered.number })], ["/planned-orders/", () => json(buffered)]);
    renderAt(`/planning/planned-orders/${buffered.id}`);
    expect(await screen.findByRole("heading", { level: 2, name: "Planned order PLN-000007" })).toBeInTheDocument();
    expect(screen.getByText("Buffer priority")).toBeInTheDocument();
    expect(screen.getByText("53.06% Yellow")).toBeInTheDocument();
  });
});

describe("Planned order page", () => {
  it("shows the order and explains it down to the sales line", async () => {
    const fetchMock = stubApi(["/pegging", () => json(pegging)], ["/planned-orders/", () => json(order)]);
    renderAt(`/planning/planned-orders/${order.id}`);
    expect(await screen.findByRole("heading", { level: 2, name: "Planned order PLN-000001" })).toBeInTheDocument();
    expect(screen.getByText("2026-12-05")).toBeInTheDocument();
    const tree = await screen.findByRole("list", { name: "Pegging tree" });
    expect(within(tree).getByText("SO-1")).toBeInTheDocument();
    expect(within(tree).getByText("Sales line")).toBeInTheDocument();
    expect(screen.getByText("Pegging explains 110 of 110, including any surplus.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => url.endsWith(`/planned-orders/${order.id}/pegging?depth=20`))).toBe(true);
  });

  it("keeps the order visible when the pegging fails", async () => {
    stubApi(["/pegging", () => problem(500, "COM-0500", "boom")], ["/planned-orders/", () => json(order)]);
    renderAt(`/planning/planned-orders/${order.id}`);
    expect(await screen.findByRole("heading", { level: 2, name: "Planned order PLN-000001" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(/Could not load the pegging tree/)).toBeInTheDocument());
  });

  it("explains an unknown order", async () => {
    stubApi(["/planned-orders/", () => problem(404, "PLAN-ORDER-NOT-FOUND", "planned order not found")]);
    renderAt(`/planning/planned-orders/${order.id}`);
    expect(await screen.findByRole("heading", { name: "Planned order not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to planned orders" })).toHaveAttribute("href", "/planning/planned-orders?plan=MAIN");
  });
});

describe("Action messages page", () => {
  it("lists messages and filters by action", async () => {
    const fetchMock = stubApi(["/messages", () => json({ items: [delay] })]);
    renderAt("/planning/messages?plan=MAIN");
    const table = await findTable({ name: "Messages" });
    await waitFor(() => expect(within(table).getByText("SO-1")).toBeInTheDocument());
    expect(within(table).getByText("Delay")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Action"), { target: { value: "postpone" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([url]) => url.includes("action=postpone"))).toBe(true));
  });

  it("explains an empty list", async () => {
    stubApi(["/messages", () => json({ items: [] })]);
    renderAt("/planning/messages");
    expect(await screen.findByRole("heading", { name: "No messages" })).toBeInTheDocument();
  });
});

describe("Plan runs page", () => {
  it("lists versions newest first", async () => {
    stubApi(["/versions", () => json({ items: [version, { ...version, id: "v2", number: 2, status: "complete" }] })]);
    renderAt("/planning/runs");
    const table = await findTable({ name: "Plan runs" });
    await waitFor(() => expect(within(table).getByText("Complete")).toBeInTheDocument());
    expect(within(table).getAllByText("0f3a9c1d2e4b5a69")).toHaveLength(2);
  });

  it("shows per version how many approved orders the run firmed (FR-22.11)", async () => {
    stubApi(["/versions", () => json({ items: [{ ...version, approvedFirmed: 7 }, { ...version, id: "v2", number: 2, status: "complete" }] })]);
    renderAt("/planning/runs");
    const table = await findTable({ name: "Plan runs" });
    expect(await within(table).findByRole("columnheader", { name: "Approved firmed" })).toBeInTheDocument();
    const [, newest, older] = within(table).getAllByRole("row");
    expect(within(newest).getByText("7")).toBeInTheDocument();
    expect(within(older).getAllByText("0").length).toBeGreaterThan(0);
  });

  it("explains an empty history", async () => {
    stubApi(["/versions", () => json({ items: [] })]);
    renderAt("/planning/runs");
    expect(await screen.findByRole("heading", { name: "No runs yet" })).toBeInTheDocument();
  });
});

describe("Run plan page", () => {
  const site = "00000000-0000-0000-0000-0000000000d1";

  it("validates the form before calling the service", async () => {
    const fetchMock = stubApi();
    renderAt("/planning/runs/new");
    fireEvent.change(screen.getByLabelText("Items"), { target: { value: "{" } });
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    expect(await screen.findByText("Enter the site id as a UUID.")).toBeInTheDocument();
    expect(screen.getByText("Items are not valid JSON.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("runs the plan with an Idempotency-Key and reports the new version", async () => {
    const fetchMock = stubApi([":run", () => json(version, 201)]);
    renderAt("/planning/runs/new?plan=MAIN");
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: site } });
    fireEvent.change(screen.getByLabelText("Plan date"), { target: { value: "2026-11-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    const status = await findStatus();
    await waitFor(() => expect(status).toHaveTextContent("Version 3 of plan MAIN is current"));
    expect(status).toHaveTextContent("6 planned orders and 4 messages from 3 items.");

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/planning/plans/MAIN:run");
    expect(init?.method).toBe("POST");
    expect((init?.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    const body = JSON.parse(init?.body as string);
    expect(body).toMatchObject({ asOf: "2026-11-02", siteId: site });
    expect(body.items[0].demand[0].ref).toBe("SO-1");
  });

  it("reports the approved orders the run firmed (FR-22.11)", async () => {
    stubApi([":run", () => json({ ...version, approvedFirmed: 2 }, 201)]);
    renderAt("/planning/runs/new?plan=MAIN");
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: site } });
    fireEvent.change(screen.getByLabelText("Plan date"), { target: { value: "2026-11-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    const status = await findStatus();
    await waitFor(() => expect(status).toHaveTextContent("2 approved orders of the previous version were firmed."));
  });

  it("names one firmed approved order in the singular and none when the run firmed nothing", async () => {
    let firmed = 1;
    stubApi([":run", () => json({ ...version, approvedFirmed: firmed }, 201)]);
    renderAt("/planning/runs/new?plan=MAIN");
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: site } });
    fireEvent.change(screen.getByLabelText("Plan date"), { target: { value: "2026-11-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    const status = await findStatus();
    await waitFor(() => expect(status).toHaveTextContent("1 approved order of the previous version was firmed."));
    firmed = 0;
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    await waitFor(() => expect(status).not.toHaveTextContent("approved order"));
    expect(status).toHaveTextContent("6 planned orders");
  });

  it("sends carryForward only when firmed orders are counted as supply", async () => {
    const fetchMock = stubApi([":run", () => json(version, 201)]);
    renderAt("/planning/runs/new?plan=MAIN");
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: site } });
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    await findStatus();
    expect(JSON.parse(fetchMock.mock.calls[0][1]?.body as string)).not.toHaveProperty("carryForward");

    fireEvent.click(screen.getByLabelText("Count firmed orders as supply"));
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(JSON.parse(fetchMock.mock.calls[1][1]?.body as string)).toMatchObject({ carryForward: true });
    const key = (i: number) => (fetchMock.mock.calls[i][1]?.headers as Record<string, string>)["Idempotency-Key"];
    expect(key(1)).not.toBe(key(0));
  });

  it("sends warehouseScopesFromCoverage only when the stored warehouse scopes are planned", async () => {
    const fetchMock = stubApi([":run", () => json(version, 201)]);
    renderAt("/planning/runs/new?plan=MAIN");
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: site } });
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    await findStatus();
    expect(JSON.parse(fetchMock.mock.calls[0][1]?.body as string)).not.toHaveProperty("warehouseScopesFromCoverage");

    fireEvent.click(screen.getByLabelText("Plan warehouse scopes from item coverage"));
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(JSON.parse(fetchMock.mock.calls[1][1]?.body as string)).toMatchObject({ warehouseScopesFromCoverage: true });
  });

  it("reuses the Idempotency-Key when the same request is retried", async () => {
    let calls = 0;
    const fetchMock = stubApi([":run", () => (++calls === 1 ? problem(503, "COM-0503", "unavailable") : json(version, 200))]);
    renderAt("/planning/runs/new");
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: site } });
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    await waitFor(() => expect(screen.getByText(/COM-0503/)).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const key = (i: number) => (fetchMock.mock.calls[i][1]?.headers as Record<string, string>)["Idempotency-Key"];
    expect(key(1)).toBe(key(0));
  });

  it("shows PLAN-0001 when a run is already in progress", async () => {
    stubApi([":run", () => problem(409, "PLAN-0001", "a run of plan MAIN is already in progress")]);
    renderAt("/planning/runs/new");
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: site } });
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    await waitFor(() => expect(screen.getByText("The plan did not run")).toBeInTheDocument());
    expect(screen.getByText(/PLAN-0001/)).toBeInTheDocument();
  });

  it("validateRun accepts the sample snapshot and rejects an empty array", () => {
    const ok = validateRun({ plan: "MAIN", asOf: "2026-11-02", siteId: site, items: "[{}]" });
    expect(ok.errors).toEqual({});
    expect(ok.items).toHaveLength(1);
    expect(validateRun({ plan: "MAIN", asOf: "2026-11-02", siteId: site, items: "[]" }).errors.items).toBe(
      "Items must be a non-empty JSON array.",
    );
    expect(validateRun({ plan: "m", asOf: "", siteId: site, items: "[1]" }).errors).toMatchObject({
      plan: "Use 2 to 20 capital letters, digits, _ or -.",
      asOf: "Enter the plan date.",
    });
  });
});
