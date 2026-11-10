import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { NetRequirement, PlannedOrder } from "./api/client";
import planning from "./module";
import { trimQty } from "./NetRequirementsPage";
import { validateRun } from "./RunPlanPage";

const item = "00000000-0000-0000-0000-000000000300";
const site = "00000000-0000-0000-0000-0000000000d1";

// RM-300 of the BR-22.2 example (AC-22.10): PAB 400, 190, 110 in weeks 2 to 4.
const rows: NetRequirement[] = [
  { itemId: item, date: "2026-11-16", grossRequirements: "600.000000", scheduledReceipts: "300.000000", plannedReceipts: "500.000000", projectedAvailableBalance: "400.000000", netRequirement: "100.000000" },
  { itemId: item, date: "2026-11-23", grossRequirements: "710.000000", scheduledReceipts: "0.000000", plannedReceipts: "500.000000", projectedAvailableBalance: "190.000000", netRequirement: "310.000000" },
  { itemId: item, date: "2026-11-30", grossRequirements: "80.000000", scheduledReceipts: "0.000000", plannedReceipts: "0.000000", projectedAvailableBalance: "-15.500000", netRequirement: "0.000000" },
];

const order: PlannedOrder = {
  id: "00000000-0000-0000-0000-0000000000a5",
  versionId: "00000000-0000-0000-0000-0000000000b1",
  number: "PLN-000005",
  itemId: item,
  orderType: "purchase",
  qty: "500.000000",
  orderDate: "2026-11-02",
  deliveryDate: "2026-11-16",
  futuresDate: "2026-11-23",
  delayDays: 7,
  lotRule: "min",
  status: "unprocessed",
  version: 1,
};

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

type Handler = [match: string, respond: (url: string, init?: RequestInit) => Response];

function stubApi(...routes: Handler[]) {
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

afterEach(() => vi.unstubAllGlobals());

describe("Net requirements page", () => {
  it("shows the item's time-phased view and its planned orders", async () => {
    const fetchMock = stubApi(["/net-requirements", () => json({ items: rows })], ["/planned-orders", () => json({ items: [order] })]);
    renderAt(`/planning/net-requirements?plan=MAIN&item=${item}`);
    expect(screen.getByRole("heading", { level: 2, name: "Net requirements" })).toBeInTheDocument();
    const table = await findTable({ name: "Net requirements" });
    await waitFor(() => expect(within(table).getByText("2026-11-30")).toBeInTheDocument());
    const body = within(table).getAllByRole("row").slice(1);
    expect(body.map((r) => within(r).getAllByRole("cell")[4].textContent)).toEqual(["400", "190", "-15.5"]);
    expect(within(body[1]).getAllByRole("cell")[5]).toHaveTextContent("310");
    expect(within(body[2]).getAllByRole("cell")[5]).toHaveTextContent("—");
    expect(fetchMock.mock.calls.some(([url]) => url === `/api/v1/planning/net-requirements?planCode=MAIN&itemId=${item}`)).toBe(true);
    expect(fetchMock.mock.calls.some(([url]) => url.includes(`/planned-orders?planCode=MAIN&itemId=${item}`))).toBe(true);

    const orders = await findTable({ name: "Planned orders of the item" });
    fireEvent.click(await within(orders).findByRole("button", { name: "PLN-000005" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/planning/planned-orders/${order.id}?plan=MAIN`));
  });

  it("asks for an item and calls nothing until one is chosen", async () => {
    const fetchMock = stubApi(["/net-requirements", () => json({ items: [] })], ["/planned-orders", () => json({ items: [] })]);
    renderAt("/planning/net-requirements");
    expect(screen.getByRole("heading", { name: "Choose an item" })).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Item"), { target: { value: "RM-300" } });
    fireEvent.click(screen.getByRole("button", { name: "Show net requirements" }));
    expect(await screen.findByText("Enter the item id as a UUID.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Item"), { target: { value: item } });
    fireEvent.change(screen.getByLabelText("Plan"), { target: { value: "nightly" } });
    fireEvent.click(screen.getByRole("button", { name: "Show net requirements" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/planning/net-requirements?plan=NIGHTLY&item=${item}`));
    expect(await screen.findByRole("heading", { name: "No net requirements" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "No planned orders" })).toBeInTheDocument();
  });

  it("shows a loading table while the request is pending", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => undefined)));
    renderAt(`/planning/net-requirements?item=${item}`);
    expect(await screen.findByRole("table", { name: "Net requirements" })).toHaveAttribute("aria-busy", "true");
  });

  it("shows the problem and no table on error", async () => {
    stubApi(["/net-requirements", () => problem(400, "COM-0001", "request failed validation")], ["/planned-orders", () => json({ items: [] })]);
    renderAt(`/planning/net-requirements?item=${item}`);
    const alerts = await screen.findAllByRole("alert");
    await waitFor(() => expect(alerts.some((a) => a.textContent?.includes("COM-0001"))).toBe(true));
    expect(screen.queryByRole("table", { name: "Net requirements" })).not.toBeInTheDocument();
  });

  it("is reachable from a planned order", async () => {
    stubApi(["/pegging", () => json({ orderId: order.id, number: order.number, qty: "500", tree: { ref: order.number, refType: "planned-order", qty: "500", children: [] } })], ["/planned-orders/", () => json(order)]);
    renderAt(`/planning/planned-orders/${order.id}?plan=MAIN`);
    const link = await screen.findByRole("link", { name: "Net requirements of the item" });
    expect(link).toHaveAttribute("href", `/planning/net-requirements?plan=MAIN&item=${item}`);
  });

  it("reads one warehouse scope apart from the site (FR-22.18, AC-22.16)", async () => {
    const wh = "00000000-0000-0000-0000-0000000000c2";
    const whOrder: PlannedOrder = { ...order, id: "00000000-0000-0000-0000-0000000000a6", number: "PLN-000006", orderType: "transfer", warehouseId: wh };
    const fetchMock = stubApi(
      ["/net-requirements", () => json({ items: rows.slice(0, 1).map((r) => ({ ...r, warehouseId: wh })) })],
      ["/planned-orders", () => json({ items: [whOrder] })],
    );
    renderAt(`/planning/net-requirements?plan=MAIN&item=${item}&warehouse=${wh}`);
    const table = await findTable({ name: "Net requirements" });
    await waitFor(() => expect(within(table).getByText("2026-11-16")).toBeInTheDocument());
    expect(fetchMock.mock.calls.some(([url]) => url === `/api/v1/planning/net-requirements?planCode=MAIN&itemId=${item}&warehouseId=${wh}`)).toBe(true);
    expect(fetchMock.mock.calls.some(([url]) => url.includes(`/planned-orders?planCode=MAIN&itemId=${item}&warehouseId=${wh}`))).toBe(true);
    expect(screen.getByRole("heading", { name: "Planned orders of this item in the warehouse" })).toBeInTheDocument();
    const orders = await findTable({ name: "Planned orders of the item" });
    expect(await within(orders).findByText(wh)).toBeInTheDocument();
  });

  it("rejects a warehouse that is not a UUID and keeps the site scope without one", async () => {
    const fetchMock = stubApi(["/net-requirements", () => json({ items: [] })], ["/planned-orders", () => json({ items: [] })]);
    renderAt(`/planning/net-requirements?plan=MAIN&item=${item}`);
    await screen.findByRole("heading", { name: "No net requirements" });
    expect(fetchMock.mock.calls.every(([url]) => !url.includes("warehouseId"))).toBe(true);
    fireEvent.change(screen.getByLabelText("Warehouse scope (empty: site)"), { target: { value: "WH-2" } });
    fireEvent.click(screen.getByRole("button", { name: "Show net requirements" }));
    expect(await screen.findByText("Enter the warehouse id as a UUID, or leave it empty for the site.")).toBeInTheDocument();
  });

  it("links a warehouse planned order to its own net requirements", async () => {
    const wh = "00000000-0000-0000-0000-0000000000c2";
    stubApi(
      ["/pegging", () => json({ orderId: order.id, number: order.number, qty: "500", tree: { ref: order.number, refType: "planned-order", qty: "500", children: [] } })],
      ["/planned-orders/", () => json({ ...order, orderType: "transfer", warehouseId: wh })],
    );
    renderAt(`/planning/planned-orders/${order.id}?plan=MAIN`);
    const link = await screen.findByRole("link", { name: "Net requirements of the item" });
    expect(link).toHaveAttribute("href", `/planning/net-requirements?plan=MAIN&item=${item}&warehouse=${wh}`);
    expect(await screen.findByText("Transfer from the site")).toBeInTheDocument();
  });

  it("trims decimal zeros without rounding", () => {
    expect(trimQty("400.000000")).toBe("400");
    expect(trimQty("0.000100")).toBe("0.0001");
    expect(trimQty("-15.500000")).toBe("-15.5");
    expect(trimQty("120")).toBe("120");
  });
});

