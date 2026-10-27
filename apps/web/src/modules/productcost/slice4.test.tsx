import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { injectionExample, validateAdHoc } from "./AdHocEstimatePage";
import type { AdHocEstimate, EstimateVersion, SalesEstimate } from "./api/client";
import productcost from "./module";
import { currentVersion, validateRefresh } from "./SalesVersionsPage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { url: URL; body: unknown }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ url, body });
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

const injectionEstimate: AdHocEstimate = {
  modelCode: "INJ-PART",
  objectType: "Quote",
  objectId: "Q-1001",
  currency: "USD",
  characteristics: { cavities: "4", weight: "0.5", hours: "0.5" },
  lines: [
    { type: "Item", ref: "RM-GRAN", component: "DM", quantity: "2", price: "3", amount: "6", source: "Version" },
    { type: "Activity", ref: "MACHINE", component: "DL", quantity: "0.5", price: "40", amount: "20", source: "Version" },
    { type: "Overhead", ref: "OH-MACH", component: "OVH", quantity: "0.5", price: "2", amount: "1", source: "Formula" },
  ],
  total: "27",
  split: { DM: "6", DL: "20", OVH: "1" },
  diagnostics: [],
};

describe("ad-hoc estimate helpers", () => {
  it("builds the model, characteristic values and prices from the form", () => {
    const { errors, request } = validateAdHoc(injectionExample);
    expect(errors).toEqual({});
    expect(request?.model.characteristics).toEqual([
      { name: "cavities", unit: "pcs", default: "4" },
      { name: "weight", unit: "kg", default: undefined },
      { name: "hours", unit: "h", default: undefined },
    ]);
    // cavities has no value, so the service applies its default.
    expect(request?.characteristics).toEqual({ weight: "0.5", hours: "0.5" });
    expect(request?.prices).toEqual({ "RM-GRAN": { amount: "3.00", source: "Version" }, MACHINE: { amount: "40.00", source: "Version" } });
    expect(request?.model.lines[2]).toEqual({ type: "Overhead", ref: "OH-MACH", component: "OVH", quantityFormula: "hours", priceFormula: "2" });
    expect(request?.model.lines[0].priceFormula).toBeUndefined();
    expect(request?.sheet).toBeUndefined();
  });

  it("names the grid fields that are wrong", () => {
    const { errors, request } = validateAdHoc({
      ...injectionExample,
      objectId: " ",
      currency: "us",
      characteristics: [
        { name: "material weight", unit: "", default: "", value: "1" },
        { name: "hours", unit: "", default: "", value: "" },
      ],
      lines: [{ type: "Item", ref: "", component: "DM", quantityFormula: "", priceFormula: "" }],
      prices: [
        { ref: "RM", amount: "-1", source: "Version" },
        { ref: "RM", amount: "2", source: "Version" },
      ],
      sheet: "{",
      routingHours: "x",
    });
    expect(request).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual([
      "characteristics[0].name",
      "characteristics[1].value",
      "currency",
      "lines[0].quantityFormula",
      "lines[0].ref",
      "objectId",
      "prices[0].amount",
      "prices[1].ref",
      "routingHours",
      "sheet",
    ]);
  });

  it("skips empty characteristic and price rows", () => {
    const { errors, request } = validateAdHoc({
      ...injectionExample,
      characteristics: [{ name: "", unit: "", default: "", value: "" }],
      lines: [{ type: "Overhead", ref: "OH", component: "OVH", quantityFormula: "1", priceFormula: "5" }],
      prices: [{ ref: "", amount: "", source: "Version" }],
    });
    expect(errors).toEqual({});
    expect(request?.model.characteristics).toEqual([]);
    expect(request?.prices).toEqual({});
  });
});

