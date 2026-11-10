import { findTable } from "../../test-utils";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { convertPlannedIntake, dismissPlannedIntake, listPlannedIntake, type PlannedIntake } from "./api/client";
import production from "./module";
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
const rowId = uuid(700);

const row = (overrides: Partial<PlannedIntake> = {}): PlannedIntake => ({
  id: rowId,
  legalEntityId: uuid(1),
  plannedOrderId: uuid(701),
  planVersionId: uuid(702),
  batchId: uuid(703),
  targetId: uuid(704),
  productId: uuid(2),
  siteId: uuid(3),
  warehouseId: uuid(4),
  quantity: "40",
  orderDate: "2026-11-02",
  deliveryDate: "2026-11-20",
  quotaArrangementId: null,
  quotaSourceId: null,
  status: "open",
  orderId: null,
  dismissReason: null,
  version: 1,
  receivedAt: "2026-11-02T09:00:00Z",
  updatedAt: "2026-11-02T09:00:00Z",
  ...overrides,
});

describe("planned order intake client", () => {
  it("sends only the filters given and tolerates a body without a list", async () => {
    const fetchMock = stubFetch(() => json({}));
    expect(await listPlannedIntake({ status: "open", siteId: uuid(3) })).toEqual({ items: [], nextCursor: undefined });
    expect(calls(fetchMock)[0][0]).toBe(`/api/v1/production/planned-intake?status=open&siteId=${uuid(3)}&limit=50`);
    await listPlannedIntake();
    expect(calls(fetchMock)[1][0]).toBe("/api/v1/production/planned-intake?limit=50");
  });

  it("converts under an idempotency key and dismisses under If-Match", async () => {
    const fetchMock = stubFetch((url) => (url.endsWith(":convert") ? json(order()) : json(row({ status: "dismissed", version: 2 }))));
    await convertPlannedIntake(rowId, { productId: "", siteId: "", warehouseId: "", currency: "USD", quantity: "", bom: [], operations: [] }, "key-1");
    await dismissPlannedIntake(rowId, 1, "Demand withdrawn");
    const [convertUrl, convert] = calls(fetchMock)[0];
    const [dismissUrl, dismiss] = calls(fetchMock)[1];
    expect(convertUrl).toBe(`/api/v1/production/planned-intake/${rowId}:convert`);
    expect((convert?.headers as Record<string, string>)["Idempotency-Key"]).toBe("key-1");
    expect(dismissUrl).toBe(`/api/v1/production/planned-intake/${rowId}:dismiss`);
    expect((dismiss?.headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(JSON.parse(String(dismiss?.body))).toEqual({ reason: "Demand withdrawn" });
  });
});

describe("planned orders list (FR-22.10)", () => {
  // No artificial delay between simulated keystrokes and clicks: every step awaits the state it needs.
  const user = () => userEvent.setup({ delay: null });
  const firstPage = () => json({ items: [row(), row({ id: uuid(711), warehouseId: null, quantity: "12.5" })], nextCursor: "cursor-1" });

  it("lists the waiting planned orders with their warehouse", async () => {
    const fetchMock = stubFetch(firstPage);
    const { container } = renderAt("/production/planned-intake");
    const table = await findTable({ name: "Planned orders" });
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/planned-intake?status=open&limit=50");
    expect(await within(table).findByText("12.5")).toBeInTheDocument();
    expect(within(table).getByText("Site scope")).toBeInTheDocument();
    expect(within(table).getByText(uuid(4))).toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("loads the next page after the rows shown, and keeps them when that fails", async () => {
    let failMore = false;
    const fetchMock = stubFetch((url) => {
      if (url.includes("cursor=")) {
        return failMore ? problem(500, "COM-0500", "database unavailable") : json({ items: [row({ id: uuid(710), quantity: "7", status: "converted", orderId })] });
      }
      return firstPage();
    });
    renderAt("/production/planned-intake");
    await user().click(await screen.findByRole("button", { name: "Load more planned orders" }));
    const table = await findTable({ name: "Planned orders" });
    expect(await within(table).findByText("Converted")).toBeInTheDocument();
    expect(within(table).getByText("12.5")).toBeInTheDocument();
    expect(calls(fetchMock).at(-1)?.[0]).toBe("/api/v1/production/planned-intake?status=open&cursor=cursor-1&limit=50");
    expect(screen.queryByRole("button", { name: "Load more planned orders" })).not.toBeInTheDocument();

    failMore = true;
    cleanup();
    renderAt("/production/planned-intake");
    await user().click(await screen.findByRole("button", { name: "Load more planned orders" }));
    expect(await screen.findByText("database unavailable")).toBeInTheDocument();
    expect(within(await findTable({ name: "Planned orders" })).getByText("12.5")).toBeInTheDocument();
  });

  it("filters by status and opens a row", async () => {
    const fetchMock = stubFetch(firstPage);
    const { location } = renderAt("/production/planned-intake");
    await within(await findTable({ name: "Planned orders" })).findByText("12.5");
    const form = screen.getByRole("form", { name: "Filter the planned orders" });
    await user().selectOptions(within(form).getByLabelText("Status"), "dismissed");
    await user().click(within(form).getByRole("button", { name: "Show" }));
    await waitFor(() => expect(calls(fetchMock).at(-1)?.[0]).toBe("/api/v1/production/planned-intake?status=dismissed&limit=50"));
    await user().click(await within(await findTable({ name: "Planned orders" })).findByText("12.5"));
    expect(location.pathname).toBe(`/production/planned-intake/${uuid(711)}`);
  });

  it("shows loading, empty, no-duty and error states", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    const first = renderAt("/production/planned-intake");
    expect(await screen.findByRole("table", { name: "Planned orders" })).toHaveAttribute("aria-busy", "true");
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [] }));
    const second = renderAt("/production/planned-intake");
    expect(await screen.findByRole("heading", { name: "No planned orders" })).toBeInTheDocument();
    second.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(403, "AUTH-0003", "missing duty production.order.read"));
    const third = renderAt("/production/planned-intake");
    expect(await screen.findByText("You need the View production orders duty to see the planned orders.")).toBeInTheDocument();
    third.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "COM-0500", "database unavailable"));
    renderAt("/production/planned-intake");
    expect(await screen.findByText("database unavailable")).toBeInTheDocument();
  });
});

