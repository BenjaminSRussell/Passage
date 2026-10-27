import { findTable } from "../../test-utils";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { canEditBOM, editOrderBOM, type OrderEditEntry } from "./api/client";
import production from "./module";
import { describeImage, emptyEdit, toEdits } from "./OrderEditPanel";
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
  return render(
    <MemoryRouter initialEntries={[path]}>
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

const uuid = (n: number) => `0195d6a0-0000-7000-8000-${String(n).padStart(12, "0")}`;

const trailRow = (overrides: Partial<OrderEditEntry> = {}): OrderEditEntry => ({
  orderVersion: 3,
  seq: 1,
  target: "line",
  op: "change",
  key: "10",
  before: { lineNo: "10", itemId: "RM-STEEL", qtyPer: "3", seriesSize: "1", scrapPct: "5", roundUp: false, qtyRequired: "315", unitCost: "10" },
  after: { lineNo: "10", itemId: "RM-STEEL", qtyPer: "4", seriesSize: "1", scrapPct: "5", roundUp: false, qtyRequired: "420", unitCost: "10" },
  actor: "engineer-1",
  traceId: "trace-1",
  occurredAt: "2026-10-26T13:00:00Z",
  ...overrides,
});

/** Answers the detail page's reads; PATCH …/bom goes to onEdit. */
function detailFetch(state: { order: ReturnType<typeof order>; trail: OrderEditEntry[] }, onEdit?: Handler) {
  return stubFetch((url, init) => {
    if (url.endsWith("/bom-edits")) {
      return json({ orderId, items: state.trail });
    }
    if (url.endsWith("/status-log")) {
      return json({ orderId, items: [] });
    }
    if (url.endsWith("/costs")) {
      return json({ orderId, currency: "USD", totals: {}, total: "0", rows: [] });
    }
    if (url.includes("/journals") || url.includes("/as-built") || url.includes("?")) {
      return json({ items: [] });
    }
    if (init?.method === "PATCH" && onEdit) {
      return onEdit(url, init);
    }
    return json(state.order);
  });
}

describe("order BOM and route editor (FR-21.18)", () => {
  it("is offered from Created to Released only", () => {
    expect(canEditBOM("Created")).toBe(true);
    expect(canEditBOM("Released")).toBe(true);
    expect(canEditBOM("Started")).toBe(false);
    expect(canEditBOM("Ended")).toBe(false);
  });

  it("builds a change with only the fields filled in", () => {
    expect(toEdits("change-line", { ...emptyEdit, lineNo: "10", qtyPer: "4", roundUp: "yes" })).toEqual({
      edits: { lines: [{ op: "change", lineNo: "10", qtyPer: "4", roundUp: true }] },
      errors: {},
    });
    expect(toEdits("remove-operation", { ...emptyEdit, operNumber: "20", resourceId: "ignored" }).edits).toEqual({
      operations: [{ op: "remove", operNumber: 20 }],
    });
    expect(toEdits("change-line", { ...emptyEdit, lineNo: "10" }).errors.form).toBe("Fill in at least one value to change.");
  });

  it("checks an addition before sending it", () => {
    const line = toEdits("add-line", { ...emptyEdit, lineNo: "40", itemId: "paint", qtyPer: "1.1234567", scrapPct: "100", seriesSize: "0" });
    expect(line.edits).toBeUndefined();
    expect(Object.keys(line.errors).sort()).toEqual(["itemId", "qtyPer", "scrapPct", "seriesSize"]);
    const op = toEdits("add-operation", { ...emptyEdit, operNumber: "0" });
    expect(Object.keys(op.errors).sort()).toEqual(["operNumber", "resourceId"]);
  });

  it("describes audit images", () => {
    expect(describeImage(null)).toBe("—");
    expect(describeImage(trailRow().after)).toBe("RM-STEEL · 4 per unit · requires 420");
  });

  it("sends the edit with If-Match and shows the order and the history", async () => {
    const state = { order: order({ status: "Estimated", version: 2 }), trail: [] as OrderEditEntry[] };
    const fetchMock = detailFetch(state, () => {
      state.order = order({
        status: "Estimated",
        version: 3,
        orderBomModified: true,
        lines: [{ lineNo: "10", itemId: "RM-STEEL", qtyPer: "4", seriesSize: "1", scrapPct: "5", roundUp: false, qtyRequired: "420", unitCost: "10" }],
      });
      state.trail = [trailRow()];
      return json(state.order);
    });
    const { container } = renderAt(`/production/${orderId}`);
    const history = await findTable({ name: "Edit history" });
    await waitFor(() => expect(within(history).getByText("No edits yet")).toBeInTheDocument());

    const form = screen.getByRole("form", { name: "Edit the BOM or route" });
    await userEvent.selectOptions(within(form).getByLabelText("Line"), "10");
    await userEvent.type(within(form).getByLabelText(/Quantity per unit/), "4");
    await userEvent.click(within(form).getByRole("button", { name: "Save the edit" }));

    expect(await screen.findByText(/saved\. The order is now version 3\./)).toBeInTheDocument();
    const patch = calls(fetchMock).find(([, init]) => init?.method === "PATCH");
    expect(patch?.[0]).toBe(`/api/v1/production/orders/${orderId}/bom`);
    expect((patch?.[1]?.headers as Record<string, string>)["If-Match"]).toBe('"2"');
    expect(JSON.parse(String(patch?.[1]?.body))).toEqual({ lines: [{ op: "change", lineNo: "10", qtyPer: "4" }] });
    expect(screen.getByText("BOM edited")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Order components" })).getByText("420")).toBeInTheDocument();
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Edit history" })).getByText("Changed line 10")).toBeInTheDocument());
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("adds an operation with the fields filled in", async () => {
    const state = { order: order({ status: "Created", version: 1 }), trail: [] as OrderEditEntry[] };
    const fetchMock = detailFetch(state, () => json({ ...state.order, version: 2 }));
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Edit the BOM or route" });
    await userEvent.selectOptions(within(form).getByLabelText("Change"), "add-operation");
    await userEvent.type(within(form).getByLabelText("Operation number"), "20");
    await userEvent.type(within(form).getByLabelText("Resource"), uuid(9));
    await userEvent.type(within(form).getByLabelText(/Run hours per unit/), "0.25");
    await userEvent.click(within(form).getByRole("button", { name: "Save the edit" }));
    await waitFor(() => expect(calls(fetchMock).some(([, init]) => init?.method === "PATCH")).toBe(true));
    const patch = calls(fetchMock).find(([, init]) => init?.method === "PATCH");
    expect(JSON.parse(String(patch?.[1]?.body))).toEqual({ operations: [{ op: "add", operNumber: 20, resourceId: uuid(9), runTimeHPer: "0.25" }] });
  });

  it("shows field errors without calling the service", async () => {
    const state = { order: order({ status: "Created", version: 1 }), trail: [] as OrderEditEntry[] };
    const fetchMock = detailFetch(state);
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Edit the BOM or route" });
    await userEvent.selectOptions(within(form).getByLabelText("Change"), "add-line");
    await userEvent.click(within(form).getByRole("button", { name: "Save the edit" }));
    expect(await within(form).findByText("Enter the item to add.")).toBeInTheDocument();
    expect(within(form).getByText("Enter a line number of up to 20 characters.")).toBeInTheDocument();
    expect(calls(fetchMock).some(([, init]) => init?.method === "PATCH")).toBe(false);
  });

  it("shows a refused edit with its code", async () => {
    const state = { order: order({ status: "Released", version: 4 }), trail: [] as OrderEditEntry[] };
    detailFetch(state, () => problem(403, "AUTH-0003", 'editing the BOM or route of a Released order requires the duty "Maintain order BOM after release"'));
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText(/A released order also needs the Maintain order BOM after release duty/)).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Edit the BOM or route" });
    await userEvent.selectOptions(within(form).getByLabelText("Change"), "remove-line");
    await userEvent.selectOptions(within(form).getByLabelText("Line"), "30.1");
    await userEvent.click(within(form).getByRole("button", { name: "Remove the line" }));
    const alert = await screen.findByText(/The edit was refused/);
    expect(alert.closest("[role=alert]")).toHaveTextContent("AUTH-0003");
  });

  it("is not offered once the order is started, and shows a history load failure", async () => {
    detailFetch({ order: order({ status: "Started", version: 5 }), trail: [] });
    renderAt(`/production/${orderId}`);
    expect(await screen.findByRole("heading", { name: "Production order PO-000001" })).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Edit the BOM or route" })).not.toBeInTheDocument();

    vi.unstubAllGlobals();
    stubFetch((url) => (url.endsWith("/bom-edits") ? problem(500, "COM-0500", "internal error") : json(url.includes("?") ? { items: [] } : order({ status: "Created" }))));
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("Could not load the edit history")).toBeInTheDocument();
  });

  it("calls PATCH …/bom from the client", async () => {
    const fetchMock = stubFetch(() => json(order({ version: 2 })));
    await editOrderBOM("id 1", 1, { lines: [{ op: "remove", lineNo: "10" }] });
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/orders/id%201/bom");
  });
});
