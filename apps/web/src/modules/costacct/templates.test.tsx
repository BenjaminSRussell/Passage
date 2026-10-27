import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { AllocationTemplate, CostElement, CostObject, StatisticalMeasure, TemplateRun } from "./api/client";
import { formulaRefs, validateMeasureValues, validateTemplate } from "./logic";
import { CreateMeasurePage, MeasuresPage, MeasureValuesPage } from "./MeasuresPage";
import costacct from "./module";
import { TemplateDetailPage, TemplateEditorPage, TemplateRunDetailPage, TemplatesPage } from "./TemplatesPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const QC = "22222222-2222-4222-8222-222222222222";
const ORDER = "33333333-3333-4333-8333-333333333333";
const SEC = "55555555-5555-4555-8555-555555555555";
const MAT = "66666666-6666-4666-8666-666666666666";
const M1 = "77777777-7777-4777-8777-777777777777";
const T1 = "88888888-8888-4888-8888-888888888888";
const RUN = "99999999-9999-4999-8999-999999999999";

const objects: CostObject[] = [
  { id: QC, legalEntityId: LE, kind: "CostCenter", code: "QUALITY", name: "Quality control" },
  { id: ORDER, legalEntityId: LE, kind: "InternalOrder", code: "ORDER-1", name: "Order 1" },
];
const elements: CostElement[] = [
  { id: MAT, legalEntityId: LE, kind: "Primary", code: "MATERIAL", name: "Material", mainAccountFrom: "500000", mainAccountTo: "509999" },
  { id: SEC, legalEntityId: LE, kind: "Secondary", code: "INSPECT", name: "Inspection" },
];
const measure: StatisticalMeasure = { id: M1, legalEntityId: LE, code: "inspectionLots", name: "Inspection lots", unit: "lot" };
const template: AllocationTemplate = {
  id: T1,
  legalEntityId: LE,
  code: "TPL-INSP",
  name: "Inspection",
  currency: "USD",
  senderCostObjectId: QC,
  allowNegative: false,
  lines: [{ receiverCostObjectId: ORDER, secondaryElementId: SEC, formula: "25.00 × m.inspectionLots + 0.005 × b.MATERIAL" }],
  version: 1,
  updatedAt: "2026-10-01T08:00:00Z",
};
const run: TemplateRun = {
  id: RUN,
  legalEntityId: LE,
  templateId: T1,
  templateVersion: 1,
  period: "2026-09",
  currency: "USD",
  senderCostObjectId: QC,
  total: "200",
  lines: [{ receiverCostObjectId: ORDER, secondaryElementId: SEC, amount: "200", divByZero: false }],
  warnings: [],
  status: "Completed",
  createdAt: "2026-10-02T08:00:00Z",
};

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });

function stubRoutes(routes: Record<string, Handler>) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    const method = init.method ?? "GET";
    const key = Object.keys(routes)
      .filter((k) => {
        const [m, prefix] = k.split(" ");
        return m === method && url.startsWith(prefix);
      })
      .sort((a, b) => b.length - a.length)[0];
    if (!key) return json(500, { title: "unrouted", code: "TEST-0000", detail: `${method} ${url}` });
    return routes[key](url, init);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function calls(fetchMock: ReturnType<typeof vi.fn>, method: string, fragment: string) {
  return (fetchMock.mock.calls as unknown as [string, RequestInit][]).filter(([url, init]) => (init?.method ?? "GET") === method && url.includes(fragment));
}

const ledger = (extra: Record<string, Handler> = {}) =>
  stubRoutes({
    "GET /api/v1/controlling/cost-objects": () => json(200, { items: objects }),
    "GET /api/v1/controlling/cost-elements": () => json(200, { items: elements }),
    "GET /api/v1/controlling/statistical-measures": () => json(200, { items: [measure] }),
    "GET /api/v1/controlling/allocation-templates": () => json(200, { items: [template] }),
    [`GET /api/v1/controlling/allocation-templates/${T1}`]: () => json(200, template),
    "GET /api/v1/controlling/template-allocations/runs": () => json(200, { items: [run] }),
    [`GET /api/v1/controlling/template-allocations/runs/${RUN}`]: () => json(200, run),
    ...extra,
  });

