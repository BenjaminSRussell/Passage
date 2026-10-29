import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { PackRunDetail, ParallelRun, ProjectOpening, Variance, Xref } from "./api/client";
import { parseProjectExtract, parseTuples } from "./domain";
import migrationFactory from "./module";
import { validateParallelRun, type ParallelRunValues } from "./NewParallelRunPage";
import { validateProjectRun, type ProjectRunValues } from "./NewProjectRunPage";

const RUN = "0195f0a0-0000-7000-8000-0000000000a1";
const PRUN = "0195f0a0-0000-7000-8000-0000000000c1";

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

const loaded: PackRunDetail = {
  runId: RUN,
  pack: "open_items",
  sourceSystem: "AX2012",
  sourceCompany: "USMF",
  cutoffTs: "2026-12-31T00:00:00Z",
  inputHash: "ab".repeat(32),
  status: "LOADED",
  recordsIn: 3,
  recordsLoaded: 2,
  recordsQuarantined: 1,
  alreadyPresent: 0,
  version: 1,
  createdAt: "2027-01-01T04:10:00Z",
  controlTotals: [
    { name: "open_items.count", key: { company: "USMF" }, source: "3.000000", target: "2.000000", delta: "-1.000000", status: "MISMATCH" },
  ],
  quarantined: [
    { seq: 1, legacyId: "OI-BAD", rule: "REFERENTIAL", reason: 'party "V999" is not in master data', status: "OPEN", party: "V999", account: "2100", currency: "USD", amount: "5.000000" },
  ],
};

const reconciled: PackRunDetail = {
  ...loaded,
  status: "RECONCILED",
  recordsLoaded: 3,
  recordsQuarantined: 0,
  version: 2,
  controlTotals: [{ ...loaded.controlTotals[0], target: "3.000000", delta: "0.000000", status: "MATCH" }],
  quarantined: [{ ...loaded.quarantined[0], status: "RELEASED", releasedBy: "ops", released: { legacyId: "OI-BAD", party: "V001", account: "2100", currency: "USD", amount: "5.000000" } }],
};

