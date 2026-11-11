import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CostElement, CostObject, Run } from "./api/client";
import { AllocationRunDetailPage, AllocationRunsPage, NewAllocationRunPage } from "./AllocationRunsPage";
import { CostElementsPage, CreateCostElementPage } from "./CostElementsPage";
import { CostObjectDetailPage, CostObjectsPage, CreateCostObjectPage } from "./CostObjectsPage";
import { ReconciliationPage } from "./ReconciliationPage";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

const LE = "11111111-1111-4111-8111-111111111111";
const A = "22222222-2222-4222-8222-222222222222";
const B = "33333333-3333-4333-8333-333333333333";
const C = "44444444-4444-4444-8444-444444444444";
const SEC = "55555555-5555-4555-8555-555555555555";
const PRI = "66666666-6666-4666-8666-666666666666";
const RUN = "77777777-7777-4777-8777-777777777777";

const objects: CostObject[] = [
  { id: A, legalEntityId: LE, kind: "CostCenter", code: "CC-A", name: "Maintenance" },
  { id: B, legalEntityId: LE, kind: "CostCenter", code: "CC-B", name: "IT" },
  { id: C, legalEntityId: LE, kind: "CostCenter", code: "CC-C", name: "Assembly" },
];
const elements: CostElement[] = [
  { id: PRI, legalEntityId: LE, kind: "Primary", code: "PR-600", name: "Payroll", mainAccountFrom: "600000", mainAccountTo: "609999" },
  { id: SEC, legalEntityId: LE, kind: "Secondary", code: "SE-ALLOC", name: "Allocated" },
];
const run: Run = {
  id: RUN,
  legalEntityId: LE,
  period: "2026-09",
  currency: "USD",
  secondaryElementId: SEC,
  status: "Completed",
  method: "exact-lu",
  iterations: 0,
  residual: "0",
  converged: true,
  swept: "0",
  totalIn: "350.25",
  totalOut: "350.25",
  costObjectIds: [A, B, C],
  flows: [
    { from: A, to: B, amount: "500.5" },
    { from: B, to: A, amount: "350.25" },
    { from: B, to: C, amount: "350.25" },
  ],
  postings: [
    { centerId: A, amount: "-150.25" },
    { centerId: B, amount: "-200" },
    { centerId: C, amount: "350.25" },
  ],
  unallocated: [],
  createdAt: "2026-10-01T08:00:00Z",
  warnings: [],
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
        createElement(Route, { path: "/costacct", element: createElement(CostObjectsPage) }),
        createElement(Route, { path: "/costacct/cost-objects/new", element: createElement(CreateCostObjectPage) }),
        createElement(Route, { path: "/costacct/cost-objects/:id", element: createElement(CostObjectDetailPage) }),
        createElement(Route, { path: "/costacct/cost-elements", element: createElement(CostElementsPage) }),
        createElement(Route, { path: "/costacct/cost-elements/new", element: createElement(CreateCostElementPage) }),
        createElement(Route, { path: "/costacct/allocation-runs", element: createElement(AllocationRunsPage) }),
        createElement(Route, { path: "/costacct/allocation-runs/new", element: createElement(NewAllocationRunPage) }),
        createElement(Route, { path: "/costacct/allocation-runs/:id", element: createElement(AllocationRunDetailPage) }),
        createElement(Route, { path: "/costacct/reconciliation", element: createElement(ReconciliationPage) }),
      ),
    ),
  );
}

const lists = (extra: Record<string, Handler> = {}) =>
  stubRoutes({
    "GET /api/v1/controlling/cost-objects": () => json(200, { items: objects }),
    "GET /api/v1/controlling/cost-elements": () => json(200, { items: elements }),
    "GET /api/v1/controlling/allocation-runs": () => json(200, { items: [run] }),
    ...extra,
  });

afterEach(() => vi.unstubAllGlobals());

