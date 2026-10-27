import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Program, ProgramPosition, ProgramView } from "./api/client";
import { positionTree, validateAssignment, validateDistribution, validateProgram } from "./logic";
import budgeting from "./module";
import { CreateProgramPage, ProgramDetailPage, ProgramsPage } from "./ProgramsPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const P1 = "77777777-7777-4777-8777-777777777777";
const PLANT = "88888888-8888-4888-8888-888888888888";
const LINE = "99999999-9999-4999-8999-999999999999";

const program: Program = {
  id: P1,
  legalEntityId: LE,
  code: "CAPEX-26",
  name: "Plant capex",
  approvalYear: 2026,
  programType: "Capex",
  currency: "USD",
  budget: "5000000",
  distributed: "4800000",
  status: "Open",
  version: 3,
  createdAt: "2026-01-05T08:00:00Z",
};

const position = (p: Partial<ProgramPosition> & Pick<ProgramPosition, "id" | "code" | "level">): ProgramPosition => ({
  programId: P1,
  category: "Capex",
  budget: "0",
  distributed: "0",
  undistributed: "0",
  plan: "0",
  actual: "0",
  commitment: "0",
  available: "0",
  carriedForward: "0",
  assignments: [],
  ...p,
});

const view: ProgramView = {
  ...program,
  plan: "2500000",
  actual: "1000000",
  commitment: "500000",
  available: "3500000",
  carriedForward: "0",
  positions: [
    position({ id: PLANT, code: "PLANT", level: 1, budget: "4800000", distributed: "3000000", undistributed: "1800000", plan: "2500000", actual: "1000000", commitment: "500000", available: "3300000" }),
    position({
      id: LINE,
      parentId: PLANT,
      code: "LINE1",
      level: 2,
      budget: "3000000",
      plan: "2500000",
      actual: "1000000",
      commitment: "500000",
      available: "1500000",
      assignments: [{ id: "a1", positionId: LINE, objectType: "CapexRequest", objectRef: "CR-000123", plan: "2500000", actual: "1000000", commitment: "500000", updatedAt: "2026-03-01T08:00:00Z" }],
    }),
  ],
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

function at(path: string) {
  return render(
    createElement(
      MemoryRouter,
      { initialEntries: [path] },
      createElement(
        Routes,
        null,
        createElement(Route, { path: "/budgeting/programs", element: createElement(ProgramsPage) }),
        createElement(Route, { path: "/budgeting/programs/new", element: createElement(CreateProgramPage) }),
        createElement(Route, { path: "/budgeting/programs/:id", element: createElement(ProgramDetailPage) }),
      ),
    ),
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("program logic", () => {
  it("validates the create form", () => {
    expect(validateProgram({ legalEntityId: LE, code: "CAPEX-26", name: "", approvalYear: "2026", currency: "USD", budget: "5000000.00" })).toEqual({});
    const errors = validateProgram({ legalEntityId: "x", code: "c", name: "", approvalYear: "26", currency: "usd", budget: "-1" });
    expect(Object.keys(errors).sort()).toEqual(["approvalYear", "budget", "code", "currency", "legalEntityId"]);
  });

  it("refuses a distribution above the parent, counting children that keep their budget (AC-38.13)", () => {
    const existing = [{ code: "PLANT", budget: "4800000" }];
    expect(validateDistribution("5000000", existing, [{ positionCode: "IT", category: "", budget: "300000" }])).toEqual([
      "The positions would exceed the parent budget by 100000.00 (CO-0641).",
    ]);
    expect(validateDistribution("5000000", existing, [{ positionCode: "IT", category: "", budget: "200000" }])).toEqual([]);
    // Re-setting an existing child replaces its budget instead of adding to it.
    expect(validateDistribution("5000000", existing, [{ positionCode: "PLANT", category: "", budget: "5000000" }])).toEqual([]);
    expect(validateDistribution("10", [], [{ positionCode: "A", category: "", budget: "1" }, { positionCode: "A", category: "", budget: "1" }])).toEqual(["Row 2: A appears twice."]);
  });

  it("validates an assignment", () => {
    expect(validateAssignment({ positionId: LINE, objectType: "Project", objectRef: "PRJ-1", plan: "0", actual: "10.5", commitment: "0" })).toEqual([]);
    expect(validateAssignment({ positionId: "", objectType: "Asset", objectRef: " ", plan: "-1", actual: "x", commitment: "0" })).toHaveLength(5);
  });

  it("orders positions parent first with their depth", () => {
    const tree = positionTree([
      { id: "c", parentId: "a", code: "B2" },
      { id: "a", code: "A" },
      { id: "b", parentId: "a", code: "B1" },
      { id: "z", code: "Z" },
    ]);
    expect(tree.map((t) => `${t.position.code}:${t.depth}`)).toEqual(["A:0", "B1:1", "B2:1", "Z:0"]);
  });
});

describe("ProgramsPage", () => {
  it("lists programs and opens one", async () => {
    stubRoutes({ "GET /api/v1/budget/investment-programs": () => json(200, { items: [program] }), [`GET /api/v1/budget/investment-programs/${P1}`]: () => json(200, view) });
    const { container } = at("/budgeting/programs");
    const table = await screen.findByRole("table", { name: "Investment programs" });
    expect(await within(table).findByText("Plant capex")).toBeInTheDocument();
    expect(within(table).getByText("4800000.00")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "CAPEX-26" }));
    expect(await screen.findByRole("heading", { name: "Program CAPEX-26 (2026)" })).toBeInTheDocument();
  });

  it("shows the empty state, an error with a retry, and filters by legal entity", async () => {
    let fail = true;
    const fetchMock = stubRoutes({
      "GET /api/v1/budget/investment-programs": () => (fail ? json(503, { title: "down", code: "COM-0500", detail: "internal error" }) : json(200, { items: [] })),
    });
    at("/budgeting/programs");
    expect(await screen.findByRole("alert")).toHaveTextContent("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No investment programs yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    await waitFor(() => expect(calls(fetchMock, "GET", `investment-programs?legalEntityId=${LE}`)).toHaveLength(1));
  });
});

describe("CreateProgramPage", () => {
  it("validates, then posts with an Idempotency-Key and opens the program", async () => {
    const fetchMock = stubRoutes({
      "POST /api/v1/budget/investment-programs": () => json(201, { ...view, positions: [], distributed: "0" }),
      [`GET /api/v1/budget/investment-programs/${P1}`]: () => json(200, { ...view, positions: [], distributed: "0" }),
    });
    const { container } = at("/budgeting/programs/new");
    fireEvent.click(screen.getByRole("button", { name: "Create program" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "investment-programs")).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Program code"), { target: { value: "capex-26" } });
    fireEvent.change(screen.getByLabelText("Program budget"), { target: { value: "5000000.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Create program" }));
    expect(await screen.findByRole("heading", { name: "Program CAPEX-26 (2026)" })).toBeInTheDocument();
    const [, init] = calls(fetchMock, "POST", "investment-programs")[0];
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.parse(init.body as string)).toMatchObject({ legalEntityId: LE, code: "CAPEX-26", budget: "5000000.00", currency: "USD" });
    expect(typeof JSON.parse(init.body as string).approvalYear).toBe("number");
  });
});

describe("ProgramDetailPage", () => {
  it("shows totals, the hierarchy with rollup and the assigned objects", async () => {
    stubRoutes({ [`GET /api/v1/budget/investment-programs/${P1}`]: () => json(200, view) });
    const { container } = at(`/budgeting/programs/${P1}`);
    expect(await screen.findByRole("heading", { name: "Program CAPEX-26 (2026)" })).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Program totals" })).toHaveTextContent("Available 3500000.00");
    const positions = screen.getByRole("table", { name: "Program positions" });
    const rows = within(positions).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("PLANT");
    expect(rows[1]).toHaveTextContent("3300000.00");
    expect(rows[2]).toHaveTextContent("LINE1");
    expect(within(screen.getByRole("table", { name: "Assigned objects" })).getByText("CR-000123")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a load error with a retry", async () => {
    stubRoutes({ [`GET /api/v1/budget/investment-programs/${P1}`]: () => json(404, { title: "not found", code: "BUD-0030", detail: "investment program not found" }) });
    at(`/budgeting/programs/${P1}`);
    expect(await screen.findByRole("alert")).toHaveTextContent("BUD-0030");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("checks a distribution against the parent before posting, then posts it and shows the server's CO-0641", async () => {
    let reject = false;
    const fetchMock = stubRoutes({
      [`GET /api/v1/budget/investment-programs/${P1}`]: () => json(200, view),
      [`POST /api/v1/budget/investment-programs/${P1}:distribute`]: () =>
        reject
          ? json(422, { title: "exceeds", code: "CO-0641", detail: "distribution exceeds parent position budget by 1.00" })
          : json(200, { ...view, version: 4, distributed: "5000000", positions: [...view.positions, position({ id: "it", code: "IT", level: 1, budget: "200000" })] }),
    });
    at(`/budgeting/programs/${P1}`);
    const form = await screen.findByRole("form", { name: "Distribute budget" });
    fireEvent.change(within(form).getByLabelText("Position code"), { target: { value: "it" } });
    fireEvent.change(within(form).getByLabelText("Budget"), { target: { value: "300000" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save distribution" }));
    expect(await screen.findByText("The positions would exceed the parent budget by 100000.00 (CO-0641).")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", ":distribute")).toHaveLength(0);

    fireEvent.change(within(form).getByLabelText("Budget"), { target: { value: "200000" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save distribution" }));
    expect(await screen.findByText("Distributed under the program.")).toBeInTheDocument();
    const [, init] = calls(fetchMock, "POST", ":distribute")[0];
    expect(JSON.parse(init.body as string)).toEqual({ allocations: [{ positionCode: "IT", budget: "200000" }] });
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(await within(screen.getByRole("table", { name: "Program positions" })).findByText("IT")).toBeInTheDocument();

    reject = true;
    fireEvent.change(within(form).getByLabelText("Parent"), { target: { value: PLANT } });
    fireEvent.change(within(form).getByLabelText("Position code"), { target: { value: "LINE2" } });
    fireEvent.change(within(form).getByLabelText("Budget"), { target: { value: "1" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save distribution" }));
    expect(await screen.findByText(/CO-0641: distribution exceeds parent position budget by 1.00/)).toBeInTheDocument();
    expect(JSON.parse(calls(fetchMock, "POST", ":distribute")[1][1].body as string)).toMatchObject({ parentPositionId: PLANT });
  });

  it("assigns an object to a position and carries unused budget forward", async () => {
    const fetchMock = stubRoutes({
      [`GET /api/v1/budget/investment-programs/${P1}`]: () => json(200, view),
      [`POST /api/v1/budget/investment-programs/${P1}:assign`]: () => json(200, { ...view, version: 4 }),
      [`POST /api/v1/budget/investment-programs/${P1}:carry-forward`]: () =>
        json(200, { programId: P1, fromYear: 2026, toYear: 2027, amount: "3300000", positions: [{ positionId: PLANT, code: "PLANT", amount: "1800000" }, { positionId: LINE, code: "LINE1", amount: "1500000" }] }),
    });
    at(`/budgeting/programs/${P1}`);
    const form = await screen.findByRole("form", { name: "Assign to a position" });
    fireEvent.click(within(form).getByRole("button", { name: "Save assignment" }));
    expect(await screen.findByText("Choose a position.")).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("Position"), { target: { value: LINE } });
    fireEvent.change(within(form).getByLabelText("Object type"), { target: { value: "Project" } });
    fireEvent.change(within(form).getByLabelText("Reference"), { target: { value: "PRJ-9" } });
    fireEvent.change(within(form).getByLabelText("Actual"), { target: { value: "1200.50" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save assignment" }));
    expect(await screen.findByText("PRJ-9 is assigned.")).toBeInTheDocument();
    expect(JSON.parse(calls(fetchMock, "POST", ":assign")[0][1].body as string)).toEqual({
      positionId: LINE,
      objectType: "Project",
      objectRef: "PRJ-9",
      plan: "0",
      actual: "1200.50",
      commitment: "0",
    });

    fireEvent.click(screen.getByRole("button", { name: "Carry unused budget to 2027" }));
    expect(await screen.findByText("Carried 3300000.00 USD to 2027")).toBeInTheDocument();
    expect(screen.getByText("PLANT 1800000.00, LINE1 1500000.00")).toBeInTheDocument();
    expect(JSON.parse(calls(fetchMock, "POST", ":carry-forward")[0][1].body as string)).toEqual({ toYear: 2027 });
  });
});

describe("module registration", () => {
  it("registers the program routes and the nav entry", () => {
    const paths = budgeting.routes.map((r) => r.path);
    expect(paths).toEqual(expect.arrayContaining(["/budgeting/programs", "/budgeting/programs/new", "/budgeting/programs/:id"]));
    expect(budgeting.nav).toContainEqual({ label: "Investment programs", to: "/budgeting/programs", duty: "budget.view" });
  });
});
