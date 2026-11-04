import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Run, RunComparison, RunMeasures } from "./api/client";
import scheduling from "./module";

const BASE = "5e000000-0000-4000-8000-000000000001";
const OTHER = "5e000000-0000-4000-8000-000000000002";
const NEW_RUN = "5e000000-0000-4000-8000-000000000003";
const MIX = "3f1c0000-0000-4000-8000-000000000001";
const ORDER = "b1000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init?: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

let handler: Handler;
let calls: { url: URL; init?: RequestInit }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init?: RequestInit) => {
      const url = new URL(input, "http://localhost");
      calls.push({ url, init });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
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

const baseRun: Run = {
  id: BASE,
  scope: "site",
  mode: "jobs",
  direction: "forward",
  finite: true,
  algorithm: "heuristic",
  status: "completed",
  operationsCount: 5,
  makespanMinutes: 600,
  totalLatenessMinutes: 90,
  solverStatus: null,
  messages: [],
  warnings: [],
  createdAt: "2027-03-01T06:00:00Z",
  snapshot: true,
  rollbackOf: null,
};
const otherRun: Run = { ...baseRun, id: OTHER, createdAt: "2027-03-02T06:00:00Z" };
const moveRun: Run = { ...baseRun, id: "5e000000-0000-4000-8000-000000000009", scope: "move", snapshot: false };

const measures = (jobs: number, late: number): RunMeasures => ({
  jobs,
  orders: 2,
  lateJobs: late > 0 ? 1 : 0,
  totalLateMinutes: late,
  maxLateMinutes: late,
  changeoverMinutes: 30,
  makespanMinutes: 480,
  bookedMinutes: 360,
  openMinutes: 480,
  utilizationPct: 75,
});

const comparison: RunComparison = {
  base: { run: baseRun, measures: measures(3, 90) },
  other: { run: otherRun, measures: measures(4, 0) },
  addedCount: 1,
  removedCount: 0,
  changedCount: 1,
  unchangedCount: 2,
  added: [{ jobId: "a1", orderId: ORDER, operNumber: 30, resourceId: MIX, start: "2027-03-02T08:00:00Z", end: "2027-03-02T09:00:00Z", lateMinutes: 0 }],
  removed: [],
  changed: [
    {
      jobId: "c1",
      orderId: ORDER,
      operNumber: 10,
      fromResourceId: MIX,
      toResourceId: MIX,
      fromStart: "2027-03-01T08:00:00Z",
      toStart: "2027-03-01T09:30:00Z",
      fromEnd: "2027-03-01T10:00:00Z",
      toEnd: "2027-03-01T11:30:00Z",
      startShiftMinutes: 90,
      endShiftMinutes: 90,
      lateDeltaMinutes: -90,
    },
  ],
  truncated: false,
};

describe("Run comparison", () => {
  it("picks another run and shows measures, moved, added and dropped jobs", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/compare")) return { status: 200, body: comparison };
      if (url.pathname.endsWith("/runs")) return { status: 200, body: { items: [otherRun, baseRun, moveRun] } };
      return undefined;
    };
    const { container } = renderAt(`/scheduling/runs/${BASE}/compare`);
    const select = await screen.findByLabelText("Compare run");
    const options = Array.from(select.querySelectorAll("option")).map((o) => o.value);
    expect(options).toEqual(["", OTHER]); // not the base run, not a move run
    expect(screen.queryByText("Moved jobs")).not.toBeInTheDocument();
    fireEvent.change(select, { target: { value: OTHER } });
    fireEvent.click(screen.getByRole("button", { name: "Compare" }));

    const measureTable = await findTable({ name: "Plan measures of the two runs" });
    expect(measureTable).toHaveTextContent("Total lateness1 h 30 min0");
    expect(measureTable).toHaveTextContent("Utilization75.0 %75.0 %");
    expect(screen.queryByText("The runs have a different scope")).not.toBeInTheDocument();
    const moved = await findTable({ name: "Jobs placed differently" });
    expect(moved).toHaveTextContent("+1 h 30 min");
    expect(moved).toHaveTextContent("-1 h 30 min");
    expect(await findTable({ name: "Jobs only the other run placed" })).toHaveTextContent("30");
    expect(screen.getByText("Nothing dropped")).toBeInTheDocument();
    expect(screen.getByText(/1 moved, 1 only in the other run, 0 only in the base run, 2 unchanged/)).toBeInTheDocument();
    const compare = calls.find((c) => c.url.pathname.endsWith("/compare"));
    expect(compare?.url.pathname).toBe(`/api/v1/scheduling/runs/${BASE}/compare`);
    expect(compare?.url.searchParams.get("to")).toBe(OTHER);
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("warns when the two runs have a different scope", async () => {
    const orderRun: Run = { ...otherRun, scope: "order" };
    handler = (url) => {
      if (url.pathname.endsWith("/compare")) return { status: 200, body: { ...comparison, other: { run: orderRun, measures: measures(1, 0) } } };
      if (url.pathname.endsWith("/runs")) return { status: 200, body: { items: [orderRun, baseRun] } };
      return undefined;
    };
    renderAt(`/scheduling/runs/${BASE}/compare?to=${OTHER}`);
    expect(await screen.findByText("The runs have a different scope")).toBeInTheDocument();
    expect(screen.getByText(/whole site and the other one order/)).toBeInTheDocument();
  });

  it("explains when there is no other run to compare with", async () => {
    handler = (url) => (url.pathname.endsWith("/runs") ? { status: 200, body: { items: [baseRun, moveRun] } } : undefined);
    renderAt(`/scheduling/runs/${BASE}/compare`);
    expect(await screen.findByText("No other run to compare with")).toBeInTheDocument();
  });

  it("shows a refused comparison with a retry", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/compare")) return { status: 422, body: { code: "SCHED-0143", detail: "that run kept no job snapshot" } };
      if (url.pathname.endsWith("/runs")) return { status: 200, body: { items: [otherRun] } };
      return undefined;
    };
    renderAt(`/scheduling/runs/${BASE}/compare?to=${OTHER}`);
    expect(await findAlert()).toHaveTextContent("SCHED-0143");
    handler = (url) => (url.pathname.endsWith("/compare") ? { status: 200, body: comparison } : { status: 200, body: { items: [otherRun] } });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Jobs placed differently" })).toBeInTheDocument();
  });

  it("shows a loading state and a failure to list runs", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "internal error" } });
    renderAt(`/scheduling/runs/${BASE}/compare`);
    expect(screen.getByText("Loading runs…")).toBeInTheDocument();
    expect(await findAlert()).toHaveTextContent("COM-0500");
  });
});

