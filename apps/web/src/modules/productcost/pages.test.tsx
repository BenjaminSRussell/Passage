import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import type { CostResult } from "./api/client";
import { amount } from "./format";
import productcost from "./module";
import { parseItems, splitRows } from "./RollPage";
import { validateCompare } from "./ComparePage";
import { validateMixed } from "./MixedCostPage";
import { splitFromLines } from "./PostingsPage";
import { sumDecimals } from "./shared";
import { validateOrder } from "./VariancePage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; body: unknown; init: RequestInit }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method: init.method ?? "GET", url, body, init });
      const reply = handler(url, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
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
        {productcost.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const fg100: CostResult = {
  itemId: "FG-100",
  status: "Pending",
  level: 2,
  unitCost: "104.32",
  own: { DM: "4", DL: "24", "IND-MAT": "0.4", "IND-LAB": "12" },
  rolled: { DM: "17.2", DL: "30", "IND-MAT": "1.72", "IND-LAB": "15" },
};

describe("productcost module", () => {
  it("is discovered by the shell and keeps every route and nav entry under /productcost", () => {
    expect(loadModules().some((m) => m.id === "productcost")).toBe(true);
    expect(productcost.routes.every((r) => r.element != null && r.path?.startsWith("/productcost"))).toBe(true);
    expect(productcost.nav.map((n) => [n.to, n.duty])).toEqual([
      ["/productcost", "CostingView"],
      ["/productcost/roll", "CostingCalculate"],
      ["/productcost/mixed-cost", "CostingCalculate"],
      ["/productcost/sales-order-estimate", "CostingCalculate"],
      ["/productcost/sales-order-versions", "CostingCalculate"],
      ["/productcost/estimates", "CostingCalculate"],
      ["/productcost/compare", "CostingView"],
      ["/productcost/variances", "CostingSystem"],
      ["/productcost/postings", "CostingCalculate"],
      ["/productcost/reconciliation", "CostingView"],
      ["/productcost/activation-preview", "CostingActivate"],
      ["/productcost/cost-components", "CostingView"],
      ["/productcost/cost-groups", "CostingView"],
      ["/productcost/versions", "CostingView"],
      ["/productcost/conversion", "CostingActivate"],
    ]);
    expect(productcost.routes.map((r) => r.path)).toEqual(productcost.nav.map((n) => n.to));
  });

  it("links the workspace to each costing tool", () => {
    const { container } = renderAt("/productcost");
    expect(screen.getByRole("heading", { level: 2, name: "Costing workspace" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Roll items" })).toHaveAttribute("href", "/productcost/roll");
    expect(screen.getByRole("link", { name: "Decompose a variance" })).toHaveAttribute("href", "/productcost/variances");
    expect(screen.getByRole("link", { name: "Preview a revaluation" })).toHaveAttribute("href", "/productcost/activation-preview");
    expect(screen.getByRole("link", { name: "Mix alternatives" })).toHaveAttribute("href", "/productcost/mixed-cost");
    expect(screen.getByRole("link", { name: "Cost a sales line" })).toHaveAttribute("href", "/productcost/sales-order-estimate");
    expect(screen.getByRole("link", { name: "Compare estimates" })).toHaveAttribute("href", "/productcost/compare");
    expect(screen.getByRole("link", { name: "Preview postings" })).toHaveAttribute("href", "/productcost/postings");
    expect(screen.getByRole("link", { name: "Reconcile inventory" })).toHaveAttribute("href", "/productcost/reconciliation");
    expect(screen.getByRole("link", { name: "Maintain components" })).toHaveAttribute("href", "/productcost/cost-components");
    expect(screen.getByRole("link", { name: "Maintain cost groups" })).toHaveAttribute("href", "/productcost/cost-groups");
    expect(screen.getByRole("link", { name: "Manage versions" })).toHaveAttribute("href", "/productcost/versions");
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("helpers", () => {
  it("pads decimal strings without rounding", () => {
    expect(amount("21.2")).toBe("21.20");
    expect(amount("54")).toBe("54.00");
    expect(amount("13.333333")).toBe("13.333333");
    expect(amount("-7.5")).toBe("-7.50");
    expect(amount(undefined)).toBe("—");
  });

  it("rejects item lists that are not JSON arrays of identified items", () => {
    expect(parseItems("not json").error).toMatch(/JSON array/);
    expect(parseItems("[]").error).toMatch(/at least one item/);
    expect(parseItems('[{"kind":"Purchased"}]').error).toMatch(/Item 1 needs an id/);
    expect(parseItems('[{"id":"X","kind":"Made"}]').error).toMatch(/X: kind must be/);
    expect(parseItems('[{"id":"X","kind":"Purchased","price":"1"}]').items).toHaveLength(1);
  });

  it("lists own, rolled and combined amounts per component", () => {
    const rows = splitRows(fg100, { DM: "21.2", DL: "54", "IND-MAT": "2.12", "IND-LAB": "27" });
    expect(rows.find((r) => r.component === "DM")).toEqual({ component: "DM", own: "4", rolled: "17.2", total: "21.2" });
    expect(rows.map((r) => r.component)).toEqual(["DL", "DM", "IND-LAB", "IND-MAT"]);
  });

  it("builds the order the service expects and refuses bad numbers", () => {
    const { order } = validateOrder({
      currency: "usd",
      plannedQty: "100",
      rafQty: "90",
      lines: [
        { component: "DL", kind: "fixed", plannedQty: "", stdUnitCost: "0.4", estPerLot: "40", actualQty: "1", actualUnitCost: "40" },
        { component: "DM", kind: "unplanned", plannedQty: "9", stdUnitCost: "9", estPerLot: "", actualQty: "5", actualUnitCost: "1.5" },
      ],
    });
    expect(order).toEqual({
      currency: "USD",
      plannedQty: "100",
      rafQty: "90",
      lines: [
        { component: "DL", planned: true, fixed: true, estPerLot: "40", stdUnitCost: "0.4", actualQty: "1", actualUnitCost: "40" },
        { component: "DM", planned: false, actualQty: "5", actualUnitCost: "1.5" },
      ],
    });
    const bad = validateOrder({
      currency: "EURO",
      plannedQty: "0",
      rafQty: "-1",
      lines: [{ component: "", kind: "planned", plannedQty: "x", stdUnitCost: "", estPerLot: "", actualQty: "", actualUnitCost: "" }],
    });
    expect(bad.order).toBeUndefined();
    expect(Object.keys(bad.errors).sort()).toEqual([
      "currency",
      "lines[0].actualQty",
      "lines[0].actualUnitCost",
      "lines[0].component",
      "lines[0].plannedQty",
      "lines[0].stdUnitCost",
      "plannedQty",
      "rafQty",
    ]);
  });
});

describe("cost roll-up page", () => {
  it("starts empty, then rolls the worked example and shows FG-100 with its split", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/product-costing/calculations"
        ? { status: 200, body: { result: fg100, split: { DM: "21.2", DL: "54", "IND-MAT": "2.12", "IND-LAB": "27" } } }
        : undefined;
    const { container } = renderAt("/productcost/roll");
    expect(screen.getByRole("heading", { name: "No roll yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Roll item" }));
    const table = await findTable({ name: "Cost component split of FG-100" });
    expect(calls).toHaveLength(1);
    expect(calls[0].method).toBe("POST");
    expect(calls[0].init.credentials).toBe("same-origin");
    const body = calls[0].body as { itemId: string; items: { id: string }[]; options: unknown };
    expect(body.itemId).toBe("FG-100");
    expect(body.items.map((i) => i.id)).toEqual(["RM-300", "RM-301", "RM-302", "SA-200", "FG-100"]);
    expect(body.options).toEqual({ mode: "Multilevel", fallbackToActive: false, view: "Legal" });
    expect(Array.from((screen.getByLabelText("Explosion mode") as HTMLSelectElement).options).map((o) => o.value)).toEqual([
      "Multilevel",
      "SingleLevel",
      "MakeToOrder",
      "ByLineType",
    ]);
    expect(screen.getByText("Unit cost").parentElement).toHaveTextContent("104.32");
    const dm = within(table).getByRole("row", { name: /^DM/ });
    expect(dm).toHaveTextContent("4.00");
    expect(dm).toHaveTextContent("17.20");
    expect(dm).toHaveTextContent("21.20");
    expect(pageViolations(container)).toEqual([]);
  });

  it("refuses an item list that is not JSON without calling the service", () => {
    renderAt("/productcost/roll");
    fireEvent.change(screen.getByLabelText("Items"), { target: { value: "{oops" } });
    fireEvent.click(screen.getByRole("button", { name: "Roll item" }));
    expect(screen.getByText(/Enter the items as a JSON array/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("shows the service's field errors when the roll is refused", async () => {
    handler = () => ({
      status: 400,
      body: { code: "COM-0001", detail: "request failed validation", errors: { "items[3].sheet[0].type": "must be Surcharge or Rate" } },
    });
    renderAt("/productcost/roll");
    fireEvent.click(screen.getByRole("button", { name: "Mass roll" }));
    const alert = await screen.findByText("The roll was refused");
    const box = alert.closest("[role=alert]") as HTMLElement;
    expect(box).toHaveTextContent("request failed validation (COM-0001)");
    expect(box).toHaveTextContent("items[3].sheet[0].type must be Surcharge or Rate");
  });

  it("mass rolls, counts errors and opens one item's split from the results", async () => {
    const failed: CostResult = {
      itemId: "LOOP",
      status: "Error",
      level: 0,
      unitCost: "0",
      own: {},
      rolled: {},
      diagnostics: [{ code: "CST-0701", severity: "error", message: "cycle detected: LOOP -> LOOP", ref: "LOOP" }],
    };
    handler = (url) =>
      url.pathname === "/api/v1/product-costing/rolls"
        ? { status: 200, body: { itemCount: 2, errorCount: 1, durationMs: 3, results: [fg100, failed] } }
        : undefined;
    renderAt("/productcost/roll");
    fireEvent.click(screen.getByRole("button", { name: "Mass roll" }));
    expect(await screen.findByRole("list", { name: "Roll run" })).toHaveTextContent("Errors 1");
    const results = screen.getByRole("table", { name: "Roll results" });
    expect(within(results).getByRole("row", { name: /LOOP/ })).toHaveTextContent("CST-0701");
    expect(calls[0].body).not.toHaveProperty("itemId");

    fireEvent.click(within(results).getByRole("button", { name: "FG-100" }));
    const split = await findTable({ name: "Cost component split of FG-100" });
    expect(within(split).getByRole("row", { name: /^IND-MAT/ })).toHaveTextContent("2.12");
    expect(within(split).getByRole("row", { name: /^DL/ })).toHaveTextContent("54.00");

    fireEvent.click(within(results).getByRole("button", { name: "LOOP" }));
    const diagnostics = await findTable({ name: "Diagnostics of LOOP" });
    expect(diagnostics).toHaveTextContent("cycle detected: LOOP -> LOOP");
  });
});

describe("production variance page", () => {
  it("sends the AC-39.5 order and shows the decomposition", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/product-costing/variances/compute"
        ? {
            status: 200,
            body: {
              total: "51.5",
              types: { lotSize: "0", quantity: "40", price: "4", substitution: "7.5" },
              byComponent: [
                { component: "DL", total: "4", types: { lotSize: "0", quantity: "0", price: "4", substitution: "0" } },
                { component: "DM", total: "47.5", types: { lotSize: "0", quantity: "40", price: "0", substitution: "7.5" } },
              ],
              residual: "0",
            },
          }
        : undefined;
    const { container } = renderAt("/productcost/variances");
    expect(screen.getByRole("heading", { name: "No variance computed yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Compute variance" }));
    const stats = await screen.findByRole("list", { name: "Order variance" });
    const table = screen.getByRole("table", { name: "Variance by component" });
    expect(calls[0].body).toEqual({
      currency: "USD",
      plannedQty: "100",
      rafQty: "90",
      lines: [
        { component: "DM", planned: true, plannedQty: "300", stdUnitCost: "2.00", actualQty: "290", actualUnitCost: "2.00" },
        { component: "DM", planned: false, actualQty: "5", actualUnitCost: "1.50" },
        { component: "DL", planned: true, fixed: true, estPerLot: "40.00", stdUnitCost: "0.40", actualQty: "1", actualUnitCost: "40.00" },
      ],
    });
    expect(stats).toHaveTextContent("Total variance 51.50");
    expect(within(table).getByRole("row", { name: /^DM/ })).toHaveTextContent("40.00");
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a variance that does not close (CST-0720)", async () => {
    handler = () => ({ status: 422, body: { code: "CST-0720", detail: "DM residual 0.05" } });
    renderAt("/productcost/variances");
    fireEvent.click(screen.getByRole("button", { name: "Compute variance" }));
    const title = await screen.findByText("The variance does not close");
    expect(title.closest("[role=alert]")).toHaveTextContent("DM residual 0.05 (CST-0720)");
    expect(screen.queryByRole("table", { name: "Variance by component" })).not.toBeInTheDocument();
  });

  it("adds and removes lines and checks them before sending", () => {
    renderAt("/productcost/variances");
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    expect(screen.getByRole("group", { name: "Line 4" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Compute variance" }));
    expect(screen.getByRole("group", { name: "Line 4" })).toHaveTextContent("Enter a number, 0 or more.");
    expect(calls).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Remove line 4" }));
    expect(screen.queryByRole("group", { name: "Line 4" })).not.toBeInTheDocument();
  });
});

describe("activation preview page", () => {
  it("previews AC-39.4 and shows the total to confirm and the journal", async () => {
    handler = (url, body) =>
      url.pathname === "/api/v1/product-costing/activations:preview"
        ? {
            status: 200,
            body: {
              currency: (body as { currency?: string }).currency,
              total: "500",
              lines: [{ itemId: "RM-301", siteId: "PLANT-1", qty: "1000", oldStd: "10", newStd: "10.5", amount: "500" }],
              journal: [
                { account: "Inventory", debit: "500", credit: "0" },
                { account: "CostChangeVariance", debit: "0", credit: "500" },
              ],
            },
          }
        : undefined;
    const { container } = renderAt("/productcost/activation-preview");
    expect(screen.getByRole("heading", { name: "No preview yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview revaluation" }));
    const journal = await findTable({ name: "Revaluation journal" });
    expect(calls[0].body).toEqual({
      currency: "USD",
      lines: [{ itemId: "RM-301", siteId: "PLANT-1", qty: "1000", oldStd: "10.00", newStd: "10.50" }],
    });
    expect(screen.getByText("Activation will revalue inventory").parentElement).toHaveTextContent(
      "Total revaluation 500.00 USD (CST-0712)",
    );
    expect(within(journal).getByRole("row", { name: /^Inventory/ })).toHaveTextContent("500.00");
    expect(within(journal).getByRole("row", { name: /^CostChangeVariance/ })).toHaveTextContent("—");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a refusal and keeps the entered positions", async () => {
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "missing duty CostingActivate" } });
    renderAt("/productcost/activation-preview");
    fireEvent.change(screen.getByLabelText("New standard"), { target: { value: "11.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview revaluation" }));
    await waitFor(() => expect(screen.getByText("The preview was not computed")).toBeInTheDocument());
    expect(screen.getByText("The preview was not computed").closest("[role=alert]")).toHaveTextContent(
      "missing duty CostingActivate (AUTH-0003)",
    );
    expect(screen.getByLabelText("New standard")).toHaveValue("11.00");
  });

  it("requires a site for every position", () => {
    renderAt("/productcost/activation-preview");
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview revaluation" }));
    expect(screen.getByText("Enter the site.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("slice 2 helpers", () => {
  it("sums ratio strings exactly", () => {
    expect(sumDecimals(["60", "40"])).toBe("100");
    expect(sumDecimals(["33.33", "33.33", "33.34"])).toBe("100");
    expect(sumDecimals(["0.1", "0.2"])).toBe("0.3");
    expect(sumDecimals(["1", "x"])).toBeUndefined();
  });

  it("refuses mixing ratios that do not total 100 (CST-0741) and alternatives that are not in the items", () => {
    const { errors, alternatives } = validateMixed({
      items: '[{"id":"BUY-A","kind":"Purchased","price":"10"}]',
      itemId: "FG-MIX",
      fallbackToActive: false,
      alternatives: [
        { source: "VENDOR-A", percent: "60", itemId: "BUY-A" },
        { source: "VENDOR-A", percent: "30", itemId: "MISSING" },
      ],
    });
    expect(alternatives).toBeUndefined();
    expect(errors.alternatives).toMatch(/total 90%.*CST-0741/);
    expect(errors["alternatives[1].source"]).toBe("Each source appears once.");
    expect(errors["alternatives[1].itemId"]).toBe("The item must be one of the items above.");
  });

  it("builds a split from component lines and refuses duplicates", () => {
    const errors: Record<string, string> = {};
    expect(
      splitFromLines(
        [
          { component: "DM", amount: "5.00" },
          { component: "DM", amount: "1" },
          { component: "", amount: "-1" },
        ],
        "standard",
        errors,
      ),
    ).toEqual({ DM: "1" });
    expect(Object.keys(errors).sort()).toEqual(["standard[1].component", "standard[2].amount", "standard[2].component"]);
  });

  it("keys item errors of a comparison by side", () => {
    const { errors, request } = validateCompare({ baseline: "[]", candidate: "{", mode: "ByLineType", fallbackToActive: true });
    expect(request).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["baseline.items", "candidate.items"]);
  });
});

describe("cost roll-up page explosion modes", () => {
  it("sends the chosen explosion mode", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/product-costing/rolls" ? { status: 200, body: { itemCount: 1, errorCount: 0, durationMs: 1, results: [fg100] } } : undefined;
    renderAt("/productcost/roll");
    fireEvent.change(screen.getByLabelText("Explosion mode"), { target: { value: "MakeToOrder" } });
    fireEvent.click(screen.getByRole("button", { name: "Mass roll" }));
    await findTable({ name: "Roll results" });
    expect((calls[0].body as { options: unknown }).options).toEqual({ mode: "MakeToOrder", fallbackToActive: false, view: "Legal" });
  });
});

describe("mixed cost page", () => {
  it("starts empty, then mixes AC-39.11 and shows 11.00 with the alternatives and split", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/product-costing/mixed-costs"
        ? {
            status: 200,
            body: {
              itemId: "FG-MIX",
              status: "Pending",
              unitCost: "11",
              split: { DM: "11" },
              alternatives: [
                { source: "VENDOR-A", percent: "60", itemId: "BUY-A", status: "Pending", unitCost: "10", split: { DM: "10" } },
                { source: "IN-HOUSE", percent: "40", itemId: "MAKE", status: "Pending", unitCost: "12.5", split: { DM: "12.5" } },
              ],
            },
          }
        : undefined;
    const { container } = renderAt("/productcost/mixed-cost");
    expect(screen.getByRole("heading", { name: "No mixed cost yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Compute mixed cost" }));
    await screen.findByRole("list", { name: "Mixed cost" });
    const alts = screen.getByRole("table", { name: "Alternatives" });
    const body = calls[0].body as { itemId: string; alternatives: unknown; items: { id: string }[] };
    expect(body.itemId).toBe("FG-MIX");
    expect(body.alternatives).toEqual([
      { source: "VENDOR-A", percent: "60", itemId: "BUY-A" },
      { source: "IN-HOUSE", percent: "40", itemId: "MAKE" },
    ]);
    expect(body.items.map((i) => i.id)).toEqual(["BUY-A", "RM-500", "MAKE"]);
    expect(screen.getByRole("list", { name: "Mixed cost" })).toHaveTextContent("Unit cost 11.00");
    expect(within(alts).getByRole("row", { name: /IN-HOUSE/ })).toHaveTextContent("12.50");
    expect(within(screen.getByRole("table", { name: "Weighted split of FG-MIX" })).getByRole("row", { name: /^DM/ })).toHaveTextContent(
      "11.00",
    );
    expect(pageViolations(container)).toEqual([]);
  });

  it("checks the ratio total before calling the service", () => {
    renderAt("/productcost/mixed-cost");
    fireEvent.change(screen.getAllByLabelText("Ratio (%)")[1], { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Compute mixed cost" }));
    expect(screen.getByText(/The ratios total 90%/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("shows the service's refusal with its code", async () => {
    handler = () => ({ status: 422, body: { code: "CST-0741", detail: "CST-0741: mixing ratios total 99.99" } });
    renderAt("/productcost/mixed-cost");
    fireEvent.click(screen.getByRole("button", { name: "Compute mixed cost" }));
    const title = await screen.findByText("The mixed cost was not computed");
    expect(title.closest("[role=alert]")).toHaveTextContent("mixing ratios total 99.99 (CST-0741)");
  });
});

describe("sales order estimate page", () => {
  it("costs the AC-39.13 line and shows margin 540.00 (22.50%)", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/product-costing/sales-order-estimates"
        ? {
            status: 200,
            body: {
              itemId: "CFG-LINE",
              calculationType: "SalesOrder",
              status: "Pending",
              quantity: "1",
              unitCost: "1860",
              cost: "1860",
              split: { DM: "1500", DL: "360" },
              netAmount: "2400",
              margin: "540",
              marginPct: "22.5",
            },
          }
        : undefined;
    const { container } = renderAt("/productcost/sales-order-estimate");
    expect(screen.getByRole("heading", { name: "No estimate yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Cost the line" }));
    const stats = await screen.findByRole("list", { name: "Line margin" });
    const body = calls[0].body as Record<string, unknown>;
    expect(body).toMatchObject({ currency: "USD", itemId: "CFG-LINE", quantity: "1", netAmount: "2400.00" });
    expect(body.options).toEqual({ mode: "Multilevel", fallbackToActive: false });
    expect(stats).toHaveTextContent("Estimated cost 1860.00");
    expect(stats).toHaveTextContent("Margin 540.00");
    expect(stats).toHaveTextContent("Margin % 22.50");
    expect(within(screen.getByRole("table", { name: "Cost by component of CFG-LINE" })).getByRole("row", { name: /^DL/ })).toHaveTextContent(
      "360.00",
    );
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the diagnostics of a line that fails to roll", async () => {
    handler = () => ({
      status: 200,
      body: {
        itemId: "CFG-LINE",
        calculationType: "SalesOrder",
        status: "Error",
        quantity: "1",
        unitCost: "0",
        cost: "0",
        split: {},
        netAmount: "2400",
        margin: "0",
        marginPct: "0",
        diagnostics: [{ code: "CST-0702", severity: "error", message: "no price for RM-FRAME in this version", ref: "RM-FRAME" }],
      },
    });
    renderAt("/productcost/sales-order-estimate");
    fireEvent.click(screen.getByRole("button", { name: "Cost the line" }));
    const diagnostics = await findTable({ name: "Diagnostics of CFG-LINE" });
    expect(diagnostics).toHaveTextContent("CST-0702");
    expect(screen.getByRole("list", { name: "Line margin" })).toHaveTextContent("Margin —");
    expect(screen.queryByRole("table", { name: "Cost by component of CFG-LINE" })).not.toBeInTheDocument();
  });

  it("requires a positive net amount before calling the service", () => {
    renderAt("/productcost/sales-order-estimate");
    fireEvent.change(screen.getByLabelText("Net line amount"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Cost the line" }));
    expect(screen.getByText("Enter the net line amount, above 0.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("estimate comparison page", () => {
  it("compares the what-if candidate and opens one item's component change", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/product-costing/comparisons"
        ? {
            status: 200,
            body: {
              totalDelta: "4.95",
              rows: [
                { itemId: "FG-100", status: "Compared", baseline: "104.32", candidate: "107.62", delta: "3.3", deltaPct: "3.16", byComponent: { DM: "3", "IND-MAT": "0.3" } },
                { itemId: "NEW-1", status: "CandidateOnly", baseline: "0", candidate: "5", delta: "0", deltaPct: "0", byComponent: {} },
              ],
            },
          }
        : undefined;
    const { container } = renderAt("/productcost/compare");
    expect(screen.getByRole("heading", { name: "No comparison yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Compare" }));
    await screen.findByRole("list", { name: "Comparison" });
    const table = screen.getByRole("table", { name: "Comparison by item" });
    const body = calls[0].body as { baseline: { items: { id: string; price?: string }[] }; candidate: { items: { id: string; price?: string }[] } };
    expect(body.baseline.items.find((i) => i.id === "RM-301")?.price).toBe("2.50");
    expect(body.candidate.items.find((i) => i.id === "RM-301")?.price).toBe("2.75");
    expect(screen.getByRole("list", { name: "Comparison" })).toHaveTextContent("Changed 1");
    expect(within(table).getByRole("row", { name: /NEW-1/ })).toHaveTextContent("Candidate only");
    fireEvent.click(within(table).getByRole("button", { name: "FG-100" }));
    const change = await findTable({ name: "Change by component of FG-100" });
    expect(within(change).getByRole("row", { name: /^DM/ })).toHaveTextContent("3.00");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the side the service refused", async () => {
    handler = () => ({ status: 400, body: { code: "COM-0001", detail: "request failed validation", errors: { "candidate.options": "mode must be Multilevel" } } });
    renderAt("/productcost/compare");
    fireEvent.click(screen.getByRole("button", { name: "Compare" }));
    const title = await screen.findByText("The comparison was not computed");
    expect(title.closest("[role=alert]")).toHaveTextContent("candidate.options mode must be Multilevel");
  });
});

describe("component postings page", () => {
  it("previews the AC-39.12 COGS lines and the AC-39.7 price difference", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/product-costing/postings:cogs")
        return {
          status: 200,
          body: {
            currency: "USD",
            total: "1043.2",
            journal: [
              { account: "COGS-DL", debit: "540", credit: "0" },
              { account: "COGS-DM", debit: "212", credit: "0" },
              { account: "COGS-IND-LAB", debit: "270", credit: "0" },
              { account: "COGS-IND-MAT", debit: "21.2", credit: "0" },
              { account: "Inventory", debit: "0", credit: "1043.2" },
            ],
          },
        };
      if (url.pathname === "/api/v1/product-costing/postings:price-difference")
        return {
          status: 200,
          body: {
            currency: "USD",
            total: "2",
            journal: [
              { account: "PPV-DM", debit: "2", credit: "0" },
              { account: "PriceDifferenceClearing", debit: "0", credit: "2" },
            ],
          },
        };
      return undefined;
    };
    const { container } = renderAt("/productcost/postings");
    expect(screen.getByRole("heading", { name: "No COGS posting yet" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "No price difference yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Preview COGS posting" }));
    await screen.findByRole("list", { name: "COGS journal total" });
    const cogs = screen.getByRole("table", { name: "COGS journal" });
    expect(calls[0].body).toEqual({ currency: "USD", quantity: "10", unitCost: { DM: "21.20", DL: "54.00", "IND-MAT": "2.12", "IND-LAB": "27.00" } });
    expect(within(cogs).getByRole("row", { name: /^COGS-DM/ })).toHaveTextContent("212.00");
    expect(within(cogs).getByRole("row", { name: /^Inventory/ })).toHaveTextContent("1043.20");
    expect(screen.getByRole("list", { name: "COGS journal total" })).toHaveTextContent("Total 1043.20 USD");

    fireEvent.click(screen.getByRole("button", { name: "Preview price difference" }));
    await screen.findByRole("list", { name: "Price difference journal total" });
    const ppv = screen.getByRole("table", { name: "Price difference journal" });
    expect(calls[1].body).toEqual({ currency: "USD", quantity: "10", receiptUnitCost: "5.20", standard: { DM: "5.00" } });
    expect(within(ppv).getByRole("row", { name: /^PPV-DM/ })).toHaveTextContent("2.00");
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains an empty journal when the receipt equals the standard", async () => {
    handler = () => ({ status: 200, body: { currency: "USD", total: "0", journal: [] } });
    renderAt("/productcost/postings");
    fireEvent.click(screen.getByRole("button", { name: "Preview price difference" }));
    expect(await screen.findByText("The receipt cost equals the standard, so there is no variance.")).toBeInTheDocument();
  });

  it("checks the components before calling the service", () => {
    renderAt("/productcost/postings");
    fireEvent.change(screen.getAllByLabelText("Cost component")[0], { target: { value: "DL" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview COGS posting" }));
    expect(screen.getByText("Each component appears once.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("standard cost reconciliation page", () => {
  it("reconciles two sites and flags the one that does not tie", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/product-costing/reconciliations"
        ? {
            status: 200,
            body: {
              currency: "USD",
              differences: 1,
              sites: [
                { siteId: "PLANT-1", lines: 2, standardValue: "11543.2", glBalance: "11543.2", difference: "0", status: "Reconciled" },
                { siteId: "PLANT-2", lines: 1, standardValue: "1598", glBalance: "1600", difference: "-2", status: "Difference" },
              ],
            },
          }
        : undefined;
    const { container } = renderAt("/productcost/reconciliation");
    expect(screen.getByRole("heading", { name: "No reconciliation yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Reconcile" }));
    await screen.findByRole("list", { name: "Reconciliation" });
    const table = screen.getByRole("table", { name: "Reconciliation by site" });
    const body = calls[0].body as { currency: string; onHand: unknown[]; glBalances: unknown[] };
    expect(body.currency).toBe("USD");
    expect(body.onHand).toHaveLength(3);
    expect(body.glBalances).toEqual([
      { siteId: "PLANT-1", amount: "11543.20" },
      { siteId: "PLANT-2", amount: "1600.00" },
    ]);
    expect(screen.getByRole("list", { name: "Reconciliation" })).toHaveTextContent("Differences 1");
    const plant2 = within(table).getByRole("row", { name: /PLANT-2/ });
    expect(plant2).toHaveTextContent("-2.00");
    expect(plant2).toHaveTextContent("Difference");
    expect(within(table).getByRole("row", { name: /PLANT-1/ })).toHaveTextContent("Reconciled");
    expect(pageViolations(container)).toEqual([]);
  });

  it("requires a site on every on-hand line", () => {
    renderAt("/productcost/reconciliation");
    fireEvent.change(screen.getAllByLabelText("Site")[0], { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Reconcile" }));
    expect(screen.getByText("Enter the site.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("shows a missing duty", async () => {
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "missing duty CostingView" } });
    renderAt("/productcost/reconciliation");
    fireEvent.click(screen.getByRole("button", { name: "Reconcile" }));
    const title = await screen.findByText("The reconciliation was not computed");
    expect(title.closest("[role=alert]")).toHaveTextContent("missing duty CostingView (AUTH-0003)");
  });
});
