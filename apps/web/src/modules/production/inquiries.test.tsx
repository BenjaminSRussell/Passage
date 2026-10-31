import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { getJournalInquiry, getOperationsOverview, type JournalInquiryRow, type OperationOverviewRow } from "./api/client";
import { validateJournalFilter } from "./JournalInquiryPage";
import production from "./module";
import { validateOperationsFilter } from "./OperationsOverviewPage";
import { json, order, orderId, problem } from "./testData";

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
const resource = uuid(60);
const site = uuid(61);

const opRow = (overrides: Partial<OperationOverviewRow> = {}): OperationOverviewRow => ({
  orderId,
  orderNumber: "PO-000001",
  orderStatus: "Started",
  legalEntityId: uuid(1),
  siteId: uuid(2),
  productId: uuid(3),
  dueDate: "2026-11-30",
  operNumber: 10,
  resourceId: resource,
  subcontract: false,
  processQty: "100",
  plannedHours: "50.00",
  hoursReported: "20.00",
  qtyGood: "60",
  qtyError: "2",
  operStatus: "started",
  ...overrides,
});

const totals = { operations: 2, open: 1, started: 1, finished: 0, plannedHours: "50.00", hoursReported: "20.00", qtyGood: "60", qtyError: "2" };

const journalRow = (overrides: Partial<JournalInquiryRow> = {}): JournalInquiryRow => ({
  id: uuid(20),
  orderId,
  orderNumber: "PO-000001",
  legalEntityId: uuid(1),
  type: "picking-list",
  journalNumber: "PJ-000001",
  postingDate: "2026-10-20",
  posted: true,
  postedAt: "2026-10-20T08:00:00Z",
  qtyGood: "0",
  qtyError: "0",
  reversalOf: null,
  journalEntryId: null,
  currency: "USD",
  lines: 2,
  consumed: "3111.00",
  received: "0.00",
  ...overrides,
});

describe("operations overview client", () => {
  it("sends only the filters given and tolerates an empty body", async () => {
    const fetchMock = stubFetch(() => json({}));
    const result = await getOperationsOverview({ status: "open", orderStatus: "", resourceId: resource, dueTo: "2026-11-30" });
    expect(calls(fetchMock)[0][0]).toBe(`/api/v1/production/operations?status=open&resourceId=${resource}&dueTo=2026-11-30&limit=50`);
    expect(result.items).toEqual([]);
    expect(result.totals.operations).toBe(0);
  });

  it("throws the service's problem", async () => {
    stubFetch(() => problem(400, "COM-0001", "request failed validation", { cursor: "must be the nextCursor of a previous page" }));
    await expect(getOperationsOverview({ cursor: "x" })).rejects.toMatchObject({ status: 400, code: "COM-0001" });
  });
});

describe("journal inquiry client", () => {
  it("sends the filters and tolerates an empty body", async () => {
    const fetchMock = stubFetch(() => json({}));
    const result = await getJournalInquiry({ type: "route-card", posted: "false", from: "2026-10-01" });
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/journals?type=route-card&posted=false&from=2026-10-01&limit=50");
    expect(result).toEqual({ items: [], total: 0, nextCursor: undefined });
  });
});

describe("filter checks", () => {
  it("refuses a malformed resource and reversed ranges", () => {
    const base = { status: "" as const, orderStatus: "" as const, resourceId: "", siteId: "", dueFrom: "", dueTo: "" };
    expect(validateOperationsFilter(base)).toEqual({});
    expect(validateOperationsFilter({ ...base, resourceId: "x", siteId: "Plant 1", dueFrom: "2026-12-01", dueTo: "2026-11-01" })).toEqual({
      resourceId: "Enter the resource's id (a UUID), or leave it empty.",
      siteId: "Enter the site's id (a UUID), or leave it empty.",
      dueTo: "The end of the due range must not be before its start.",
    });
    const journals = { type: "" as const, posted: "" as const, orderNumber: "" };
    expect(validateJournalFilter({ ...journals, from: "2026-12-01", to: "2026-11-01" }).to).toMatch(/must not be before/);
    expect(validateJournalFilter({ ...journals, from: "", to: "2026-11-01" })).toEqual({});
  });
});

