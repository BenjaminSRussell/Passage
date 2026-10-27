import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import type { CostResult } from "./api/client";
import { amount } from "./format";
import productcost from "./module";
import { parseItems, splitRows } from "./RollPage";
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
    expect(productcost.nav.map((n) => n.to)).toEqual([
      "/productcost",
      "/productcost/roll",
      "/productcost/variances",
      "/productcost/activation-preview",
    ]);
    expect(productcost.nav.map((n) => n.duty)).toEqual(["CostingView", "CostingCalculate", "CostingSystem", "CostingActivate"]);
  });

  it("links the workspace to each costing tool", () => {
    const { container } = renderAt("/productcost");
    expect(screen.getByRole("heading", { level: 2, name: "Costing workspace" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Roll items" })).toHaveAttribute("href", "/productcost/roll");
    expect(screen.getByRole("link", { name: "Decompose a variance" })).toHaveAttribute("href", "/productcost/variances");
    expect(screen.getByRole("link", { name: "Preview a revaluation" })).toHaveAttribute("href", "/productcost/activation-preview");
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
      url.pathname === "/api/v1/costing/calculations"
        ? { status: 200, body: { result: fg100, split: { DM: "21.2", DL: "54", "IND-MAT": "2.12", "IND-LAB": "27" } } }
        : undefined;
    const { container } = renderAt("/productcost/roll");
    expect(screen.getByRole("heading", { name: "No roll yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Roll item" }));
    const table = await screen.findByRole("table", { name: "Cost component split of FG-100" });
    expect(calls).toHaveLength(1);
    expect(calls[0].method).toBe("POST");
    expect(calls[0].init.credentials).toBe("same-origin");
    const body = calls[0].body as { itemId: string; items: { id: string }[]; options: unknown };
    expect(body.itemId).toBe("FG-100");
    expect(body.items.map((i) => i.id)).toEqual(["RM-300", "RM-301", "RM-302", "SA-200", "FG-100"]);
    expect(body.options).toEqual({ mode: "Multilevel", fallbackToActive: false });
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
      url.pathname === "/api/v1/costing/rolls"
        ? { status: 200, body: { itemCount: 2, errorCount: 1, durationMs: 3, results: [fg100, failed] } }
        : undefined;
    renderAt("/productcost/roll");
    fireEvent.click(screen.getByRole("button", { name: "Mass roll" }));
    expect(await screen.findByRole("list", { name: "Roll run" })).toHaveTextContent("Errors 1");
    const results = screen.getByRole("table", { name: "Roll results" });
    expect(within(results).getByRole("row", { name: /LOOP/ })).toHaveTextContent("CST-0701");
    expect(calls[0].body).not.toHaveProperty("itemId");

    fireEvent.click(within(results).getByRole("button", { name: "FG-100" }));
    const split = await screen.findByRole("table", { name: "Cost component split of FG-100" });
    expect(within(split).getByRole("row", { name: /^IND-MAT/ })).toHaveTextContent("2.12");
    expect(within(split).getByRole("row", { name: /^DL/ })).toHaveTextContent("54.00");

    fireEvent.click(within(results).getByRole("button", { name: "LOOP" }));
    const diagnostics = await screen.findByRole("table", { name: "Diagnostics of LOOP" });
    expect(diagnostics).toHaveTextContent("cycle detected: LOOP -> LOOP");
  });
});

describe("production variance page", () => {
  it("sends the AC-39.5 order and shows the decomposition", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/costing/variances/compute"
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
      url.pathname === "/api/v1/costing/activations:preview"
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
    const journal = await screen.findByRole("table", { name: "Revaluation journal" });
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
