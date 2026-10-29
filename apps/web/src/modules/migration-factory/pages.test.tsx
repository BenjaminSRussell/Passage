import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { CutoverPlan, PackRun, PackRunDetail, RunbookStep } from "./api/client";
import migrationFactory from "./module";
import { validatePlan } from "./NewCutoverPlanPage";
import { validatePackRun, type PackRunValues } from "./NewPackRunPage";

const RUN = "0195f0a0-0000-7000-8000-0000000000a1";
const PLAN = "0195f0a0-0000-7000-8000-0000000000b1";

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

afterEach(() => vi.unstubAllGlobals());

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

const run: PackRun = {
  runId: RUN,
  pack: "open_items",
  sourceSystem: "AX2012",
  sourceCompany: "USMF",
  cutoffTs: "2026-12-31T00:00:00Z",
  inputHash: "ab".repeat(32),
  status: "LOADED",
  recordsIn: 201,
  recordsLoaded: 200,
  recordsQuarantined: 1,
  alreadyPresent: 0,
  version: 1,
  createdAt: "2027-01-01T04:10:00Z",
};

const detail: PackRunDetail = {
  ...run,
  controlTotals: [
    { name: "open_items.amount", key: { company: "USMF", currency: "USD" }, source: "2055.000000", target: "2050.000000", delta: "-5.000000", tolerance: "0.000000", status: "MISMATCH" },
    { name: "open_items.count", key: { company: "USMF" }, source: "201.000000", target: "200.000000", delta: "-1.000000", tolerance: "0.000000", status: "MISMATCH" },
  ],
  quarantined: [
    { seq: 1, legacyId: "OI-BAD", rule: "REFERENTIAL", reason: 'party "V999" is not in master data', status: "OPEN", party: "V999", account: "2100", currency: "USD", amount: "5.000000" },
  ],
};

const step = (seq: number, name: string, offsetMin: number, extra: Partial<RunbookStep> = {}): RunbookStep => ({
  seq,
  offsetMin,
  name,
  owner: "Controller",
  plannedDurationMin: 240,
  actualDurationMin: 0,
  status: "PENDING",
  goNoGo: false,
  ...extra,
});

const plan: CutoverPlan = {
  planId: PLAN,
  t0: "2027-01-01T00:00:00Z",
  packs: ["open_items"],
  entities: ["USMF", "DEMF"],
  abortOffsetMin: 2160,
  rolledBack: false,
  approvers: [],
  version: 3,
  createdAt: "2026-12-01T00:00:00Z",
  stepsDone: 1,
  stepsTotal: 3,
  openGoNoGo: ["Go/no-go meeting"],
  abortDue: false,
  steps: [
    step(1, "Freeze master-data changes in legacy; final pack dry run", -20160),
    step(2, "Reconciliation complete", 1440, { goNoGo: true, status: "DONE", actualDurationMin: 300 }),
    step(3, "Go/no-go meeting", 1440, { goNoGo: true, plannedDurationMin: 60 }),
  ],
};

describe("Pack runs", () => {
  it("lists runs with source, status and record counts and opens the detail", async () => {
    handler = (url) => (url.pathname === "/api/v1/migration/runs" ? json(200, { items: [run] }) : undefined);
    const { container } = renderAt("/migration-factory");
    expect(screen.getByRole("heading", { name: "Migration factory" })).toBeInTheDocument();
    const table = await findTable({ name: "Pack runs" });
    await waitFor(() => expect(table).toHaveTextContent("AX 2012 USMF"));
    expect(table).toHaveTextContent("Loaded");
    expect(screen.getByRole("link", { name: "Start pack run" })).toHaveAttribute("href", "/migration-factory/runs/new");
    expect(screen.getByRole("table", { name: "Transactional packs" })).toHaveTextContent("Later slice");
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by pack and status", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration-factory");
    await screen.findByText("No pack runs yet");
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "RECONCILED" } });
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe("?status=RECONCILED"));
    fireEvent.change(screen.getByLabelText("Pack"), { target: { value: "open_items" } });
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe("?pack=open_items&status=RECONCILED"));
  });

  it("shows the error with its code and retries", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [run] }));
    renderAt("/migration-factory");
    expect(await screen.findByText(/Service unavailable \(COM-0503\)/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("AX 2012 USMF")).toBeInTheDocument();
  });
});