function at(path: string) {
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(
        Routes,
        null,
        createElement(Route, { path: "/costacct/measures", element: createElement(MeasuresPage) }),
        createElement(Route, { path: "/costacct/measures/new", element: createElement(CreateMeasurePage) }),
        createElement(Route, { path: "/costacct/measures/:id", element: createElement(MeasureValuesPage) }),
        createElement(Route, { path: "/costacct/templates", element: createElement(TemplatesPage) }),
        createElement(Route, { path: "/costacct/templates/new", element: createElement(TemplateEditorPage) }),
        createElement(Route, { path: "/costacct/templates/:id", element: createElement(TemplateDetailPage) }),
        createElement(Route, { path: "/costacct/templates/:id/edit", element: createElement(TemplateEditorPage) }),
        createElement(Route, { path: "/costacct/template-runs/:id", element: createElement(TemplateRunDetailPage) }),
      ),
    ),
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("template logic", () => {
  it("finds the measure and element references of a formula", () => {
    expect(formulaRefs("25.00 × m.inspectionLots + 0.005 × b.MATERIAL + max(m.inspectionLots, 1)")).toEqual({ measures: ["inspectionLots"], elements: ["MATERIAL"] });
    expect(formulaRefs("10 / 2")).toEqual({ measures: [], elements: [] });
  });

  it("flags unknown references, a receiver equal to the sender and missing fields", () => {
    const form = { legalEntityId: LE, code: "TPL-INSP", name: "Inspection", currency: "USD", senderCostObjectId: QC };
    const known = { measures: ["inspectionLots"], elements: ["MATERIAL", "INSPECT"] };
    expect(validateTemplate(form, template.lines, known)).toEqual([]);
    expect(validateTemplate(form, [{ receiverCostObjectId: QC, secondaryElementId: SEC, formula: "25 × m.lots + b.MAT" }], known)).toEqual([
      "Line 1: the receiver must differ from the sender.",
      "Line 1: m.lots is not a statistical measure of this legal entity.",
      "Line 1: b.MAT is not a cost element of this legal entity.",
    ]);
    expect(validateTemplate({ ...form, code: "x", currency: "us" }, [], known)).toHaveLength(3);
  });

  it("validates measure values and skips blank rows", () => {
    expect(validateMeasureValues("2026-09", [{ costObjectId: ORDER, value: "4" }, { costObjectId: QC, value: "" }])).toEqual([]);
    expect(validateMeasureValues("2026-13", [{ costObjectId: ORDER, label: "ORDER-1", value: "-1" }])).toEqual([
      "Enter the period as YYYY-MM.",
      "ORDER-1: enter zero or more with up to 4 decimals.",
    ]);
  });
});

