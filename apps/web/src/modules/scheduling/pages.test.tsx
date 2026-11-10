import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { GanttBar, Network, Operation, Resource, Run, RunKpis } from "./api/client";
import { dayStart, today } from "./format";
import { resetOrderCache } from "../mfg-shared/orders";
import { resetResourceCache } from "../mfg-shared/resources";
import scheduling from "./module";
import { parseIds, validateRun } from "./NewRunPage";
import { emptyOperation, parseAttributes, validateOperation } from "./NewOperationPage";
import { describeAttributes } from "./OperationsPage";
import { validateResource } from "./NewResourcePage";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const FILL = "3f1c0000-0000-4000-8000-000000000002";
const ORDER = "b1000000-0000-4000-8000-000000000001";
const JOB1 = "0a000000-0000-4000-8000-000000000001";
const JOB2 = "0a000000-0000-4000-8000-000000000002";
const RUN = "7e000000-0000-4000-8000-000000000001";
const RUN2 = "7f000000-0000-4000-8000-000000000002";
const NETWORK = "4e000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

function json(status: number, body: unknown): Reply {
  return { status, body };
}

beforeEach(() => {
  calls = [];
  resetOrderCache();
  resetResourceCache();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      // The order numbers and engineering resources the screens look up are not what these tests are about.
      const lookup = url.pathname.endsWith("/production/orders")
        ? json(200, { items: [{ id: ORDER, orderNumber: "MO-1001" }] })
        : url.pathname.endsWith(`/production/orders/${ORDER}`)
          ? json(200, { id: ORDER, orderNumber: "MO-1001", productId: "" })
          : url.pathname.endsWith("/engineering/resources")
            ? json(200, { items: [] })
            : undefined;
      if (!lookup) calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = lookup ?? handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
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
        {scheduling.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

async function pickOrder() {
  const select = screen.getByLabelText("Production order");
  await waitFor(() => expect(within(select).getByRole("option", { name: "MO-1001" })).toBeInTheDocument());
  fireEvent.change(select, { target: { value: ORDER } });
}

function header(call: { init: RequestInit }, name: string): string | null {
  return new Headers(call.init.headers).get(name);
}

const shifts = [1, 2, 3, 4, 5].map((weekday) => ({ weekday, startMinute: 480, endMinute: 960 }));
const resources: Resource[] = [
  { id: MIX, code: "MIX-1", name: "Mixing vessel", finite: true, timeZone: "Europe/Berlin", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" },
  { id: FILL, code: "FILL-1", name: "Filler", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" },
];

// Bars inside today's default window.
const day = dayStart(today());
const at = (h: number) => new Date(Date.parse(day) + h * 3_600_000).toISOString().replace(".000Z", "Z");
const bars: GanttBar[] = [
  { jobId: JOB1, orderId: ORDER, operNumber: 10, resourceId: MIX, segment: 1, start: at(8), end: at(9), lateMinutes: 0, firmed: false, setupGroup: null, changeoverMinutes: 0, changeoverCost: null, changeoverCurrency: null, cleaningSetId: null, secondaryResourceIds: [], outsideCapacity: false },
  { jobId: JOB2, orderId: ORDER, operNumber: 20, resourceId: FILL, segment: 1, start: at(9), end: at(11), lateMinutes: 45, firmed: true, setupGroup: "WHITE", changeoverMinutes: 30, changeoverCost: "45.5", changeoverCurrency: "EUR", cleaningSetId: "WET-CLEAN", secondaryResourceIds: [MIX], outsideCapacity: true },
];

const run: Run = {
  id: RUN,
  scope: "site",
  mode: "jobs",
  direction: "forward",
  finite: true,
  algorithm: "heuristic",
  status: "completed",
  operationsCount: 12,
  makespanMinutes: 150,
  totalLatenessMinutes: 45,
  solverStatus: null,
  messages: [],
  warnings: [],
  createdAt: "2026-10-26T06:00:00Z",
};

// The run detail also asks for the run's plan measures (runkpis.test.tsx covers them).
const noKpis: RunKpis = { runId: RUN, placedJobs: 0, orders: 0, lateJobs: 0, lateOrders: 0, maxLateMinutes: 0, changeoverMinutes: 0, bookedMinutes: 0, spanStart: null, spanEnd: null, resources: [] };
const kpisOr = (url: URL, reply: () => Reply): Reply => (url.pathname.endsWith("/kpis") ? json(200, noKpis) : reply());

const operation: Operation = {
  id: JOB1,
  orderId: ORDER,
  operNumber: 10,
  minutes: 60,
  priority: 100,
  resourceIds: [FILL, MIX],
  secondaryResourceIds: [],
  setupGroup: null,
  setupMinutes: 0,
  queueBeforeMinutes: 0,
  queueAfterMinutes: 0,
  attributes: {},
  networkId: null,
  status: "unplaced",
  version: 1,
  createdAt: "2026-10-26T06:00:00Z",
};

const network: Network = {
  id: NETWORK,
  code: "MIX-FILL",
  name: "Mixing to filling",
  activeFrom: "2027-01-01",
  activeTo: null,
  edges: [{ fromResourceId: MIX, toResourceId: FILL, transferMinutes: 10, maxWaitMinutes: 120 }],
  version: 1,
  createdAt: "2026-10-26T06:00:00Z",
};

function workspace(extra: Handler = () => undefined): Handler {
  return (url, init) => {
    const custom = extra(url, init);
    if (custom) return custom;
    if (url.pathname.endsWith("/resources")) return json(200, { items: resources });
    if (url.pathname.endsWith("/gantt")) return json(200, { items: bars });
    return undefined;
  };
}

describe("Scheduling workspace", () => {
  it("draws one Gantt row per resource and lists the jobs in the window", async () => {
    handler = workspace();
    const { container } = renderAt("/scheduling");
    expect(screen.getByRole("heading", { name: "Scheduling workspace" })).toBeInTheDocument();
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => expect(table).toHaveTextContent("45 min"));
    expect(table).toHaveTextContent("Firmed");
    expect(within(table).getAllByRole("row")[2]).toHaveTextContent("30 min");
    expect(container.querySelector('svg[data-resource="MIX-1"] rect')).not.toBeNull();
    expect(container.querySelectorAll("rect[data-job]")).toHaveLength(2);
    // JOB2 opens with a 30 min changeover: drawn over the bar and named in its title.
    expect(container.querySelectorAll("rect[data-changeover]")).toHaveLength(1);
    expect(container.querySelector(`rect[data-changeover="${JOB2}"]`)).not.toBeNull();
    expect(container.querySelector(`rect[data-job="${JOB2}"] title`)?.textContent).toContain(
      "starts with a 30 min changeover to WHITE (costing 45.50 EUR, cleaning set WET-CLEAN)",
    );
    // FR-23.15: the jobs table shows the matrix cost and cleaning set; the unpriced job shows dashes.
    const rows = within(table).getAllByRole("row");
    expect(rows[2]).toHaveTextContent("45.50 EUR");
    expect(rows[2]).toHaveTextContent("WET-CLEAN");
    expect(rows[1]).not.toHaveTextContent("EUR");
    // BR-23.4: the jobs table and the bar title name the secondary resources booked with the job.
    expect(rows[2]).toHaveTextContent("MIX-1");
    expect(container.querySelector(`rect[data-job="${JOB2}"] title`)?.textContent).toContain("with secondary MIX-1");
    // AC-23.7: a job outside open capacity (a downtime added after booking) is flagged in the table and on the bar.
    expect(rows[2]).toHaveTextContent("Outside open capacity");
    expect(rows[1]).not.toHaveTextContent("Outside open capacity");
    expect(container.querySelector(`rect[data-job="${JOB2}"] title`)?.textContent).toContain("outside open capacity (SCHED-0002)");
    expect(container.querySelector(`rect[data-job="${JOB1}"] title`)?.textContent).not.toContain("outside open capacity");
    expect(within(table).getByRole("columnheader", { name: "Changeover cost" })).toBeInTheDocument();
    const gantt = calls.find((c) => c.url.pathname.endsWith("/gantt"));
    expect(gantt?.url.searchParams.get("from")).toBe(day);
    expect(gantt?.url.searchParams.get("to")).toBe(dayStart(today(), 2));
    expect(pageViolations(container)).toEqual([]);
  });

  it("previews a move, then commits it with an Idempotency-Key", async () => {
    const preview = {
      dryRun: true,
      feasible: true,
      violations: [],
      changes: [
        { operationId: JOB1, orderId: ORDER, resourceId: MIX, oldStart: at(8), oldEnd: at(9), newStart: at(12), newEnd: at(13), lateMinutes: 0 },
        { operationId: JOB2, orderId: ORDER, resourceId: FILL, oldStart: at(9), oldEnd: at(11), newStart: at(13), newEnd: at(15), lateMinutes: 0 },
      ],
      orderId: ORDER,
      orderStart: at(12),
      orderEnd: at(15),
    };
    handler = workspace((url, init) => {
      if (!url.pathname.endsWith(":move")) return undefined;
      const body = JSON.parse(String(init.body)) as { dryRun: boolean };
      return json(200, { ...preview, dryRun: body.dryRun });
    });
    renderAt("/scheduling");
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => expect(within(table).getAllByRole("button", { name: /^Move order/ })).toHaveLength(2));
    fireEvent.click(within(table).getByRole("button", { name: "Move order MO-1001 operation 10" }));
    fireEvent.change(screen.getByLabelText("New start (UTC)"), { target: { value: at(12).slice(0, 16) } });
    fireEvent.click(screen.getByRole("button", { name: "Preview move" }));

    const affected = await findTable({ name: "Affected operations" });
    expect(within(affected).getAllByRole("row")).toHaveLength(3);
    const dry = calls.find((c) => c.url.pathname.endsWith(`/jobs/${JOB1}:move`));
    expect(dry?.body).toEqual({ start: at(12), resourceId: MIX, dryRun: true });
    expect(header(dry!, "Idempotency-Key")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Confirm move" }));
    expect(await screen.findByText(/Move saved\. 2 operation\(s\) rescheduled/)).toBeInTheDocument();
    const commit = calls.filter((c) => c.url.pathname.endsWith(":move")).at(-1)!;
    expect(commit.body).toEqual({ start: at(12), resourceId: MIX, dryRun: false });
    expect(header(commit, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    await waitFor(() => expect(calls.filter((c) => c.url.pathname.endsWith("/gantt"))).toHaveLength(2));
  });

  it("shows violations from the dry run and offers no commit", async () => {
    handler = workspace((url) =>
      url.pathname.endsWith(":move")
        ? json(200, { dryRun: true, feasible: false, violations: [{ code: "SCHED-0001", detail: "overlaps a booked job on the resource", jobId: JOB2 }], changes: [] })
        : undefined,
    );
    const { container } = renderAt("/scheduling");
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => within(table).getByRole("button", { name: "Move order MO-1001 operation 10" }));
    fireEvent.click(within(table).getByRole("button", { name: "Move order MO-1001 operation 10" }));
    fireEvent.click(screen.getByRole("button", { name: "Preview move" }));
    expect(await screen.findByText("This move breaks a constraint and cannot be saved")).toBeInTheDocument();
    expect(screen.getByRole("listitem")).toHaveTextContent("overlaps a booked job on the resource (SCHED-0001)");
    // The blocking job gets the violation as its Gantt tooltip (FR-23.7).
    expect(container.querySelector(`rect[data-job="${JOB2}"] title`)).toHaveTextContent("overlaps a booked job on the resource (SCHED-0001)");
    expect(screen.queryByRole("button", { name: "Confirm move" })).not.toBeInTheDocument();
  });

  it("firms a job and reports a failed commit with its code", async () => {
    handler = workspace((url) => {
      if (url.pathname.endsWith(":firm")) return json(200, { jobId: JOB1, orderId: ORDER, resourceId: MIX, start: at(8), end: at(9), firmed: true });
      return undefined;
    });
    renderAt("/scheduling");
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => within(table).getByRole("button", { name: "Firm order MO-1001 operation 10" }));
    fireEvent.click(within(table).getByRole("button", { name: "Firm order MO-1001 operation 10" }));
    expect(await screen.findByText(/Operation 10 is firmed/)).toBeInTheDocument();
    const firm = calls.find((c) => c.url.pathname.endsWith(`/jobs/${JOB1}:firm`))!;
    expect(firm.body).toEqual({ firmed: true });
    expect(header(firm, "Idempotency-Key")).toBeTruthy();

    handler = workspace((url) => (url.pathname.endsWith(":firm") ? json(404, { code: "SCHED-0105", detail: "scheduled job not found" }) : undefined));
    fireEvent.click(within(table).getByRole("button", { name: "Unfirm order MO-1001 operation 20" }));
    expect(await screen.findByText("scheduled job not found")).toBeInTheDocument();
  });

  it("tells a new tenant to add a resource first", async () => {
    handler = (url) => (url.pathname.endsWith("/resources") || url.pathname.endsWith("/gantt") ? json(200, { items: [] }) : undefined);
    renderAt("/scheduling");
    expect(await screen.findByText("No resources yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add a resource" })).toHaveAttribute("href", "/scheduling/resources/new");
  });

  it("shows a load failure and retries", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/scheduling");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("internal error");
    handler = workspace();
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Jobs in window" })).toBeInTheDocument();
  });
});

describe("Capacity load", () => {
  it("lists load by resource code and flags bottlenecks", async () => {
    handler = (url) =>
      url.pathname.endsWith("/capacity-load")
        ? json(200, {
            items: [
              { resourceId: MIX, weekStart: "2026-10-26", openMinutes: 2400, bookedMinutes: 2280, loadPct: 95, bottleneck: true },
              { resourceId: FILL, weekStart: "2026-10-26", openMinutes: 0, bookedMinutes: 0, loadPct: 0, bottleneck: false },
            ],
          })
        : json(200, { items: resources });
    const { container } = renderAt("/scheduling/load");
    const table = await findTable({ name: "Capacity load" });
    await waitFor(() => expect(table).toHaveTextContent("MIX-1"));
    expect(table).toHaveTextContent("Bottleneck");
    expect(table).toHaveTextContent("40.0");
    expect(table).toHaveTextContent("95.0");
    expect(calls[0].url.searchParams.get("from")).toBe(today());
    expect(calls[0].url.searchParams.get("weeks")).toBe("4");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Weeks"), { target: { value: "8" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("weeks") === "8")).toBe(true));
  });

  it("explains an empty load and shows errors", async () => {
    handler = () => json(200, { items: [] });
    const first = renderAt("/scheduling/load");
    expect(await screen.findByText("No capacity to show")).toBeInTheDocument();
    first.unmount();
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty scheduling.read" });
    renderAt("/scheduling/load");
    expect(await findAlert()).toHaveTextContent("missing duty scheduling.read");
  });
});

describe("Schedule runs", () => {
  it("lists runs and opens one", async () => {
    handler = (url) => kpisOr(url, () => (url.pathname.endsWith(`/runs/${RUN}`) ? json(200, { ...run, messages: [`${JOB1}: no candidate resource has open capacity in the horizon`] }) : json(200, { items: [run, { ...run, id: RUN2, direction: "backward" }] })));
    const { container } = renderAt("/scheduling/runs");
    const table = await findTable({ name: "Schedule runs" });
    await waitFor(() => expect(table).toHaveTextContent("2 h 30 min"));
    expect(table).toHaveTextContent("Whole site");
    expect(within(table).getByText("Forward")).toBeInTheDocument();
    expect(within(table).getByText("Backward")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getAllByRole("button")[0]);
    expect(await screen.findByRole("heading", { name: "Schedule run" })).toBeInTheDocument();
    expect(await screen.findByText(/no candidate resource has open capacity/)).toBeInTheDocument();
    expect(screen.getByText("Heuristic")).toBeInTheDocument();
    expect(screen.getByText("No warnings")).toBeInTheDocument();
  });

  it("lists the warnings a run raised on placed operations", async () => {
    const warnings = [
      { code: "SCHED-0012", operationId: JOB1, detail: "setup matrix has no entry A -> D; route setup time 15 min used" },
      { code: "SCHED-0014", operationId: JOB2, detail: "job does not match any block on PAINT-1; placed in unblocked capacity" },
    ];
    handler = (url) => kpisOr(url, () => json(200, { ...run, warnings, messages: [`${JOB1}: SCHED-0009 NETWORK_INFEASIBLE: no allowed successor resource in network MIX-FILL`] }));
    const { container } = renderAt(`/scheduling/runs/${RUN}`);
    const list = await screen.findByRole("list", { name: "Run warnings" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(list).toHaveTextContent("SCHED-0012 No setup matrix entry");
    expect(list).toHaveTextContent("route setup time 15 min used");
    expect(list).toHaveTextContent("SCHED-0014 No matching capacity block");
    expect(screen.getByText(/NETWORK_INFEASIBLE/)).toBeInTheDocument();
    expect(await screen.findByText("No jobs carry this run")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains an empty run list", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/scheduling/runs");
    expect(await screen.findByText("No schedule runs yet")).toBeInTheDocument();
  });

  it("shows a missing run as an error", async () => {
    handler = () => json(404, { code: "SCHED-0104", detail: "schedule run not found" });
    renderAt(`/scheduling/runs/${RUN}`);
    expect(await findAlert()).toHaveTextContent("schedule run not found");
  });

  it("validates a selection run before calling the service", () => {
    const base = { scope: "selection" as const, mode: "jobs" as const, direction: "forward" as const, finite: true, operationIds: "", start: "", horizonDays: "" };
    expect(validateRun(base).operationIds).toBe("List at least one operation id.");
    expect(validateRun({ ...base, operationIds: "nope" }).operationIds).toBe("Every entry must be an operation id (a UUID).");
    expect(validateRun({ ...base, operationIds: JOB1, horizonDays: "10" })).toEqual({ horizonDays: "Enter a whole number of days from 30 to 730." });
    expect(parseIds(`${JOB1}\n ${JOB2.toUpperCase()},`)).toEqual([JOB1, JOB2]);
  });

  it("starts an infinite site run and opens it", async () => {
    handler = (url, init) => kpisOr(url, () => (init.method === "POST" ? json(201, { ...run, finite: false }) : json(200, { ...run, finite: false })));
    const { container } = renderAt("/scheduling/runs/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByLabelText("Finite capacity"));
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    expect(await screen.findByRole("heading", { name: "Schedule run" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.url.pathname).toBe("/api/v1/scheduling/runs");
    expect(post.body).toEqual({ scope: "site", mode: "jobs", direction: "forward", finite: false });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(await screen.findByText("Infinite")).toBeInTheDocument();
  });

  it("starts a backward run and shows the late-fallback warning on the run", async () => {
    const warning = { code: "SCHED-0003", operationId: JOB1, detail: "order a is late by 90 min (backward scheduling fell back to forward)" };
    handler = (url, init) => kpisOr(url, () => json(init.method === "POST" ? 201 : 200, { ...run, direction: "backward", warnings: [warning] }));
    renderAt("/scheduling/runs/new");
    fireEvent.change(screen.getByLabelText("Direction"), { target: { value: "backward" } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    const list = await screen.findByRole("list", { name: "Run warnings" });
    expect(list).toHaveTextContent("SCHED-0003 Order late after backward fallback");
    expect(list).toHaveTextContent("fell back to forward");
    expect(screen.getByText("Backward")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ scope: "site", mode: "jobs", direction: "backward", finite: true });
  });

  it("puts server field errors next to the field", async () => {
    handler = () => json(400, { code: "COM-0001", detail: "request failed validation", errors: { start: "must not be before today" } });
    renderAt("/scheduling/runs/new");
    fireEvent.change(screen.getByLabelText("Start (UTC)"), { target: { value: "2020-01-01T08:00" } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    expect(await screen.findByText("must not be before today")).toBeInTheDocument();
  });
});

describe("Resources", () => {
  it("lists resources with their calendars and opens one with its open capacity", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/capacity")) return json(200, { items: [{ start: at(8), end: at(16), minutes: 480 }] });
      if (url.pathname.endsWith("/block-utilization")) return json(200, { items: [] });
      if (url.pathname.endsWith(`/resources/${MIX}`)) return json(200, resources[0]);
      return json(200, { items: resources });
    };
    const { container } = renderAt("/scheduling/resources");
    const table = await findTable({ name: "Resources" });
    await waitFor(() => expect(table).toHaveTextContent("Mon 08:00–16:00"));
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "MIX-1" }));
    expect(await screen.findByRole("heading", { name: "MIX-1" })).toBeInTheDocument();
    expect(await screen.findByText("8 h open", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("Europe/Berlin")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Open intervals" })).toHaveTextContent("480");
    const capacity = calls.find((c) => c.url.pathname.endsWith("/capacity"))!;
    expect(capacity.url.searchParams.get("from")).toBe(day);
  });

  it("shows the block windows of the next week with held, used and unused minutes", async () => {
    const windows = [
      {
        blockId: "b0000000-0000-4000-8000-000000000001",
        code: "MON-WHITE",
        priority: 10,
        condition: [{ attribute: "color", op: "=", value: "white" }],
        start: at(8),
        end: at(12),
        blockMinutes: 240,
        matchedMinutes: 90,
        unusedMinutes: 150,
      },
    ];
    handler = (url) => {
      if (url.pathname.endsWith("/capacity")) return json(200, { items: [] });
      if (url.pathname.endsWith(`/resources/${MIX}/block-utilization`)) return json(200, { items: windows });
      if (url.pathname.endsWith(`/resources/${MIX}`)) return json(200, resources[0]);
      return undefined;
    };
    const { container } = renderAt(`/scheduling/resources/${MIX}`);
    const table = await findTable({ name: "Block windows" });
    await waitFor(() => expect(table).toHaveTextContent("MON-WHITE"));
    const row = within(table).getByText("MON-WHITE").closest("tr")!;
    expect(row).toHaveTextContent("color = white");
    expect(within(row).getAllByRole("cell").map((c) => c.textContent)).toEqual(expect.arrayContaining(["240", "90", "150"]));
    expect(screen.getByText("Blocks hold 4 h of open time; matching jobs use 1 h 30 min.")).toBeInTheDocument();
    const call = calls.find((c) => c.url.pathname.endsWith("/block-utilization"))!;
    expect(call.url.searchParams.get("from")).toBe(day);
    expect(call.url.searchParams.get("to")).toBe(dayStart(today(), 7));
    expect(screen.getByRole("link", { name: "New block" })).toHaveAttribute("href", "/scheduling/capacity-blocks/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when a resource has no block windows, and keeps the page when they fail to load", async () => {
    let fail = true;
    handler = (url) => {
      if (url.pathname.endsWith("/capacity")) return json(200, { items: [{ start: at(8), end: at(16), minutes: 480 }] });
      if (url.pathname.endsWith("/block-utilization")) {
        return fail ? json(500, { code: "COM-0500", detail: "database unavailable" }) : json(200, { items: [] });
      }
      if (url.pathname.endsWith(`/resources/${MIX}`)) return json(200, resources[0]);
      return undefined;
    };
    renderAt(`/scheduling/resources/${MIX}`);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the block windows");
    expect(screen.getByRole("table", { name: "Open intervals" })).toHaveTextContent("480");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No block windows in the next 7 days")).toBeInTheDocument();
    expect(screen.queryByText(/Blocks hold/)).not.toBeInTheDocument();
  });

  it("validates a new resource and builds one shift per working day", () => {
    const values = { code: "mix", name: "", timeZone: "Mars", finite: true, days: [], start: "16:00", end: "08:00" };
    expect(validateResource(values).errors).toEqual({
      code: "Use 1 to 32 capital letters, digits, _ or -, starting with a letter or digit.",
      name: "Enter a name of up to 120 characters.",
      days: "Choose at least one working day.",
      end: "The shift must end after it starts.",
    });
    expect(validateResource({ code: "MIX-1", name: "Mixer", timeZone: "UTC", finite: true, days: [1], start: "08:00", end: "16:00", engineeringResourceId: "x" }).errors.engineeringResourceId).toBeTruthy();
    for (const bad of ["0", "501", "1.5", "x"]) {
      expect(validateResource({ code: "MIX-1", name: "Mixer", timeZone: "UTC", finite: true, days: [1], start: "08:00", end: "16:00", efficiencyPct: bad }).errors.efficiencyPct).toBeTruthy();
    }
    expect(validateResource({ code: "MIX-1", name: "Mixer", timeZone: "UTC", finite: true, days: [1], start: "08:00", end: "16:00", efficiencyPct: " 80 " }).input?.efficiencyPct).toBe(80);
    expect(validateResource({ code: "MIX-1", name: "Mixer", timeZone: "UTC", finite: true, days: [1], start: "08:00", end: "16:00" }).input?.efficiencyPct).toBe(100);
    const ok = validateResource({ code: "MIX-1", name: "Mixer", timeZone: "Europe/Berlin", finite: false, days: [1, 2], start: "22:00", end: "00:00" });
    expect(ok.input?.shifts).toEqual([
      { weekday: 1, startMinute: 1320, endMinute: 1440 },
      { weekday: 2, startMinute: 1320, endMinute: 1440 },
    ]);
  });

  it("creates a resource and shows a duplicate code next to the field", async () => {
    handler = () => json(409, { code: "SCHED-0101", detail: "a resource with this code already exists" });
    const { container } = renderAt("/scheduling/resources/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "mix-1" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Mixer" } });
    fireEvent.click(screen.getByLabelText("Saturday"));
    fireEvent.click(screen.getByRole("button", { name: "Create resource" }));
    expect(await screen.findByText("A resource with this code already exists.")).toBeInTheDocument();
    const post = calls[0];
    expect(post.body).toMatchObject({ code: "MIX-1", name: "Mixer", timeZone: "UTC", finite: true });
    expect((post.body as { shifts: { weekday: number }[] }).shifts.map((s) => s.weekday)).toEqual([1, 2, 3, 4, 5, 6]);
  });

  it("shows an empty resource list", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/scheduling/resources");
    expect(await screen.findByText("No resources yet")).toBeInTheDocument();
  });
});

describe("Operations", () => {
  it("lists operations with candidate codes and filters by status", async () => {
    const constrained = { ...operation, id: JOB2, operNumber: 20, setupGroup: "WHITE", attributes: { color: "white" }, networkId: NETWORK };
    handler = (url) => {
      if (url.pathname.endsWith("/operations")) return json(200, { items: [operation, constrained] });
      if (url.pathname.endsWith("/networks")) return json(200, { items: [network] });
      return json(200, { items: resources });
    };
    const { container } = renderAt("/scheduling/operations");
    const table = await findTable({ name: "Operations" });
    await waitFor(() => expect(table).toHaveTextContent("FILL-1, MIX-1"));
    expect(table).toHaveTextContent("Unplaced");
    const row = within(table).getAllByRole("row")[2];
    expect(row).toHaveTextContent("WHITE");
    expect(row).toHaveTextContent("MIX-FILL");
    expect(row).toHaveTextContent("color=white");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "unplaced" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("status") === "unplaced")).toBe(true));
  });

  it("validates a new operation", () => {
    const values = { ...emptyOperation, orderId: "x", operNumber: "0", priority: "2000", setupGroup: "white", setupMinutes: "99999", queueBeforeMinutes: "-5", queueAfterMinutes: "x", attributes: "color" };
    expect(Object.keys(validateOperation(values).errors).sort()).toEqual([
      "attributes",
      "minutes",
      "operNumber",
      "orderId",
      "priority",
      "queueAfterMinutes",
      "queueBeforeMinutes",
      "resourceIds",
      "setupGroup",
      "setupMinutes",
    ]);
  });

  it("reads attributes as name = value lines", () => {
    expect(parseAttributes(" color = white \n\ngrade=2")).toEqual({ attributes: { color: "white", grade: "2" } });
    expect(parseAttributes("color white").error).toBe("Line 1: write name = value.");
    expect(parseAttributes(`campaignKey = ${"F".repeat(65)}`).error).toBe("Line 1: a campaign key is at most 64 characters.");
    expect(parseAttributes(`campaignKey = ${"F".repeat(64)}`).error).toBeUndefined();
    expect(parseAttributes("color=white\ncolor=red").error).toBe("Line 2: color is listed twice.");
    expect(parseAttributes(" = white").error).toBe("Line 1: the name is 1 to 64 characters.");
    expect(describeAttributes({ grade: "2", color: "white" })).toBe("color=white, grade=2");
  });

  it("adds an operation with candidates in the order they were checked", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, operation);
      if (url.pathname.endsWith("/operations")) return json(200, { items: [operation] });
      return json(200, { items: resources });
    };
    const { container } = renderAt("/scheduling/operations/new");
    const fill = await screen.findByLabelText("FILL-1");
    expect(pageViolations(container)).toEqual([]);
    await pickOrder();
    fireEvent.change(screen.getByLabelText("Minutes"), { target: { value: "90" } });
    fireEvent.change(screen.getByLabelText("Due (UTC)"), { target: { value: "2026-11-02T17:00" } });
    fireEvent.click(fill);
    fireEvent.click(screen.getByLabelText("MIX-1"));
    expect(screen.getByText("#2")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add operation" }));
    expect(await findTable({ name: "Operations" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ orderId: ORDER, operNumber: 10, minutes: 90, dueAt: "2026-11-02T17:00:00Z", resourceIds: [FILL, MIX] });
  });

  it("adds an operation with its setup group, route setup, network and attributes", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, operation);
      if (url.pathname.endsWith("/operations")) return json(200, { items: [operation] });
      if (url.pathname.endsWith("/networks")) return json(200, { items: [network] });
      return json(200, { items: resources });
    };
    const { container } = renderAt("/scheduling/operations/new");
    fireEvent.click(await screen.findByLabelText("FILL-1"));
    expect(pageViolations(container)).toEqual([]);
    await pickOrder();
    fireEvent.change(screen.getByLabelText("Operation number"), { target: { value: "20" } });
    fireEvent.change(screen.getByLabelText("Minutes"), { target: { value: "60" } });
    fireEvent.change(screen.getByLabelText(/^Setup group/), { target: { value: "WHITE" } });
    fireEvent.change(screen.getByLabelText(/^Route setup/), { target: { value: "25" } });
    fireEvent.change(screen.getByLabelText(/^Queue before/), { target: { value: "30" } });
    fireEvent.change(screen.getByLabelText(/^Queue after/), { target: { value: "45" } });
    fireEvent.change(screen.getByLabelText(/^Resource network/), { target: { value: NETWORK } });
    fireEvent.change(screen.getByLabelText(/^Attributes/), { target: { value: "color = white\ngrade = 2" } });
    fireEvent.click(screen.getByRole("button", { name: "Add operation" }));
    expect(await findTable({ name: "Operations" })).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({
      orderId: ORDER,
      operNumber: 20,
      minutes: 60,
      resourceIds: [FILL],
      setupGroup: "WHITE",
      setupMinutes: 25,
      queueBeforeMinutes: 30,
      queueAfterMinutes: 45,
      attributes: { color: "white", grade: "2" },
      networkId: NETWORK,
    });
  });

  it("adds an operation with secondary resources booked alongside the primary (BR-23.4)", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, { ...operation, secondaryResourceIds: [MIX] });
      if (url.pathname.endsWith("/operations")) return json(200, { items: [operation] });
      return json(200, { items: resources });
    };
    const { container } = renderAt("/scheduling/operations/new");
    fireEvent.click(await screen.findByLabelText("FILL-1"));
    expect(pageViolations(container)).toEqual([]);
    // A candidate cannot be picked as its own secondary.
    expect(screen.getByLabelText("FILL-1 (candidate)")).toBeDisabled();
    fireEvent.click(document.getElementById(`operation-secondary-${MIX}`)!);
    await pickOrder();
    fireEvent.change(screen.getByLabelText("Minutes"), { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: "Add operation" }));
    expect(await findTable({ name: "Operations" })).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ orderId: ORDER, operNumber: 10, minutes: 60, resourceIds: [FILL], secondaryResourceIds: [MIX] });
  });

  it("drops a resource from the secondaries when it becomes a candidate", async () => {
    handler = () => json(200, { items: resources });
    renderAt("/scheduling/operations/new");
    await screen.findByLabelText("FILL-1");
    const secondary = document.getElementById(`operation-secondary-${MIX}`) as HTMLInputElement;
    fireEvent.click(secondary);
    expect(secondary.checked).toBe(true);
    fireEvent.click(screen.getByLabelText("MIX-1"));
    expect(secondary.checked).toBe(false);
    expect(secondary).toBeDisabled();
  });

  it("validates secondary resources", () => {
    const base = { ...emptyOperation, orderId: ORDER, minutes: "30", resourceIds: [FILL] };
    expect(validateOperation({ ...base, secondaryResourceIds: [FILL] }).errors.secondaryResourceIds).toBe("A candidate resource cannot also be a secondary resource.");
    const many = ["1", "2", "3", "4", "5", "6"].map((n) => `3f1c0000-0000-4000-8000-00000000010${n}`);
    expect(validateOperation({ ...base, secondaryResourceIds: many }).errors.secondaryResourceIds).toBe("Choose at most 5 secondary resources.");
    expect(validateOperation({ ...base, secondaryResourceIds: [MIX] }).input?.secondaryResourceIds).toEqual([MIX]);
    expect(validateOperation(base).input?.secondaryResourceIds).toBeUndefined();
  });

  it("shows a vanished network on its field", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "SCHED-0109", detail: "the resource network does not exist" });
      if (url.pathname.endsWith("/networks")) return json(200, { items: [network] });
      return json(200, { items: resources });
    };
    renderAt("/scheduling/operations/new");
    fireEvent.click(await screen.findByLabelText("MIX-1"));
    await pickOrder();
    fireEvent.change(screen.getByLabelText("Minutes"), { target: { value: "60" } });
    fireEvent.change(screen.getByLabelText(/^Resource network/), { target: { value: NETWORK } });
    fireEvent.click(screen.getByRole("button", { name: "Add operation" }));
    expect(await screen.findByText("This resource network no longer exists.")).toBeInTheDocument();
  });

  it("asks for a resource before an operation can be added", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/scheduling/operations/new");
    expect(await screen.findByText("Add a resource first")).toBeInTheDocument();
  });
});
