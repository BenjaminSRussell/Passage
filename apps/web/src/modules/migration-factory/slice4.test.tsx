import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { CatalogEntry, CutoverPlan, RollbackStep, RunbookStep } from "./api/client";
import { elapsedSince, formatMinutes } from "./domain";
import migrationFactory from "./module";

const PLAN = "0195f0a0-0000-7000-8000-0000000000c1";
const NOW = Date.parse("2027-01-01T13:08:00Z");

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

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
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {migrationFactory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));

const systems: CatalogEntry["sourceSystems"] = ["AX2012", "D365FO", "SAPECC", "S4HANA"];
const catalog: CatalogEntry[] = [
  { pack: "open_items", sourceSystems: systems, controlTotals: "count, amount per currency, amount per (entity, account, currency)", available: true },
  { pack: "gl_balances", sourceSystems: systems, controlTotals: "balance per (entity, account, dimensions, currency)", available: true },
  { pack: "onhand", sourceSystems: systems, controlTotals: "quantity per (item, site, warehouse, location, batch, serial)", available: false },
  { pack: "project_wip", sourceSystems: systems, controlTotals: "cost, sales, WIP cost and WIP sales per (project, category)", available: true },
];

const step = (seq: number, name: string, offsetMin: number, extra: Partial<RunbookStep> = {}): RunbookStep => ({
  seq,
  offsetMin,
  name,
  owner: "Migration engineer",
  plannedDurationMin: 60,
  actualDurationMin: 0,
  status: "PENDING",
  goNoGo: false,
  ...extra,
});

const rollbackSteps: RollbackStep[] = [
  { seq: 1, offsetMin: 2160, name: "Decision point: go/no-go items still open at the abort deadline", owner: "Cutover manager", plannedDurationMin: 30, decisionPoint: true },
  { seq: 2, offsetMin: 2190, name: "Stop Passage user access", owner: "Cutover manager", plannedDurationMin: 15, decisionPoint: false },
  { seq: 3, offsetMin: 2205, name: "Restore legacy write access", owner: "Legacy operations lead", plannedDurationMin: 30, decisionPoint: false },
  { seq: 4, offsetMin: 2235, name: "Resume legacy coexistence adapters", owner: "Migration engineer", plannedDurationMin: 60, decisionPoint: false },
  { seq: 5, offsetMin: 2295, name: "Export Passage postings since T-0 as re-keying files", owner: "Migration engineer", plannedDurationMin: 60, decisionPoint: false },
  { seq: 6, offsetMin: 2355, name: "Re-key the exported postings in legacy and reconcile", owner: "Controller", plannedDurationMin: 480, decisionPoint: false },
];

const plan: CutoverPlan = {
  planId: PLAN,
  t0: "2027-01-01T00:00:00Z",
  packs: ["open_items"],
  entities: ["USMF"],
  abortOffsetMin: 2160,
  rolledBack: false,
  approvers: [],
  version: 2,
  createdAt: "2026-12-01T00:00:00Z",
  stepsDone: 0,
  stepsTotal: 2,
  openGoNoGo: [],
  abortDue: false,
  steps: [step(1, "Extract transactional packs", 0, { plannedDurationMin: 180 }), step(2, "Load pack open_items", 720)],
  rollbackSteps,
};

/** Step 2 started 68 minutes before NOW: 113 % of its 60 planned minutes, amber. */
const running: CutoverPlan = {
  ...plan,
  version: 3,
  steps: [plan.steps![0], { ...plan.steps![1], startedAt: new Date(NOW - 68 * 60_000).toISOString(), startedBy: "owner", elapsedMin: 68, tone: "amber" }],
};

describe("timer helpers", () => {
  it("counts whole minutes since the start, never negative and 0 for a bad time", () => {
    const start = "2027-01-01T12:00:00Z";
    const at = Date.parse(start);
    expect(elapsedSince(start, at)).toBe(0);
    expect(elapsedSince(start, at + 59_999)).toBe(0);
    expect(elapsedSince(start, at + 90_000)).toBe(1);
    expect(elapsedSince(start, at + 125 * 60_000)).toBe(125);
    expect(elapsedSince(start, at - 60_000)).toBe(0);
    expect(elapsedSince("yesterday", at)).toBe(0);
  });

  it("formats minutes as hours and minutes", () => {
    expect(formatMinutes(45)).toBe("45 min");
    expect(formatMinutes(60)).toBe("1 h 00 min");
    expect(formatMinutes(125)).toBe("2 h 05 min");
  });
});

