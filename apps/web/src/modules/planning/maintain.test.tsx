import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ForecastLine, ForecastModel, PlannedOrder } from "./api/client";
import { validateCell } from "./ForecastPage";
import { validateOrderChange } from "./MaintainOrder";
import planning from "./module";

const order: PlannedOrder = {
  id: "00000000-0000-0000-0000-0000000000a2",
  versionId: "00000000-0000-0000-0000-0000000000b1",
  number: "PLN-000005",
  itemId: "00000000-0000-0000-0000-0000000000c1",
  orderType: "purchase",
  qty: "500",
  orderDate: "2026-11-02",
  deliveryDate: "2026-11-09",
  futuresDate: null,
  delayDays: 0,
  lotRule: "min",
  status: "unprocessed",
  version: 1,
};
const vendor = "00000000-0000-0000-0000-0000000000e1";
const pegging = { orderId: order.id, number: order.number, qty: "500", tree: { ref: order.number, refType: "planned-order", qty: "500", children: [] } };

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

type Handler = (url: string, init?: RequestInit) => Response;

function stubApi(...routes: [string, Handler][]) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    for (const [match, respond] of routes) {
      if (url.includes(match)) return respond(url, init);
    }
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

/** The order endpoint: GET returns `get`, PATCH and DELETE return `write`. */
function orderApi(get: () => Response, write: Handler = () => json(order)) {
  return stubApi(["/pegging", () => json(pegging)], ["/planned-orders/", (url, init) => (init?.method ? write(url, init) : get())]);
}

const callsOf = (fetchMock: ReturnType<typeof stubApi>, method: string) => fetchMock.mock.calls.filter(([, init]) => init?.method === method);
const ifMatchOf = (init?: RequestInit) => (init?.headers as Record<string, string>)["If-Match"];

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

