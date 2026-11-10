import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { getSubcontractOperations, type SubcontractRow } from "./api/client";
import production from "./module";
import { json, order, orderId, problem } from "./testData";
import { validateSubcontractFilter } from "./SubcontractOperationsPage";

afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;

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

const uuid = (n: number) => `0195d6a0-0000-7000-8000-${String(n).padStart(12, "0")}`;
const vendor = uuid(80);
const page = "/production/subcontract-operations";

const subRow = (overrides: Partial<SubcontractRow> = {}): SubcontractRow => ({
  orderId,
  orderNumber: "PO-000001",
  orderStatus: "Released",
  legalEntityId: uuid(1),
  siteId: uuid(2),
  currency: "USD",
  dueDate: "2026-11-30",
  operNumber: 20,
  resourceId: vendor,
  qty: "100",
  qtyGood: "0",
  unitPrice: "1.25",
  amount: "125.00",
  operStatus: "open",
  poRequestedAt: "2026-10-26T14:00:00Z",
  ...overrides,
});

describe("subcontract operations client", () => {
  it("sends only the filters given and tolerates a body without lists", async () => {
    const fetchMock = stubFetch(() => json({}));
    const report = await getSubcontractOperations({ status: "open", orderStatus: "", resourceId: vendor, dueTo: "2026-11-30" });
    expect(calls(fetchMock)[0][0]).toBe(`/api/v1/production/subcontract-operations?status=open&resourceId=${vendor}&dueTo=2026-11-30&limit=50`);
    expect(report).toEqual({ items: [], totals: [], nextCursor: undefined });
  });

  it("throws the service's problem", async () => {
    stubFetch(() => problem(400, "COM-0001", "request failed validation", { cursor: "must be the nextCursor of a previous page" }));
    await expect(getSubcontractOperations({ cursor: "x" })).rejects.toMatchObject({
      status: 400,
      code: "COM-0001",
      errors: { cursor: "must be the nextCursor of a previous page" },
    });
  });
});

describe("subcontract filter checks", () => {
  const base = { status: "" as const, orderStatus: "" as const, resourceId: "", dueFrom: "", dueTo: "" };

  it("accepts an empty filter and refuses a malformed vendor or a reversed due range", () => {
    expect(validateSubcontractFilter(base)).toEqual({});
    expect(validateSubcontractFilter({ ...base, resourceId: ` ${vendor} `, dueFrom: "2026-11-01", dueTo: "2026-11-01" })).toEqual({});
    const errors = validateSubcontractFilter({ ...base, resourceId: "ACME", dueFrom: "2026-12-01", dueTo: "2026-11-01" });
    expect(errors.resourceId).toMatch(/UUID/);
    expect(errors.dueTo).toMatch(/must not be before/);
  });
});

