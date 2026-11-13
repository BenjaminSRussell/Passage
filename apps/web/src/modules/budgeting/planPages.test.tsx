import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { setLookupFetcher } from "../../shell/lookup/entities";
import type { PlanCell, PlanView } from "./api/plans";
import { PlanDetailPage, PlanGridPage } from "./PlanPages";
import { amountMap, cellError, emptyGenerate, parseAccounts, parseCostCenterWeights, toGenerateInput, validateGenerate, changedCells, emptySpread, periodsOf, rowsOf, toSpreadInput, total, validateSpread } from "./planLogic";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const RULE = "22222222-2222-4222-8222-222222222222";
const PLAN = "33333333-3333-4333-8333-333333333333";
const S1 = "44444444-4444-4444-8444-444444444444";
const S2 = "55555555-5555-4555-8555-555555555555";

const plan: PlanView = {
  id: PLAN,
  legalEntityId: LE,
  ruleVersionId: RULE,
  name: "FY2027 operating budget",
  fiscalYear: 2027,
  currency: "USD",
  status: "Draft",
  submission: 0,
  version: 1,
  createdBy: "ana",
  createdAt: "2026-10-01T00:00:00Z",
  scenarios: [
    { id: S1, name: "Baseline", kind: "Baseline", version: 7, total: "300" },
    { id: S2, name: "Higher volume", kind: "Optimistic", version: 1, total: "330" },
  ],
  stageOwners: [],
};
const cells: PlanCell[] = [
  { account: "6100", costCenter: "CR-PLANT", period: "2027-01", amount: "100" },
  { account: "6100", costCenter: "CR-PLANT", period: "2027-02", amount: "200" },
];

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });

function stub(routes: (method: string, url: string, init: RequestInit) => Response | undefined) {
  const mock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => routes(init.method ?? "GET", String(input), init) ?? json(500, { title: "unrouted", code: "TEST-0000", detail: String(input) }));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const callsTo = (mock: ReturnType<typeof vi.fn>, method: string, fragment: string) =>
  (mock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => (init?.method ?? "GET") === method && url.includes(fragment));

function at(path: string) {
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(
        Routes,
        null,
        createElement(Route, { path: "/budgeting/plans/:id", element: createElement(PlanDetailPage) }),
        createElement(Route, { path: "/budgeting/plans/:id/scenarios/:sid", element: createElement(PlanGridPage) }),
      ),
    ),
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("plan grid logic", () => {
  it("lists the 12 periods and totals without floating point", () => {
    expect(periodsOf(2027)).toHaveLength(12);
    expect(periodsOf(2027)[0]).toBe("2027-01");
    expect(total(["0.1", "0.2", undefined, ""])).toBe("0.30");
  });

  it("rejects negative and over-precise amounts and ignores empty boxes", () => {
    expect(cellError("-1")).toMatch(/negative/);
    expect(cellError("1.23456")).toMatch(/4 decimal/);
    expect(cellError("")).toBeUndefined();
    expect(cellError("12.5")).toBeUndefined();
  });

  it("sends only changed cells and trims trailing zeros", () => {
    const loaded = amountMap(cells);
    const key = (p: string) => `6100\tCR-PLANT\t${p}`;
    const edits = new Map([
      [key("2027-01"), "100.00"],
      [key("2027-02"), "250.50"],
      [key("2027-03"), "75"],
      [key("2027-04"), ""],
    ]);
    expect(changedCells(loaded, edits)).toEqual([
      { account: "6100", costCenter: "CR-PLANT", period: "2027-02", amount: "250.5" },
      { account: "6100", costCenter: "CR-PLANT", period: "2027-03", amount: "75" },
    ]);
  });

  it("orders rows by account then cost center and merges added rows", () => {
    expect(rowsOf(cells, [{ account: "6000", costCenter: "A" }]).map((r) => `${r.account}/${r.costCenter}`)).toEqual(["6000/A", "6100/CR-PLANT"]);
  });

  it("validates the spread the way the service does", () => {
    const f = emptySpread();
    expect(Object.keys(validateSpread(f)).sort()).toEqual(["account", "costCenters", "total"].sort());
    const ok = { ...f, account: "6100", total: "1400", method: "custom" as const, costCenters: [{ costCenter: "CC1", weight: "1" }] };
    expect(validateSpread(ok)).toEqual({});
    expect(validateSpread({ ...ok, periodWeights: Array(12).fill("0") }).periodWeights).toMatch(/above zero/);
    expect(validateSpread({ ...ok, costCenters: [{ costCenter: "CC1", weight: "0" }] }).costCenters).toMatch(/above zero/);
    expect(toSpreadInput(ok).periodWeights).toHaveLength(12);
    expect(toSpreadInput({ ...ok, method: "even" })).not.toHaveProperty("periodWeights");
  });
});