describe("Quarantine release", () => {
  it("releases a corrected record with the run version and shows the reconciled run", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(200, reconciled) : json(200, loaded));
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release record OI-BAD" }));
    expect(screen.getByLabelText("Party")).toHaveValue("V999");
    fireEvent.change(screen.getByLabelText("Party"), { target: { value: "V001" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Release record" }));
    expect(await screen.findByText(/Released OI-BAD\. Every control total now matches\./)).toBeInTheDocument();
    const [post] = posts("/quarantine/1/release");
    expect(post.body).toEqual({ party: "V001", version: 1 });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(within(screen.getByRole("table", { name: "Quarantined records" })).getByText("By ops")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Release record OI-BAD" })).not.toBeInTheDocument();
  });

  it("sends added master-data parties and shows a rule that still fails", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(422, { code: "MIG-0001", detail: 'invalid input: REFERENTIAL rule still fails: party "V999" is not in master data' }) : json(200, loaded);
    renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release record OI-BAD" }));
    fireEvent.change(screen.getByLabelText("Parties added to master data"), { target: { value: "V999, V998" } });
    fireEvent.click(screen.getByRole("button", { name: "Release record" }));
    expect(await screen.findByText(/REFERENTIAL rule still fails: .* \(MIG-0001\)/)).toBeInTheDocument();
    expect(posts("/release")[0].body).toEqual({ parties: ["V999", "V998"], version: 1 });
  });

  it("reloads the run on a version conflict", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(409, { code: "MIG-0006", detail: "run is at version 2" }) : json(200, loaded));
    renderAt(`/migration-factory/runs/${RUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Release record OI-BAD" }));
    fireEvent.click(screen.getByRole("button", { name: "Release record" }));
    expect(await screen.findByText(/Someone changed this run/)).toBeInTheDocument();
    await waitFor(() => expect(calls.filter((c) => c.method === "GET").length).toBeGreaterThan(1));
  });

  it("offers no release on a reconciled or project run", async () => {
    handler = () => json(200, { ...loaded, pack: "project_wip" });
    renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByRole("table", { name: "Quarantined records" })).toHaveTextContent("OI-BAD");
    expect(screen.queryByRole("button", { name: "Release record OI-BAD" })).not.toBeInTheDocument();
  });
});

const projectRun: PackRunDetail = {
  ...loaded,
  pack: "project_wip",
  recordsQuarantined: 0,
  quarantined: [],
  controlTotals: [
    { name: "project_wip.cost", key: { project: "PRJ-TM", category: "HOUR" }, source: "48000.000000", target: "48000.000000", delta: "0.000000", status: "MATCH" },
    { name: "project_wip.cost", key: { project: "PRJ-FP", category: "EXPENSE" }, source: "250.000000", target: "0.000000", delta: "-250.000000", status: "MISMATCH" },
  ],
};

const opening: ProjectOpening = {
  runId: RUN,
  projectId: "PRJ-FP",
  status: "OPENING_MISMATCH",
  totals: [projectRun.controlTotals[1]],
  lineCount: 1,
  lines: [
    {
      legacyId: "PT-001",
      category: "EXPENSE",
      transactionDate: "2026-12-01",
      costAmount: "0.000000",
      salesAmount: "0.000000",
      currency: "USD",
      billingStatus: "NON_CHARGEABLE",
      beginningBalance: true,
    },
  ],
};

describe("Project beginning balances", () => {
  it("lists a project_wip run's projects with their reconciliation and opens one", async () => {
    handler = (url) => {
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, projectRun);
      if (url.pathname === `/api/v1/migration/runs/${RUN}/projects/PRJ-FP/opening`) return json(200, opening);
      return undefined;
    };
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    const table = await findTable({ name: "Projects" });
    await waitFor(() => expect(table).toHaveTextContent("PRJ-FP"));
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent("Opening mismatch");
    expect(within(table).getAllByRole("row")[2]).toHaveTextContent("Reconciled");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByText("PRJ-FP"));
    expect(await screen.findByRole("heading", { name: "Project PRJ-FP" })).toBeInTheDocument();
    expect(await screen.findByText(/can't be invoiced or estimated/)).toBeInTheDocument();
    expect(await findTable({ name: "Opening lines" })).toHaveTextContent("PT-001");
    expect(screen.getByRole("table", { name: "Totals by category" })).toHaveTextContent("-250.00");
  });

  it("pages the opening lines by legacy id", async () => {
    const page = (from: number) => ({
      ...opening,
      status: "RECONCILED",
      lineCount: 60,
      lines: Array.from({ length: from === 0 ? 50 : 10 }, (_, i) => ({ ...opening.lines[0], legacyId: `PT-${String(from + i).padStart(3, "0")}` })),
    });
    handler = (url) => json(200, url.searchParams.get("after") ? page(50) : page(0));
    const { container } = renderAt(`/migration-factory/runs/${RUN}/projects/PRJ-TM`);
    const table = await findTable({ name: "Opening lines" });
    await waitFor(() => expect(table).toHaveTextContent("PT-049"));
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Next lines" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Opening lines" })).toHaveTextContent("PT-059"));
    expect(calls.at(-1)?.url.search).toBe("?limit=50&after=PT-049");
    expect(screen.getByRole("button", { name: "Next lines" })).toBeDisabled();
  });

  it("shows a project the run does not cover as an error", async () => {
    handler = () => json(404, { code: "MIG-0003", detail: "run does not cover project PRJ-X" });
    renderAt(`/migration-factory/runs/${RUN}/projects/PRJ-X`);
    expect(await screen.findByText(/does not cover project PRJ-X \(MIG-0003\)/)).toBeInTheDocument();
  });

  const values: ProjectRunValues = {
    sourceSystem: "AX2012",
    sourceCompany: "USMF",
    cutoff: "2026-12-31T00:00",
    freeze: "2026-12-31T18:00",
    quarantineMaxPct: "",
    lines: "projectId,legacyId,category,transactionDate,qty,costAmount,salesAmount,currency,billingStatus,wipCost,wipSales\nPRJ-TM,PT-1,hour,2026-12-01,8,400,800,usd,CHARGEABLE_UNBILLED,400,800",
    totals: "PRJ-TM,HOUR,400,800,400,800\nPRJ-FP,EXPENSE,250,,,",
  };

  it("validates the extract and groups lines and totals by project", () => {
    expect(validateProjectRun(values).input).toEqual({
      sourceSystem: "AX2012",
      sourceCompany: "USMF",
      cutoffTs: "2026-12-31T00:00:00Z",
      freezeTs: "2026-12-31T18:00:00Z",
      projects: [
        {
          projectId: "PRJ-TM",
          legacyTotals: [{ category: "HOUR", cost: "400", sales: "800", wipCost: "400", wipSales: "800" }],
          lines: [{ legacyId: "PT-1", category: "HOUR", transactionDate: "2026-12-01", qty: "8", costAmount: "400", salesAmount: "800", currency: "USD", billingStatus: "CHARGEABLE_UNBILLED", wipCost: "400", wipSales: "800" }],
        },
        { projectId: "PRJ-FP", legacyTotals: [{ category: "EXPENSE", cost: "250" }], lines: [] },
      ],
    });
    const bad = parseProjectExtract("P,L1,TRAVEL,2026-12-01,1,1,1,USD,BILLED,,\nP,L2,HOUR,01.12.2026,1,1,1,USD,BILLED,,", "P,HOUR,1,1,1,1\nP,HOUR,2,2,2,2");
    expect(bad.lineErrors).toEqual([expect.stringMatching(/Line 1: category "TRAVEL"/), expect.stringMatching(/Line 2: date "01.12.2026"/)]);
    expect(bad.totalErrors).toEqual([expect.stringMatching(/Line 2: project P already has a HOUR total/)]);
    const { errors } = validateProjectRun({ ...values, sourceCompany: "", lines: "", cutoff: "2027-01-02T00:00" });
    expect(Object.keys(errors).sort()).toEqual(["cutoff", "lines", "sourceCompany"]);
  });

  it("submits the extract and opens the run", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/packs/project_wip/runs")) return json(201, projectRun);
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, projectRun);
      return undefined;
    };
    const { container } = renderAt("/migration-factory/runs/new-project");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Legacy company"), { target: { value: "USMF" } });
    fireEvent.change(screen.getByLabelText("Extract cutoff (UTC)"), { target: { value: values.cutoff } });
    fireEvent.change(screen.getByLabelText("Legacy freeze (UTC)"), { target: { value: values.freeze } });
    fireEvent.change(screen.getByLabelText("Opening lines"), { target: { value: values.lines } });
    fireEvent.change(screen.getByLabelText("Legacy totals"), { target: { value: values.totals } });
    fireEvent.click(screen.getByRole("button", { name: "Run project_wip" }));
    expect(await screen.findByRole("heading", { name: "Pack run project_wip" })).toBeInTheDocument();
    const [post] = posts("/packs/project_wip/runs");
    expect(post.body).toMatchObject({ sourceCompany: "USMF", projects: [{ projectId: "PRJ-TM" }, { projectId: "PRJ-FP" }] });
  });
});

const unexplained: Variance = {
  varianceId: "0195f0a0-0000-7000-8000-0000000000d1",
  area: "TB",
  key: "USMF|1100|USD",
  legacy: "120.000000",
  passage: "150.000000",
  delta: "30.000000",
  class: "UNEXPLAINED",
};
const expected: Variance = { ...unexplained, varianceId: "0195f0a0-0000-7000-8000-0000000000d2", key: "USMF|4000|USD", delta: "0.010000", class: "EXPECTED", ruleId: "RND-001" };

const review: ParallelRun = {
  parallelRunId: PRUN,
  period: "2026-11",
  sourceSystem: "AX2012",
  documentsReplayed: 42000,
  revenue: "1000000.000000",
  explainedMaxPct: "0.1",
  inputHash: "cd".repeat(32),
  state: "REVIEW",
  unexplainedCount: 1,
  unexplainedAmount: "30.000000",
  explainedAmount: "0.000000",
  expectedByRule: { "RND-001": 1 },
  canSignOff: false,
  signOffBlocker: "MIG-1509 parallel run cannot be signed off: 1 unexplained variances (30.00)",
  startedBy: "alice",
  version: 1,
  createdAt: "2026-12-02T08:00:00Z",
  variances: [expected, unexplained],
};
const explained: ParallelRun = {
  ...review,
  unexplainedCount: 0,
  unexplainedAmount: "0.000000",
  explainedAmount: "30.000000",
  canSignOff: true,
  signOffBlocker: undefined,
  version: 2,
  variances: [expected, { ...unexplained, class: "EXPLAINED", reason: "Invoice 7781 after cut-off", explainedBy: "bob" }],
};

describe("Parallel runs", () => {
  it("lists runs with their state and unexplained counts and filters by state", async () => {
    handler = () => json(200, { items: [{ ...review, variances: undefined }] });
    const { container } = renderAt("/migration-factory/parallel-runs");
    const table = await findTable({ name: "Parallel runs" });
    await waitFor(() => expect(table).toHaveTextContent("2026-11"));
    expect(table).toHaveTextContent("30.00");
    expect(screen.getByRole("link", { name: "Start parallel run" })).toHaveAttribute("href", "/migration-factory/parallel-runs/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("State"), { target: { value: "SIGNED_OFF" } });
    await waitFor(() => expect(calls.at(-1)?.url.search).toBe("?state=SIGNED_OFF"));
  });

  it("shows the empty state and the error state", async () => {
    handler = () => json(200, { items: [] });
    const { unmount } = renderAt("/migration-factory/parallel-runs");
    expect(await screen.findByText("No parallel runs yet")).toBeInTheDocument();
    unmount();
    handler = () => json(503, { code: "COM-0503", detail: "Service unavailable" });
    renderAt("/migration-factory/parallel-runs");
    expect(await screen.findByText(/Service unavailable \(COM-0503\)/)).toBeInTheDocument();
  });

  it("explains a variance, then signs off after confirmation", async () => {
    const signed: ParallelRun = { ...explained, state: "SIGNED_OFF", canSignOff: false, signedOffBy: "bob", signedOffAt: "2026-12-03T09:00:00Z", version: 3 };
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/explain")) return json(200, explained);
      if (init.method === "POST" && url.pathname.endsWith("/sign-off")) return json(200, signed);
      return json(200, review);
    };
    const { container } = renderAt(`/migration-factory/parallel-runs/${PRUN}`);
    expect(await screen.findByText(/MIG-1509/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign off" })).toBeDisabled();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Explain variance TB USMF|1100|USD" }));
    fireEvent.click(screen.getByRole("button", { name: "Save reason" }));
    expect(await screen.findByText(/Give the reason/)).toBeInTheDocument();
    expect(posts("/explain")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "Invoice 7781 after cut-off" } });
    fireEvent.click(screen.getByRole("button", { name: "Save reason" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Sign off" })).toBeEnabled());
    expect(posts(`/variances/${unexplained.varianceId}/explain`)[0].body).toEqual({ reason: "Invoice 7781 after cut-off", version: 1 });
    expect(within(screen.getByRole("table", { name: "Variances" })).getByText("By bob")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Sign off" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Sign off" }));
    expect(await screen.findByText(/Signed off by bob/)).toBeInTheDocument();
    expect(posts("/sign-off")[0].body).toEqual({ version: 2 });
  });

  it("filters variances by class", async () => {
    handler = () => json(200, review);
    renderAt(`/migration-factory/parallel-runs/${PRUN}`);
    const table = await findTable({ name: "Variances" });
    await waitFor(() => expect(within(table).getAllByRole("row")).toHaveLength(3));
    fireEvent.change(screen.getByLabelText("Class"), { target: { value: "EXPECTED" } });
    expect(within(screen.getByRole("table", { name: "Variances" })).getAllByRole("row")).toHaveLength(2);
    expect(screen.getByRole("table", { name: "Variances" })).toHaveTextContent("RND-001");
  });

  it("explains why the person who started the run can't review it", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? json(403, { code: "MIG-0008", detail: "segregation of duties: MIG-001 alice started this parallel run and cannot review its variances" }) : json(200, review);
    renderAt(`/migration-factory/parallel-runs/${PRUN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Explain variance TB USMF|1100|USD" }));
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "timing" } });
    fireEvent.click(screen.getByRole("button", { name: "Save reason" }));
    expect(await screen.findByText(/Another reviewer must explain and sign off this run\. \(MIG-0008\)/)).toBeInTheDocument();
  });

  const values: ParallelRunValues = {
    period: "2026-11",
    sourceSystem: "SAPECC",
    documentsReplayed: "42000",
    revenue: "1000000.00",
    explainedMaxPct: "",
    legacy: "area,key,amount\ntb,1000|400000|EUR,500000.00",
    passage: "TB,1000|400000|EUR,500000.01",
  };

  it("validates the period, counts and tuples", () => {
    expect(validateParallelRun(values).input).toEqual({
      period: "2026-11",
      sourceSystem: "SAPECC",
      documentsReplayed: 42000,
      revenue: "1000000.00",
      legacy: [{ area: "TB", key: "1000|400000|EUR", amount: "500000.00" }],
      passage: [{ area: "TB", key: "1000|400000|EUR", amount: "500000.01" }],
    });
    const { errors } = validateParallelRun({ ...values, period: "2026-13", documentsReplayed: "-1", revenue: "abc", explainedMaxPct: "2", passage: "GL,k,1" });
    expect(Object.keys(errors).sort()).toEqual(["documentsReplayed", "explainedMaxPct", "passage", "period", "revenue"]);
    expect(parseTuples("TB,,1").errors[0]).toMatch(/key is blank/);
  });

  it("starts a parallel run and opens it, and explains a period already diffed", async () => {
    let reply = json(201, review);
    handler = (url, init) => (init.method === "POST" ? reply : url.pathname === `/api/v1/migration/parallel-runs/${PRUN}` ? json(200, review) : undefined);
    const { container } = renderAt("/migration-factory/parallel-runs/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Period"), { target: { value: values.period } });
    fireEvent.change(screen.getByLabelText("Documents replayed"), { target: { value: values.documentsReplayed } });
    fireEvent.change(screen.getByLabelText("Period revenue"), { target: { value: values.revenue } });
    fireEvent.change(screen.getByLabelText("Legacy tuples"), { target: { value: values.legacy } });
    fireEvent.change(screen.getByLabelText("Passage tuples"), { target: { value: values.passage } });
    reply = json(409, { code: "MIG-0009", detail: "a parallel run for this period and source system already exists with different input" });
    fireEvent.click(screen.getByRole("button", { name: "Diff period" }));
    expect(await screen.findByText(/open the existing run from the list\. \(MIG-0009\)/)).toBeInTheDocument();
    reply = json(201, review);
    fireEvent.click(screen.getByRole("button", { name: "Diff period" }));
    expect(await screen.findByRole("heading", { name: "Parallel run 2026-11" })).toBeInTheDocument();
    expect(new Headers(posts("/parallel-runs")[1].init.headers).get("Idempotency-Key")).toBe(new Headers(posts("/parallel-runs")[0].init.headers).get("Idempotency-Key"));
  });
});

