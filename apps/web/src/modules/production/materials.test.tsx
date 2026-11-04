import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { getMaterialItems, getMaterialRequirements, type MaterialItemRow, type MaterialLineRow } from "./api/client";
import { validateMaterialFilter } from "./MaterialRequirementsPage";
import production from "./module";
import { json, order, orderId, problem } from "./testData";

afterEach(() => vi.unstubAllGlobals());

function stubFetch(handler: (url: string) => Response | Promise<Response>) {
  const mock = vi.fn(async (url: string) => handler(url));
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
const item = uuid(70);
const site = uuid(71);

const itemRow = (overrides: Partial<MaterialItemRow> = {}): MaterialItemRow => ({
  itemId: item,
  orders: 2,
  lines: 3,
  qtyRequired: "500",
  qtyIssued: "50",
  qtyOutstanding: "450",
  earliestDue: "2026-11-30",
  ...overrides,
});

const lineRow = (overrides: Partial<MaterialLineRow> = {}): MaterialLineRow => ({
  orderId,
  orderNumber: "PO-000001",
  orderStatus: "Released",
  legalEntityId: uuid(1),
  siteId: site,
  productId: uuid(3),
  dueDate: "2026-11-30",
  lineNumber: "10",
  itemId: item,
  qtyRequired: "200",
  qtyIssued: "50",
  qtyOutstanding: "150",
  ...overrides,
});

const totals = { lines: 4, items: 2, orders: 3 };

describe("material requirements client", () => {
  it("sends only the filters given and tolerates an empty body", async () => {
    const fetchMock = stubFetch(() => json({}));
    const lines = await getMaterialRequirements({ itemId: item, orderStatus: "", all: true, dueTo: "2026-11-30" });
    expect(calls(fetchMock)[0][0]).toBe(`/api/v1/production/material-requirements?itemId=${item}&dueTo=2026-11-30&all=true&limit=50`);
    expect(lines).toEqual({ items: [], totals: { lines: 0, items: 0, orders: 0 }, nextCursor: undefined });
    const items = await getMaterialItems({ cursor: item });
    expect(calls(fetchMock)[1][0]).toBe(`/api/v1/production/material-requirements/by-item?cursor=${item}&limit=50`);
    expect(items.items).toEqual([]);
  });

  it("throws the service's problem", async () => {
    stubFetch(() => problem(400, "COM-0001", "request failed validation", { cursor: "must be the nextCursor of a previous page" }));
    await expect(getMaterialItems({ cursor: "x" })).rejects.toMatchObject({ status: 400, code: "COM-0001" });
  });
});

describe("material filter check", () => {
  it("refuses malformed ids and a reversed due range", () => {
    const base = { view: "items" as const, itemId: "", siteId: "", orderStatus: "" as const, dueFrom: "", dueTo: "", all: false };
    expect(validateMaterialFilter(base)).toEqual({});
    expect(validateMaterialFilter({ ...base, itemId: "x", siteId: "Plant", dueFrom: "2026-12-01", dueTo: "2026-11-01" })).toEqual({
      itemId: "Enter the component's id (a UUID), or leave it empty.",
      siteId: "Enter the site's id (a UUID), or leave it empty.",
      dueTo: "The end of the due range must not be before its start.",
    });
  });
});

describe("material requirements page (4.21.14)", () => {
  const page = "/production/material-requirements";

  it("lists the outstanding demand per component and switches to the order lines", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/by-item")) {
        return json({ items: [itemRow()] });
      }
      return url.includes("/material-requirements?") ? json({ items: [lineRow()], totals }) : json(order());
    });
    const { container, location } = renderAt(page);
    const table = await findTable({ name: "Components" });
    await waitFor(() => expect(within(table).getByText(item)).toBeInTheDocument());
    expect(within(table).getByText("450")).toBeInTheDocument();
    expect(within(table).getByText("2026-11-30")).toBeInTheDocument();
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/material-requirements/by-item?limit=50");
    expect(checkAxeAA(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Filter the material requirements" });
    await userEvent.selectOptions(within(form).getByLabelText("Show"), "lines");
    await userEvent.selectOptions(within(form).getByLabelText("Order status"), "Released");
    await userEvent.selectOptions(within(form).getByLabelText("Lines"), "all");
    await userEvent.type(within(form).getByLabelText(/Site/), site);
    fireEvent.change(within(form).getByLabelText(/Due from/), { target: { value: "2026-11-01" } });
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    const lines = await findTable({ name: "Order lines" });
    await waitFor(() => expect(within(lines).getByText("PO-000001")).toBeInTheDocument());
    expect(calls(fetchMock).at(-1)?.[0]).toBe(
      `/api/v1/production/material-requirements?siteId=${site}&orderStatus=Released&dueFrom=2026-11-01&all=true&limit=50`,
    );
    expect(screen.getByRole("list", { name: "Material totals" })).toHaveTextContent("Orders 3");
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.click(within(lines).getByText("PO-000001"));
    expect(location.pathname).toBe(`/production/${orderId}`);
  });

  it("loads more components and keeps the rows when loading more fails", async () => {
    const other = uuid(72);
    stubFetch((url) => {
      if (url.includes("cursor=")) {
        return url.includes(other) ? json({ items: [itemRow({ itemId: uuid(73) })] }) : problem(503, "COM-0503", "database unavailable");
      }
      return json({ items: [itemRow()], nextCursor: item });
    });
    renderAt(page);
    const table = await findTable({ name: "Components" });
    await waitFor(() => expect(within(table).getByText(item)).toBeInTheDocument());
    await userEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("database unavailable (COM-0503)")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Components" })).getByText(item)).toBeInTheDocument();
  });

  it("appends the next page", async () => {
    stubFetch((url) => (url.includes("cursor=") ? json({ items: [itemRow({ itemId: uuid(73) })] }) : json({ items: [itemRow()], nextCursor: item })));
    renderAt(page);
    await findTable({ name: "Components" });
    await userEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText(uuid(73))).toBeInTheDocument();
    expect(screen.getByText(item)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("refuses a bad filter without calling the service", async () => {
    const fetchMock = stubFetch(() => json({ items: [] }));
    renderAt(page);
    await findTable({ name: "Components" });
    const form = screen.getByRole("form", { name: "Filter the material requirements" });
    await userEvent.type(within(form).getByLabelText(/Component/), "ACME");
    await userEvent.click(within(form).getByRole("button", { name: "Show" }));
    expect(await screen.findByText("Enter the component's id (a UUID), or leave it empty.")).toBeInTheDocument();
    expect(calls(fetchMock)).toHaveLength(1);
  });

  it("shows loading, empty and error states", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderAt(page);
    expect(await screen.findByRole("table", { name: "Components" })).toHaveAttribute("aria-busy", "true");
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [] }));
    const second = renderAt(page);
    expect(await screen.findByRole("heading", { name: "No open material requirements" })).toBeInTheDocument();
    second.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(403, "AUTH-0003", "missing duty production.order.read"));
    const third = renderAt(page);
    expect(await screen.findByText("You need the View production orders duty to see the material requirements.")).toBeInTheDocument();
    third.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "COM-0500", "database unavailable"));
    renderAt(page);
    expect(await screen.findByText("database unavailable (COM-0500)")).toBeInTheDocument();
  });
});