describe("MeasuresPage", () => {
  it("lists measures with their formula name, shows empty and error states", async () => {
    let mode: "ok" | "empty" | "fail" = "fail";
    const fetchMock = stubRoutes({
      "GET /api/v1/controlling/statistical-measures": () =>
        mode === "fail" ? json(503, { title: "down", code: "COM-0500", detail: "internal error" }) : json(200, { items: mode === "ok" ? [measure] : [] }),
    });
    const { container } = at("/costacct/measures");
    expect(await findAlert()).toHaveTextContent("COM-0500");
    mode = "empty";
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No statistical measures yet")).toBeInTheDocument();
    mode = "ok";
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    await waitFor(() => expect(calls(fetchMock, "GET", `statistical-measures?legalEntityId=${LE}`)).toHaveLength(1));
    const table = await findTable({ name: "Statistical measures" });
    expect(await within(table).findByText("m.inspectionLots")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a measure with an Idempotency-Key", async () => {
    const fetchMock = ledger({ "POST /api/v1/controlling/statistical-measures": () => json(201, measure) });
    at("/costacct/measures/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "2lots" } });
    fireEvent.click(screen.getByRole("button", { name: "Save measure" }));
    expect(await screen.findByText("Start with a letter or _, then up to 39 letters, digits or _.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "statistical-measures")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "inspectionLots" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Inspection lots" } });
    fireEvent.change(screen.getByLabelText("Unit"), { target: { value: "lot" } });
    fireEvent.click(screen.getByRole("button", { name: "Save measure" }));
    expect(await screen.findByRole("heading", { name: "inspectionLots Inspection lots" })).toBeInTheDocument();
    const [, init] = calls(fetchMock, "POST", "statistical-measures")[0];
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("shows a period's values and loads new ones per cost object", async () => {
    const fetchMock = ledger({
      [`GET /api/v1/controlling/statistical-measures/${M1}/values`]: () => json(200, { items: [{ costObjectId: ORDER, value: "4", updatedAt: "2026-10-01T08:00:00Z" }] }),
      [`POST /api/v1/controlling/statistical-measures/${M1}/values`]: () => json(200, { measureId: M1, period: "2026-09", loaded: 1 }),
    });
    const { container } = at(`/costacct/measures/${M1}`);
    fireEvent.change(await screen.findByLabelText("Period"), { target: { value: "2026-09" } });
    const values = await findTable({ name: "Measure values" });
    expect(await within(values).findByText("ORDER-1")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Load values" });
    fireEvent.change(await within(form).findByLabelText("ORDER-1 Order 1"), { target: { value: "6" } });
    fireEvent.click(within(form).getByRole("button", { name: "Load values" }));
    expect(await screen.findByText("1 values loaded for 2026-09.")).toBeInTheDocument();
    const [, init] = calls(fetchMock, "POST", "/values")[0];
    expect(JSON.parse(init.body as string)).toEqual({ legalEntityId: LE, period: "2026-09", values: [{ costObjectId: ORDER, value: "6" }] });
  });
});

describe("TemplatesPage", () => {
  it("lists templates and opens one", async () => {
    ledger();
    const { container } = at("/costacct/templates");
    const table = await findTable({ name: "Allocation templates" });
    expect(await within(table).findByText("Inspection")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "TPL-INSP" }));
    expect(await screen.findByRole("heading", { name: "Template TPL-INSP" })).toBeInTheDocument();
  });

  it("shows the empty state and a load error with a retry", async () => {
    let fail = true;
    stubRoutes({ "GET /api/v1/controlling/allocation-templates": () => (fail ? json(503, { title: "down", code: "COM-0500" }) : json(200, { items: [] })) });
    at("/costacct/templates");
    expect(await findAlert()).toHaveTextContent("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No allocation templates yet")).toBeInTheDocument();
  });
});

describe("TemplateEditorPage", () => {
  it("catches an unknown measure on the page, then saves version 1 with expectedVersion 0", async () => {
    const fetchMock = ledger({ "POST /api/v1/controlling/allocation-templates": () => json(201, template) });
    const { container } = at("/costacct/templates/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "tpl-insp" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Inspection" } });
    await screen.findAllByRole("option", { name: "QUALITY Quality control" });
    fireEvent.change(screen.getByLabelText("Sender"), { target: { value: QC } });
    fireEvent.change(screen.getByLabelText("Receiver"), { target: { value: ORDER } });
    fireEvent.change(screen.getByLabelText("Secondary element"), { target: { value: SEC } });
    fireEvent.change(screen.getByLabelText("Formula"), { target: { value: "25.00 × m.lots" } });
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));
    expect(await screen.findByText("Line 1: m.lots is not a statistical measure of this legal entity.")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "allocation-templates")).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Formula"), { target: { value: "25.00 × m.inspectionLots + 0.005 × b.MATERIAL" } });
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));
    expect(await screen.findByRole("heading", { name: "Template TPL-INSP" })).toBeInTheDocument();
    const [, init] = calls(fetchMock, "POST", "allocation-templates")[0];
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.parse(init.body as string)).toEqual({
      legalEntityId: LE,
      code: "TPL-INSP",
      name: "Inspection",
      currency: "USD",
      senderCostObjectId: QC,
      allowNegative: false,
      lines: [{ receiverCostObjectId: ORDER, secondaryElementId: SEC, formula: "25.00 × m.inspectionLots + 0.005 × b.MATERIAL" }],
      expectedVersion: 0,
    });
  });

  it("edits an existing template with the version it read and shows the server's conflict", async () => {
    const fetchMock = ledger({
      "POST /api/v1/controlling/allocation-templates": () => json(409, { title: "conflict", code: "CST-0018", detail: "template TPL-INSP is at version 2, not 1; reload it and save again" }),
    });
    at(`/costacct/templates/${T1}/edit`);
    expect(await screen.findByRole("heading", { name: "Edit TPL-INSP" })).toBeInTheDocument();
    await screen.findAllByRole("option", { name: "ORDER-1 Order 1" });
    fireEvent.click(screen.getByRole("button", { name: "Save version 2" }));
    expect(await screen.findByText(/CST-0018: template TPL-INSP is at version 2/)).toBeInTheDocument();
    expect(JSON.parse(calls(fetchMock, "POST", "allocation-templates")[0][1].body as string)).toMatchObject({ expectedVersion: 1, code: "TPL-INSP" });
  });
});