describe("operations overview page (4.21.14)", () => {
  const page = "/production/operations";

  it("lists operations with hours, quantities and status, shows the totals and opens the order", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("/operations?")
        ? json({
            items: [opRow(), opRow({ operNumber: 20, subcontract: true, plannedHours: "0.00", hoursReported: "0.00", qtyGood: "0", qtyError: "0", operStatus: "open" })],
            totals,
          })
        : json(order()),
    );
    const { container, location } = renderAt(page);
    const table = await findTable({ name: "Operations" });
    await waitFor(() => expect(within(table).getByText(`${resource} (vendor)`)).toBeInTheDocument());
    expect(within(table).getByText("50.00")).toBeInTheDocument();
    expect(within(table).getByText("20.00")).toBeInTheDocument();
    expect(within(table).getAllByText("2026-11-30")).toHaveLength(2);
    expect(screen.getByRole("list", { name: "Operation totals" })).toHaveTextContent("Reported hours 20.00");
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/operations?limit=50");
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.click(within(table).getAllByText("PO-000001")[0]);
    expect(location.pathname).toBe(`/production/${orderId}`);
  });

  it("filters and loads more", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("cursor=")
        ? json({ items: [opRow({ orderId: uuid(7), orderNumber: "PO-000007" })], totals })
        : json({ items: [opRow()], totals, nextCursor: `${orderId}:10` }),
    );
    renderAt(page);
    await findTable({ name: "Operations" });
    const form = screen.getByRole("form", { name: "Filter the operations" });
    await userEvent.selectOptions(within(form).getByLabelText("Operation status"), "started");
    await userEvent.selectOptions(within(form).getByLabelText("Order status"), "Started");
    await userEvent.type(within(form).getByLabelText(/Resource/), resource);
    await userEvent.type(within(form).getByLabelText(/Site/), site);
    fireEvent.change(within(form).getByLabelText(/Due from/), { target: { value: "2026-11-01" } });
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    const filtered = `/api/v1/production/operations?status=started&orderStatus=Started&resourceId=${resource}&siteId=${site}&dueFrom=2026-11-01&limit=50`;
    await waitFor(() => expect(calls(fetchMock).some(([url]) => url === filtered)).toBe(true));

    await userEvent.click(await screen.findByRole("button", { name: "Load more operations" }));
    expect(await screen.findByText("PO-000007")).toBeInTheDocument();
    expect(calls(fetchMock).at(-1)?.[0]).toContain(`cursor=${encodeURIComponent(`${orderId}:10`)}`);
    expect(screen.queryByRole("button", { name: "Load more operations" })).not.toBeInTheDocument();
  });

  it("keeps the rows shown when loading more fails", async () => {
    stubFetch((url) => (url.includes("cursor=") ? problem(503, "COM-0503", "database unavailable") : json({ items: [opRow()], totals, nextCursor: `${orderId}:10` })));
    renderAt(page);
    const table = await findTable({ name: "Operations" });
    await waitFor(() => expect(within(table).getByText("PO-000001")).toBeInTheDocument());
    await userEvent.click(await screen.findByRole("button", { name: "Load more operations" }));
    expect(await screen.findByText("database unavailable (COM-0503)")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Operations" })).getByText("PO-000001")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Operation totals" })).toBeInTheDocument();
  });

  it("refuses a bad filter without calling the service", async () => {
    const fetchMock = stubFetch(() => json({ items: [], totals }));
    renderAt(page);
    await findTable({ name: "Operations" });
    const form = screen.getByRole("form", { name: "Filter the operations" });
    await userEvent.type(within(form).getByLabelText(/Resource/), "ACME");
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    expect(await screen.findByText("Enter the resource's id (a UUID), or leave it empty.")).toBeInTheDocument();
    expect(calls(fetchMock)).toHaveLength(1);
  });

  it("shows loading, empty and error states", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderAt(page);
    expect(await screen.findByRole("table", { name: "Operations" })).toHaveAttribute("aria-busy", "true");
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [], totals: { ...totals, operations: 0 } }));
    const second = renderAt(page);
    expect(await screen.findByRole("heading", { name: "No operations" })).toBeInTheDocument();
    second.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(403, "AUTH-0003", "missing duty production.order.read"));
    const third = renderAt(page);
    expect(await screen.findByText("You need the View production orders duty to see the operations overview.")).toBeInTheDocument();
    third.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "COM-0500", "database unavailable"));
    renderAt(page);
    expect(await screen.findByText("database unavailable (COM-0500)")).toBeInTheDocument();
  });
});

