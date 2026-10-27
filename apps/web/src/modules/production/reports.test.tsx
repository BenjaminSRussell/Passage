import { findAlert, findTable } from "../../test-utils";
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import {
  getVarianceReport,
  getWIPBalances,
  type VarianceRow,
  type WIPRow,
} from "./api/client";
import production from "./module";
import { json, order, orderId, problem } from "./testData";
import { validatePeriod } from "./VarianceReportPage";

afterEach(() => vi.unstubAllGlobals());

type Handler = (
  url: string,
  init?: RequestInit,
) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) =>
    handler(url, init),
  );
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) =>
  mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;

function renderAt(path: string) {
  const at = { pathname: path };
  function Probe() {
    at.pathname = useLocation().pathname;
    return null;
  }
  const view = render(
    <MemoryRouter initialEntries={[path]}>
      <a href="#main">Skip to content</a>
      <nav aria-label="Workspaces" />
      <main id="main">
        <h1>Production</h1>
        <Probe />
        <Routes>
          {production.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </main>
    </MemoryRouter>,
  );
  return { location: at, ...view };
}

const uuid = (n: number) =>
  `0195d6a0-0000-7000-8000-${String(n).padStart(12, "0")}`;

const wipRow = (overrides: Partial<WIPRow> = {}): WIPRow => ({
  orderId,
  orderNumber: "PO-000001",
  legalEntityId: uuid(1),
  siteId: uuid(2),
  productId: uuid(3),
  status: "ReportedAsFinished",
  currency: "USD",
  consumption: "4951.00",
  rafValue: "5000.00",
  settled: "0.00",
  wipBalance: "-49.00",
  ...overrides,
});

const varianceRow = (overrides: Partial<VarianceRow> = {}): VarianceRow => ({
  id: uuid(50),
  orderId,
  orderNumber: "PO-000001",
  siteId: uuid(2),
  currency: "USD",
  type: "production-quantity",
  costGroup: "labor",
  amount: "-120.00",
  planned: "1500.00",
  pctOfPlanned: "-8.00",
  postedOn: "2026-10-26",
  ...overrides,
});

describe("WIP balance report (4.21.14)", () => {
  it("shows orders with consumption, received value and balance, and the totals", async () => {
    const fetchMock = stubFetch((url) =>
      !url.includes("/wip-balances")
        ? json(
            url.includes("?")
              ? { items: [] }
              : order({
                  id: uuid(9),
                  orderNumber: "PO-000002",
                  status: "Started",
                }),
          )
        : json({
            asOf: "2026-10-26",
            items: [
              wipRow(),
              wipRow({
                orderId: uuid(9),
                orderNumber: "PO-000002",
                status: "Started",
                consumption: "100.00",
                rafValue: "0.00",
                wipBalance: "100.00",
              }),
            ],
            totals: [
              {
                currency: "USD",
                orders: 2,
                consumption: "5051.00",
                rafValue: "5000.00",
                settled: "0.00",
                wipBalance: "51.00",
              },
            ],
          }),
    );
    const { container, location } = renderAt("/production/wip-balances");
    const table = await findTable({
      name: "WIP balance as of 2026-10-26",
    });
    await waitFor(() =>
      expect(within(table).getByText("PO-000002")).toBeInTheDocument(),
    );
    expect(within(table).getByText("-49.00 USD")).toBeInTheDocument();
    expect(within(table).getByText("Reported as finished")).toBeInTheDocument();
    const totals = screen.getByRole("list", { name: "WIP totals in USD" });
    expect(totals).toHaveTextContent("WIP balance 51.00 USD");
    expect(totals).toHaveTextContent("Orders 2");
    expect(calls(fetchMock)[0][0]).toBe(
      "/api/v1/production/wip-balances?limit=50",
    );
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.click(within(table).getByText("PO-000002"));
    expect(location.pathname).toBe(`/production/${uuid(9)}`);
  });

  it("filters by status and date and loads more", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("cursor=")
        ? json({
            asOf: "2026-10-01",
            items: [wipRow({ orderId: uuid(7), orderNumber: "PO-000007" })],
            totals: [],
          })
        : json({
            asOf: "2026-10-01",
            items: [wipRow()],
            totals: [],
            nextCursor: orderId,
          }),
    );
    renderAt("/production/wip-balances");
    await findTable({ name: /WIP balance/ });
    const form = screen.getByRole("form", { name: "Filter the WIP balance" });
    await userEvent.selectOptions(
      within(form).getByLabelText("Status"),
      "Started",
    );
    fireEvent.change(within(form).getByLabelText(/As of/), {
      target: { value: "2026-10-01" },
    });
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    await waitFor(() =>
      expect(
        calls(fetchMock).some(
          ([url]) =>
            url ===
            "/api/v1/production/wip-balances?status=Started&asOf=2026-10-01&limit=50",
        ),
      ).toBe(true),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "Load more orders" }),
    );
    expect(await screen.findByText("PO-000007")).toBeInTheDocument();
    expect(calls(fetchMock).at(-1)?.[0]).toContain(`cursor=${orderId}`);
  });

  it("refuses a malformed site without calling the service", async () => {
    const fetchMock = stubFetch(() =>
      json({ asOf: "2026-10-26", items: [], totals: [] }),
    );
    renderAt("/production/wip-balances");
    await findTable({ name: /WIP balance/ });
    const form = screen.getByRole("form", { name: "Filter the WIP balance" });
    await userEvent.type(within(form).getByLabelText(/Site/), "plant 1");
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    expect(
      await screen.findByText(
        "Enter the site's id (a UUID), or leave it empty.",
      ),
    ).toBeInTheDocument();
    expect(calls(fetchMock)).toHaveLength(1);
  });

  it("shows loading, empty and error states", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderAt("/production/wip-balances");
    expect(
      await screen.findByRole("table", { name: "WIP balance" }),
    ).toHaveAttribute("aria-busy", "true");
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => json({ asOf: "2026-10-26", items: [], totals: [] }));
    const second = renderAt("/production/wip-balances");
    expect(
      await screen.findByRole("heading", { name: "No work in progress" }),
    ).toBeInTheDocument();
    second.unmount();

    vi.unstubAllGlobals();
    stubFetch(() =>
      problem(403, "AUTH-0003", "missing duty production.cost.read"),
    );
    renderAt("/production/wip-balances");
    expect(
      await screen.findByText(
        "You need the View production costs duty to see the WIP balance.",
      ),
    ).toBeInTheDocument();
  });
});