describe("ad-hoc estimate page", () => {
  it("computes the example estimate and shows its lines, split and total", async () => {
    handler = (url) => (url.pathname === "/api/v1/costing/estimates" ? { status: 200, body: injectionEstimate } : undefined);
    const { container } = renderAt("/productcost/estimates");
    expect(screen.getByRole("heading", { level: 2, name: "Ad-hoc estimate" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "No estimate yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Compute the estimate" }));
    const totals = await screen.findByRole("list", { name: "Estimate total" });
    expect(totals).toHaveTextContent("Total cost 27.00");
    expect(calls[0].body).toMatchObject({ currency: "USD", objectType: "Quote", objectId: "Q-1001", model: { code: "INJ-PART" } });
    const lines = screen.getByRole("table", { name: "Lines of Q-1001" });
    expect(within(lines).getByRole("row", { name: /OH-MACH/ })).toHaveTextContent("Formula");
    expect(within(screen.getByRole("table", { name: "Cost by component of Q-1001" })).getByRole("row", { name: /^DL/ })).toHaveTextContent(
      "20.00",
    );
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a fallback warning returned with the estimate", async () => {
    handler = () => ({
      status: 200,
      body: { ...injectionEstimate, diagnostics: [{ code: "CST-0703", severity: "warning", message: "fallback", ref: "MACHINE" }] },
    });
    renderAt("/productcost/estimates");
    fireEvent.click(screen.getByRole("button", { name: "Compute the estimate" }));
    expect(await screen.findByRole("table", { name: "Diagnostics of Q-1001" })).toHaveTextContent("CST-0703");
  });

  it("shows the service's refusal and keeps the form", async () => {
    handler = () => ({ status: 422, body: { code: "CST-0764", detail: "characteristic hours has no value and no default" } });
    renderAt("/productcost/estimates");
    fireEvent.click(screen.getByRole("button", { name: "Compute the estimate" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The estimate was not computed");
    expect(alert).toHaveTextContent("CST-0764");
    expect(screen.getByRole("heading", { name: "No estimate yet" })).toBeInTheDocument();
  });

  it("does not call the service while a line is incomplete", () => {
    renderAt("/productcost/estimates");
    fireEvent.change(screen.getByLabelText("Quantity formula", { selector: "#adhoc-line-0-quantityFormula" }), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Compute the estimate" }));
    expect(screen.getAllByText("Required.").length).toBeGreaterThan(0);
    expect(calls).toHaveLength(0);
  });
});

const estimate = (cost: string, margin: string, pct: string, status: "Pending" | "Error" = "Pending"): SalesEstimate => ({
  itemId: "CFG-LINE",
  calculationType: "SalesOrder",
  status,
  quantity: "1",
  unitCost: cost,
  cost,
  split: { DM: "1500", DL: "360" },
  netAmount: "2400",
  margin,
  marginPct: pct,
});

const v1: EstimateVersion = { lineId: "SO-1001/10", version: 1, configHash: "cfg-1", status: "Pending", estimate: estimate("1860", "540", "22.5") };

describe("sales order estimate version helpers", () => {
  it("adds the line, configuration and history to the estimate request", () => {
    const values = {
      lineId: "SO-1/10",
      configHash: " cfg-9 ",
      currency: "USD",
      items: JSON.stringify([{ id: "A", kind: "Purchased", price: "1" }]),
      itemId: "A",
      quantity: "1",
      netAmount: "2",
      mode: "Multilevel" as const,
      fallbackToActive: false,
    };
    expect(validateRefresh(values, [v1]).request).toMatchObject({ lineId: "SO-1/10", configHash: "cfg-9", itemId: "A", versions: [v1] });
    expect(Object.keys(validateRefresh({ ...values, lineId: "", configHash: "" }, []).errors).sort()).toEqual(["configHash", "lineId"]);
  });

  it("takes the highest version that is not superseded as current", () => {
    const superseded = { ...v1, status: "Superseded" as const };
    const v2 = { ...v1, version: 2, configHash: "cfg-2" };
    expect(currentVersion([superseded, v2])?.version).toBe(2);
    expect(currentVersion([superseded])).toBeUndefined();
    expect(currentVersion([])).toBeUndefined();
  });
});

describe("sales order estimate versions page", () => {
  it("refreshes, supersedes on a configuration change and freezes on confirmation", async () => {
    const v2: EstimateVersion = { ...v1, version: 2, configHash: "cfg-2", estimate: estimate("1900", "500", "20.83") };
    handler = (url, body) => {
      const req = body as { configHash?: string; versions: EstimateVersion[] };
      if (url.pathname === "/api/v1/costing/sales-order-estimates:refresh") {
        if (req.configHash === "cfg-1") return { status: 200, body: { current: v1, versions: [v1], changed: true, diagnostics: [] } };
        return {
          status: 200,
          body: {
            current: v2,
            versions: [{ ...v1, status: "Superseded" }, v2],
            changed: true,
            diagnostics: [{ code: "CST-0751", severity: "warning", message: "estimate version 1 superseded", ref: "SO-1001/10" }],
          },
        };
      }
      if (url.pathname === "/api/v1/costing/sales-order-estimates:freeze") {
        const frozen = { ...req.versions[1], status: "Frozen" };
        return { status: 200, body: { versions: [req.versions[0], frozen], confirmed: frozen } };
      }
      return undefined;
    };
    const { container } = renderAt("/productcost/sales-order-versions");
    expect(screen.getByRole("heading", { name: "No estimate versions yet" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Confirm the line" })).toBeDisabled();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Refresh the estimate" }));
    const table = await screen.findByRole("table", { name: "Estimate versions" });
    expect(within(table).getByRole("row", { name: /cfg-1/ })).toHaveTextContent("1860.00");
    expect(screen.getByRole("list", { name: "Current estimate" })).toHaveTextContent("Margin 540.00");
    expect(calls[0].body).toMatchObject({ lineId: "SO-1001/10", configHash: "cfg-1", versions: [] });

    fireEvent.change(screen.getByLabelText("Configuration"), { target: { value: "cfg-2" } });
    fireEvent.click(screen.getByRole("button", { name: "Refresh the estimate" }));
    expect(await screen.findByRole("table", { name: "Refresh diagnostics" })).toHaveTextContent("CST-0751");
    expect((calls[1].body as { versions: EstimateVersion[] }).versions).toEqual([v1]);
    expect(within(screen.getByRole("table", { name: "Estimate versions" })).getByRole("row", { name: /cfg-1/ })).toHaveTextContent(
      "Superseded",
    );

    fireEvent.click(screen.getByRole("button", { name: "Confirm the line" }));
    await waitFor(() =>
      expect(within(screen.getByRole("table", { name: "Estimate versions" })).getByRole("row", { name: /cfg-2/ })).toHaveTextContent("Frozen"),
    );
    expect(calls[2].body).toMatchObject({ lineId: "SO-1001/10" });
    expect(screen.getByRole("status")).toHaveTextContent("Version 2 is frozen");
    expect(pageViolations(container)).toEqual([]);
  });

  it("reports a line that cannot be frozen", async () => {
    const broken: EstimateVersion = { ...v1, estimate: { ...estimate("0", "0", "0", "Error"), diagnostics: [] } };
    handler = (url) =>
      url.pathname.endsWith(":refresh")
        ? { status: 200, body: { current: broken, versions: [broken], changed: true, diagnostics: [] } }
        : { status: 422, body: { code: "CST-0763", detail: "estimate of line SO-1001/10 did not roll and cannot be frozen" } };
    renderAt("/productcost/sales-order-versions");
    fireEvent.click(screen.getByRole("button", { name: "Refresh the estimate" }));
    const table = await screen.findByRole("table", { name: "Estimate versions" });
    expect(table).toHaveTextContent("Not costed");
    fireEvent.click(screen.getByRole("button", { name: "Confirm the line" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The line's estimate was not frozen");
    expect(alert).toHaveTextContent("CST-0763");
  });

  it("starts a new history when the line changes", async () => {
    handler = () => ({ status: 200, body: { current: v1, versions: [v1], changed: true, diagnostics: [] } });
    renderAt("/productcost/sales-order-versions");
    fireEvent.click(screen.getByRole("button", { name: "Refresh the estimate" }));
    await screen.findByRole("table", { name: "Estimate versions" });
    fireEvent.change(screen.getByLabelText("Sales order line"), { target: { value: "SO-2/10" } });
    expect(screen.getByRole("heading", { name: "No estimate versions yet" })).toBeInTheDocument();
  });
});

describe("cost roll-up page profit center view", () => {
  it("offers and sends the ProfitCenter view", async () => {
    handler = () => ({
      status: 200,
      body: { result: { itemId: "FG-100", status: "Pending", level: 2, unitCost: "1", own: {}, rolled: {} }, split: {} },
    });
    renderAt("/productcost/roll");
    fireEvent.change(screen.getByLabelText("Valuation view"), { target: { value: "ProfitCenter" } });
    fireEvent.click(screen.getByRole("button", { name: "Roll item" }));
    await waitFor(() => expect(calls).toHaveLength(1));
    expect((calls[0].body as { options: { view: string } }).options.view).toBe("ProfitCenter");
  });
});
