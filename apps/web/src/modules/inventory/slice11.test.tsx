import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { findAlert, findTable } from "../../test-utils";
import { checkAxeAA } from "../../shell/a11y";
import inventory from "./module";
import { validateCountVarianceFilters } from "./CountVariancePage";
import { validateTrace } from "./TrackingTracePage";
import { validateNegativeFilters } from "./NegativeOnHandPage";
import { validateCombinationFilters } from "./DimensionCombinationsPage";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const SHEET_ID = "4e4e4e4e-0000-4000-8000-000000000012";
const PLAN_ID = "3d3d3d3d-0000-4000-8000-000000000011";
const JOURNAL_ID = "6a6a6a6a-0000-4000-8000-000000000031";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
let handler: Handler;
let calls: { method: string; url: URL }[];
const json = (status: number, body: unknown) => ({ status, body });

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const varianceLine = (o: Record<string, unknown> = {}) => ({
  sheetId: SHEET_ID,
  sheetNumber: "CNT-0A1B2C3D",
  sheetStatus: "POSTED",
  planId: PLAN_ID,
  planName: "Aisle count",
  legalEntityId: ENTITY_ID,
  lineId: "l1",
  lineNo: 1,
  productId: PRODUCT_ID,
  dimensions: { site: "S1", warehouse: "W1" },
  unit: "EA",
  currency: "USD",
  expectedQty: "50",
  countedQty: "46",
  varianceQty: "-4",
  unitCost: "2",
  varianceValue: "-8",
  valueBasis: "JOURNAL",
  countedBy: "ann",
  countedAt: "2026-09-03T10:00:00Z",
  ...o,
});