describe("production journals page (4.21.14)", () => {
  const page = "/production/journals";

  it("lists journals across orders with status and values and opens the order", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("/journals?")
        ? json({
            items: [
              journalRow(),
              journalRow({ id: uuid(21), journalNumber: "PJ-000002", type: "route-card", posted: false, postedAt: null, consumed: "1840.00", orderNumber: "PO-000002" }),
              journalRow({ id: uuid(22), journalNumber: "PJ-000003", reversalOf: uuid(20), consumed: "-3111.00", journalEntryId: uuid(99) }),
            ],
            total: 3,
          })
        : json(order()),
    );
    const { container, location } = renderAt(page);
    const table = await findTable({ name: "Production journals" });
    await waitFor(() => expect(within(table).getByText("PJ-000002")).toBeInTheDocument());
    expect(within(table).getByText("3111.00 USD")).toBeInTheDocument();
    expect(within(table).getByText("-3111.00 USD")).toBeInTheDocument();
    expect(within(table).getByText("Draft")).toBeInTheDocument();
    // A draft is valued when it posts: its values are not shown.
    const draftRow = within(table).getByText("PJ-000002").closest("tr") as HTMLElement;
    expect(within(draftRow).queryByText(/USD/)).not.toBeInTheDocument();
    expect(within(table).getByText("Reversal")).toBeInTheDocument();
    expect(within(table).getByText("Route card")).toBeInTheDocument();
    expect(within(table).getByText(uuid(99))).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Journal count" })).toHaveTextContent("Journals 3");
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/journals?limit=50");
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.click(within(table).getByText("PO-000002"));
    expect(location.pathname).toBe(`/production/${orderId}`);
  });

  it("filters by type, posted state and dates and loads more", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("cursor=")
        ? json({ items: [journalRow({ id: uuid(30), journalNumber: "PJ-000030" })], total: 2 })
        : json({ items: [journalRow()], total: 2, nextCursor: uuid(20) }),
    );
    renderAt(page);
    await findTable({ name: "Production journals" });
    const form = screen.getByRole("form", { name: "Filter the production journals" });
    await userEvent.selectOptions(within(form).getByLabelText("Journal type"), "route-card");
    await userEvent.selectOptions(within(form).getByLabelText("Posted"), "true");
    await userEvent.type(within(form).getByLabelText(/Order number/), " PO-000001 ");
    fireEvent.change(within(form).getByLabelText(/Posting date from/), { target: { value: "2026-10-01" } });
    fireEvent.change(within(form).getByLabelText(/Posting date to/), { target: { value: "2026-10-31" } });
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    const filtered = "/api/v1/production/journals?type=route-card&posted=true&orderNumber=PO-000001&from=2026-10-01&to=2026-10-31&limit=50";
    await waitFor(() => expect(calls(fetchMock).some(([url]) => url === filtered)).toBe(true));

    await userEvent.click(await screen.findByRole("button", { name: "Load more journals" }));
    expect(await screen.findByText("PJ-000030")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more journals" })).not.toBeInTheDocument();
  });

  it("keeps the rows shown when loading more fails", async () => {
    stubFetch((url) =>
      url.includes("cursor=") ? problem(503, "COM-0503", "database unavailable") : json({ items: [journalRow()], total: 2, nextCursor: uuid(20) }),
    );
    renderAt(page);
    const table = await findTable({ name: "Production journals" });
    await waitFor(() => expect(within(table).getByText("PJ-000001")).toBeInTheDocument());
    await userEvent.click(await screen.findByRole("button", { name: "Load more journals" }));
    expect(await screen.findByText("database unavailable (COM-0503)")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Production journals" })).getByText("PJ-000001")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Load more journals" })).toBeInTheDocument();
  });

  it("refuses a reversed date range without calling the service", async () => {
    const fetchMock = stubFetch(() => json({ items: [], total: 0 }));
    renderAt(page);
    await findTable({ name: "Production journals" });
    const form = screen.getByRole("form", { name: "Filter the production journals" });
    fireEvent.change(within(form).getByLabelText(/Posting date from/), { target: { value: "2026-12-01" } });
    fireEvent.change(within(form).getByLabelText(/Posting date to/), { target: { value: "2026-11-01" } });
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    expect(await screen.findByText("The end of the date range must not be before its start.")).toBeInTheDocument();
    expect(calls(fetchMock)).toHaveLength(1);
  });

  it("shows loading, empty and error states", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderAt(page);
    expect(await screen.findByRole("table", { name: "Production journals" })).toHaveAttribute("aria-busy", "true");
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [], total: 0 }));
    const second = renderAt(page);
    expect(await screen.findByRole("heading", { name: "No production journals" })).toBeInTheDocument();
    second.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(403, "AUTH-0003", "missing duty production.order.read"));
    const third = renderAt(page);
    expect(await screen.findByText("You need the View production orders duty to see the production journals.")).toBeInTheDocument();
    third.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "COM-0500", "database unavailable"));
    renderAt(page);
    expect(await screen.findByText("database unavailable (COM-0500)")).toBeInTheDocument();
  });
});