describe("subcontract operations page (4.21.14, FR-21.10)", () => {
  it("lists the operations with amount, due date and PO request, totals per currency, and opens the order", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("/subcontract-operations")
        ? json({
            items: [
              subRow(),
              subRow({
                orderId: uuid(9),
                orderNumber: "PO-000002",
                orderStatus: "Estimated",
                operNumber: 30,
                currency: "EUR",
                unitPrice: "0.333",
                amount: "33.30",
                dueDate: null,
                poRequestedAt: null,
              }),
              subRow({ orderId: uuid(8), orderNumber: "PO-000003", orderStatus: "Cancelled", operNumber: 40, unitPrice: "3", amount: "300.00", dueDate: "2026-12-15", poRequestedAt: null, poCancelledAt: "2026-10-28T09:00:00Z" }),
            ],
            totals: [
              { currency: "EUR", operations: 1, amount: "33.30" },
              { currency: "USD", operations: 1, amount: "125.00" },
            ],
          })
        : url.includes("?")
          ? json({ items: [] })
          : json(order({ id: uuid(9), orderNumber: "PO-000002", status: "Estimated" })),
    );
    const { container, location } = renderAt(page);
    const table = await findTable({ name: "Subcontract operations" });
    await waitFor(() => expect(within(table).getByText("PO-000002")).toBeInTheDocument());
    expect(within(table).getByText("125.00 USD")).toBeInTheDocument();
    expect(within(table).getByText("0.333 EUR")).toBeInTheDocument();
    expect(within(table).getByText("2026-11-30")).toBeInTheDocument();
    expect(within(table).getByText("2026-10-26")).toBeInTheDocument();
    expect(within(table).getByText("At release")).toBeInTheDocument();
    expect(within(table).getByText("Withdrawn")).toBeInTheDocument();
    expect(within(table).getByText("2026-10-28")).toBeInTheDocument();
    expect(within(table).getByText("300.00 USD")).toBeInTheDocument();
    expect(within(table).getAllByText(vendor)).toHaveLength(3);
    expect(screen.getByRole("list", { name: "Subcontract totals in USD" })).toHaveTextContent("Planned amount 125.00 USD");
    expect(screen.getByRole("list", { name: "Subcontract totals in EUR" })).toHaveTextContent("Operations 1");
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/subcontract-operations?limit=50");
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.click(within(table).getByText("PO-000002"));
    expect(location.pathname).toBe(`/production/${uuid(9)}`);
  });

  it("filters by statuses, vendor and due range and loads more", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("cursor=")
        ? json({ items: [subRow({ orderId: uuid(7), orderNumber: "PO-000007" })], totals: [] })
        : json({ items: [subRow()], totals: [], nextCursor: `${orderId}:20` }),
    );
    renderAt(page);
    await findTable({ name: "Subcontract operations" });
    const form = screen.getByRole("form", { name: "Filter the subcontract operations" });
    await userEvent.selectOptions(within(form).getByLabelText("Operation status"), "started");
    await userEvent.selectOptions(within(form).getByLabelText("Order status"), "Started");
    await userEvent.type(within(form).getByLabelText(/Vendor resource/), vendor);
    fireEvent.change(within(form).getByLabelText(/Due from/), { target: { value: "2026-11-01" } });
    fireEvent.change(within(form).getByLabelText(/Due to/), { target: { value: "2026-11-30" } });
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    const filtered = `/api/v1/production/subcontract-operations?status=started&orderStatus=Started&resourceId=${vendor}&dueFrom=2026-11-01&dueTo=2026-11-30&limit=50`;
    await waitFor(() => expect(calls(fetchMock).some(([url]) => url === filtered)).toBe(true));

    await userEvent.click(await screen.findByRole("button", { name: "Load more operations" }));
    expect(await screen.findByText("PO-000007")).toBeInTheDocument();
    expect(calls(fetchMock).at(-1)?.[0]).toContain(`cursor=${encodeURIComponent(`${orderId}:20`)}`);
    expect(screen.queryByRole("button", { name: "Load more operations" })).not.toBeInTheDocument();
  });

  it("refuses a malformed vendor or a reversed due range without calling the service", async () => {
    const fetchMock = stubFetch(() => json({ items: [], totals: [] }));
    renderAt(page);
    await findTable({ name: "Subcontract operations" });
    const form = screen.getByRole("form", { name: "Filter the subcontract operations" });
    await userEvent.type(within(form).getByLabelText(/Vendor resource/), "ACME");
    fireEvent.change(within(form).getByLabelText(/Due from/), { target: { value: "2026-12-01" } });
    fireEvent.change(within(form).getByLabelText(/Due to/), { target: { value: "2026-11-01" } });
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    expect(await screen.findByText("Enter the vendor resource's id (a UUID), or leave it empty.")).toBeInTheDocument();
    expect(screen.getByText("The end of the due range must not be before its start.")).toBeInTheDocument();
    expect(calls(fetchMock)).toHaveLength(1);
  });

  it("shows loading, empty and error states", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderAt(page);
    expect(await screen.findByRole("table", { name: "Subcontract operations" })).toHaveAttribute("aria-busy", "true");
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [], totals: [] }));
    const second = renderAt(page);
    expect(await screen.findByRole("heading", { name: "No subcontract operations" })).toBeInTheDocument();
    second.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(403, "AUTH-0003", "missing duty production.order.read"));
    const third = renderAt(page);
    expect(await screen.findByText("You need the View production orders duty to see the subcontract operations.")).toBeInTheDocument();
    third.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "COM-0500", "database unavailable"));
    renderAt(page);
    expect(await screen.findByText("database unavailable")).toBeInTheDocument();
  });
});

describe("order route tab", () => {
  it("shows when release requested a subcontract operation's vendor PO", async () => {
    stubFetch((url) => {
      if (url.endsWith(`/orders/${orderId}`)) {
        return json(
          order({
            status: "Released",
            operations: [
              {
                operNumber: 10,
                resourceId: uuid(70),
                setupTimeH: "0",
                runTimeHPer: "0.5",
                scrapPct: "0",
                laborRate: "30",
                overheadRate: "10",
                subcontract: false,
                subcontractPrice: "0",
                processQty: "100",
                status: "open",
              },
              {
                operNumber: 20,
                resourceId: vendor,
                setupTimeH: "0",
                runTimeHPer: "0",
                scrapPct: "0",
                laborRate: "0",
                overheadRate: "0",
                subcontract: true,
                subcontractPrice: "1.25",
                processQty: "100",
                status: "open",
                poRequestedAt: "2026-10-26T14:00:00Z",
              },
            ],
          }),
        );
      }
      return json({ items: [] });
    });
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("Yes, PO requested 2026-10-26")).toBeInTheDocument();
    expect(screen.getAllByText("Open").length).toBeGreaterThanOrEqual(2);
  });
});

describe("order route tab, withdrawn request", () => {
  it("shows that the vendor PO request was withdrawn", async () => {
    stubFetch((url) => {
      if (url.endsWith(`/orders/${orderId}`)) {
        return json(
          order({
            status: "Cancelled",
            operations: [
              {
                operNumber: 20,
                resourceId: vendor,
                setupTimeH: "0",
                runTimeHPer: "0",
                scrapPct: "0",
                laborRate: "0",
                overheadRate: "0",
                subcontract: true,
                subcontractPrice: "1.25",
                processQty: "100",
                status: "open",
                poCancelledAt: "2026-10-28T09:00:00Z",
              },
            ],
          }),
        );
      }
      return json({ items: [] });
    });
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("Yes, PO request withdrawn 2026-10-28")).toBeInTheDocument();
  });
});