describe("Pack run detail", () => {
  it("shows the control-total board with mismatches and the quarantined records", async () => {
    handler = (url) => (url.pathname === `/api/v1/migration/runs/${RUN}` ? json(200, detail) : undefined);
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText(/2 control total\(s\) do not match/)).toBeInTheDocument();
    const totals = await findTable({ name: "Control totals" });
    expect(totals).toHaveTextContent("company=USMF, currency=USD");
    expect(totals).toHaveTextContent("2055.00");
    expect(totals).toHaveTextContent("-5.00");
    expect(screen.getByRole("table", { name: "Quarantined records" })).toHaveTextContent("OI-BAD");
    expect(screen.getByRole("list", { name: "Records" })).toHaveTextContent("Quarantined 1");
    expect(pageViolations(container)).toEqual([]);
  });

  it("says a re-run loaded nothing new", async () => {
    const rerun = { ...detail, status: "RECONCILED", recordsLoaded: 0, alreadyPresent: 200, quarantined: [], message: "Re-run loaded 0 new records; all 200 already present by cross-reference.", controlTotals: [] };
    handler = () => json(200, rerun);
    renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText(/all 200 already present/)).toBeInTheDocument();
    expect(await screen.findByText("No totals yet")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Quarantined records" })).not.toBeInTheDocument();
  });

  it("shows a not-found run as an error", async () => {
    handler = () => json(404, { code: "MIG-0003", detail: "not found for this tenant" });
    renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText(/not found for this tenant \(MIG-0003\)/)).toBeInTheDocument();
  });
});

describe("Start pack run", () => {
  const values: PackRunValues = {
    sourceSystem: "SAPECC",
    sourceCompany: " 1000 ",
    cutoff: "2026-12-31T00:00",
    freeze: "2026-12-31T18:00",
    quarantineMaxPct: "",
    parties: "V001, V002",
    records: "BSIK-1,V001,160000,EUR,100.00",
  };

  it("validates what the service checks before it loads", () => {
    expect(validatePackRun(values).input).toEqual({
      sourceSystem: "SAPECC",
      sourceCompany: "1000",
      cutoffTs: "2026-12-31T00:00:00Z",
      freezeTs: "2026-12-31T18:00:00Z",
      parties: ["V001", "V002"],
      records: [{ legacyId: "BSIK-1", party: "V001", account: "160000", currency: "EUR", amount: "100.00" }],
    });
    const { errors } = validatePackRun({ ...values, sourceCompany: "", cutoff: "2027-01-02T00:00", quarantineMaxPct: "11", parties: "", records: "x" });
    expect(Object.keys(errors).sort()).toEqual(["cutoff", "parties", "quarantineMaxPct", "records", "sourceCompany"]);
    expect(errors.cutoff).toMatch(/MIG-1501/);
  });

  it("submits the extract with an Idempotency-Key and opens the run", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/packs/open_items/runs")) return json(201, detail);
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, detail);
      return undefined;
    };
    const { container } = renderAt("/migration-factory/runs/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legacy company"), { target: { value: "USMF" } });
    fireEvent.change(screen.getByLabelText("Extract cutoff (UTC)"), { target: { value: "2026-12-31T00:00" } });
    fireEvent.change(screen.getByLabelText("Legacy freeze (UTC)"), { target: { value: "2026-12-31T18:00" } });
    fireEvent.change(screen.getByLabelText("Parties in master data"), { target: { value: "V001" } });
    fireEvent.change(screen.getByLabelText("Open items"), { target: { value: "OI-1,V001,2100,USD,10.25" } });
    fireEvent.click(screen.getByRole("button", { name: "Run open_items" }));
    expect(await screen.findByRole("heading", { name: "Pack run open_items" })).toBeInTheDocument();
    const [post] = posts("/packs/open_items/runs");
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toMatchObject({ sourceSystem: "AX2012", sourceCompany: "USMF", parties: ["V001"], records: [{ legacyId: "OI-1", amount: "10.25" }] });
  });

  it("shows the service's rejection with its code", async () => {
    handler = () => json(422, { code: "MIG-0001", detail: "MIG-1504 quarantine share 50.00% exceeds 1.00%; load stopped" });
    renderAt("/migration-factory/runs/new");
    fireEvent.change(screen.getByLabelText("Legacy company"), { target: { value: "USMF" } });
    fireEvent.change(screen.getByLabelText("Extract cutoff (UTC)"), { target: { value: "2026-12-31T00:00" } });
    fireEvent.change(screen.getByLabelText("Legacy freeze (UTC)"), { target: { value: "2026-12-31T18:00" } });
    fireEvent.change(screen.getByLabelText("Parties in master data"), { target: { value: "V001" } });
    fireEvent.change(screen.getByLabelText("Open items"), { target: { value: "OI-1,V001,2100,USD,1\nOI-2,V9,2100,USD,1" } });
    fireEvent.click(screen.getByRole("button", { name: "Run open_items" }));
    expect(await screen.findByText(/MIG-1504 .* \(MIG-0001\)/)).toBeInTheDocument();
  });
});

