import { findAlert, findTable } from "../../test-utils";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { resetResourceCache } from "../mfg-shared/resources";
import { initialValues, toInput, validateOrder, formKey } from "./CreateOrderPage";
import production from "./module";
import { json, logEntry, order, orderId, problem, summary } from "./testData";

beforeEach(() => resetResourceCache());
afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

/** A fetch stub that answers by method and path and records every call. */
function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

/**
 * Renders the module's routes inside a minimal stand-in for the shell frame
 * (skip link, nav, main, the one h1) so the in-repo AA checks judge the page.
 */
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

describe("Production orders list", () => {
  it("shows orders with status, remaining and planned cost", async () => {
    const fetchMock = stubFetch(() =>
      json({ items: [summary({ status: "Estimated", plannedCost: "5000" }), summary({ id: uuid(2), orderNumber: "PO-000002", status: "ReportedAsFinished", remaining: "40" })] }),
    );
    const { container } = renderAt("/production");
    const table = await findTable({ name: "Production orders" });
    await waitFor(() => expect(within(table).getByText("PO-000002")).toBeInTheDocument());
    expect(within(table).getByText("5000.00 USD")).toBeInTheDocument();
    expect(within(table).getByText("Reported as finished")).toBeInTheDocument();
    expect(within(table).getByText("40")).toBeInTheDocument();
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/production/orders?limit=50");
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("shows a loading skeleton while the request is pending", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    renderAt("/production");
    expect(await screen.findByRole("table", { name: "Production orders" })).toHaveAttribute("aria-busy", "true");
  });

  it("explains an empty list", async () => {
    stubFetch(() => json({ items: [] }));
    renderAt("/production");
    expect(await screen.findByRole("heading", { name: "No production orders yet" })).toBeInTheDocument();
  });

  it("shows the problem code when the list fails", async () => {
    stubFetch(() => problem(403, "AUTH-0003", "missing duty production.order.read"));
    renderAt("/production");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load production orders");
    expect(alert).toHaveTextContent("missing duty production.order.read");
  });

  it("filters by status", async () => {
    const fetchMock = stubFetch(() => json({ items: [] }));
    renderAt("/production");
    await screen.findByRole("heading", { name: "No production orders yet" });
    await userEvent.selectOptions(screen.getByLabelText("Status"), "Released");
    await waitFor(() => expect(fetchMock.mock.calls.at(-1)?.[0]).toBe("/api/v1/production/orders?limit=50&status=Released"));
    expect(await screen.findByRole("heading", { name: "No released orders" })).toBeInTheDocument();
  });

  it("loads the next page from the cursor", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("cursor=") ? json({ items: [summary({ id: uuid(9), orderNumber: "PO-000009" })] }) : json({ items: [summary()], nextCursor: orderId }),
    );
    renderAt("/production");
    await userEvent.click(await screen.findByRole("button", { name: "Load more orders" }));
    expect(await screen.findByText("PO-000009")).toBeInTheDocument();
    expect(screen.getByText("PO-000001")).toBeInTheDocument();
    expect(fetchMock.mock.calls[1][0]).toBe(`/api/v1/production/orders?limit=50&cursor=${orderId}`);
    expect(screen.queryByRole("button", { name: "Load more orders" })).not.toBeInTheDocument();
  });

  it("opens an order and the create form", async () => {
    stubFetch((url) => {
      if (url.includes("?")) {
        return json({ items: [summary()] });
      }
      if (url.endsWith("/status-log")) {
        return json({ items: [] });
      }
      return url.endsWith("/costs") ? json({ orderId, currency: "USD", totals: {}, total: "0", rows: [] }) : json(order());
    });
    const { location } = renderAt("/production");
    await userEvent.click(await screen.findByRole("button", { name: "PO-000001" }));
    await waitFor(() => expect(location.pathname).toBe(`/production/${orderId}`));
    cleanup();
    const second = renderAt("/production");
    await userEvent.click(await screen.findByRole("button", { name: "New production order" }));
    await waitFor(() => expect(second.location.pathname).toBe("/production/new"));
  });
});