describe("Run rollback", () => {
  const detail = (run: Run): Handler => (url) => {
    if (url.pathname.endsWith("/kpis")) {
      return { status: 200, body: { runId: run.id, placedJobs: 0, orders: 0, lateJobs: 0, lateOrders: 0, maxLateMinutes: 0, changeoverMinutes: 0, bookedMinutes: 0, spanStart: null, spanEnd: null, resources: [] } };
    }
    if (url.pathname.endsWith(":rollback")) return undefined;
    return { status: 200, body: run };
  };

  it("confirms, sends an Idempotency-Key and opens the rollback run", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith(":rollback")) {
        expect(init?.method).toBe("POST");
        return { status: 201, body: { run: { ...baseRun, id: NEW_RUN, scope: "rollback", rollbackOf: BASE }, restored: 5, skipped: [] } };
      }
      return detail(baseRun)(url);
    };
    renderAt(`/scheduling/runs/${BASE}`);
    expect(await screen.findByRole("link", { name: "Compare with another run" })).toHaveAttribute("href", `/scheduling/runs/${BASE}/compare`);
    fireEvent.click(await screen.findByRole("button", { name: "Roll back to this run" }));
    expect(calls.some((c) => c.url.pathname.endsWith(":rollback"))).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Roll back the plan" }));
    await waitFor(() => expect(calls.some((c) => c.url.pathname.endsWith(":rollback"))).toBe(true));
    const sent = calls.find((c) => c.url.pathname.endsWith(":rollback"));
    expect(new Headers(sent?.init?.headers).get("Idempotency-Key")).toBeTruthy();
    expect(sent?.url.pathname).toBe(`/api/v1/scheduling/runs/${BASE}:rollback`);
    await waitFor(() => expect(calls.some((c) => c.url.pathname === `/api/v1/scheduling/runs/${NEW_RUN}`)).toBe(true));
  });

  it("lists what was skipped and stays on the run", async () => {
    handler = (url) =>
      url.pathname.endsWith(":rollback")
        ? { status: 201, body: { run: { ...baseRun, id: NEW_RUN, scope: "rollback", rollbackOf: BASE }, restored: 1, skipped: [{ jobId: "a0000000-0000-4000-8000-000000000001", reason: "firmed" }] } }
        : detail(baseRun)(url);
    renderAt(`/scheduling/runs/${BASE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Roll back to this run" }));
    fireEvent.click(screen.getByRole("button", { name: "Roll back the plan" }));
    expect(await screen.findByText("1 job(s) restored")).toBeInTheDocument();
    expect(screen.getByText(/firmed, so it stays where it is/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open the rollback run" })).toHaveAttribute("href", `/scheduling/runs/${NEW_RUN}`);
  });

  it("shows a refused rollback", async () => {
    handler = (url) =>
      url.pathname.endsWith(":rollback")
        ? { status: 409, body: { code: "SCHED-0144", detail: "a job of that run overlaps a booking made since; nothing was changed" } }
        : detail(baseRun)(url);
    renderAt(`/scheduling/runs/${BASE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Roll back to this run" }));
    fireEvent.click(screen.getByRole("button", { name: "Roll back the plan" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The plan changed since this run");
    expect(alert).toHaveTextContent("SCHED-0144");
  });

  it("lists the jobs that break a rule when the rollback is refused with SCHED-0146", async () => {
    const job = "a0000000-0000-4000-8000-000000000002";
    handler = (url) =>
      url.pathname.endsWith(":rollback")
        ? {
            status: 409,
            body: { code: "SCHED-0146", detail: "restoring that run would break 1 scheduling rule(s)", errors: { [job]: "SCHED-0002: segment 1 lies outside the open capacity" } },
          }
        : detail(baseRun)(url);
    renderAt(`/scheduling/runs/${BASE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Roll back to this run" }));
    fireEvent.click(screen.getByRole("button", { name: "Roll back the plan" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The restored plan would break scheduling rules");
    expect(screen.getByRole("list", { name: "Jobs that block the rollback" })).toHaveTextContent("outside the open capacity");
  });

  it("names an operation changed since the run among the skipped jobs", async () => {
    handler = (url) =>
      url.pathname.endsWith(":rollback")
        ? { status: 201, body: { run: { ...baseRun, id: NEW_RUN, scope: "rollback", rollbackOf: BASE }, restored: 2, skipped: [{ jobId: "a0000000-0000-4000-8000-000000000003", reason: "operation_changed" }] } }
        : detail(baseRun)(url);
    renderAt(`/scheduling/runs/${BASE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Roll back to this run" }));
    fireEvent.click(screen.getByRole("button", { name: "Roll back the plan" }));
    expect(await screen.findByText(/changed since the run, so the old placement is out of date/)).toBeInTheDocument();
  });

  it("explains that a move run cannot be rolled back to", async () => {
    handler = detail(moveRun);
    renderAt(`/scheduling/runs/${moveRun.id}`);
    expect(await screen.findByText("No job snapshot")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Roll back to this run" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Compare with another run" })).not.toBeInTheDocument();
  });
});
