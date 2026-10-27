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
    expect(within(areas).getAllByRole("link")).toHaveLength(4);
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
    const table = await screen.findByRole("table", { name: "Formulas" });
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
    expect(await screen.findByRole("alert")).toHaveTextContent("service unavailable (COM-0500)");
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
    fireEvent.change(screen.getByLabelText("Product item id"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: FLOUR } });
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
    expect(await screen.findByRole("table", { name: "Formula lines" })).toHaveTextContent("Fixed");
    fireEvent.change(screen.getByLabelText("Batch size (kg)"), { target: { value: "2500" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview scaling" }));
    const scaled = await screen.findByRole("table", { name: "Scaled lines" });
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
    const table = await screen.findByRole("table", { name: "Batch orders" });
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
    expect(await screen.findByRole("table", { name: "Batch order lines" })).toHaveTextContent("875");
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
    expect(await screen.findByText("batch order BO-000001 is at version 2, not 1 (PMF-0104)")).toBeInTheDocument();
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
    expect(await screen.findByRole("alert")).toHaveTextContent("batch order not found (PMF-0102)");
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
    expect(await screen.findByRole("alert")).toHaveTextContent("compensating line would become negative (-25.000000) (PMF-0002)");
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
    const table = await screen.findByRole("table", { name: "Allocated cost" });
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