describe("CostObjectsPage", () => {
  it("lists cost objects, filters by kind and opens one with its entries", async () => {
    const fetchMock = lists({
      [`GET /api/v1/controlling/cost-objects/${A}`]: () => json(200, objects[0]),
      [`GET /api/v1/controlling/cost-objects/${A}/entries`]: () =>
        json(200, {
          items: [
            { id: "e1", costElementId: PRI, sourceId: "j1", period: "2026-09", currency: "USD", amount: "150.25", behavior: "Unclassified", origin: "Journal" },
            { id: "e2", costElementId: SEC, sourceId: RUN, allocationRunId: RUN, period: "2026-09", currency: "USD", amount: "-150.25", behavior: "Unclassified", origin: "Allocation" },
          ],
        }),
    });
    const { container } = at("/costacct");
    const table = await findTable({ name: "Cost objects" });
    expect(await within(table).findByText("Maintenance")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "InternalOrder" } });
    await waitFor(() => expect(calls(fetchMock, "GET", "cost-objects?kind=InternalOrder")).toHaveLength(1));

    fireEvent.click(within(table).getByRole("button", { name: "CC-A" }));
    expect(await screen.findByRole("heading", { name: "CC-A Maintenance" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-09" } });
    const entries = await findTable({ name: "Cost entries" });
    expect(await within(entries).findByText("PR-600")).toBeInTheDocument();
    expect(within(entries).getByRole("link", { name: "Allocation run" })).toHaveAttribute("href", `/costacct/allocation-runs/${RUN}`);
    expect(screen.getByText("Period total (USD)").nextSibling).toHaveTextContent("0.00");
  });

  it("shows the empty state, and an error with a retry", async () => {
    let fail = true;
    stubRoutes({
      "GET /api/v1/controlling/cost-objects": () => (fail ? json(503, { title: "down", code: "COM-0500", detail: "internal error" }) : json(200, { items: [] })),
    });
    at("/costacct");
    expect(await findAlert()).toHaveTextContent("Something went wrong on our side");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No cost objects yet")).toBeInTheDocument();
  });

  it("validates, creates a cost object and shows a duplicate code refusal", async () => {
    let dup = true;
    const fetchMock = lists({
      "POST /api/v1/controlling/cost-objects": () => (dup ? json(409, { title: "Conflict", code: "CST-0003", detail: "cost object code already exists" }) : json(201, objects[0])),
      [`GET /api/v1/controlling/cost-objects/${A}`]: () => json(200, objects[0]),
      [`GET /api/v1/controlling/cost-objects/${A}/entries`]: () => json(200, { items: [] }),
    });
    at("/costacct/cost-objects/new");
    fireEvent.click(screen.getByRole("button", { name: "Save cost object" }));
    expect(await screen.findByText("Enter the legal entity's id (a UUID).")).toBeInTheDocument();
    expect(calls(fetchMock, "POST", "cost-objects")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "CC-A" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Maintenance" } });
    fireEvent.click(screen.getByRole("button", { name: "Save cost object" }));
    expect(await screen.findByText(/cost object code already exists/)).toBeInTheDocument();
    dup = false;
    fireEvent.click(screen.getByRole("button", { name: "Save cost object" }));
    expect(await screen.findByRole("heading", { name: "CC-A Maintenance" })).toBeInTheDocument();
    const posts = calls(fetchMock, "POST", "cost-objects");
    // The retry of the same form replays the same Idempotency-Key.
    expect((posts[0][1].headers as Record<string, string>)["Idempotency-Key"]).toBe((posts[1][1].headers as Record<string, string>)["Idempotency-Key"]);
  });
});

describe("CostElementsPage", () => {
  it("lists elements with their ranges and creates a primary element", async () => {
    const fetchMock = lists({ "POST /api/v1/controlling/cost-elements": () => json(201, elements[0]) });
    const { container } = at("/costacct/cost-elements");
    const table = await findTable({ name: "Cost elements" });
    expect(await within(table).findByText("600000..609999")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("link", { name: "New cost element" }));
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "PR-600" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Payroll" } });
    fireEvent.change(screen.getByLabelText("Main account from"), { target: { value: "609999" } });
    fireEvent.change(screen.getByLabelText("Main account to"), { target: { value: "600000" } });
    fireEvent.click(screen.getByRole("button", { name: "Save cost element" }));
    expect(await screen.findByText(/is reversed/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Main account from"), { target: { value: "600000" } });
    fireEvent.change(screen.getByLabelText("Main account to"), { target: { value: "609999" } });
    fireEvent.change(screen.getByLabelText("Default behavior"), { target: { value: "Fixed" } });
    fireEvent.click(screen.getByRole("button", { name: "Save cost element" }));
    expect(await findTable({ name: "Cost elements" })).toBeInTheDocument();
    expect(JSON.parse(calls(fetchMock, "POST", "cost-elements")[0][1].body as string)).toEqual({
      legalEntityId: LE,
      code: "PR-600",
      name: "Payroll",
      kind: "Primary",
      mainAccountFrom: "600000",
      mainAccountTo: "609999",
      defaultBehavior: "Fixed",
    });
  });
});

describe("Allocation runs", () => {
  it("lists runs and shows a run's conserved totals, postings and flows", async () => {
    lists({ [`GET /api/v1/controlling/allocation-runs/${RUN}`]: () => json(200, run) });
    const { container } = at("/costacct/allocation-runs");
    const table = await findTable({ name: "Allocation runs" });
    fireEvent.click(await within(table).findByRole("button", { name: "2026-09" }));
    expect(await screen.findByRole("heading", { name: "Allocation run 2026-09" })).toBeInTheDocument();
    expect(screen.getByText("Conserved to the cent")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Run totals" })).toHaveTextContent("Cost out 350.25");
    const postings = screen.getByRole("table", { name: "Net postings" });
    expect(await within(postings).findByRole("link", { name: "CC-C" })).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Flows" })).getByText("500.50")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("builds the basis matrix from the chosen centers and posts the run", async () => {
    const fetchMock = lists({
      "POST /api/v1/controlling/allocation-runs": () => json(201, { runId: RUN, period: "2026-09", result: run }),
      [`GET /api/v1/controlling/allocation-runs/${RUN}`]: () => json(200, run),
    });
    at("/costacct/allocation-runs/new");
    fireEvent.click(screen.getByRole("button", { name: "Run allocation" }));
    expect(await screen.findByText("Choose at least two cost objects.")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-09" } });
    fireEvent.change(await screen.findByLabelText("Secondary element"), { target: { value: SEC } });
    await screen.findByRole("option", { name: "SE-ALLOC Allocated" });
    fireEvent.change(screen.getByLabelText("Secondary element"), { target: { value: SEC } });
    for (const code of ["CC-A Maintenance", "CC-B IT", "CC-C Assembly"]) fireEvent.click(await screen.findByLabelText(code));
    fireEvent.change(screen.getByLabelText("Basis from CC-A to CC-B"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Basis from CC-B to CC-A"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Basis from CC-B to CC-C"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Run allocation" }));
    expect(await screen.findByRole("heading", { name: "Allocation run 2026-09" })).toBeInTheDocument();
    expect(JSON.parse(calls(fetchMock, "POST", "allocation-runs")[0][1].body as string)).toEqual({
      legalEntityId: LE,
      period: "2026-09",
      currency: "USD",
      secondaryElementId: SEC,
      costObjectIds: [A, B, C],
      basis: [
        ["0", "1", "0"],
        ["1", "0", "1"],
        ["0", "0", "0"],
      ],
    });
  });

  it("marks a stale run with CO-0615 and links a reversed run to its rerun (BR-38.7, BR-38.8)", async () => {
    const NEW = "88888888-8888-4888-8888-888888888888";
    const stale: Run = {
      ...run,
      unallocated: [C],
      warnings: [
        { code: "CO-0613", detail: `sender ${C} has zero basis; its cost was not allocated` },
        { code: "CO-0615", detail: "journal postings after the last allocation run for 2026-09; rerun the allocation" },
      ],
    };
    const reversed: Run = { ...run, status: "Reversed", reversedByRunId: NEW, warnings: [] };
    const current: Run = { ...run, id: NEW, reversesRunId: RUN, warnings: [] };
    let first = stale;
    lists({
      [`GET /api/v1/controlling/allocation-runs/${RUN}`]: () => json(200, first),
      [`GET /api/v1/controlling/allocation-runs/${NEW}`]: () => json(200, current),
    });
    const { container, unmount } = at(`/costacct/allocation-runs/${RUN}`);
    expect(await screen.findByText("CO-0615: journal postings after this run")).toBeInTheDocument();
    expect(screen.getByText("CO-0613: senders with zero basis")).toBeInTheDocument();
    expect(await screen.findByText(/no basis, so the cost stayed where it was: CC-C/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Rerun allocation" })).toHaveAttribute("href", "/costacct/allocation-runs/new");
    expect(pageViolations(container)).toEqual([]);
    unmount();

    first = reversed;
    at(`/costacct/allocation-runs/${RUN}`);
    expect(await screen.findByText("This run was reversed")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Rerun allocation" })).not.toBeInTheDocument();
    expect(screen.queryByText("CO-0615: journal postings after this run")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("link", { name: "Open the current run" }));
    expect(await screen.findByRole("link", { name: "The earlier run of 2026-09" })).toHaveAttribute("href", `/costacct/allocation-runs/${RUN}`);
  });

  it("shows each run's status in the list", async () => {
    lists({ "GET /api/v1/controlling/allocation-runs": () => json(200, { items: [{ ...run, id: "r2", warnings: [] }, { ...run, status: "Reversed", reversedByRunId: "r2" }] }) });
    at("/costacct/allocation-runs");
    const table = await findTable({ name: "Allocation runs" });
    expect(await within(table).findByText("Reversed")).toBeInTheDocument();
    expect(within(table).getByText("Completed")).toBeInTheDocument();
  });

  it("shows a closed-cycle refusal", async () => {
    lists({ "POST /api/v1/controlling/allocation-runs": () => json(422, { title: "Unprocessable", code: "CST-0005", detail: "the cycle is closed" }) });
    at("/costacct/allocation-runs/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-09" } });
    await screen.findByRole("option", { name: "SE-ALLOC Allocated" });
    fireEvent.change(screen.getByLabelText("Secondary element"), { target: { value: SEC } });
    fireEvent.click(await screen.findByLabelText("CC-A Maintenance"));
    fireEvent.click(screen.getByLabelText("CC-B IT"));
    fireEvent.click(screen.getByRole("button", { name: "Run allocation" }));
    expect(await screen.findByText(/the cycle is closed/)).toBeInTheDocument();
  });
});

describe("ReconciliationPage", () => {
  it("requests the reconciliation without balances and shows differences by element (AC-38.6)", async () => {
    const fetchMock = lists({
      "POST /api/v1/controlling/reconciliations": () =>
        json(200, {
          reconciled: false,
          lines: [{ costElementId: PRI, journal: "350.25", costLedger: "100", difference: "250.25" }],
          unmappedAccounts: [{ mainAccount: "400000", amount: "-500" }],
        }),
    });
    const { container } = at("/costacct/reconciliation");
    fireEvent.click(screen.getByRole("button", { name: "Reconcile" }));
    expect(await screen.findByText(/legal entity's id/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: "2026-09" } });
    fireEvent.click(screen.getByRole("button", { name: "Reconcile" }));
    expect(await screen.findByText("Differences found")).toBeInTheDocument();
    const byElement = screen.getByRole("table", { name: "Reconciliation by element" });
    expect(await within(byElement).findByText("PR-600")).toBeInTheDocument();
    expect(within(byElement).getByText("250.25")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Accounts outside the cost ledger" })).toHaveTextContent("400000");
    const [, init] = calls(fetchMock, "POST", "reconciliations")[0];
    expect(JSON.parse(init.body as string)).toEqual({
      legalEntityId: LE,
      period: "2026-09",
      currency: "USD",
    });
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeUndefined();
    expect(pageViolations(container)).toEqual([]);
  });
});