describe("planned order detail (FR-22.10)", () => {
  const path = `/production/planned-intake/${rowId}`;

  it("shows the facts and opens the convert form", async () => {
    stubFetch(() => json(row()));
    const { container, location } = renderAt(path);
    expect(await screen.findByRole("heading", { name: "Planned order of 40" })).toBeInTheDocument();
    expect(screen.getByText("Waiting")).toBeInTheDocument();
    expect(screen.getByText(uuid(4))).toBeInTheDocument();
    expect(screen.getByText(uuid(1))).toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);
    await userEvent.click(screen.getByRole("button", { name: "Convert to production order" }));
    expect(location.pathname).toBe(`${path}/convert`);
  });

  it("dismisses with a reason under the row's version", async () => {
    const fetchMock = stubFetch((_url, init) => (init?.method === "POST" ? json(row({ status: "dismissed", version: 2, dismissReason: "Demand withdrawn" })) : json(row())));
    renderAt(path);
    await screen.findByRole("heading", { name: "Planned order of 40" });
    await userEvent.click(screen.getByRole("button", { name: "Dismiss planned order" }));
    expect(await screen.findByText("Enter a reason of up to 500 characters.")).toBeInTheDocument();
    expect(calls(fetchMock)).toHaveLength(1);
    await userEvent.type(screen.getByLabelText("Reason"), "Demand withdrawn");
    await userEvent.click(screen.getByRole("button", { name: "Dismiss planned order" }));
    expect(await screen.findByText("Planned order dismissed: no production order is created for it.")).toBeInTheDocument();
    const [url, init] = calls(fetchMock)[1];
    expect(url).toBe(`/api/v1/production/planned-intake/${rowId}:dismiss`);
    expect((init?.headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(screen.getByText("Demand withdrawn")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Convert to production order" })).not.toBeInTheDocument();
  });

  it("shows a refused dismissal, a converted row's order link and a missing row", async () => {
    stubFetch((_url, init) => (init?.method === "POST" ? problem(409, "PRODUCTION_INTAKE_NOT_OPEN", "the planned order is already converted") : json(row())));
    const first = renderAt(path);
    await screen.findByRole("heading", { name: "Planned order of 40" });
    await userEvent.type(screen.getByLabelText("Reason"), "x");
    await userEvent.click(screen.getByRole("button", { name: "Dismiss planned order" }));
    expect(await screen.findByText("the planned order is already converted")).toBeInTheDocument();
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch((url) => (url.includes("/planned-intake/") ? json(row({ status: "converted", orderId })) : json(order())));
    const second = renderAt(path);
    await userEvent.click(await screen.findByRole("button", { name: "Open production order" }));
    expect(second.location.pathname).toBe(`/production/${orderId}`);
    second.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(404, "PRODUCTION_INTAKE_NOT_FOUND", "planned order intake not found"));
    renderAt(path);
    expect(await screen.findByText("This planned order does not exist.")).toBeInTheDocument();
  });
});

describe("convert a planned order (FR-22.10, FR-22.18)", () => {
  const path = `/production/planned-intake/${rowId}/convert`;
  const fill = (label: string, value: string, scope: HTMLElement = document.body) => fireEvent.change(within(scope).getByLabelText(label), { target: { value } });

  it("locks what the planned order fixes and converts with the BOM and route entered", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init?.method === "POST") return json(order(), 201);
      if (url.includes("/orders/")) return json(order());
      return json(row());
    });
    const { location } = renderAt(path);
    expect(await screen.findByRole("heading", { name: "Convert planned order" })).toBeInTheDocument();
    for (const label of ["Product", "Quantity", "Delivery date", "Site", "Warehouse"]) {
      expect(screen.getByLabelText(label)).toHaveAttribute("readonly");
    }
    expect(screen.getByLabelText("Product")).toHaveValue(uuid(2));
    expect(screen.getByLabelText("Warehouse")).toHaveValue(uuid(4));
    expect(screen.queryByLabelText("Order type")).not.toBeInTheDocument();

    const component = screen.getByRole("group", { name: "Component 1" });
    fill("Item", uuid(9), component);
    fill("Quantity per unit", "3", component);
    const operation = screen.getByRole("group", { name: "Operation 1" });
    fill("Resource", uuid(10), operation);
    fill("Run hours per unit", "0.5", operation);
    await userEvent.click(screen.getByRole("button", { name: "Create production order" }));
    await waitFor(() => expect(location.pathname).toBe(`/production/${orderId}`));
    const [url, init] = calls(fetchMock).find(([, i]) => i?.method === "POST") ?? ["", undefined];
    expect(url).toBe(`/api/v1/production/planned-intake/${rowId}:convert`);
    expect((init?.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/.+/);
    const body = JSON.parse(String(init?.body));
    expect(body).toMatchObject({ productId: uuid(2), siteId: uuid(3), warehouseId: uuid(4), quantity: "40", deliveryDate: "2026-11-20" });
    expect(body.bom).toEqual([{ lineNo: "10", itemId: uuid(9), qtyPer: "3" }]);
  });

  it("asks for the warehouse of a site scope planned order", async () => {
    stubFetch(() => json(row({ warehouseId: null })));
    renderAt(path);
    await screen.findByRole("heading", { name: "Convert planned order" });
    expect(screen.getByLabelText("Warehouse")).not.toHaveAttribute("readonly");
    expect(screen.getByLabelText("Warehouse")).toHaveValue("");
  });

  it("refuses a row that is already handled and reports a missing row", async () => {
    stubFetch(() => json(row({ status: "converted", orderId })));
    const first = renderAt(path);
    expect(await screen.findByText("This planned order is converted.")).toBeInTheDocument();
    first.unmount();

    vi.unstubAllGlobals();
    stubFetch(() => problem(404, "PRODUCTION_INTAKE_NOT_FOUND", "planned order intake not found"));
    renderAt(path);
    expect(await screen.findByText("planned order intake not found (PRODUCTION_INTAKE_NOT_FOUND)")).toBeInTheDocument();
  });

  it("is on the production menu behind the read duty", () => {
    expect(production.nav.find((item) => item.label === "Planned orders")).toEqual({ label: "Planned orders", to: "/production/planned-intake", duty: "production.order.read" });
  });
});