describe("Production order detail", () => {
  function detailFetch(state: { order: ReturnType<typeof order> }, onCommand?: Handler) {
    return stubFetch((url, init) => {
      if (url.endsWith("/status-log")) {
        return json({ orderId, items: [logEntry()] });
      }
      if (url.endsWith("/costs")) {
        return json({ orderId, currency: "USD", totals: { material: "3950.00", labor: "1500.00" }, total: "5450.00", rows: [
          { group: "material", key: "RM-STEEL", qty: "315", amount: "3150", fixed: "0" },
        ] });
      }
      if (init?.method === "POST" && onCommand) {
        return onCommand(url, init);
      }
      return json(state.order);
    });
  }

  it("shows the header, components, route, planned cost and status log", async () => {
    detailFetch({ order: order({ status: "Estimated", version: 2 }) });
    const { container } = renderAt(`/production/${orderId}`);
    expect(await screen.findByRole("heading", { name: "Production order PO-000001" })).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Order components" })).getByText("30.1")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Order operations" })).getByText("WC-WELD")).toBeInTheDocument();
    const totals = await screen.findByRole("list", { name: "Planned cost by group" });
    expect(totals).toHaveTextContent("Material 3950.00 USD");
    expect(totals).toHaveTextContent("Total 5450.00 USD");
    const log = await findTable({ name: "Status log" });
    await waitFor(() => expect(within(log).getByText("trace-abc")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Schedule order" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Re-estimate" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel order" })).toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("shows the quota arrangement and source of a converted planned order", async () => {
    detailFetch({ order: order({ originType: "PlannedOrder", quotaArrangementId: "quota-arr-1", quotaSourceId: "quota-src-1" }) });
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("quota-arr-1")).toBeInTheDocument();
    expect(screen.getByText("quota-src-1")).toBeInTheDocument();
  });

  it("shows the released product the order's quantity rules come from", async () => {
    detailFetch({ order: order({ releasedProductId: "released-1" }) });
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("Released product")).toBeInTheDocument();
    expect(screen.getByText("released-1")).toBeInTheDocument();
  });

  it("omits the released product line on an order made without one", async () => {
    detailFetch({ order: order() });
    renderAt(`/production/${orderId}`);
    await findTable({ name: "Status log" });
    expect(screen.queryByText("Released product")).not.toBeInTheDocument();
  });

  it("applies a command with If-Match and an Idempotency-Key, then shows the new status", async () => {
    const state = { order: order({ status: "Released", version: 4 }) };
    const fetchMock = detailFetch(state, () => {
      state.order = order({ status: "Started", version: 5 });
      return json(state.order);
    });
    renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "Start" }));
    expect(await screen.findByText("Start applied. Status: Started.")).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(post?.[0]).toBe(`/api/v1/production/orders/${orderId}:start`);
    const headers = post?.[1]?.headers as Record<string, string>;
    expect(headers["If-Match"]).toBe('"4"');
    expect(headers["Idempotency-Key"]).toMatch(/.+/);
    expect(screen.getByRole("form", { name: "Report as finished" })).toBeInTheDocument();
  });

  it("shows a refused command with its code and keeps the order", async () => {
    detailFetch({ order: order({ status: "Scheduled", version: 3 }) }, () =>
      problem(409, "PRODUCTION_ILLEGAL_TRANSITION", "transition Scheduled via release is not allowed"),
    );
    renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "Release" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The command was refused");
    expect(alert).toHaveTextContent("PRODUCTION_ILLEGAL_TRANSITION");
    expect(screen.getByRole("heading", { name: "Production order PO-000001" })).toBeInTheDocument();
  });

  it("reuses the Idempotency-Key when the same command is retried on the same version", async () => {
    let attempt = 0;
    const fetchMock = detailFetch({ order: order({ status: "Created", version: 1 }) }, () => {
      attempt += 1;
      return attempt === 1 ? problem(503, "COM-0500", "internal error") : json(order({ status: "Estimated", version: 2 }));
    });
    renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "Estimate" }));
    await screen.findByText(/COM-0500/);
    await userEvent.click(screen.getByRole("button", { name: "Estimate" }));
    await screen.findByText("Estimate applied. Status: Estimated.");
    const keys = fetchMock.mock.calls
      .filter(([, init]) => init?.method === "POST")
      .map(([, init]) => (init?.headers as Record<string, string>)["Idempotency-Key"]);
    expect(keys).toHaveLength(2);
    expect(keys[0]).toBe(keys[1]);
  });

  it("reports as finished with quantities and validates them first", async () => {
    const fetchMock = detailFetch({ order: order({ status: "Started", version: 5 }) }, () =>
      json(order({ status: "ReportedAsFinished", version: 6, qtyRaf: "60", qtyError: "2" })),
    );
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Report as finished" });
    await userEvent.click(within(form).getByRole("button", { name: "Report as finished" }));
    expect(await within(form).findByText("Enter a positive quantity with up to 6 decimals.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);

    await userEvent.type(within(form).getByLabelText("Good quantity"), "60");
    await userEvent.type(within(form).getByLabelText("Error quantity"), "2");
    await userEvent.click(within(form).getByRole("button", { name: "Report as finished" }));
    expect(await screen.findByText("Report as finished applied. Status: Reported as finished.")).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(post?.[0]).toBe(`/api/v1/production/orders/${orderId}:report-finished`);
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ quantity: "60", errorQuantity: "2" });
  });

  it("resets to a chosen earlier status", async () => {
    const fetchMock = detailFetch({ order: order({ status: "Released", version: 4 }) }, () => json(order({ status: "Created", version: 5 })));
    renderAt(`/production/${orderId}`);
    await userEvent.selectOptions(await screen.findByLabelText("Reset to"), "Created");
    await userEvent.click(screen.getByRole("button", { name: "Reset status" }));
    await screen.findByText("Reset status applied. Status: Created.");
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(post?.[0]).toBe(`/api/v1/production/orders/${orderId}:reset-status`);
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ status: "Created" });
  });

  it("asks before cancelling, and sends nothing when the user keeps the order", async () => {
    const fetchMock = detailFetch({ order: order({ status: "Released", version: 4 }) }, () => json(order({ status: "Cancelled", version: 5 })));
    renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "Cancel order" }));
    expect(screen.getByText("A cancelled order cannot be reopened.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Keep it open" }));
    expect(screen.queryByText("A cancelled order cannot be reopened.")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false);

    await userEvent.click(screen.getByRole("button", { name: "Cancel order" }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, cancel the order" }));
    expect(await screen.findByText("Cancel order applied. Status: Cancelled.")).toBeInTheDocument();
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(post?.[0]).toBe(`/api/v1/production/orders/${orderId}:cancel`);
  });

  it("asks before ending an order", async () => {
    const fetchMock = detailFetch({ order: order({ status: "ReportedAsFinished", version: 6, qtyRaf: "100" }) }, () =>
      json(order({ status: "Ended", version: 7, qtyRaf: "100" })),
    );
    renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "End" }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, end the order" }));
    expect(await screen.findByText("End applied. Status: Ended.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
  });

  it("offers no actions on an ended order", async () => {
    detailFetch({ order: order({ status: "Ended", version: 9 }) });
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("This order is ended; no further actions apply.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reset status" })).not.toBeInTheDocument();
  });

  it("explains a missing order and a cost read without the duty", async () => {
    stubFetch(() => problem(404, "PRODUCTION_ORDER_NOT_FOUND", "production order not found"));
    const { location } = renderAt(`/production/${orderId}`);
    expect(await screen.findByRole("heading", { name: "We couldn't find that production order" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Back to production orders" }));
    await waitFor(() => expect(location.pathname).toBe("/production"));
  });

  it("keeps the order visible when costs are forbidden", async () => {
    stubFetch((url) => (url.endsWith("/costs") ? problem(403, "AUTH-0003", "missing duty production.cost.read") : url.endsWith("/status-log") ? json({ items: [] }) : json(order())));
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("You need the View production costs duty to see costs.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Production order PO-000001" })).toBeInTheDocument();
  });

  it("shows a loading state first", async () => {
    stubFetch(() => new Promise<Response>(() => undefined));
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("Loading production order…")).toBeInTheDocument();
  });
});