describe("Pack catalog", () => {
  it("lists the packs from the service with their systems, totals and availability", async () => {
    handler = (url) => (url.pathname === "/api/v1/migration/packs" ? json(200, { items: catalog }) : undefined);
    const { container } = renderAt("/migration-factory/packs");
    expect(screen.getByRole("heading", { name: "Transactional packs" })).toBeInTheDocument();
    const table = await findTable({ name: "Transactional packs" });
    await waitFor(() => expect(within(table).getAllByRole("row")).toHaveLength(catalog.length + 1));
    const [, openItems, , onhand] = within(table).getAllByRole("row");
    expect(openItems).toHaveTextContent("AX 2012, D365 F&O, SAP ECC, SAP S/4HANA");
    expect(openItems).toHaveTextContent("Available");
    expect(onhand).toHaveTextContent("Later slice");
    expect(within(onhand).queryByRole("link")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Start open_items run" })).toHaveAttribute("href", "/migration-factory/runs/new");
    expect(screen.getByRole("link", { name: "Start gl_balances run" })).toHaveAttribute("href", "/migration-factory/runs/new-gl");
    expect(screen.getByRole("link", { name: "Start project_wip run" })).toHaveAttribute("href", "/migration-factory/runs/new-project");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state when the service lists no packs", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration-factory/packs");
    expect(await screen.findByText("No packs")).toBeInTheDocument();
  });

  it("shows the error with its code and retries", async () => {
    let fail = true;
    handler = () => (fail ? json(403, { code: "AUTH-0003", detail: "missing duty migration.factory.view" }) : json(200, { items: catalog }));
    renderAt("/migration-factory/packs");
    expect(await screen.findByText(/missing duty migration.factory.view \(AUTH-0003\)/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("link", { name: "Start open_items run" })).toBeInTheDocument();
  });
});

describe("Cutover runbook timer and rollback plan", () => {
  it("starts a step with the plan version and an idempotency key", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(200, running) : json(200, plan));
    renderAt(`/migration-factory/cutover/${PLAN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Start step 2" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Start step 2" })).not.toBeInTheDocument());
    const [post] = posts("/steps/2/start");
    expect(post.body).toEqual({ version: 2 });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    // Still completable once started; the other step can still be started.
    expect(screen.getByRole("button", { name: "Complete step 2" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start step 1" })).toBeInTheDocument();
  });

  it("explains a start the service refuses", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(422, { code: "MIG-0001", detail: 'CUT-1601 step "Open user access" cannot start: go/no-go items are open' }) : json(200, plan));
    renderAt(`/migration-factory/cutover/${PLAN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Start step 2" }));
    expect(await screen.findByText(/CUT-1601/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start step 2" })).toBeInTheDocument();
  });

  it("runs the timer: elapsed against planned turns from amber to red and warns with CUT-1602", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
    handler = () => json(200, running);
    renderAt(`/migration-factory/cutover/${PLAN}`);
    const table = await findTable({ name: "Runbook steps" });
    await waitFor(() => expect(within(table).getAllByRole("row")[2]).toHaveTextContent("Running"));
    const row = () => within(table).getAllByRole("row")[2];
    expect(row()).toHaveTextContent("68");
    expect(row()).toHaveTextContent("Amber");
    expect(screen.getByRole("list", { name: "Steps over plan" })).toHaveTextContent("CUT-1602 Step 2 Load pack open_items at 113% of planned duration");
    // A step not started has no timer.
    expect(within(table).getAllByRole("row")[1]).not.toHaveTextContent("On plan");
    await act(async () => {
      vi.advanceTimersByTime(8 * 60_000);
    });
    expect(row()).toHaveTextContent("76");
    expect(row()).toHaveTextContent("Red");
    expect(screen.getByRole("list", { name: "Steps over plan" })).toHaveTextContent("at 126%");
  });

  it("completes a started step with the measured time when the minutes are left blank", async () => {
    const done: CutoverPlan = { ...running, version: 4, stepsDone: 1, steps: [running.steps![0], { ...running.steps![1], status: "DONE", actualDurationMin: 68 }] };
    handler = (_url, init) => (init.method === "POST" ? json(200, done) : json(200, running));
    renderAt(`/migration-factory/cutover/${PLAN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Complete step 2" }));
    const minutes = screen.getByLabelText(/Actual duration \(min\)/);
    expect(minutes).toHaveValue("");
    expect(screen.getByText(/leave blank to use the measured time/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Mark step done" }));
    await waitFor(() => expect(posts("/steps/2/complete")).toHaveLength(1));
    expect(posts("/steps/2/complete")[0].body).toEqual({ version: 3 });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Complete step 2" })).not.toBeInTheDocument());
  });

  it("still asks for whole minutes when a started step's duration is typed", async () => {
    handler = () => json(200, running);
    renderAt(`/migration-factory/cutover/${PLAN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Complete step 2" }));
    fireEvent.change(screen.getByLabelText(/Actual duration \(min\)/), { target: { value: "1h" } });
    fireEvent.click(screen.getByRole("button", { name: "Mark step done" }));
    expect(await screen.findByText(/or leave blank to use the measured time/)).toBeInTheDocument();
    expect(posts("/complete")).toHaveLength(0);
  });

  it("shows the generated rollback plan with the decision point at the abort deadline", async () => {
    handler = () => json(200, plan);
    const { container } = renderAt(`/migration-factory/cutover/${PLAN}`);
    const table = await findTable({ name: "Rollback plan" });
    const rows = within(table).getAllByRole("row");
    expect(rows).toHaveLength(rollbackSteps.length + 1);
    expect(rows[1]).toHaveTextContent("T+36h");
    expect(rows[1]).toHaveTextContent("Decision");
    expect(rows[3]).toHaveTextContent("Restore legacy write access");
    expect(rows[4]).toHaveTextContent("Resume legacy coexistence adapters");
    expect(rows[2]).not.toHaveTextContent("Decision");
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers neither start nor complete on a rolled-back plan", async () => {
    handler = () => json(200, { ...plan, rolledBack: true, approvers: ["alice", "bob"] });
    renderAt(`/migration-factory/cutover/${PLAN}`);
    await screen.findByText("Rolled back with the approval of alice and bob.");
    expect(screen.queryByRole("button", { name: /^Start step/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Complete step/ })).not.toBeInTheDocument();
  });
});