describe("Run plan work calendar", () => {
  it("sends the closed weekdays and dates with the run", async () => {
    const version = { id: "v", planCode: "MAIN", number: 1, status: "current", asOf: "2026-11-02", itemsPlanned: 1, plannedOrders: 1, messages: 0, durationMs: 1, planHash: "ab" };
    const fetchMock = stubApi([":run", () => json(version, 201)]);
    renderAt("/planning/runs/new?plan=MAIN");
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: site } });
    fireEvent.click(screen.getByLabelText("Saturday"));
    fireEvent.click(screen.getByLabelText("Sunday"));
    fireEvent.change(screen.getByLabelText("Closed dates"), { target: { value: "2026-11-26, 2026-12-25" } });
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const body = JSON.parse(fetchMock.mock.calls[0][1]?.body as string);
    expect(body.calendar).toEqual({ closedWeekdays: [0, 6], closedDates: ["2026-11-26", "2026-12-25"] });
  });

  it("omits the calendar when no day is closed and refuses an all-closed week or a bad date", () => {
    const base = { plan: "MAIN", asOf: "2026-11-02", siteId: site, items: "[{}]" };
    expect(validateRun(base).calendar).toBeUndefined();
    expect(validateRun({ ...base, closedWeekdays: [0, 1, 2, 3, 4, 5, 6] }).errors.closedWeekdays).toBe("Keep at least one weekday open.");
    expect(validateRun({ ...base, closedDates: "25/12/2026" }).errors.closedDates).toBe(
      "Enter dates as YYYY-MM-DD, separated by commas or spaces.",
    );
    expect(validateRun({ ...base, closedDates: "2026-12-25\n2026-12-26" }).calendar).toEqual({ closedDates: ["2026-12-25", "2026-12-26"] });
  });

  it("does not call the service when the calendar is invalid", async () => {
    const fetchMock = stubApi();
    renderAt("/planning/runs/new");
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: site } });
    for (const day of ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"]) {
      fireEvent.click(screen.getByLabelText(day));
    }
    fireEvent.click(screen.getByRole("button", { name: "Run plan" }));
    expect(await screen.findByText("Keep at least one weekday open.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