const xref: Xref = {
  xrefId: "0195f0a0-0000-7000-8000-0000000000e1",
  system: "AX2012",
  company: "USMF",
  table: "open_items",
  legacyId: "CIV-00123",
  passageEntity: "open_item",
  passageId: "0195f0a0-0000-7000-8000-0000000000e2",
  runId: RUN,
  createdAt: "2027-01-01T04:10:00Z",
};

describe("Legacy reference", () => {
  it("resolves a legacy id to the Passage record and links the run", async () => {
    handler = () => json(200, { items: [xref] });
    const { container } = renderAt("/migration-factory/xref");
    fireEvent.click(screen.getByRole("button", { name: "Look up" }));
    expect(await screen.findByText(/Enter the legacy id/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Legacy id"), { target: { value: " CIV-00123 " } });
    fireEvent.click(screen.getByRole("button", { name: "Look up" }));
    const table = await findTable({ name: "Legacy references" });
    await waitFor(() => expect(table).toHaveTextContent("CIV-00123"));
    expect(within(table).getByRole("link", { name: "Open run" })).toHaveAttribute("href", `/migration-factory/runs/${RUN}`);
    expect(calls[0].url.search).toBe("?system=AX2012&table=open_items&id=CIV-00123");
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when no record has the legacy id, and shows errors", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration-factory/xref");
    fireEvent.change(screen.getByLabelText("Company"), { target: { value: "DEMF" } });
    fireEvent.change(screen.getByLabelText("Legacy id"), { target: { value: "X-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Look up" }));
    expect(await screen.findByText("No legacy reference")).toBeInTheDocument();
    expect(calls[0].url.searchParams.get("company")).toBe("DEMF");
    handler = () => json(422, { code: "MIG-0001", detail: "table and id are required with system" });
    fireEvent.click(screen.getByRole("button", { name: "Look up" }));
    expect(await screen.findByText(/table and id are required with system \(MIG-0001\)/)).toBeInTheDocument();
  });
});