describe("plan pages", () => {
  it("shows the scenarios with their totals and compares two of them", async () => {
    const mock = stub((method, url) => {
      if (url.endsWith(`/api/v1/budget/plans/${PLAN}`)) return json(200, plan);
      if (url.includes(`/control-rules/${RULE}`)) return json(200, { id: RULE, legalEntityId: LE, code: "DEPT", version: 3, warnPct: "90", blockPct: "105", currency: "USD", effectiveFrom: "2026-01-01" });
      if (url.includes("/org/operating-units")) return json(200, { items: [] });
      if (url.includes("/plans/") && url.includes("scenarios:compare"))
        return json(200, { groupBy: "account", base: plan.scenarios[0], other: plan.scenarios[1], rows: [{ key: "6100", base: "300", other: "330", delta: "30", deltaPct: "10.00" }], delta: "30" });
      return method === "GET" ? json(404, { code: "COM-0404", title: "no" }) : undefined;
    });
    const { container } = at(`/budgeting/plans/${PLAN}`);
    await screen.findByText("FY2027 operating budget");
    expect(screen.getByText("DEPT version 3")).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Compare"), { target: { value: S1 } });
    fireEvent.change(screen.getByLabelText("With"), { target: { value: S2 } });
    await screen.findByText("10.00%");
    expect(callsTo(mock, "GET", "scenarios:compare")[0][0]).toContain(`base=${S1}&other=${S2}&groupBy=account`);
    expect(screen.getByText("Total change: 30.00 USD.")).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
  });

  it("saves edited cells with the grid version in If-Match and shows a stale grid message on 412", async () => {
    let status = 200;
    const mock = stub((method, url) => {
      if (url.endsWith(`/api/v1/budget/plans/${PLAN}`)) return json(200, plan);
      if (url.includes(`/scenarios/${S1}/grid`) && method === "GET") return json(200, { scenario: plan.scenarios[0], cells, total: "300", truncated: false });
      if (url.includes(`/scenarios/${S1}/grid`) && method === "PUT")
        return status === 200 ? json(200, { scenarioId: S1, version: 8, cells: 2, total: "350" }) : json(412, { code: "BUD-0050", title: "stale", detail: "the grid changed" });
      return undefined;
    });
    const { container } = at(`/budgeting/plans/${PLAN}/scenarios/${S1}`);
    const jan = await screen.findByLabelText("6100 CR-PLANT Jan");
    expect(screen.queryByText(S1)).toBeNull();
    fireEvent.change(jan, { target: { value: "-5" } });
    expect((screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(jan, { target: { value: "150" } });
    fireEvent.click(screen.getByRole("button", { name: "Save 1 changes" }));
    await waitFor(() => expect(callsTo(mock, "PUT", "/grid")).toHaveLength(1));
    const [, init] = callsTo(mock, "PUT", "/grid")[0];
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"7"');
    expect(JSON.parse(String(init.body))).toEqual({ cells: [{ account: "6100", costCenter: "CR-PLANT", period: "2027-01", amount: "150" }] });
    await screen.findByText("Saved 1 amounts.");
    status = 412;
    fireEvent.change(await screen.findByLabelText("6100 CR-PLANT Feb"), { target: { value: "210" } });
    fireEvent.click(screen.getByRole("button", { name: "Save 1 changes" }));
    await screen.findByText("Someone else changed this grid");
    expect(pageViolations(container)).toEqual([]);
  });

  it("makes the grid read only once the plan is submitted", async () => {
    stub((_m, url) => {
      if (url.endsWith(`/api/v1/budget/plans/${PLAN}`)) return json(200, { ...plan, status: "Submitted" });
      if (url.includes("/grid")) return json(200, { scenario: plan.scenarios[0], cells, total: "300", truncated: false });
      return undefined;
    });
    at(`/budgeting/plans/${PLAN}/scenarios/${S1}`);
    await screen.findByText("Grid total: 300.00 USD");
    expect(screen.queryByLabelText("6100 CR-PLANT Jan")).toBeNull();
    expect(screen.queryByRole("button", { name: /Save/ })).toBeNull();
  });
});

describe("plan generation", () => {
  it("reads accounts and cost center weights the way a person writes them", () => {
    expect(parseAccounts(" 6100, 6200 ,,\n6300")).toEqual(["6100", "6200", "6300"]);
    expect(parseCostCenterWeights("CC1=2, CC2")).toEqual([
      { costCenter: "CC1", weight: "2" },
      { costCenter: "CC2", weight: "1" },
    ]);
    expect(parseCostCenterWeights("CC1=lots")).toBeNull();
  });

  it("checks the form like the service and builds the request", () => {
    expect(validateGenerate({ ...emptyGenerate(), source: "prior-plan", adjustment: "", accounts: "", costCenters: "" })).toEqual({});
    expect(validateGenerate({ ...emptyGenerate(), source: "prior-plan", adjustment: "-100", accounts: "6100,6100", costCenters: "" })).toMatchObject({ adjustment: expect.any(String), accounts: "An account appears twice." });
    expect(Object.keys(validateGenerate({ ...emptyGenerate(), source: "gl-actuals", adjustment: "", accounts: "", costCenters: "" })).sort()).toEqual(["accounts", "costCenters"]);
    expect(validateGenerate({ ...emptyGenerate(), source: "gl-actuals", adjustment: "5", accounts: "6100", costCenters: "CC1=0" }).costCenters).toMatch(/above zero/);
    expect(toGenerateInput({ ...emptyGenerate(), source: "gl-actuals", adjustment: "5", accounts: "6100", costCenters: "CC1=2, CC2" })).toEqual({
      source: "gl-actuals",
      adjustmentPct: "5",
      accounts: ["6100"],
      costCenters: [
        { costCenter: "CC1", weight: "2" },
        { costCenter: "CC2", weight: "1" },
      ],
    });
    expect(toGenerateInput({ ...emptyGenerate(), source: "prior-plan", adjustment: "", accounts: "", costCenters: "CC1" })).toEqual({ source: "prior-plan" });
  });

  it("checks the driver sources and builds their requests", () => {
    const positions = { ...emptyGenerate(), source: "positions" as const };
    expect(Object.keys(validateGenerate(positions)).sort()).toEqual(["account", "departments.0.costCenter", "departments.0.departmentId", "rates.0.jobCode", "rates.0.monthlyCost"]);
    const filled = { ...positions, account: "6000", rates: [{ jobCode: "ENG", monthlyCost: "6500" }, { jobCode: "ENG", monthlyCost: "0" }], departments: [{ departmentId: "d1", costCenter: "CC-ENG" }] };
    expect(validateGenerate(filled)).toEqual({ "rates.1.jobCode": "This job appears twice.", "rates.1.monthlyCost": expect.any(String) });
    expect(toGenerateInput({ ...filled, rates: filled.rates.slice(0, 1) })).toEqual({ source: "positions", account: "6000", rates: [{ jobCode: "ENG", monthlyCost: "6500" }], departments: [{ departmentId: "d1", costCenter: "CC-ENG" }] });

    const projects = { ...emptyGenerate(), source: "projects" as const, account: "5100", projects: [{ projectId: "P-1001", modelId: "m1", costCenter: "CC-PRJ", amount: "sales" as const }, { projectId: "P-1001", modelId: "m1", costCenter: "CC-PRJ", amount: "cost" as const }] };
    expect(validateGenerate(projects)).toEqual({ "projects.1.modelId": "This project and forecast appear twice." });
    expect(validateGenerate({ ...projects, projects: [{ ...projects.projects[0], projectId: "not a code" }] })).toEqual({ "projects.0.projectId": "Choose the project." });
    expect(toGenerateInput({ ...projects, projects: projects.projects.slice(0, 1) })).toEqual({ source: "projects", account: "5100", projects: [{ projectId: "P-1001", modelId: "m1", costCenter: "CC-PRJ", amount: "sales" }] });

    const demand = { ...emptyGenerate(), source: "demand" as const, account: "4000", modelCode: "FC-2027", costCenters: "CC1=3, CC2", unitPrices: [{ itemId: "i1", price: "12.5" }] };
    expect(validateGenerate(demand)).toEqual({});
    expect(validateGenerate({ ...demand, modelCode: "", costCenters: "" })).toEqual({ modelCode: expect.any(String), costCenters: "Name at least one cost center." });
    expect(toGenerateInput({ ...demand, adjustment: "10" })).toEqual({
      source: "demand",
      adjustmentPct: "10",
      account: "4000",
      modelCode: "FC-2027",
      unitPrices: [{ itemId: "i1", price: "12.5" }],
      costCenters: [
        { costCenter: "CC1", weight: "3" },
        { costCenter: "CC2", weight: "1" },
      ],
    });

    const assets = { ...emptyGenerate(), source: "fixed-assets" as const, account: "6500", costCenters: "CC1" };
    expect(validateGenerate(assets)).toEqual({});
    expect(toGenerateInput({ ...assets, book: " TAX " })).toEqual({ source: "fixed-assets", account: "6500", book: "TAX", costCenters: [{ costCenter: "CC1", weight: "1" }] });
  });

  it("fills the grid from last year's plan with the grid version in If-Match", async () => {
    const mock = stub((method, url) => {
      if (url.endsWith(`/api/v1/budget/plans/${PLAN}`)) return json(200, plan);
      if (url.includes(`/scenarios/${S1}/grid`) && method === "GET") return json(200, { scenario: plan.scenarios[0], cells, total: "300", truncated: false });
      if (url.includes(`/scenarios/${S1}:generate`)) return json(200, { scenarioId: S1, version: 8, cells: 12, total: "900" });
      return undefined;
    });
    at(`/budgeting/plans/${PLAN}/scenarios/${S1}`);
    await screen.findByRole("heading", { name: "Fill the grid from a source" });
    fireEvent.change(screen.getByLabelText(/Change every amount by percent/), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Fill the grid" }));
    await waitFor(() => expect(callsTo(mock, "POST", ":generate")).toHaveLength(1));
    const [, init] = callsTo(mock, "POST", ":generate")[0];
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"7"');
    expect(JSON.parse(String(init.body))).toEqual({ source: "prior-plan", adjustmentPct: "5" });
    await screen.findByText("Amounts generated.");
  });
});

describe("plan generation from the drivers", () => {
  it("picks a project by its code, lists its forecasts and sends the projects source", async () => {
    const user = userEvent.setup();
    const MODEL = "0196a6a4-0000-7000-8000-0000000000e2";
    const mock = stub((method, url) => {
      if (url.endsWith(`/api/v1/budget/plans/${PLAN}`)) return json(200, plan);
      if (url.includes(`/scenarios/${S1}/grid`) && method === "GET") return json(200, { scenario: plan.scenarios[0], cells, total: "300", truncated: false });
      if (url.includes("/api/v1/projects/P-1001/forecasts")) return json(200, { items: [{ id: MODEL, projectId: "P-1001", name: "Rebuild forecast", currency: "USD" }] });
      if (url.includes("/api/v1/projects")) return json(200, { items: [{ id: "P-1001", legalEntityId: plan.legalEntityId, projectType: "FIXED_PRICE", stage: "IN_PROCESS", currency: "USD" }] });
      if (url.includes(`/scenarios/${S1}:generate`)) return json(200, { scenarioId: S1, version: 8, cells: 12, total: "900" });
      return undefined;
    });
    setLookupFetcher(async (url) => mock(url, {}));
    at(`/budgeting/plans/${PLAN}/scenarios/${S1}`);
    await screen.findByRole("heading", { name: "Fill the grid from a source" });
    fireEvent.change(screen.getByLabelText("Start from"), { target: { value: "projects" } });
    fireEvent.click(screen.getByRole("button", { name: "Fill the grid" }));
    expect(await screen.findByText("Choose the project.")).toBeInTheDocument();
    expect(callsTo(mock, "POST", ":generate")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Plan on account"), { target: { value: "5100" } });
    await user.type(screen.getByRole("combobox", { name: "Project" }), "P-10");
    await user.click(await screen.findByRole("option", { name: /P-1001/ }));
    const forecast = await screen.findByRole("option", { name: "Rebuild forecast (USD)" });
    fireEvent.change(forecast.closest("select")!, { target: { value: MODEL } });
    fireEvent.change(screen.getByLabelText("Plan the"), { target: { value: "sales" } });
    fireEvent.change(within(screen.getByRole("group", { name: "Project 1" })).getByLabelText("Cost center"), { target: { value: "CC-PRJ" } });
    fireEvent.click(screen.getByRole("button", { name: "Fill the grid" }));
    await waitFor(() => expect(callsTo(mock, "POST", ":generate")).toHaveLength(1));
    const [, init] = callsTo(mock, "POST", ":generate")[0];
    expect(JSON.parse(String(init.body))).toEqual({ source: "projects", account: "5100", projects: [{ projectId: "P-1001", modelId: MODEL, costCenter: "CC-PRJ", amount: "sales" }] });
    await screen.findByText("Amounts generated.");
  });
});

describe("plan workbook", () => {
  const XLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";

  it("checks a workbook without writing, then imports it with the grid version and the file as the body", async () => {
    const mock = stub((method, url, init) => {
      if (url.endsWith(`/api/v1/budget/plans/${PLAN}`)) return json(200, plan);
      if (url.includes(`/scenarios/${S1}/grid`) && method === "GET") return json(200, { scenario: plan.scenarios[0], cells, total: "300", truncated: false });
      if (url.includes(`/scenarios/${S1}:import?dryRun=true`)) return json(200, { valid: true, dryRun: true, rows: 3, cells: 36, total: "1675.3734" });
      if (url.includes(`/scenarios/${S1}:import`)) return init.body instanceof Blob ? json(200, { scenarioId: S1, version: 8, cells: 36, total: "1675.3734" }) : undefined;
      return undefined;
    });
    at(`/budgeting/plans/${PLAN}/scenarios/${S1}`);
    const file = new File(["x"], "plan.xlsx", { type: XLSX });
    fireEvent.change(await screen.findByLabelText("Edited workbook"), { target: { files: [file] } });
    fireEvent.click(screen.getByRole("button", { name: "Check the workbook" }));
    await screen.findByText("The workbook is valid");
    expect(screen.getByText(/3 rows and 36 amounts/)).toBeTruthy();
    expect(callsTo(mock, "POST", ":import?dryRun=true")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Import the workbook" }));
    await screen.findByText("The workbook was imported.");
    const [, init] = callsTo(mock, "POST", ":import")[1];
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"7"');
    expect((init.headers as Record<string, string>)["content-type"]).toBe(XLSX);
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
  });

  it("lists every problem of a workbook by its cell and writes nothing", async () => {
    stub((method, url) => {
      if (url.endsWith(`/api/v1/budget/plans/${PLAN}`)) return json(200, plan);
      if (url.includes(`/scenarios/${S1}/grid`) && method === "GET") return json(200, { scenario: plan.scenarios[0], cells, total: "300", truncated: false });
      if (url.includes(`/scenarios/${S1}:import`))
        return json(400, { code: "COM-0001", title: "Bad Request", detail: "request failed validation", errors: { "Plan!C10": "must be a number", "Plan!D2": "must not be negative" } });
      return undefined;
    });
    const { container } = at(`/budgeting/plans/${PLAN}/scenarios/${S1}`);
    fireEvent.change(await screen.findByLabelText("Edited workbook"), { target: { files: [new File(["x"], "plan.xlsx")] } });
    fireEvent.click(screen.getByRole("button", { name: "Import the workbook" }));
    await screen.findByText(/The workbook has problems/);
    const list = screen.getByRole("list", { name: "Problems in the workbook" });
    expect(list.textContent).toContain("Cell D2: must not be negative");
    expect(list.textContent).toContain("Cell C10: must be a number");
    expect(pageViolations(container)).toEqual([]);
  });

  it("downloads the grid as a workbook", async () => {
    const mock = stub((method, url) => {
      if (url.endsWith(`/api/v1/budget/plans/${PLAN}`)) return json(200, plan);
      if (url.includes(`/scenarios/${S1}/grid`) && method === "GET") return json(200, { scenario: plan.scenarios[0], cells, total: "300", truncated: false });
      if (url.includes(`/scenarios/${S1}:export`)) return new Response(new Uint8Array([80, 75, 3, 4]), { status: 200, headers: { ETag: '"7"', "content-type": XLSX } });
      return undefined;
    });
    URL.createObjectURL = vi.fn(() => "blob:plan");
    URL.revokeObjectURL = vi.fn();
    at(`/budgeting/plans/${PLAN}/scenarios/${S1}`);
    fireEvent.click(await screen.findByRole("button", { name: "Download as spreadsheet" }));
    await screen.findByText(/The workbook was downloaded/);
    expect(callsTo(mock, "GET", ":export")).toHaveLength(1);
  });
});