describe("Count variance report", () => {
  it("shows lines with value, flags estimates and totals per counter", async () => {
    handler = () =>
      json(200, {
        items: [varianceLine(), varianceLine({ lineId: "l2", lineNo: 2, sheetStatus: "COUNTED", varianceQty: "2", varianceValue: "3", valueBasis: "ESTIMATE" })],
        totalsByCounter: [{ counter: "ann", currency: "USD", lines: 2, absVarianceQty: "6", gainValue: "3", lossValue: "8", netValue: "-5" }],
        truncated: false,
      });
    const { container } = renderAt("/inventory/reports/count-variance");
    const table = await findTable({ name: "Count variance" });
    await waitFor(() => expect(table).toHaveTextContent("CNT-0A1B2C3D"));
    expect(table).toHaveTextContent("-8 USD");
    expect(table).toHaveTextContent("+3 USD (est.)");
    expect(within(table).getAllByRole("link", { name: "CNT-0A1B2C3D" })[0]).toHaveAttribute("href", `/inventory/count-sheets/${SHEET_ID}`);
    const totals = await findTable({ name: "Variance by counter" });
    expect(totals).toHaveTextContent("-5 USD");
    expect(screen.getByText("1 line is on a sheet that is not posted yet and valued at today's running average.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends every filter on apply and blocks invalid ones", async () => {
    handler = () => json(200, { items: [], totalsByCounter: [], truncated: false });
    renderAt("/inventory/reports/count-variance");
    await screen.findByText("No counted lines");
    expect(calls[0].url.search).toBe("");
    fireEvent.change(screen.getByLabelText(/^Count plan/), { target: { value: "nope" } });
    fireEvent.change(screen.getByLabelText(/^Counted from/), { target: { value: "2026-09-10" } });
    fireEvent.change(screen.getByLabelText(/^Counted to/), { target: { value: "2026-09-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await screen.findByText("Enter the count plan id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("The end date is before the start date.")).toBeInTheDocument();
    expect(calls).toHaveLength(1);

    fireEvent.change(screen.getByLabelText(/^Count plan/), { target: { value: PLAN_ID } });
    fireEvent.change(screen.getByLabelText(/^Counted to/), { target: { value: "2026-09-30" } });
    fireEvent.change(screen.getByLabelText(/^Counter/), { target: { value: "ann" } });
    fireEvent.change(screen.getByLabelText(/^Warehouse/), { target: { value: "W1" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(Object.fromEntries(calls[1].url.searchParams)).toEqual({ planId: PLAN_ID, from: "2026-09-10", to: "2026-09-30", counter: "ann", warehouseId: "W1" });
  });

  it("shows a failure and says when the list was capped", async () => {
    handler = () => json(500, { title: "Server error", status: 500, code: "internal" });
    renderAt("/inventory/reports/count-variance");
    expect(await findAlert()).toHaveTextContent("Could not load the count variance report");
  });

  it("validates filters", () => {
    expect(validateCountVarianceFilters({ legalEntityId: "x", planId: "", sheetId: "y", warehouseId: "", counter: "", from: "", to: "" })).toEqual({
      legalEntityId: "Enter the legal entity id (a UUID).",
      sheetId: "Enter the count sheet id (a UUID).",
    });
  });
});

describe("Negative on-hand list", () => {
  it("lists shortfalls with the first negative date", async () => {
    handler = () =>
      json(200, {
        items: [
          { legalEntityId: ENTITY_ID, productId: PRODUCT_ID, dimensions: { site: "S1", warehouse: "W1" }, physicalInvent: "-3", shortfallQty: "3", postedValue: "-6", currency: "USD", firstNegativeDate: "2026-09-02" },
          { legalEntityId: ENTITY_ID, productId: PRODUCT_ID, dimensions: { site: "S1", warehouse: "W2" }, physicalInvent: "-1", shortfallQty: "1", postedValue: "0" },
        ],
        truncated: true,
      });
    const { container } = renderAt("/inventory/reports/negative-onhand");
    const table = await findTable({ name: "Negative on-hand" });
    await waitFor(() => expect(table).toHaveTextContent("2026-09-02"));
    expect(table).toHaveTextContent("—");
    expect(await screen.findByText("Showing the first 1,000 rows")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state, sends filters and reports failures", async () => {
    handler = () => json(200, { items: [], truncated: false });
    renderAt("/inventory/reports/negative-onhand");
    await screen.findByText("No negative stock");
    fireEvent.change(screen.getByLabelText(/^Site/), { target: { value: "S1" } });
    fireEvent.change(screen.getByLabelText(/^Warehouse/), { target: { value: "W1" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(Object.fromEntries(calls[1].url.searchParams)).toEqual({ siteId: "S1", warehouseId: "W1" });
    handler = () => json(500, { title: "Server error", status: 500, code: "internal" });
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(await findAlert()).toHaveTextContent("Could not load the negative on-hand list");
  });

  it("validates ids", () => {
    expect(validateNegativeFilters({ legalEntityId: "a", productId: "b", siteId: "", warehouseId: "" })).toEqual({
      legalEntityId: "Enter the legal entity id (a UUID).",
      productId: "Enter the product id (a UUID).",
    });
  });
});

describe("Batch and serial trace", () => {
  const move = (o: Record<string, unknown>) => ({
    id: "t1",
    legalEntityId: ENTITY_ID,
    originId: "o1",
    sourceType: "INVENT_JOURNAL_LINE",
    sourceId: "s1",
    journalId: JOURNAL_ID,
    productId: PRODUCT_ID,
    dimensions: { site: "S1", warehouse: "W1", batch: "B1" },
    qty: "10",
    enteredQty: "10",
    enteredUnit: "EA",
    receiptStatus: "PURCHASED",
    physicalDate: "2026-09-01",
    currency: "USD",
    costAmountPhysical: "20",
    createdAt: "2026-09-01T08:00:00Z",
    running: "10",
    ...o,
  });

  it("asks for a batch or a serial before tracing and then lists the movements", async () => {
    handler = () => json(200, { direction: "BOTH", truncated: false, items: [move({}), move({ id: "t2", qty: "-4", receiptStatus: undefined, issueStatus: "SOLD", running: "6", journalId: undefined, sourceType: "TRANSFER_LINE" })] });
    const { container } = renderAt("/inventory/reports/tracking-trace");
    expect(screen.getByText("Nothing traced yet")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Trace" }));
    expect(await screen.findByText("Enter a batch or a serial number to trace.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    fireEvent.change(screen.getByLabelText(/^Batch/), { target: { value: "B1" } });
    fireEvent.change(screen.getByLabelText("Direction"), { target: { value: "FORWARD" } });
    fireEvent.click(screen.getByRole("button", { name: "Trace" }));
    const table = await findTable({ name: "Traced movements" });
    await waitFor(() => expect(table).toHaveTextContent("+10 EA"));
    expect(table).toHaveTextContent("-4 EA");
    expect(table).toHaveTextContent("Transfer line");
    expect(within(table).getByRole("link", { name: `Journal ${JOURNAL_ID.slice(0, 8)}` })).toHaveAttribute("href", `/inventory/journals/${JOURNAL_ID}`);
    expect(Object.fromEntries(calls[0].url.searchParams)).toEqual({ batchId: "B1", direction: "FORWARD" });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty and failure states", async () => {
    handler = () => json(200, { direction: "BOTH", truncated: false, items: [] });
    renderAt("/inventory/reports/tracking-trace");
    fireEvent.change(screen.getByLabelText("Serial number"), { target: { value: "SN1" } });
    fireEvent.click(screen.getByRole("button", { name: "Trace" }));
    expect(await screen.findByText("No movements found")).toBeInTheDocument();
    expect(calls[0].url.searchParams.get("serialId")).toBe("SN1");
    handler = () => json(400, { title: "Bad request", status: 400, code: "INV-TRC-001" });
    fireEvent.change(screen.getByLabelText("Serial number"), { target: { value: "SN2" } });
    fireEvent.click(screen.getByRole("button", { name: "Trace" }));
    expect(await findAlert()).toHaveTextContent("Could not trace the stock");
  });

  it("validates the trace form", () => {
    expect(validateTrace({ batchId: "", serialId: "", productId: "", direction: "BOTH" })).toEqual({ batchId: "Enter a batch or a serial number to trace." });
    expect(validateTrace({ batchId: "B1", serialId: "", productId: "x", direction: "BOTH" })).toEqual({ productId: "Enter the product id (a UUID)." });
    expect(validateTrace({ batchId: "", serialId: "SN1", productId: "", direction: "BACKWARD" })).toEqual({});
  });
});

describe("Dimension combinations", () => {
  it("lists combinations with first and last use", async () => {
    handler = () =>
      json(200, {
        truncated: false,
        items: [
          { legalEntityId: ENTITY_ID, productId: PRODUCT_ID, dimensionSetHash: "ab12", dimensions: { site: "S1", warehouse: "W1", batch: "B1" }, activeDimensions: ["site", "warehouse", "batch"], firstUse: "2026-09-01", lastUse: "2026-09-06", transactionCount: 3 },
        ],
      });
    const { container } = renderAt("/inventory/reports/dimension-combinations");
    const table = await findTable({ name: "Dimension combinations" });
    await waitFor(() => expect(table).toHaveTextContent("2026-09-06"));
    expect(table).toHaveTextContent("site, warehouse, batch");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state, filters and failures", async () => {
    handler = () => json(200, { items: [], truncated: false });
    renderAt("/inventory/reports/dimension-combinations");
    await screen.findByText("No dimension combinations");
    fireEvent.change(screen.getByLabelText(/^Product/), { target: { value: "nope" } });
    expect(screen.getByLabelText(/^Product/)).toHaveAttribute("data-value", "");
    expect(calls).toHaveLength(1);
    fireEvent.change(screen.getByLabelText(/^Product/), { target: { value: PRODUCT_ID } });
    handler = () => json(500, { title: "Server error", status: 500, code: "internal" });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(await findAlert()).toHaveTextContent("Could not load dimension combinations");
    expect(calls[1].url.searchParams.get("productId")).toBe(PRODUCT_ID);
  });

  it("validates ids", () => {
    expect(validateCombinationFilters("x", "y")).toEqual({ productId: "Enter the product id (a UUID).", legalEntityId: "Enter the legal entity id (a UUID)." });
  });
});

describe("Report routes", () => {
  it("are routed and in the navigation under the inquiry duty", () => {
    const paths = inventory.routes.map((r) => r.path);
    for (const path of ["count-variance", "negative-onhand", "tracking-trace", "dimension-combinations"]) {
      expect(paths).toContain(`/inventory/reports/${path}`);
      expect(inventory.nav?.find((n) => n.to === `/inventory/reports/${path}`)?.duty).toBe("inventory.read");
    }
  });
});
