import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { listCostSnapshots, runCostSnapshot, type CostSnapshot } from "./api/client";
import { validateSnapshotFilter } from "./CostSnapshotsPage";
import production from "./module";
import { json, orderId, problem } from "./testData";

afterEach(() => vi.unstubAllGlobals());

function stubFetch(handler: (url: string, init?: RequestInit) => Response | Promise<Response>) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/production/cost-snapshots"]}>
      <a href="#main">Skip to content</a>
      <nav aria-label="Workspaces" />
      <main id="main">
        <h1>Production</h1>
        <Routes>
          {production.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </main>
    </MemoryRouter>,
  );
}

const snapshot = (overrides: Partial<CostSnapshot> = {}): CostSnapshot => ({
  orderId,
  orderNumber: "PO-000001",
  snapshotDate: "2026-10-30",
  status: "Started",
  currency: "USD",
  qtyOrdered: "100",
  qtyReported: "40",
  plannedCost: "1000.50",
  actualCost: "1200.25",
  wipBalance: "300.00",
  costVariance: "199.75",
  ...overrides,
});

describe("cost snapshot client", () => {
  it("sends only the filters given and tolerates an empty body", async () => {
    const fetchMock = stubFetch(() => json({}));
    const page = await listCostSnapshots({ orderId, from: "2026-10-01", to: "" });
    expect(calls(fetchMock)[0][0]).toBe(`/api/v1/production/cost-snapshots?orderId=${orderId}&from=2026-10-01&limit=50`);
    expect(page).toEqual({ items: [], nextCursor: undefined });
  });

  it("posts the day to take, or an empty body for today", async () => {
    const fetchMock = stubFetch(() => json({ snapshotDate: "2026-10-30", orders: 2 }));
    expect(await runCostSnapshot("2026-10-30")).toEqual({ snapshotDate: "2026-10-30", orders: 2 });
    await runCostSnapshot();
    expect(JSON.parse(String(calls(fetchMock)[0][1]?.body))).toEqual({ date: "2026-10-30" });
    expect(JSON.parse(String(calls(fetchMock)[1][1]?.body))).toEqual({});
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/cost-snapshots:run");
  });

  it("throws the service's problem", async () => {
    stubFetch(() => problem(400, "COM-0001", "request failed validation", { date: "must not be in the future" }));
    await expect(runCostSnapshot("2999-01-01")).rejects.toMatchObject({ status: 400, code: "COM-0001" });
  });
});

describe("snapshot filter", () => {
  it("refuses a malformed order id and a reversed period", () => {
    expect(validateSnapshotFilter({ orderId: "", from: "", to: "" })).toEqual({});
    expect(validateSnapshotFilter({ orderId: "x", from: "2026-11-01", to: "2026-10-01" })).toEqual({
      orderId: "Enter the order's id (a UUID), or leave it empty.",
      to: "The end of the period must not be before its start.",
    });
  });
});

describe("order cost snapshots page (4.21.16)", () => {
  it("lists the snapshots with planned against actual cost, accessibly, and loads more", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("cursor=")
        ? json({ items: [snapshot({ snapshotDate: "2026-10-29", orderNumber: "PO-000002" })] })
        : json({ items: [snapshot()], nextCursor: "2026-10-30|" + orderId }),
    );
    const { container } = renderPage();
    const table = await findTable({ name: "Order cost snapshots" });
    await waitFor(() => expect(within(table).getByText("PO-000001")).toBeInTheDocument());
    expect(within(table).getByText("1000.50 USD")).toBeInTheDocument();
    expect(within(table).getByText("199.75 USD")).toBeInTheDocument();
    expect(within(table).getByText("40 / 100")).toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);
    await userEvent.click(screen.getByRole("button", { name: "Load more snapshots" }));
    await waitFor(() => expect(within(table).getByText("PO-000002")).toBeInTheDocument());
    expect(calls(fetchMock)[1][0]).toContain(`cursor=${encodeURIComponent("2026-10-30|" + orderId)}`);
    expect(screen.queryByRole("button", { name: "Load more snapshots" })).not.toBeInTheDocument();
  });

  it("filters by order and period, and refuses a bad filter without calling the service", async () => {
    const fetchMock = stubFetch(() => json({ items: [snapshot()] }));
    renderPage();
    await findTable({ name: "Order cost snapshots" });
    const form = screen.getByRole("form", { name: "Filter the snapshots" });
    await userEvent.type(within(form).getByLabelText(/Order/), "bolt");
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    expect(await screen.findByText("Enter the order's id (a UUID), or leave it empty.")).toBeInTheDocument();
    expect(calls(fetchMock)).toHaveLength(1);
    await userEvent.clear(within(form).getByLabelText(/Order/));
    await userEvent.type(within(form).getByLabelText(/Order/), orderId);
    fireEvent.change(within(form).getByLabelText(/From/), { target: { value: "2026-10-01" } });
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    await waitFor(() => expect(calls(fetchMock).some(([url]) => url === `/api/v1/production/cost-snapshots?orderId=${orderId}&from=2026-10-01&limit=50`)).toBe(true));
  });

  it("takes a snapshot for a day and reloads the list", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init?.method === "POST" ? json({ snapshotDate: "2026-10-30", orders: 1 }) : json({ items: [snapshot()] }),
    );
    renderPage();
    await findTable({ name: "Order cost snapshots" });
    const form = screen.getByRole("form", { name: "Take a snapshot" });
    fireEvent.change(within(form).getByLabelText(/Snapshot day/), { target: { value: "2026-10-30" } });
    await userEvent.click(within(form).getByRole("button", { name: "Take snapshot" }));
    expect(await screen.findByText("Snapshot for 2026-10-30 taken: 1 open order.")).toBeInTheDocument();
    expect(JSON.parse(String(calls(fetchMock).find(([, init]) => init?.method === "POST")?.[1]?.body))).toEqual({ date: "2026-10-30" });
    await waitFor(() => expect(calls(fetchMock).filter(([, init]) => !init?.method || init.method === "GET")).toHaveLength(2));
  });

  it("says what is missing when the snapshot is refused", async () => {
    stubFetch((_url, init) =>
      init?.method === "POST" ? problem(403, "AUTH-0003", "forbidden") : json({ items: [] }),
    );
    renderPage();
    expect(await screen.findByRole("heading", { name: "No snapshots yet" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Take snapshot" }));
    expect(await screen.findByText("You need the View production costs duty to take a snapshot.")).toBeInTheDocument();
  });

  it("shows the loading state and a failed list", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderPage();
    expect(await screen.findByRole("table", { name: "Order cost snapshots" })).toHaveAttribute("aria-busy", "true");
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "COM-9999", "database unavailable"));
    renderPage();
    expect(await screen.findByText(/database unavailable \(COM-9999\)/)).toBeInTheDocument();
  });

  it("is registered in the module with the cost duty", () => {
    expect(production.routes.some((r) => r.path === "/production/cost-snapshots")).toBe(true);
    expect(production.nav?.find((n) => n.to === "/production/cost-snapshots")?.duty).toBe("production.cost.read");
  });
});