describe("Cutover plans", () => {
  it("lists plans with progress and state", async () => {
    handler = () => json(200, { items: [{ ...plan, steps: undefined }, { ...plan, planId: "p2", rolledBack: true }] });
    const { container } = renderAt("/migration-factory/cutover");
    const table = await findTable({ name: "Cutover plans" });
    await waitFor(() => expect(table).toHaveTextContent("1 / 3"));
    expect(table).toHaveTextContent("USMF, DEMF");
    expect(table).toHaveTextContent("Rolled back");
    expect(table).toHaveTextContent("In progress");
    expect(screen.getByRole("link", { name: "Generate plan" })).toHaveAttribute("href", "/migration-factory/cutover/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration-factory/cutover");
    expect(await screen.findByText("No cutover plans yet")).toBeInTheDocument();
  });
});

describe("Generate cutover plan", () => {
  it("validates T-0, packs, entities and the abort offset", () => {
    expect(validatePlan({ t0: "2027-01-01T00:00", packs: ["open_items"], entities: "USMF, DEMF", abortOffsetHours: "48" }).input).toEqual({
      t0: "2027-01-01T00:00:00Z",
      packs: ["open_items"],
      entities: ["USMF", "DEMF"],
      abortOffsetHours: 48,
    });
    const { errors } = validatePlan({ t0: "", packs: [], entities: " ", abortOffsetHours: "6" });
    expect(Object.keys(errors).sort()).toEqual(["abortOffsetHours", "entities", "packs", "t0"]);
  });

  it("generates the plan and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/cutover/plans/generate")) return json(201, plan);
      if (url.pathname === `/api/v1/cutover/plans/${PLAN}`) return json(200, plan);
      return undefined;
    };
    const { container } = renderAt("/migration-factory/cutover/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("T-0 (UTC)"), { target: { value: "2027-01-01T00:00" } });
    fireEvent.change(screen.getByLabelText("Legal entities"), { target: { value: "USMF" } });
    fireEvent.click(screen.getByLabelText("gl_balances"));
    fireEvent.click(screen.getByRole("button", { name: "Generate plan" }));
    expect(await screen.findByRole("heading", { name: "Cutover runbook" })).toBeInTheDocument();
    expect(posts("/cutover/plans/generate")[0].body).toEqual({ t0: "2027-01-01T00:00:00Z", packs: ["open_items", "gl_balances"], entities: ["USMF"] });
  });
});