describe("variance report (4.21.14)", () => {
  it("shows variances with their share of planned cost, totals by type and the net", async () => {
    const fetchMock = stubFetch(() =>
      json({
        items: [
          varianceRow(),
          varianceRow({
            id: uuid(51),
            type: "production-price",
            costGroup: "material",
            amount: "61.00",
            planned: "3000.00",
            pctOfPlanned: "2.03",
          }),
          varianceRow({
            id: uuid(52),
            type: "lot-size",
            costGroup: "subcontract",
            amount: "5.00",
            planned: "0.00",
            pctOfPlanned: null,
          }),
        ],
        totals: [
          { currency: "USD", type: "production-quantity", amount: "-120.00" },
          { currency: "USD", type: "production-price", amount: "61.00" },
          { currency: "USD", type: "lot-size", amount: "5.00" },
        ],
        net: [{ currency: "USD", amount: "-54.00" }],
      }),
    );
    const { container } = renderAt("/production/variances");
    const table = await findTable({
      name: "Posted variances",
    });
    await waitFor(() =>
      expect(within(table).getByText("2.03%")).toBeInTheDocument(),
    );
    expect(within(table).getByText("-8.00%")).toBeInTheDocument();
    expect(within(table).getByText("—")).toBeInTheDocument();
    const totals = screen.getByRole("list", { name: "Variance totals in USD" });
    expect(totals).toHaveTextContent("Price 61.00 USD");
    expect(totals).toHaveTextContent("Net -54.00 USD");
    expect(totals).toHaveTextContent("Favourable");
    expect(calls(fetchMock)[0][0]).toBe(
      "/api/v1/production/variances?limit=50",
    );
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("filters by type and period, and checks the period first", async () => {
    expect(validatePeriod("2026-11-01", "2026-10-01")).toBe(
      "The end of the period must not be before its start.",
    );
    expect(validatePeriod("2026-10-01", "")).toBeUndefined();
    const fetchMock = stubFetch(() => json({ items: [], totals: [], net: [] }));
    renderAt("/production/variances");
    expect(
      await screen.findByRole("heading", { name: "No variances" }),
    ).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Filter the variances" });
    await userEvent.selectOptions(
      within(form).getByLabelText("Variance"),
      "rework",
    );
    fireEvent.change(within(form).getByLabelText(/Posted from/), {
      target: { value: "2026-11-01" },
    });
    fireEvent.change(within(form).getByLabelText(/Posted to/), {
      target: { value: "2026-10-01" },
    });
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    expect(
      await screen.findByText(
        "The end of the period must not be before its start.",
      ),
    ).toBeInTheDocument();
    expect(calls(fetchMock)).toHaveLength(1);
    fireEvent.change(within(form).getByLabelText(/Posted to/), {
      target: { value: "2026-11-30" },
    });
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    await waitFor(() =>
      expect(calls(fetchMock).at(-1)?.[0]).toBe(
        "/api/v1/production/variances?varianceType=rework&from=2026-11-01&to=2026-11-30&limit=50",
      ),
    );
  });

  it("shows a failure with its code", async () => {
    stubFetch(() => problem(500, "COM-0500", "internal error"));
    renderAt("/production/variances");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("COM-0500");
  });

  it("normalizes partial responses in the client", async () => {
    stubFetch(() => json({}));
    expect(await getVarianceReport()).toEqual({
      items: [],
      totals: [],
      net: [],
      nextCursor: undefined,
    });
    expect(await getWIPBalances({ asOf: "2026-10-01" })).toEqual({
      asOf: "2026-10-01",
      items: [],
      totals: [],
      nextCursor: undefined,
    });
  });
});