describe("Planned order maintenance", () => {
  it("saves a changed quantity and vendor with the version as If-Match", async () => {
    const fetchMock = orderApi(
      () => json(order),
      () => json({ ...order, qty: "600", vendorId: vendor, version: 2 }),
    );
    renderAt(`/planning/planned-orders/${order.id}`);
    const form = await screen.findByRole("form", { name: "Change planned order" });
    fireEvent.change(within(form).getByLabelText("Quantity"), { target: { value: "600" } });
    fireEvent.change(within(form).getByLabelText(/Vendor/), { target: { value: vendor } });
    fireEvent.click(within(form).getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Changes saved."));
    const [patch] = callsOf(fetchMock, "PATCH");
    expect(patch[0]).toBe(`/api/v1/planning/planned-orders/${order.id}`);
    expect(ifMatchOf(patch[1])).toBe('"1"');
    expect(JSON.parse(patch[1]?.body as string)).toEqual({ qty: "600", vendorId: vendor });
    // The details show what the service returned.
    expect(screen.getAllByText("600").length).toBeGreaterThan(0);
    expect(screen.getAllByText(vendor).length).toBeGreaterThan(0);
  });

  it("approves an unprocessed order", async () => {
    const fetchMock = orderApi(
      () => json(order),
      () => json({ ...order, status: "approved", version: 2 }),
    );
    renderAt(`/planning/planned-orders/${order.id}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Order approved."));
    expect(JSON.parse(callsOf(fetchMock, "PATCH")[0][1]?.body as string)).toEqual({ status: "approved" });
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("checks the dates before calling the service", async () => {
    const fetchMock = orderApi(() => json(order));
    renderAt(`/planning/planned-orders/${order.id}`);
    const form = await screen.findByRole("form", { name: "Change planned order" });
    fireEvent.change(within(form).getByLabelText("Order date"), { target: { value: "2026-11-20" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The order date must not be after the delivery date.")).toBeInTheDocument();
    expect(callsOf(fetchMock, "PATCH")).toHaveLength(0);
  });

  it("offers a reload when someone else changed the order (412)", async () => {
    let gets = 0;
    const fetchMock = orderApi(
      () => json({ ...order, version: ++gets }),
      () => problem(412, "conflict_version", "the planned order was changed by another user; current version is 2"),
    );
    renderAt(`/planning/planned-orders/${order.id}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Someone else changed this order")).toBeInTheDocument();
    expect(screen.getByText(/conflict_version/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload order" }));
    await waitFor(() => expect(gets).toBe(2));
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    await waitFor(() => expect(callsOf(fetchMock, "PATCH")).toHaveLength(2));
    expect(ifMatchOf(callsOf(fetchMock, "PATCH")[1][1])).toBe('"2"');
  });

  it("deletes only after confirmation and returns to the list", async () => {
    const fetchMock = orderApi(
      () => json(order),
      () => new Response(null, { status: 204 }),
    );
    renderAt(`/planning/planned-orders/${order.id}?plan=MAIN`);
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    expect(callsOf(fetchMock, "DELETE")).toHaveLength(0);
    expect(screen.getByText("Delete planned order PLN-000005?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Delete order" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/planning/planned-orders?plan=MAIN"));
    expect(ifMatchOf(callsOf(fetchMock, "DELETE")[0][1])).toBe('"1"');
  });

  it("shows no form for a firmed order and no vendor for production", async () => {
    orderApi(() => json({ ...order, orderType: "production", status: "firmed" }));
    renderAt(`/planning/planned-orders/${order.id}`);
    expect(await screen.findByText("This planned order is firmed")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Change planned order" })).not.toBeInTheDocument();
  });

  it("validateOrderChange sends only changed fields and refuses an empty change", () => {
    const base = { qty: "500", orderDate: order.orderDate, deliveryDate: order.deliveryDate, vendorId: "" };
    expect(validateOrderChange(order, base).errors.form).toBe("Change at least one field before saving.");
    expect(validateOrderChange(order, { ...base, qty: "500.000000" }).errors.form).toBeDefined();
    expect(validateOrderChange(order, { ...base, qty: "0" }).errors.qty).toBeDefined();
    expect(validateOrderChange(order, { ...base, qty: "1.1234567" }).errors.qty).toBeDefined();
    expect(validateOrderChange(order, { ...base, vendorId: "ACME" }).errors.vendorId).toBe("Enter the vendor id as a UUID.");
    expect(validateOrderChange(order, { ...base, deliveryDate: "2026-11-12" }).patch).toEqual({ deliveryDate: "2026-11-12" });
    const production = { ...order, orderType: "production" as const };
    expect(validateOrderChange(production, { ...base, vendorId: "ignored", qty: "7" }).patch).toEqual({ qty: "7" });
  });
});

const model: ForecastModel = { code: "BASE", version: 3, reductionPrinciple: "transactions", reductionPercent: "0.000000" };
const cell: ForecastLine = {
  itemId: "00000000-0000-0000-0000-0000000000c1",
  siteId: "00000000-0000-0000-0000-0000000000d1",
  periodStart: "2026-11-02",
  periodEnd: "2026-11-08",
  source: "manual",
  qty: "100.000000",
  salesQty: "30.000000",
  reducedQty: "70.000000",
};

function fillCell(form: HTMLElement, values: Record<string, string>) {
  for (const [label, value] of Object.entries(values)) {
    fireEvent.change(within(form).getByLabelText(new RegExp(`^${label}`)), { target: { value } });
  }
}

describe("Forecast page", () => {
  it("lists the model's cells with their reduced forecast", async () => {
    const fetchMock = stubApi(["/forecasts/", () => json({ model, items: [cell] })]);
    renderAt("/planning/forecast");
    const table = await screen.findByRole("table", { name: "Forecast cells" });
    await waitFor(() => expect(within(table).getByText("70.000000")).toBeInTheDocument());
    expect(within(table).getByText("Manual")).toBeInTheDocument();
    // The model card shows the principle; the form's select offers it too.
    expect(screen.getByText("Transactions (forecast less sales in the period)", { selector: "dd" })).toBeInTheDocument();
    expect(screen.getByText("3", { selector: "dd" })).toBeInTheDocument();
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/planning/forecasts/BASE/lines?limit=500");
  });

  it("starts a new model at version 0 and saves the first cell", async () => {
    let created = false;
    const fetchMock = stubApi([
      "/forecasts/",
      (_url, init) => {
        if (init?.method === "PUT") {
          created = true;
          return json({ model: { ...model, version: 1 }, updated: 1 });
        }
        return created ? json({ model: { ...model, version: 1 }, items: [cell] }) : problem(404, "PLAN-FORECAST-NOT-FOUND", "forecast model not found");
      },
    ]);
    renderAt("/planning/forecast?model=BASE");
    expect(await screen.findByRole("heading", { name: "Forecast model BASE has no cells yet" })).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Forecast cell" });
    fillCell(form, { Item: cell.itemId, Site: cell.siteId, "Period start": "2026-11-02", "Period end": "2026-11-08", "Forecast quantity": "100", "Open sales quantity": "30" });
    fireEvent.click(within(form).getByRole("button", { name: "Save cell" }));

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Cell saved. Forecast model BASE is at version 1."));
    const put = callsOf(fetchMock, "PUT")[0];
    expect(ifMatchOf(put[1])).toBe('"0"');
    expect(JSON.parse(put[1]?.body as string)).toEqual({
      lines: [{ itemId: cell.itemId, siteId: cell.siteId, periodStart: "2026-11-02", periodEnd: "2026-11-08", qty: "100", salesQty: "30" }],
    });
    const table = await screen.findByRole("table", { name: "Forecast cells" });
    await waitFor(() => expect(within(table).getByText("70.000000")).toBeInTheDocument());
  });

  it("opens a row in the form and sends a principle change with it", async () => {
    const fetchMock = stubApi([
      "/forecasts/",
      (_url, init) => (init?.method === "PUT" ? json({ model: { ...model, version: 4, reductionPrinciple: "percent" }, updated: 1 }) : json({ model, items: [cell] })),
    ]);
    renderAt("/planning/forecast");
    const table = await screen.findByRole("table", { name: "Forecast cells" });
    await waitFor(() => expect(within(table).getByText("70.000000")).toBeInTheDocument());
    fireEvent.click(within(table).getByText("70.000000"));
    const form = screen.getByRole("form", { name: "Forecast cell" });
    expect(within(form).getByLabelText(/^Item/)).toHaveValue(cell.itemId);
    fireEvent.change(within(form).getByLabelText(/^Reduction principle/), { target: { value: "percent" } });
    fillCell(form, { "Reduction percent": "20", "Forecast quantity": "90" });
    fireEvent.click(within(form).getByRole("button", { name: "Save cell" }));
    await waitFor(() => expect(callsOf(fetchMock, "PUT")).toHaveLength(1));
    const put = callsOf(fetchMock, "PUT")[0];
    expect(ifMatchOf(put[1])).toBe('"3"');
    expect(JSON.parse(put[1]?.body as string)).toMatchObject({ reductionPrinciple: "percent", reductionPercent: "20", lines: [{ qty: "90", salesQty: "30.000000" }] });
  });

  it("keeps the form and shows the problem when the write is refused", async () => {
    stubApi([
      "/forecasts/",
      (_url, init) => (init?.method === "PUT" ? problem(412, "conflict_version", "the forecast model was changed by another user; current version is 4") : json({ model, items: [] })),
    ]);
    renderAt("/planning/forecast");
    expect(await screen.findByRole("heading", { name: "No forecast cells" })).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Forecast cell" });
    fillCell(form, { Item: cell.itemId, Site: cell.siteId, "Period start": "2026-11-02", "Period end": "2026-11-08", "Forecast quantity": "5" });
    fireEvent.click(within(form).getByRole("button", { name: "Save cell" }));
    expect(await screen.findByText("Someone else changed this forecast; reload it and try again")).toBeInTheDocument();
    expect(within(form).getByLabelText(/^Forecast quantity/)).toHaveValue("5");
  });

  it("shows the problem code when the model cannot be read", async () => {
    stubApi(["/forecasts/", () => problem(500, "COM-0500", "internal error")]);
    renderAt("/planning/forecast");
    expect(await screen.findByText("Could not load forecast model BASE")).toBeInTheDocument();
    expect(screen.getByText(/COM-0500/)).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Forecast cell" })).not.toBeInTheDocument();
  });

  it("validateCell checks the cell and only sends a changed principle", () => {
    const draft = { itemId: cell.itemId, siteId: cell.siteId, periodStart: "2026-11-08", periodEnd: "2026-11-02", qty: "-1", salesQty: "x", principle: "transactions" as const, percent: "" };
    const bad = validateCell(draft, model);
    expect(bad.body).toBeNull();
    expect(bad.errors).toMatchObject({ periodEnd: "The period cannot end before it starts.", qty: "Enter 0 or more, with at most 6 decimals.", salesQty: "Enter 0 or more, with at most 6 decimals." });
    const ok = validateCell({ ...draft, periodStart: "2026-11-02", periodEnd: "2026-11-08", qty: "0", salesQty: "" }, model);
    expect(ok.body).toEqual({ lines: [{ itemId: cell.itemId, siteId: cell.siteId, periodStart: "2026-11-02", periodEnd: "2026-11-08", qty: "0" }] });
    expect(validateCell({ ...draft, periodStart: "2026-11-02", periodEnd: "2026-11-08", qty: "1", principle: "percent", percent: "120" }, model).errors.percent).toBe(
      "Enter a percentage from 0 to 100.",
    );
  });
});