describe("TemplateDetailPage", () => {
  it("dry-runs a period without a key, then runs it with one and opens the run (AC-38.14)", async () => {
    const fetchMock = ledger({
      [`POST /api/v1/controlling/allocation-templates/${T1}:dry-run`]: () =>
        json(200, { templateId: T1, templateVersion: 1, period: "2026-09", lines: run.lines, totalIn: "200", warnings: [] }),
      "POST /api/v1/controlling/template-allocations/runs": () => json(201, run),
    });
    const { container } = at(`/costacct/templates/${T1}`);
    expect(await screen.findByRole("heading", { name: "Template TPL-INSP" })).toBeInTheDocument();
    expect(await within(screen.getByRole("table", { name: "Template runs" })).findByText("2026-09")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-09" } });
    fireEvent.click(screen.getByRole("button", { name: "Dry run" }));
    const dry = await findTable({ name: "Dry run" });
    expect(within(dry).getByText("200.00")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Dry run totals" })).toHaveTextContent("Sender credited 200.00");
    expect(pageViolations(container)).toEqual([]);
    const [, dryInit] = calls(fetchMock, "POST", ":dry-run")[0];
    expect((dryInit.headers as Record<string, string>)["Idempotency-Key"]).toBeUndefined();

    fireEvent.click(screen.getByRole("button", { name: "Run for 2026-09" }));
    expect(await screen.findByRole("heading", { name: "Template run 2026-09" })).toBeInTheDocument();
    const [, runInit] = calls(fetchMock, "POST", "template-allocations/runs")[0];
    expect((runInit.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.parse(runInit.body as string)).toEqual({ templateId: T1, period: "2026-09" });
  });

  it("shows a refused run's problem, and zero-divisor warnings on a dry run", async () => {
    ledger({
      [`POST /api/v1/controlling/allocation-templates/${T1}:dry-run`]: () =>
        json(200, { templateId: T1, templateVersion: 1, period: "2026-09", lines: [{ ...run.lines[0], amount: "0", divByZero: true }], totalIn: "0", warnings: [`CO-0622: a divisor is zero for receiver ${ORDER}; the division evaluates to 0`] }),
      "POST /api/v1/controlling/template-allocations/runs": () => json(409, { title: "conflict", code: "CST-0002", detail: "this template already has a completed run for the period" }),
    });
    at(`/costacct/templates/${T1}`);
    fireEvent.change(await screen.findByLabelText("Period"), { target: { value: "2026-09" } });
    fireEvent.click(screen.getByRole("button", { name: "Dry run" }));
    expect(await screen.findByText(/CO-0622: a divisor is zero/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Run for 2026-09" }));
    expect(await screen.findByText(/CST-0002: this template already has a completed run/)).toBeInTheDocument();
  });
});

describe("TemplateRunDetailPage", () => {
  it("shows receivers and the sender credit", async () => {
    ledger();
    const { container } = at(`/costacct/template-runs/${RUN}`);
    expect(await screen.findByRole("heading", { name: "Template run 2026-09" })).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Run totals" })).toHaveTextContent("Sender credited 200.00");
    expect(await within(screen.getByRole("table", { name: "Run lines" })).findByText("ORDER-1")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a load error", async () => {
    stubRoutes({ [`GET /api/v1/controlling/template-allocations/runs/${RUN}`]: () => json(404, { title: "not found", code: "CST-0019", detail: "template allocation run not found" }) });
    at(`/costacct/template-runs/${RUN}`);
    expect(await findAlert()).toHaveTextContent("CST-0019");
  });
});

describe("module registration", () => {
  it("registers the measure, template and run routes with nav entries", () => {
    const paths = costacct.routes.map((r) => r.path);
    expect(paths).toEqual(
      expect.arrayContaining(["/costacct/measures", "/costacct/measures/:id", "/costacct/templates", "/costacct/templates/new", "/costacct/templates/:id", "/costacct/templates/:id/edit", "/costacct/template-runs/:id"]),
    );
    expect(costacct.nav.map((n) => n.to)).toEqual(expect.arrayContaining(["/costacct/measures", "/costacct/templates"]));
  });
});