describe("Cutover runbook", () => {
  it("shows steps with offsets and overrun, and the open go/no-go items", async () => {
    handler = () => json(200, plan);
    const { container } = renderAt(`/migration-factory/cutover/${PLAN}`);
    const table = await findTable({ name: "Runbook steps" });
    await waitFor(() => expect(table).toHaveTextContent("T-14d"));
    expect(table).toHaveTextContent("T+1d");
    // 300 min against 240 planned is 125 %: red.
    expect(within(table).getAllByRole("row")[2]).toHaveTextContent("Red");
    expect(screen.getByRole("list", { name: "Open go/no-go items" })).toHaveTextContent("Go/no-go meeting");
    expect(screen.queryByRole("button", { name: "Complete step 2" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("completes a step with the plan version and shows the updated plan", async () => {
    const after: CutoverPlan = {
      ...plan,
      version: 4,
      stepsDone: 2,
      openGoNoGo: [],
      steps: plan.steps!.map((s) => (s.seq === 3 ? { ...s, status: "DONE", actualDurationMin: 70 } : s)),
    };
    handler = (_url, init) => (init.method === "POST" ? json(200, after) : json(200, plan));
    renderAt(`/migration-factory/cutover/${PLAN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Complete step 3" }));
    fireEvent.change(screen.getByLabelText("Actual duration (min)"), { target: { value: "70" } });
    fireEvent.click(screen.getByRole("button", { name: "Mark step done" }));
    expect(await screen.findByText("All go/no-go items are complete.")).toBeInTheDocument();
    const [post] = posts("/steps/3/complete");
    expect(post.body).toEqual({ actualDurationMin: 70, version: 3 });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("explains an out-of-sequence step and a stale plan", async () => {
    let reply = json(422, { code: "MIG-0001", detail: 'CUT-1601 step "Open user access" cannot start: go/no-go items are open' });
    handler = (_url, init) => (init.method === "POST" ? reply : json(200, plan));
    renderAt(`/migration-factory/cutover/${PLAN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Complete step 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Mark step done" }));
    expect(await screen.findByText(/CUT-1601/)).toBeInTheDocument();
    reply = json(409, { code: "MIG-0006", detail: "version conflict" });
    fireEvent.click(screen.getByRole("button", { name: "Mark step done" }));
    expect(await screen.findByText(/Someone changed this plan/)).toBeInTheDocument();
    await waitFor(() => expect(calls.filter((c) => c.method === "GET").length).toBeGreaterThan(1));
  });

  it("rejects a non-numeric duration before sending", async () => {
    handler = () => json(200, plan);
    renderAt(`/migration-factory/cutover/${PLAN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Complete step 1" }));
    fireEvent.change(screen.getByLabelText("Actual duration (min)"), { target: { value: "1.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Mark step done" }));
    expect(await screen.findByText(/whole number/)).toBeInTheDocument();
    expect(posts("/complete")).toHaveLength(0);
  });

  it("warns before the abort deadline and says when it has passed", async () => {
    handler = () => json(200, { ...plan, abortWarning: "CUT-1604 Abort deadline T+36h in 30 min with open go/no-go items." });
    const { unmount } = renderAt(`/migration-factory/cutover/${PLAN}`);
    expect(await screen.findByText(/CUT-1604/)).toBeInTheDocument();
    unmount();
    handler = () => json(200, { ...plan, abortDue: true });
    renderAt(`/migration-factory/cutover/${PLAN}`);
    expect(await screen.findByText("Abort deadline passed")).toBeInTheDocument();
  });

  it("needs two different approvers and a confirmation to roll back", async () => {
    const rolled: CutoverPlan = { ...plan, rolledBack: true, approvers: ["alice", "bob"], version: 4 };
    handler = (_url, init) => (init.method === "POST" ? json(200, rolled) : json(200, plan));
    renderAt(`/migration-factory/cutover/${PLAN}`);
    fireEvent.change(await screen.findByLabelText("First approver"), { target: { value: "alice" } });
    fireEvent.change(screen.getByLabelText("Second approver"), { target: { value: "alice" } });
    fireEvent.click(screen.getByRole("button", { name: "Roll back cutover" }));
    expect(await screen.findByText(/two different approvers/)).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Second approver"), { target: { value: "bob" } });
    fireEvent.click(screen.getByRole("button", { name: "Roll back cutover" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Roll back" }));
    expect(await screen.findByText("Rolled back with the approval of alice and bob.")).toBeInTheDocument();
    expect(posts("/rollback")[0].body).toEqual({ approvers: ["alice", "bob"], postings: [], version: 3 });
    expect(screen.queryByRole("button", { name: "Complete step 1" })).not.toBeInTheDocument();
  });
});
