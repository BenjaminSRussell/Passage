import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import type { BatchOrder, Formula } from "./api/client";
import { canRescale } from "./BatchOrderPages";
import { percentTotal, validateFormula } from "./FormulaPages";
import { sumPercent } from "./format";
import processmfg from "./module";

const FORMULA = "2f000000-0000-4000-8000-000000000001";
const ORDER = "b0000000-0000-4000-8000-000000000001";
const ITEM = "1a000000-0000-4000-8000-000000000001";
const FLOUR = "1a000000-0000-4000-8000-000000000002";
const SUGAR = "1a000000-0000-4000-8000-000000000003";
const PACK = "1a000000-0000-4000-8000-000000000004";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

function json(status: number, body: unknown): Reply {
  return { status, body };
}

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {processmfg.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

function header(call: { init: RequestInit }, name: string): string | null {
  return new Headers(call.init.headers).get(name);
}

// AC-24.1: a 1,000 kg formula with a 50 kg non-scalable line.
const formula: Formula = {
  id: FORMULA,
  code: "DOUGH-1",
  name: "Bread dough",
  itemId: ITEM,
  bomVersionId: null,
  size: "1000",
  unit: "kg",
  version: 1,
  lineCount: 3,
  createdAt: "2026-10-20T08:00:00Z",
  lines: [
    { lineNumber: 1, itemId: FLOUR, type: "ingredient", qty: "600", percent: "0", scalable: true, percentControlled: false },
    { lineNumber: 2, itemId: SUGAR, type: "ingredient", qty: "350", percent: "0", scalable: true, percentControlled: false },
    { lineNumber: 3, itemId: PACK, type: "ingredient", qty: "50", percent: "0", scalable: false, percentControlled: false },
  ],
};

const order: BatchOrder = {
  id: ORDER,
  legalEntityId: "1e000000-0000-4000-8000-000000000001",
  orderNumber: "BO-000001",
  formulaId: FORMULA,
  formulaCode: "DOUGH-1",
  formulaVersion: 1,
  itemId: ITEM,
  unit: "kg",
  formulaSize: "1000",
  batchSize: "2500",
  scaleFactor: "2.5",
  status: "planned",
  version: 1,
  createdBy: "planner-1",
  createdAt: "2026-10-21T08:00:00Z",
  updatedAt: "2026-10-21T08:00:00Z",
  lines: [
    { lineNumber: 1, itemId: FLOUR, type: "ingredient", formulaQty: "600", qty: "1500", percent: "0", scalable: true, percentControlled: false },
    { lineNumber: 2, itemId: SUGAR, type: "ingredient", formulaQty: "350", qty: "875", percent: "0", scalable: true, percentControlled: false },
    { lineNumber: 3, itemId: PACK, type: "ingredient", formulaQty: "50", qty: "50", percent: "0", scalable: false, percentControlled: false },
  ],
};

describe("processmfg module", () => {
  it("is auto-discovered and routes every nav entry", () => {
    expect(processmfg.id).toBe("processmfg");
    expect(loadModules().map((m) => m.id)).toContain("processmfg");
    const paths = processmfg.routes.map((r) => r.path);
    for (const n of processmfg.nav) {
      expect(n.to.startsWith("/processmfg")).toBe(true);
      expect(paths).toContain(n.to);
    }
    expect(processmfg.routes.every((r) => r.element)).toBe(true);
  });

  it("links every area from the overview", () => {
    const { container } = renderAt("/processmfg");
    const areas = screen.getByRole("list", { name: "Process manufacturing areas" });
    expect(within(areas).getByRole("link", { name: "Open batch orders" })).toHaveAttribute("href", "/processmfg/batch-orders");
    expect(within(areas).getAllByRole("link")).toHaveLength(15);
    expect(within(areas).getByRole("link", { name: "Open bulk consolidation" })).toHaveAttribute("href", "/processmfg/consolidation");
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("formulas", () => {
  it("lists formulas and opens one", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/production/formulas") return json(200, { items: [{ ...formula, lines: undefined }] });
      if (url.pathname === `/api/v1/production/formulas/${FORMULA}`) return json(200, formula);
      return undefined;
    };
    const { container } = renderAt("/processmfg/formulas");
    const table = await findTable({ name: "Formulas" });
    expect(within(table).getByText("1000 kg")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "DOUGH-1" }));
    expect(await screen.findByRole("heading", { name: "DOUGH-1" })).toBeInTheDocument();
  });

  it("shows the empty state and a retry after a failure", async () => {
    let fail = true;
    handler = (url) => {
      if (url.pathname !== "/api/v1/production/formulas") return undefined;
      return fail ? json(503, { code: "COM-0500", detail: "service unavailable" }) : json(200, { items: [] });
    };
    renderAt("/processmfg/formulas");
    expect(await findAlert()).toHaveTextContent("service unavailable");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No formulas yet")).toBeInTheDocument();
  });

  it("checks percent-controlled lines sum to 100 exactly (FR-24.1, AC-24.9)", () => {
    const lines = [
      { itemId: FLOUR, type: "ingredient" as const, qty: "", percent: "59.5", scalable: true, percentControlled: true },
      { itemId: SUGAR, type: "ingredient" as const, qty: "", percent: "40", scalable: true, percentControlled: true },
      { itemId: PACK, type: "ingredient" as const, qty: "50", percent: "", scalable: false, percentControlled: false },
    ];
    expect(percentTotal(lines)).toBe("99.5000");
    const values = { code: "MIX-1", name: "Mix", itemId: ITEM, size: "1000", unit: "kg", lines };
    expect(validateFormula(values).errors.lines).toBe("Percent-controlled lines sum to 99.5000%, not 100%.");
    lines[0].percent = "60";
    const ok = validateFormula(values);
    expect(ok.input?.lines.map((l) => [l.lineNumber, l.percent, l.qty])).toEqual([
      [1, "60", "0"],
      [2, "40", "0"],
      [3, "0", "50"],
    ]);
    expect(sumPercent(["33.3333", "33.3333", "33.3334"])).toBe("100.0000");
  });

  it("creates a formula with an Idempotency-Key and opens it", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/production/formulas" && init.method === "POST") return json(201, formula);
      if (url.pathname === `/api/v1/production/formulas/${FORMULA}`) return json(200, formula);
      return undefined;
    };
    const { container } = renderAt("/processmfg/formulas/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "dough-1" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Bread dough" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: FLOUR } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "1000" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create formula" }));
    expect(await screen.findByRole("heading", { name: "DOUGH-1" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(post.body).toMatchObject({ code: "DOUGH-1", size: "1000", unit: "kg", lines: [{ lineNumber: 1, itemId: FLOUR, qty: "1000", scalable: true }] });
  });

  it("shows the running percent total and refuses an unbalanced formula without calling the service", () => {
    renderAt("/processmfg/formulas/new");
    fireEvent.click(screen.getByLabelText("Percent-controlled"));
    fireEvent.change(screen.getByLabelText("Percent"), { target: { value: "99.5" } });
    expect(screen.getByText("Percent-controlled total: 99.5000%")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create formula" }));
    expect(screen.getByText("Percent-controlled lines sum to 99.5000%, not 100%.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("previews scaling side by side (AC-24.1)", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/production/formulas/${FORMULA}`) return json(200, formula);
      if (url.pathname === `/api/v1/production/formulas/${FORMULA}:scale` && init.method === "POST") {
        return json(200, {
          batchSize: "2500",
          scaleFactor: "2.5",
          lines: order.lines!.map(({ lineNumber, itemId, type, formulaQty, qty }) => ({ lineNumber, itemId, type, formulaQty, qty })),
        });
      }
      return undefined;
    };
    const { container } = renderAt(`/processmfg/formulas/${FORMULA}`);
    expect(await findTable({ name: "Formula lines" })).toHaveTextContent("Fixed");
    fireEvent.change(screen.getByLabelText("Batch size (kg)"), { target: { value: "2500" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview scaling" }));
    const scaled = await findTable({ name: "Scaled lines" });
    expect(within(scaled).getByText("1500")).toBeInTheDocument();
    expect(within(scaled).getAllByText("50")).toHaveLength(2);
    expect(screen.getByRole("status")).toHaveTextContent("Scale factor 2.5 for 2500 kg.");
    expect(calls.at(-1)?.body).toEqual({ batchSize: "2500" });
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("batch orders", () => {
  it("lists orders and filters by status", async () => {
    handler = (url) => {
      if (url.pathname !== "/api/v1/production/batch-orders") return undefined;
      return url.searchParams.get("status") === "released" ? json(200, { items: [] }) : json(200, { items: [{ ...order, lines: undefined }] });
    };
    const { container } = renderAt("/processmfg/batch-orders");
    const table = await findTable({ name: "Batch orders" });
    expect(within(table).getByText("2500 kg")).toBeInTheDocument();
    expect(within(table).getByText("DOUGH-1 v1")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "released" } });
    expect(await screen.findByText("No batch orders with this status")).toBeInTheDocument();
    expect(calls.at(-1)?.url.searchParams.get("status")).toBe("released");
  });

  it("creates an order from the formula picked on the formula page", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/production/formulas") return json(200, { items: [formula] });
      if (url.pathname === "/api/v1/production/batch-orders" && init.method === "POST") return json(201, order);
      if (url.pathname === `/api/v1/production/batch-orders/${ORDER}`) return json(200, order);
      return undefined;
    };
    renderAt(`/processmfg/batch-orders/new?formulaId=${FORMULA}`);
    await screen.findByRole("option", { name: "DOUGH-1 — Bread dough (1000 kg)" });
    expect(screen.getByLabelText("Formula")).toHaveValue(FORMULA);
    fireEvent.click(screen.getByRole("button", { name: "Create batch order" }));
    expect(await screen.findByText("Enter a positive batch size with at most 6 decimals.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Batch size (kg)"), { target: { value: "2500" } });
    fireEvent.click(screen.getByRole("button", { name: "Create batch order" }));
    expect(await screen.findByRole("heading", { name: "BO-000001" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ formulaId: FORMULA, batchSize: "2500" });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
  });

  it("rescales a planned order with its version", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/production/batch-orders/${ORDER}`) return json(200, order);
      if (url.pathname === `/api/v1/production/batch-orders/${ORDER}:rescale` && init.method === "POST") {
        return json(200, { ...order, batchSize: "3000", scaleFactor: "3", version: 2 });
      }
      return undefined;
    };
    const { container } = renderAt(`/processmfg/batch-orders/${ORDER}`);
    expect(await findTable({ name: "Batch order lines" })).toHaveTextContent("875");
    expect(screen.getByRole("list", { name: "Batch order facts" })).toHaveTextContent("Scale factor 2.5");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("New batch size (kg)"), { target: { value: "3000" } });
    fireEvent.click(screen.getByRole("button", { name: "Rescale batch" }));
    expect(await screen.findByText("Rescaled to 3000 kg.")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Batch order facts" })).toHaveTextContent("Scale factor 3");
    expect(calls.at(-1)?.body).toEqual({ batchSize: "3000", version: 1 });
  });

  it("offers a reload when the order changed meanwhile", async () => {
    handler = (url) => {
      if (url.pathname === `/api/v1/production/batch-orders/${ORDER}`) return json(200, order);
      if (url.pathname.endsWith(":rescale")) return json(409, { code: "PMF-0104", detail: "batch order BO-000001 is at version 2, not 1" });
      return undefined;
    };
    renderAt(`/processmfg/batch-orders/${ORDER}`);
    fireEvent.change(await screen.findByLabelText("New batch size (kg)"), { target: { value: "3000" } });
    fireEvent.click(screen.getByRole("button", { name: "Rescale batch" }));
    expect(await screen.findByText("batch order BO-000001 is at version 2, not 1")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload order" })).toBeInTheDocument();
  });

  it("does not offer rescale once the order has started", async () => {
    handler = (url) => (url.pathname === `/api/v1/production/batch-orders/${ORDER}` ? json(200, { ...order, status: "started" }) : undefined);
    renderAt(`/processmfg/batch-orders/${ORDER}`);
    expect(await screen.findByText("The order is started; only planned or released orders can be rescaled.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rescale batch" })).toBeNull();
    expect(canRescale("released")).toBe(true);
    expect(canRescale("reported")).toBe(false);
  });

  it("shows a not-found order as an error", async () => {
    handler = () => json(404, { code: "PMF-0102", detail: "batch order not found" });
    renderAt(`/processmfg/batch-orders/${ORDER}`);
    expect(await findAlert()).toHaveTextContent("batch order not found");
  });
});

describe("calculators", () => {
  it("shows potency consumption for the BR-24.2 example", async () => {
    handler = (url) => (url.pathname === "/api/v1/production/potency:consume" ? json(200, { qty: "125", compensatingQty: "75" }) : undefined);
    const { container } = renderAt("/processmfg/potency");
    fireEvent.click(screen.getByRole("button", { name: "Calculate consumption" }));
    const tiles = await screen.findByRole("list", { name: "Consumption" });
    expect(tiles).toHaveTextContent("Active ingredient 125");
    expect(tiles).toHaveTextContent("Filler 75");
    expect(calls[0].body).toEqual({ planned: "100", standardPotency: "50", batchPotency: "40", compensatingPlanned: "100" });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the PMF-0002 refusal", async () => {
    handler = () => json(422, { code: "PMF-0002", detail: "compensating line would become negative (-25.000000)" });
    renderAt("/processmfg/potency");
    fireEvent.click(screen.getByRole("button", { name: "Calculate consumption" }));
    expect(await findAlert()).toHaveTextContent("compensating line would become negative (-25.000000)");
    expect(screen.queryByRole("list", { name: "Consumption" })).toBeNull();
  });

  it("previews the BR-24.3 allocation", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/production/co-products:allocate"
        ? json(200, {
            currency: "USD",
            main: { itemId: "A", amount: "7940.0000", percent: "80.0000" },
            coProducts: [{ itemId: "B", amount: "1985.0000", percent: "20.0000" }],
            byProductBurdens: [{ itemId: "C", amount: "75.0000", percent: "0" }],
            total: "10000.0000",
          })
        : undefined;
    const { container } = renderAt("/processmfg/allocation");
    fireEvent.click(screen.getByRole("button", { name: "Preview allocation" }));
    const table = await findTable({ name: "Allocated cost" });
    expect(within(table).getByText("7940.0000")).toBeInTheDocument();
    expect(within(table).getByText("By-product burden")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Total 10000.0000 USD");
    expect(calls[0].body).toMatchObject({
      batchCost: "10000.00",
      method: "tca",
      main: { itemId: "A", rafQty: "200", standardCost: "40" },
      byProducts: [{ itemId: "C", costType: "per-quantity", qty: "50", rate: "1.50" }],
    });
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });
});

describe("batch rules (slice 2)", () => {
  it("derives potency for AC-24.11 and sends the senders as typed", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/production/batch-derivation:preview"
        ? json(200, { attribute: "potency", value: { num: "43" }, missingSenders: [] })
        : undefined;
    const { container } = renderAt("/processmfg/derivation");
    fireEvent.click(screen.getByRole("button", { name: "Preview derivation" }));
    const tiles = await screen.findByRole("list", { name: "Derived value" });
    expect(tiles).toHaveTextContent("potency 43");
    expect(calls[0].body).toEqual({
      rule: { attribute: "potency", aggregation: "weighted-average" },
      senders: [
        { batchId: "B-101", value: { num: "40" }, qty: "200" },
        { batchId: "B-102", value: { num: "45" }, qty: "300" },
      ],
    });
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });

  it("warns about a sender without a value (PMF-0007) and validates input before calling", async () => {
    handler = () => json(200, { attribute: "potency", value: { num: "45" }, missingSenders: ["B-101"], warningCode: "PMF-0007" });
    renderAt("/processmfg/derivation");
    fireEvent.change(screen.getByLabelText("Consumed quantity", { selector: "#sender-0-qty" }), { target: { value: "abc" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview derivation" }));
    expect(await screen.findByText("Enter a quantity with at most 6 decimals.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Consumed quantity", { selector: "#sender-0-qty" }), { target: { value: "200" } });
    fireEvent.change(screen.getByLabelText("Value", { selector: "#sender-0-value" }), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview derivation" }));
    expect(await screen.findByText("No potency recorded on B-101. Record it and derive again.")).toBeInTheDocument();
    expect((calls[0].body as { senders: { value: unknown }[] }).senders[0].value).toEqual({});
  });

  it("previews the AC-24.12 merge and shows the PMF-0006 refusal", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/production/batch-merge:preview"
        ? json(200, { qty: "150", unitCost: "2", disposition: "available", values: { expiry: { date: "2027-02-01" } }, parents: ["B-201", "B-202"] })
        : undefined;
    const { container } = renderAt("/processmfg/merge");
    fireEvent.click(screen.getByRole("button", { name: "Preview merge" }));
    const table = await findTable({ name: "Target attributes" });
    expect(within(table).getByText("2027-02-01")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Target batch" })).toHaveTextContent("Quantity 150");
    expect(screen.getByRole("status")).toHaveTextContent("Genealogy parents: B-201, B-202");
    const body = calls[0].body as { batches: { id: string; values: unknown }[]; mergeOverride: string[] };
    expect(body.batches[1]).toMatchObject({ id: "B-202", qty: "50", values: { expiry: { date: "2027-02-01" } } });
    expect(body.mergeOverride).toEqual([]);
    await waitFor(() => expect(pageViolations(container)).toEqual([]));

    handler = () => json(422, { code: "PMF-0006", detail: "merge refused: attribute grade differs (A vs B)" });
    fireEvent.click(screen.getByLabelText("Grade may differ (merge override)"));
    fireEvent.click(screen.getByRole("button", { name: "Preview merge" }));
    expect(await findAlert()).toHaveTextContent("merge refused: attribute grade differs (A vs B)");
    expect((calls[1].body as { mergeOverride: string[] }).mergeOverride).toEqual(["grade"]);
  });

  it("checks the AC-24.14 customer requirement and lists excluded batches", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/production/batch-requirements:check"
        ? json(200, {
            requirement: { sellableDays: 60, sameBatch: false, ranges: {} },
            qualifying: [{ batchId: "B-302", qty: "100", expiry: "2027-04-01" }],
            excluded: [{ batchId: "B-301", reason: "45 days of shelf life left at shipment, 60 required", warningCode: "PMF-0009" }],
            qtyAvailable: "100",
            sameBatchId: null,
          })
        : undefined;
    const { container } = renderAt("/processmfg/batch-requirements");
    fireEvent.click(screen.getByRole("button", { name: "Check batches" }));
    const excluded = await findTable({ name: "Excluded batches" });
    expect(within(excluded).getByText("45 days of shelf life left at shipment, 60 required")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Qualifying batches" })).getByText("B-302")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Requirement check" })).toHaveTextContent("Same batch Not required");
    expect(calls[0].body).toMatchObject({ shipDate: "2027-01-01", qty: "80", requirements: [{ level: 3, sellableDays: 60 }] });
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });

  it("shows the same-batch shortfall (409 PMF-0008)", async () => {
    handler = () => json(409, { code: "PMF-0008", detail: "no single batch can supply 500 (largest 100)" });
    renderAt("/processmfg/batch-requirements");
    fireEvent.click(screen.getByLabelText("Same batch"));
    fireEvent.click(screen.getByRole("button", { name: "Check batches" }));
    expect(await findAlert()).toHaveTextContent("no single batch can supply 500 (largest 100)");
    expect(calls[0].body).toMatchObject({ requirements: [{ level: 3, sellableDays: 60, sameBatch: true }] });
  });

  it("previews AC-24.13 consolidation with pegged packing orders", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/production/bulk-consolidation:preview"
        ? json(200, {
            bulkOrders: [
              {
                qty: "500",
                pegs: [
                  { orderId: "PK-1", itemId: "BOTTLE-500G", bulkQty: "200" },
                  { orderId: "PK-2", itemId: "BOTTLE-500G", bulkQty: "300" },
                ],
              },
            ],
          })
        : undefined;
    const { container } = renderAt("/processmfg/consolidation");
    fireEvent.click(screen.getByRole("button", { name: "Preview consolidation" }));
    const table = await findTable({ name: "Pegged packing orders" });
    expect(within(table).getAllByText("Bulk 1 (500)")).toHaveLength(2);
    expect(screen.getByRole("list", { name: "Consolidation" })).toHaveTextContent("Bulk orders 1");
    expect(calls[0].body).toEqual({
      relation: { packQtyPerUnit: "0.5", multiple: "100", max: "0" },
      packedOrders: [
        { orderId: "PK-1", itemId: "BOTTLE-500G", units: "400", due: "2027-01-05" },
        { orderId: "PK-2", itemId: "BOTTLE-500G", units: "600", due: "2027-01-06" },
      ],
    });
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });

  it("refuses EBR approval with open exceptions (PMF-0014) and confirms when signed", async () => {
    handler = () => json(422, { code: "PMF-0014", detail: "batch record cannot be approved: 1 exceptions without disposition and signature" });
    const { container } = renderAt("/processmfg/batch-record-check");
    fireEvent.click(screen.getByRole("button", { name: "Check approval" }));
    expect(await findAlert()).toHaveTextContent("PMF-0014");
    handler = () => json(200, { approvable: true, exceptions: 1 });
    fireEvent.change(screen.getByLabelText("Disposition"), { target: { value: "accepted" } });
    fireEvent.change(screen.getByLabelText("Signature"), { target: { value: "SIG-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Check approval" }));
    expect(await findStatus()).toHaveTextContent("All 1 exceptions are dispositioned and signed.");
    expect(calls[1].body).toEqual({ exceptions: [{ id: "EXC-expired-batch", disposition: "accepted", signatureId: "SIG-1" }] });
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });
});

describe("batch attributes, picking and rework (slice 3)", () => {
  it("checks recorded values and flags the one outside its limits (FR-24.5)", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/production/batch-attributes:check"
        ? json(200, {
            results: [
              { attribute: "grade", pass: true },
              { attribute: "potency", pass: false, detail: "potency 38.5 is below the minimum 40" },
            ],
            exceptions: 1,
          })
        : undefined;
    const { container } = renderAt("/processmfg/batch-attributes");
    fireEvent.click(screen.getByRole("button", { name: "Check values" }));
    const table = await findTable({ name: "Attribute results" });
    expect(within(table).getByText("potency 38.5 is below the minimum 40")).toBeInTheDocument();
    expect(within(table).getByText("Outside limits")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Attribute check" })).toHaveTextContent("Exceptions 1");
    expect(calls[0].body).toEqual({
      definitions: [
        { name: "potency", kind: "numeric", min: "40", max: "60" },
        { name: "grade", kind: "enum", options: ["A", "B"] },
      ],
      values: { potency: { num: "38.5" }, grade: { text: "A" } },
    });
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });

  it("validates attributes before calling and shows a refusal", async () => {
    handler = () => json(400, { code: "COM-0001", detail: 'attribute "grade" value "Z" is not one of its options' });
    renderAt("/processmfg/batch-attributes");
    fireEvent.change(screen.getByLabelText("Recorded value", { selector: "#attr-0-value" }), { target: { value: "high" } });
    fireEvent.change(screen.getByLabelText("Name", { selector: "#attr-1-name" }), { target: { value: "potency" } });
    fireEvent.click(screen.getByRole("button", { name: "Check values" }));
    expect(await screen.findByText("Enter a number with at most 6 decimals.")).toBeInTheDocument();
    expect(screen.getByText("This attribute is already listed.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Recorded value", { selector: "#attr-0-value" }), { target: { value: "45" } });
    fireEvent.change(screen.getByLabelText("Name", { selector: "#attr-1-name" }), { target: { value: "grade" } });
    fireEvent.change(screen.getByLabelText("Recorded value", { selector: "#attr-1-value" }), { target: { value: "Z" } });
    fireEvent.click(screen.getByRole("button", { name: "Check values" }));
    expect(await findAlert()).toHaveTextContent('attribute "grade" value "Z" is not one of its options');
  });

  it("proposes batches and lists the expired and on-hold ones with their codes (AC-24.4, AC-24.10)", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/production/batch-picking:proposal"
        ? json(200, {
            picked: [{ batchId: "B-403", qty: "80", expiry: "2027-06-01", expiredOverride: false }],
            excluded: [
              { batchId: "B-401", code: "PMF-0003", detail: "batch B-401 expired on 2027-01-10" },
              { batchId: "B-402", code: "PMF-0016", detail: "batch B-402 is on-hold; not pickable" },
            ],
            shortfall: "0",
            overrideHeld: false,
          })
        : undefined;
    const { container } = renderAt("/processmfg/picking");
    fireEvent.click(screen.getByRole("button", { name: "Propose batches" }));
    const excluded = await findTable({ name: "Excluded batches" });
    expect(within(excluded).getByText("batch B-401 expired on 2027-01-10")).toBeInTheDocument();
    expect(within(excluded).getByText("Blocked by disposition")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Picked batches" })).getByText("B-403")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Picking proposal" })).toHaveTextContent("Shortfall 0");
    expect(calls[0].body).toEqual({
      postingDate: "2027-01-15",
      qty: "80",
      potency: { attribute: "potency", min: "40", max: "60" },
      batches: [
        { id: "B-401", qty: "50", disposition: "available", values: { expiry: { date: "2027-01-10" }, potency: { num: "45" } } },
        { id: "B-402", qty: "100", disposition: "on-hold", values: { expiry: { date: "2027-02-01" }, potency: { num: "45" } } },
        { id: "B-403", qty: "100", disposition: "available", values: { expiry: { date: "2027-06-01" }, potency: { num: "50" } } },
      ],
    });
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });

  it("sends the override reason, flags overridden batches and warns when the duty is missing (BR-24.10)", async () => {
    renderAt("/processmfg/picking");
    fireEvent.change(screen.getByLabelText("Reason to consume expired batches"), { target: { value: "short" } });
    fireEvent.click(screen.getByRole("button", { name: "Propose batches" }));
    expect(await screen.findByText("Give at least 10 characters, or leave it empty.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    handler = () =>
      json(200, {
        picked: [{ batchId: "B-401", qty: "50", expiry: "2027-01-10", expiredOverride: true }],
        excluded: [],
        shortfall: "30",
        overrideHeld: true,
      });
    fireEvent.change(screen.getByLabelText("Reason to consume expired batches"), { target: { value: "QA approved use" } });
    fireEvent.click(screen.getByRole("button", { name: "Propose batches" }));
    expect(await screen.findByText("Expired, overridden")).toBeInTheDocument();
    expect(screen.getByText("1 expired batch is used on your override; each becomes an exception in the batch record review.")).toBeInTheDocument();
    expect((calls[0].body as { overrideReason: string }).overrideReason).toBe("QA approved use");

    handler = () => json(200, { picked: [], excluded: [], shortfall: "80", overrideHeld: false });
    fireEvent.click(screen.getByRole("button", { name: "Propose batches" }));
    expect(await screen.findByText("No batch can be picked")).toBeInTheDocument();
    expect(screen.getByText("You do not hold the Consume expired batch duty, so expired batches stay excluded.")).toBeInTheDocument();
  });

  it("plans the AC-24.18 rework with the default parameters", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/production/rework-orders:preview"
        ? json(200, {
            sourceBatchId: "B-77",
            reworkQty: "500",
            outputBatchId: "B-77",
            outputQty: "480",
            outputDisposition: "quarantine",
            qualityOrderRequired: true,
            varianceType: "rework",
            parents: ["B-77"],
          })
        : undefined;
    const { container } = renderAt("/processmfg/rework");
    fireEvent.click(screen.getByRole("button", { name: "Plan rework" }));
    const plan = await screen.findByRole("list", { name: "Rework plan" });
    expect(plan).toHaveTextContent("Output batch B-77");
    expect(plan).toHaveTextContent("Output disposition quarantine");
    expect(plan).toHaveTextContent("Cost posts as Rework variance");
    expect(screen.getByRole("status")).toHaveTextContent("First ingredient: 500 of B-77. Genealogy parents: B-77");
    expect(calls[0].body).toEqual({
      batch: { id: "B-77", qty: "500", disposition: "on-hold" },
      reworkQty: "500",
      outputQty: "480",
      params: { keepBatchNumber: true, requireQualityOrder: true, outputDisposition: "quarantine", capitalize: false },
    });
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });

  it("refuses an available output behind a quality order and shows a server refusal", async () => {
    renderAt("/processmfg/rework");
    fireEvent.change(screen.getByLabelText("Output disposition"), { target: { value: "available" } });
    fireEvent.click(screen.getByRole("button", { name: "Plan rework" }));
    expect(await screen.findByText("With a quality order the output waits for quality release; choose quarantine or on hold.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    handler = () => json(400, { code: "COM-0001", detail: "rework quantity must be positive and at most the batch quantity 500" });
    fireEvent.click(screen.getByLabelText("Require a quality order"));
    fireEvent.change(screen.getByLabelText("Quantity to rework"), { target: { value: "600" } });
    fireEvent.click(screen.getByRole("button", { name: "Plan rework" }));
    expect(await findAlert()).toHaveTextContent("rework quantity must be positive and at most the batch quantity 500");
    expect(calls[0].body).toMatchObject({ reworkQty: "600", params: { requireQualityOrder: false, outputDisposition: "available" } });
  });
});
