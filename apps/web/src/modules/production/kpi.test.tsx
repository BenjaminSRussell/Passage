import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { getOrderKpis, type OrderKpiGroup } from "./api/client";
import { percent, validateKpiFilter } from "./KpiPage";
import production from "./module";
import { json, problem } from "./testData";

afterEach(() => vi.unstubAllGlobals());

function stubFetch(handler: (url: string) => Response | Promise<Response>) {
  const mock = vi.fn(async (url: string) => handler(url));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string]>;

function renderPage() {
  return render(
    <MemoryRouter initialEntries={["/production/kpi"]}>
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

const site = "0195d6a0-0000-7000-8000-000000000061";
const group = (overrides: Partial<OrderKpiGroup> = {}): OrderKpiGroup => ({
  ended: 3,
  dated: 3,
  onTime: 2,
  onTimePct: "66.67",
  avgCycleDays: "3.75",
  qtyFinished: "297",
  qtyError: "3",
  scrapPct: "1.00",
  open: 4,
  overdue: 1,
  ...overrides,
});

describe("order KPI client", () => {
  it("sends only the filters given and tolerates an empty body", async () => {
    const fetchMock = stubFetch(() => json({}));
    const result = await getOrderKpis({ groupBy: "site", from: "2026-10-01", siteId: "" });
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/kpi?groupBy=site&from=2026-10-01&limit=50");
    expect(result.groups).toEqual([]);
    expect(result.totals.onTimePct).toBeNull();
  });

  it("throws the service's problem", async () => {
    stubFetch(() => problem(400, "COM-0001", "request failed validation", { groupBy: "must be site or product" }));
    await expect(getOrderKpis({})).rejects.toMatchObject({ status: 400, code: "COM-0001" });
  });
});

describe("KPI filter and formatting", () => {
  it("refuses malformed ids and a reversed period", () => {
    const base = { groupBy: "" as const, siteId: "", productId: "", from: "", to: "", asOf: "" };
    expect(validateKpiFilter(base)).toEqual({});
    expect(validateKpiFilter({ ...base, siteId: "x", productId: "y", from: "2026-11-01", to: "2026-10-01" })).toEqual({
      siteId: "Enter the site's id (a UUID), or leave it empty.",
      productId: "Enter the product's id (a UUID), or leave it empty.",
      to: "The end of the period must not be before its start.",
    });
  });

  it("shows a dash for a rate with nothing to measure", () => {
    expect(percent("66.67")).toBe("66.67 %");
    expect(percent(null)).toBe("—");
  });
});

describe("production KPIs page (4.21.14)", () => {
  it("shows the totals as tiles and as one row, accessibly", async () => {
    const fetchMock = stubFetch(() => json({ asOf: "2026-10-20", groups: [], groupsTotal: 0, totals: group() }));
    const { container } = renderPage();
    const table = await findTable({ name: "KPIs" });
    await waitFor(() => expect(within(table).getByText("All orders")).toBeInTheDocument());
    expect(within(table).getByText("66.67 %")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Order KPIs" })).toHaveTextContent("Cycle days 3.75");
    expect(screen.getByText(/counted as of 2026-10-20/)).toBeInTheDocument();
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/kpi?limit=50");
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("groups by site with the filters and notes a truncated list", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("groupBy=site")
        ? json({ asOf: "2026-10-20", groupBy: "site", groups: [group({ key: site }), group({ key: "other", onTimePct: null, avgCycleDays: null, scrapPct: null })], groupsTotal: 3, totals: group() })
        : json({ asOf: "2026-10-20", groups: [], groupsTotal: 0, totals: group() }),
    );
    renderPage();
    await findTable({ name: "KPIs" });
    const form = screen.getByRole("form", { name: "Filter the KPIs" });
    await userEvent.selectOptions(within(form).getByLabelText("Group by"), "site");
    fireEvent.change(within(form).getByLabelText(/Ended from/), { target: { value: "2026-10-01" } });
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    await waitFor(() => expect(calls(fetchMock).some(([url]) => url === "/api/v1/production/kpi?groupBy=site&from=2026-10-01&limit=50")).toBe(true));
    const table = await screen.findByRole("table", { name: "KPIs" });
    await waitFor(() => expect(within(table).getByText(site)).toBeInTheDocument());
    expect(within(table).getByRole("columnheader", { name: "Site" })).toBeInTheDocument();
    expect(within(table).getAllByText("—").length).toBeGreaterThanOrEqual(3);
    expect(screen.getByText("Showing 2 of 3 groups; the tiles cover all of them.")).toBeInTheDocument();
  });

  it("refuses a bad filter without calling the service", async () => {
    const fetchMock = stubFetch(() => json({ asOf: "2026-10-20", groups: [], groupsTotal: 0, totals: group() }));
    renderPage();
    await findTable({ name: "KPIs" });
    const form = screen.getByRole("form", { name: "Filter the KPIs" });
    await userEvent.type(within(form).getByLabelText(/Product/), "bolt");
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    expect(await screen.findByText("Enter the product's id (a UUID), or leave it empty.")).toBeInTheDocument();
    expect(calls(fetchMock)).toHaveLength(1);
  });

  it("shows loading, empty and error states", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderPage();
    expect(await screen.findByRole("table", { name: "KPIs" })).toHaveAttribute("aria-busy", "true");
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => json({ asOf: "2026-10-20", groups: [], groupsTotal: 0, totals: group({ ended: 0, open: 0, overdue: 0, onTimePct: null, avgCycleDays: null, scrapPct: null }) }));
    const second = renderPage();
    expect(await screen.findByRole("heading", { name: "No orders" })).toBeInTheDocument();
    second.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(403, "AUTH-0003", "missing duty production.order.read"));
    const third = renderPage();
    expect(await screen.findByText("You need the View production orders duty to see the KPIs.")).toBeInTheDocument();
    third.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "COM-0500", "database unavailable"));
    renderPage();
    expect(await screen.findByText("database unavailable (COM-0500)")).toBeInTheDocument();
  });
});