describe("New production order form", () => {
  const filled = () => {
    const v = initialValues();
    v.productId = uuid(1);
    v.siteId = uuid(2);
    v.warehouseId = uuid(3);
    v.quantity = "100";
    v.lines = [{ lineNo: "10", itemId: uuid(4), qtyPer: "3", scrapPct: "5", unitCost: "10" }];
    v.operations = [{ operNumber: "10", resourceId: uuid(5), setupTimeH: "", runTimeHPer: "0.5", laborRate: "30", overheadRate: "" }];
    return v;
  };

  it("validates like the service", () => {
    expect(validateOrder(filled())).toEqual({});
    const bad = filled();
    bad.productId = "FG-100";
    bad.quantity = "0";
    bad.currency = "usd";
    bad.lines = [...bad.lines, { lineNo: "10", itemId: "x", qtyPer: "", scrapPct: "100", unitCost: "1.1234567" }];
    bad.operations = [...bad.operations, { operNumber: "5", resourceId: uuid(6), setupTimeH: "", runTimeHPer: "", laborRate: "", overheadRate: "" }];
    expect(Object.keys(validateOrder(bad)).sort()).toEqual([
      "currency",
      "lines.1.itemId",
      "lines.1.lineNo",
      "lines.1.qtyPer",
      "lines.1.scrapPct",
      "lines.1.unitCost",
      "operations.1.operNumber",
      "productId",
      "quantity",
    ]);
    expect(validateOrder({ ...filled(), operations: [] })).toHaveProperty("operations");
  });

  it("maps the form to the CreateOrder body and server field paths back to the form", () => {
    expect(toInput(filled())).toEqual({
      productId: uuid(1),
      siteId: uuid(2),
      warehouseId: uuid(3),
      currency: "USD",
      quantity: "100",
      deliveryDate: undefined,
      bom: [{ lineNo: "10", itemId: uuid(4), qtyPer: "3", scrapPct: "5", unitCost: "10" }],
      operations: [{ operNumber: 10, resourceId: uuid(5), setupTimeH: undefined, runTimeHPer: "0.5", laborRate: "30", overheadRate: undefined }],
    });
    expect(formKey("bom[2].itemId")).toBe("lines.2.itemId");
    expect(formKey("operations[0].resourceId")).toBe("operations.0.resourceId");
    expect(formKey("quantity")).toBe("quantity");
  });

  it("shows field errors and sends nothing when the form is invalid", async () => {
    const fetchMock = stubFetch(() => json(order()));
    const { container } = renderAt("/production/new");
    await userEvent.click(await screen.findByRole("button", { name: "Create production order" }));
    expect(await screen.findByText("Enter a positive quantity with up to 6 decimals.")).toBeInTheDocument();
    expect(screen.getByLabelText("Product")).toHaveAttribute("aria-invalid", "true");
    expect(within(screen.getByRole("group", { name: "Operation 1" })).getByText("Choose a resource.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toEqual([]);
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("creates the order and opens it", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init?.method === "POST") {
        return json(order(), 201);
      }
      if (url.includes("/engineering/resources")) {
        return json({ items: [{ id: uuid(5), code: "CNC-1", name: "CNC mill" }] });
      }
      if (url.endsWith("/status-log")) {
        return json({ items: [] });
      }
      if (url.endsWith("/costs")) {
        return json({ orderId, currency: "USD", totals: {}, total: "0", rows: [] });
      }
      return json(order());
    });
    const { location } = renderAt("/production/new");
    const v = filled();
    const fill = (label: string, value: string, scope: HTMLElement = document.body) => fireEvent.change(within(scope).getByLabelText(label), { target: { value } });
    fill("Product", v.productId);
    fill("Quantity", "100");
    fill("Site", v.siteId);
    fill("Warehouse", v.warehouseId);
    await userEvent.click(screen.getByRole("button", { name: "Add component" }));
    const component = screen.getByRole("group", { name: "Component 1" });
    fill("Item", v.lines[0].itemId, component);
    fill("Quantity per unit", "3", component);
    await userEvent.selectOptions(within(component).getByLabelText("Flushing"), "finish");
    fill("Item group", "FG");
    const operation = screen.getByRole("group", { name: "Operation 1" });
    await within(operation).findByRole("option", { name: "CNC mill (CNC-1)" });
    await userEvent.selectOptions(within(operation).getByLabelText("Resource"), v.operations[0].resourceId);
    fill("Run hours per unit", "0.5", operation);
    await userEvent.click(screen.getByRole("button", { name: "Add component" }));
    expect(screen.getByRole("group", { name: "Component 2" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Remove component 2" }));

    await userEvent.click(screen.getByRole("button", { name: "Create production order" }));
    await waitFor(() => expect(location.pathname).toBe(`/production/${orderId}`));
    const [url, init] = fetchMock.mock.calls.find(([, i]) => i?.method === "POST")!;
    expect(url).toBe("/api/v1/production/orders");
    expect((init?.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/.+/);
    const body = JSON.parse(String(init?.body));
    expect(body.bom).toEqual([{ lineNo: "10", itemId: v.lines[0].itemId, qtyPer: "3", flushing: "finish" }]);
    expect(body.itemGroup).toBe("FG");
    expect(body.operations).toEqual([{ operNumber: 10, resourceId: v.operations[0].resourceId, runTimeHPer: "0.5" }]);
  });

  it("shows the service's refusal and its field errors", async () => {
    stubFetch((url) =>
      url.includes("/engineering/resources")
        ? json({ items: [{ id: uuid(5), code: "CNC-1", name: "CNC mill" }] })
        : problem(400, "COM-0001", "request failed validation", { "bom[0].itemId": "must be a UUID", legalEntityId: "must be the legal entity of the caller" }),
    );
    renderAt("/production/new");
    const v = filled();
    const set = (label: string, value: string, scope: HTMLElement = document.body) => fireEvent.change(within(scope).getByLabelText(label), { target: { value } });
    set("Product", v.productId);
    set("Quantity", "100");
    set("Site", v.siteId);
    set("Warehouse", v.warehouseId);
    await userEvent.click(screen.getByRole("button", { name: "Add component" }));
    set("Item", v.lines[0].itemId, screen.getByRole("group", { name: "Component 1" }));
    set("Quantity per unit", "3", screen.getByRole("group", { name: "Component 1" }));
    const op = screen.getByRole("group", { name: "Operation 1" });
    await within(op).findByRole("option", { name: "CNC mill (CNC-1)" });
    await userEvent.selectOptions(within(op).getByLabelText("Resource"), v.operations[0].resourceId);
    await userEvent.click(screen.getByRole("button", { name: "Create production order" }));
    const alerts = await screen.findAllByRole("alert");
    const summaryAlert = alerts.find((a) => a.textContent?.includes("The order was not created"));
    expect(summaryAlert).toHaveTextContent("legalEntityId");
    expect(within(screen.getByRole("group", { name: "Component 1" })).getByText("must be a UUID")).toBeInTheDocument();
  });
});
