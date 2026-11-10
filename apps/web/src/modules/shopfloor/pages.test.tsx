import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Registration, Worker, WorkerDay } from "./api/client";
import { addDays, today } from "./format";
import shopfloor from "./module";
import { validateWorker } from "./NewWorkerPage";
import { approvalRuns } from "./TimeApprovalPage";

const ANA = "0195f0a0-0000-7000-8000-000000000001";
const BEN = "0195f0a0-0000-7000-8000-000000000002";
const PARTY = "0195f0a0-0000-7000-8000-0000000000aa";
const BUNDLE = "0195f0a0-0000-7000-8000-0000000000b1";
const JOB1 = "0195f0a0-0000-7000-8000-0000000000c1";
const JOB2 = "0195f0a0-0000-7000-8000-0000000000c2";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

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
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {shopfloor.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

function header(call: { init: RequestInit }, name: string): string | null {
  return new Headers(call.init.headers).get(name);
}

const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));

const worker = (id: string, name: string, badge: string | null): Worker => ({
  id,
  partyId: PARTY,
  name,
  badgeId: badge,
  userId: null,
  costRate: "20.000000",
  currency: "USD",
  active: true,
  version: 1,
  createdAt: "2026-10-01T08:00:00Z",
  profileId: null,
  payAgreementId: null,
  defaultResourceId: null,
});
const workers = [worker(ANA, "Ana Silva", "B-1"), worker(BEN, "Ben Ito", null)];

const line = (type: string, hours: string, amount: string) => ({ type, hours, multiplier: type === "base" ? "1" : "1.5", amount, currency: "USD" });

function workerDay(workerId: string, workerName: string, workDate: string, extra: Partial<WorkerDay> = {}): WorkerDay {
  return {
    workerId,
    workerName,
    workDate,
    hours: "10.0000",
    flags: [],
    status: "calculated",
    timeZone: "UTC",
    calculatedAt: "2026-10-21T00:00:00Z",
    approvedBy: null,
    approvedAt: null,
    payLines: [line("base", "8.00", "160.000000"), line("overtime-1.5", "2.00", "60.000000")],
    ...extra,
  };
}

describe("Workers list", () => {
  it("lists workers with badge, rate and status", async () => {
    handler = (url) => (url.pathname.endsWith("/workers") ? json(200, { items: workers }) : undefined);
    const { container } = renderAt("/shopfloor/workers");
    expect(screen.getByRole("heading", { name: "Workers" })).toBeInTheDocument();
    const table = await findTable({ name: "Workers" });
    await waitFor(() => expect(table).toHaveTextContent("Ana Silva"));
    expect(table).toHaveTextContent("B-1");
    expect(table).toHaveTextContent("20.00 USD/h");
    expect(screen.getByRole("link", { name: "New worker" })).toHaveAttribute("href", "/shopfloor/workers/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/shopfloor/workers");
    expect(await screen.findByText("No workers yet")).toBeInTheDocument();
  });

  it("shows the error with its code and retries", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: workers }));
    renderAt("/shopfloor/workers");
    expect(await screen.findByText(/Service unavailable/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Ana Silva")).toBeInTheDocument();
  });
});

describe("New worker", () => {
  const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

  it("validates the fields the service checks", () => {
    const base = { name: "Ana", partyId: PARTY, badgeId: "", userId: "", costRate: "21.50", currency: "USD" };
    expect(validateWorker(base).input).toEqual({ name: "Ana", partyId: PARTY, badgeId: null, userId: null, costRate: "21.50", currency: "USD" });
    const { errors } = validateWorker({ ...base, name: " ", partyId: "x", costRate: "-1", currency: "usd" });
    expect(Object.keys(errors).sort()).toEqual(["costRate", "currency", "name", "partyId"]);
  });

  it("creates the worker with an Idempotency-Key and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/workers")) return json(201, worker(ANA, "Ana Silva", "B-1"));
      if (url.pathname.endsWith(`/workers/${ANA}`)) return json(200, worker(ANA, "Ana Silva", "B-1"));
      if (url.pathname.endsWith("/registrations") || url.pathname.endsWith("/worker-days")) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/shopfloor/workers/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Name", "Ana Silva");
    fill("Party id", PARTY);
    fill("Badge", "B-1");
    fill("Cost rate per hour", "20");
    fireEvent.click(screen.getByRole("button", { name: "Create worker" }));
    expect(await screen.findByRole("heading", { name: "Ana Silva" })).toBeInTheDocument();
    const [create] = posts("/workers");
    expect(create.body).toEqual({ name: "Ana Silva", partyId: PARTY, badgeId: "B-1", userId: null, costRate: "20", currency: "USD" });
    expect(header(create, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("does not send an invalid form and names the fields", async () => {
    renderAt("/shopfloor/workers/new");
    fireEvent.click(screen.getByRole("button", { name: "Create worker" }));
    expect(await screen.findByText("Enter a name of up to 120 characters.")).toBeInTheDocument();
    expect(screen.getByText("Enter the employee's party id (a UUID).")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("shows a taken badge on the badge field and keeps the key for a retry", async () => {
    handler = () => json(409, { code: "SF-0105", detail: "badge is already assigned to another worker" });
    renderAt("/shopfloor/workers/new");
    fill("Name", "Ana Silva");
    fill("Party id", PARTY);
    fill("Badge", "B-1");
    fill("Cost rate per hour", "20");
    fireEvent.click(screen.getByRole("button", { name: "Create worker" }));
    expect(await screen.findByText("This badge is already assigned to another worker.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create worker" }));
    await waitFor(() => expect(posts("/workers")).toHaveLength(2));
    const [first, second] = posts("/workers");
    expect(header(second, "Idempotency-Key")).toBe(header(first, "Idempotency-Key"));
  });
});

describe("Worker detail", () => {
  const registrations: Registration[] = [
    {
      id: "0195f0a0-0000-7000-8000-0000000000a1",
      workerId: ANA,
      kind: "clock-in",
      jobId: null,
      bundleId: null,
      qtyGood: "0",
      qtyScrap: "0",
      scrapReasonId: null,
      startedAt: "2026-10-20T09:00:00Z",
      stoppedAt: null,
      hours: null,
      serverReceivedAt: "2026-10-20T09:00:01Z",
      deviceId: "kiosk-1",
      offline: true,
      note: null,
      clockOffsetSeconds: null,
      flags: [],
    },
    {
      id: "0195f0a0-0000-7000-8000-0000000000a2",
      workerId: ANA,
      kind: "job-stop",
      jobId: JOB1,
      bundleId: BUNDLE,
      qtyGood: "30",
      qtyScrap: "0",
      scrapReasonId: null,
      startedAt: "2026-10-20T09:07:00Z",
      stoppedAt: "2026-10-20T11:07:00Z",
      hours: "1.50",
      serverReceivedAt: "2026-10-20T12:00:00Z",
      deviceId: "kiosk-1",
      offline: true,
      note: null,
      clockOffsetSeconds: 420,
      flags: [],
    },
  ];

  function detail(extra: Handler = () => undefined): Handler {
    return (url, init) => {
      const custom = extra(url, init);
      if (custom) return custom;
      if (url.pathname.endsWith(`/workers/${ANA}`)) return json(200, workers[0]);
      if (url.pathname.endsWith("/registrations")) return json(200, { items: registrations });
      if (url.pathname.endsWith("/worker-days")) return json(200, { items: [workerDay(ANA, "Ana Silva", "2026-10-20")] });
      return undefined;
    };
  }

  it("shows the worker, their registrations and their worker-days for the last 7 days", async () => {
    handler = detail();
    const { container } = renderAt(`/shopfloor/workers/${ANA}`);
    expect(await screen.findByRole("heading", { name: "Ana Silva" })).toBeInTheDocument();
    expect(screen.getByText("20.00 USD per hour")).toBeInTheDocument();
    const regs = await findTable({ name: "Registrations" });
    await waitFor(() => expect(regs).toHaveTextContent("clock-in"));
    expect(regs).toHaveTextContent("Offline replay");
    expect(regs).toHaveTextContent("Clock corrected +7 min");
    expect(within(regs).getByRole("link", { name: "View bundle" })).toHaveAttribute("href", `/shopfloor/bundles/${BUNDLE}`);
    const days = screen.getByRole("table", { name: "Worker-days" });
    expect(days).toHaveTextContent("base 8.00 h, overtime-1.5 2.00 h");
    expect(days).toHaveTextContent("220.00 USD");
    const list = calls.find((c) => c.url.pathname.endsWith("/registrations"));
    expect(list?.url.searchParams.get("workerId")).toBe(ANA);
    expect(list?.url.searchParams.get("from")).toBe(addDays(today(), -6));
    expect(list?.url.searchParams.get("to")).toBe(today());
    expect(list?.url.searchParams.get("timeZone")).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
  });

  it("calculates pay for the shown range and reloads the days", async () => {
    handler = detail((url, init) =>
      init.method === "POST" && url.pathname.endsWith("/pay:calculate") ? json(200, { workerId: ANA, from: "a", to: "b", timeZone: "UTC", days: [{}, {}] }) : undefined,
    );
    renderAt(`/shopfloor/workers/${ANA}`);
    fireEvent.click(await screen.findByRole("button", { name: "Calculate pay" }));
    expect(await screen.findByText("Calculated 2 days in UTC.")).toBeInTheDocument();
    const [calc] = posts("/pay:calculate");
    expect(calc.body).toMatchObject({ workerId: ANA, from: addDays(today(), -6), to: today() });
    expect(header(calc, "Idempotency-Key")).toBeTruthy();
    await waitFor(() => expect(calls.filter((c) => c.url.pathname.endsWith("/worker-days")).length).toBeGreaterThan(1));
  });

  it("refuses a range longer than the service lists without calling it", async () => {
    handler = detail();
    renderAt(`/shopfloor/workers/${ANA}`);
    await screen.findByRole("heading", { name: "Ana Silva" });
    fireEvent.change(screen.getByLabelText("From"), { target: { value: addDays(today(), -40) } });
    expect(await screen.findByText("Show at most 31 days.")).toBeInTheDocument();
    await waitFor(() => expect(calls.filter((c) => c.url.searchParams.get("from") === addDays(today(), -40))).toHaveLength(0));
  });

  it("shows a missing worker as an error", async () => {
    handler = () => json(404, { code: "SF-0101", detail: "worker not found" });
    renderAt(`/shopfloor/workers/${ANA}`);
    expect(await screen.findByText(/worker not found/)).toBeInTheDocument();
  });
});

describe("Time approval", () => {
  function approval(days: WorkerDay[], extra: Handler = () => undefined): Handler {
    return (url, init) => {
      const custom = extra(url, init);
      if (custom) return custom;
      if (url.pathname.endsWith("/worker-days")) return json(200, { items: days });
      if (url.pathname.endsWith("/workers")) return json(200, { items: workers });
      return undefined;
    };
  }

  it("lists the days waiting for approval and only offers approval for unflagged days", async () => {
    handler = approval([
      workerDay(ANA, "Ana Silva", "2026-10-20"),
      workerDay(BEN, "Ben Ito", "2026-10-20", { flags: ["missing-clock-out"], payLines: [] }),
    ]);
    const { container } = renderAt("/shopfloor/time");
    const table = await findTable({ name: "Worker-days" });
    await waitFor(() => expect(table).toHaveTextContent("Ben Ito"));
    expect(within(table).getByText("Missing clock-out")).toBeInTheDocument();
    expect(within(table).getByRole("button", { name: "Approve Ana Silva 2026-10-20" })).toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: "Approve Ben Ito 2026-10-20" })).toBeNull();
    expect(within(table).getByRole("link", { name: "Ana Silva" })).toHaveAttribute("href", `/shopfloor/workers/${ANA}`);
    expect(screen.getByRole("button", { name: "Approve 1 ready day" })).toBeEnabled();
    const list = calls.find((c) => c.url.pathname.endsWith("/worker-days"));
    expect(list?.url.searchParams.get("status")).toBe("calculated");
    expect(pageViolations(container)).toEqual([]);
  });

  it("approves every ready range with one keyed call per worker and reports refusals", async () => {
    handler = approval(
      [workerDay(ANA, "Ana Silva", "2026-10-20"), workerDay(ANA, "Ana Silva", "2026-10-21"), workerDay(BEN, "Ben Ito", "2026-10-20")],
      (url, init) => {
        if (init.method !== "POST" || !url.pathname.endsWith("/pay:approve")) return undefined;
        const body = JSON.parse(String(init.body)) as { workerId: string; from: string; to: string };
        if (body.workerId === BEN) return json(403, { code: "SF-0106", detail: "you cannot approve your own time registrations" });
        return json(200, { ...body, days: [{ newlyApproved: true }, { newlyApproved: true }] });
      },
    );
    renderAt("/shopfloor/time");
    fireEvent.click(await screen.findByRole("button", { name: "Approve 3 ready days" }));
    expect(await screen.findByText("Approved 2 days.")).toBeInTheDocument();
    expect(screen.getByText("Could not approve Ben Ito, 2026-10-20 to 2026-10-20")).toBeInTheDocument();
    expect(screen.getByText(/you cannot approve your own time registrations/)).toBeInTheDocument();
    const approvals = posts("/pay:approve");
    expect(approvals.map((c) => c.body)).toEqual([
      { workerId: ANA, from: "2026-10-20", to: "2026-10-21" },
      { workerId: BEN, from: "2026-10-20", to: "2026-10-20" },
    ]);
    expect(new Set(approvals.map((c) => header(c, "Idempotency-Key"))).size).toBe(2);
  });

  it("calculates the chosen worker's pay for the range", async () => {
    handler = approval([], (url, init) =>
      init.method === "POST" && url.pathname.endsWith("/pay:calculate") ? json(200, { workerId: BEN, from: "a", to: "b", timeZone: "UTC", days: [{}] }) : undefined,
    );
    renderAt("/shopfloor/time");
    expect(await screen.findByText("Nothing waiting for approval")).toBeInTheDocument();
    await screen.findByRole("option", { name: "Ben Ito" });
    fireEvent.change(screen.getByLabelText("Worker"), { target: { value: BEN } });
    fireEvent.click(screen.getByRole("button", { name: "Calculate" }));
    expect(await screen.findByText("Calculated 1 day for Ben Ito.")).toBeInTheDocument();
    expect(posts("/pay:calculate")[0].body).toMatchObject({ workerId: BEN, from: addDays(today(), -6), to: today() });
  });

  it("asks for a worker before calculating", async () => {
    handler = approval([]);
    renderAt("/shopfloor/time");
    await screen.findByRole("option", { name: "Ana Silva (B-1)" });
    fireEvent.click(screen.getByRole("button", { name: "Calculate" }));
    expect(await screen.findByText("Choose the worker whose pay to calculate.")).toBeInTheDocument();
    expect(posts("/pay:calculate")).toHaveLength(0);
  });

  it("shows a load failure with a retry", async () => {
    handler = (url) => (url.pathname.endsWith("/worker-days") ? json(500, { code: "COM-0500", detail: "boom" }) : json(200, { items: workers }));
    renderAt("/shopfloor/time");
    expect(await screen.findByText(/boom/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("approvalRuns", () => {
  it("splits a worker's ready days at a flagged day and skips approved days", () => {
    const runs = approvalRuns([
      workerDay(ANA, "Ana Silva", "2026-10-22"),
      workerDay(ANA, "Ana Silva", "2026-10-20"),
      workerDay(ANA, "Ana Silva", "2026-10-21", { flags: ["orphan-clock-out"] }),
      workerDay(ANA, "Ana Silva", "2026-10-23", { status: "approved" }),
      workerDay(ANA, "Ana Silva", "2026-10-24"),
      workerDay(BEN, "Ben Ito", "2026-10-20", { status: "approved" }),
    ]);
    expect(runs).toEqual([
      { workerId: ANA, workerName: "Ana Silva", from: "2026-10-20", to: "2026-10-20", days: 1 },
      { workerId: ANA, workerName: "Ana Silva", from: "2026-10-22", to: "2026-10-24", days: 2 },
    ]);
  });
});

describe("Bundle detail", () => {
  const job = (id: string, jobId: string, good: string, hours: string | null, offset: number | null): Registration => ({
    id,
    workerId: ANA,
    kind: "job-stop",
    jobId,
    bundleId: BUNDLE,
    qtyGood: good,
    qtyScrap: "0",
    scrapReasonId: null,
    startedAt: "2026-10-20T09:07:00Z",
    stoppedAt: hours === null ? null : "2026-10-20T11:07:00Z",
    hours,
    serverReceivedAt: "2026-10-20T12:00:00Z",
    deviceId: "kiosk-1",
    offline: offset !== null,
    note: null,
    clockOffsetSeconds: offset,
    flags: [],
  });

  it("shows the jobs with their hours share and the bundle total, worked once", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/registrations") && url.searchParams.get("bundleId") === BUNDLE) {
        return json(200, { items: [job("0195f0a0-0000-7000-8000-0000000000d1", JOB1, "30", "1.50", 420), job("0195f0a0-0000-7000-8000-0000000000d2", JOB2, "10", "0.50", 420)] });
      }
      if (url.pathname.endsWith(`/workers/${ANA}`)) return json(200, workers[0]);
      return undefined;
    };
    const { container } = renderAt(`/shopfloor/bundles/${BUNDLE}`);
    const table = await findTable({ name: "Bundle jobs" });
    expect(table).toHaveTextContent(JOB1);
    expect(table).toHaveTextContent("1.50 h");
    expect(table).toHaveTextContent("0.50 h");
    expect(table).toHaveTextContent("Clock corrected +7 min");
    expect(screen.getByText("2.00 h")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Ana Silva" })).toHaveAttribute("href", `/shopfloor/workers/${ANA}`);
    expect(screen.getByRole("link", { name: "Back to Ana Silva" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("marks the job-stops the server wrote when the worker started another job (FR-25.3)", async () => {
    const stopped = { ...job("0195f0a0-0000-7000-8000-0000000000d4", JOB1, "0", "24.00", null), flags: ["auto-stopped", "auto-stop-capped", "production-finished"] as Registration["flags"] };
    handler = (url) => {
      if (url.pathname.endsWith("/registrations")) return json(200, { items: [stopped] });
      if (url.pathname.endsWith(`/workers/${ANA}`)) return json(200, workers[0]);
      return undefined;
    };
    renderAt(`/shopfloor/bundles/${BUNDLE}`);
    const table = await findTable({ name: "Bundle jobs" });
    expect(table).toHaveTextContent("Auto-stopped");
    expect(table).toHaveTextContent("Stopped by production");
    expect(table).toHaveTextContent("Capped at 24 h");
  });

  it("shows a running bundle without a total", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/registrations")) return json(200, { items: [job("0195f0a0-0000-7000-8000-0000000000d3", JOB1, "0", null, null)] });
      if (url.pathname.endsWith(`/workers/${ANA}`)) return json(200, workers[0]);
      return undefined;
    };
    renderAt(`/shopfloor/bundles/${BUNDLE}`);
    expect(await screen.findByText("Still running")).toBeInTheDocument();
  });

  it("shows the empty state for an unknown bundle without asking for a worker", async () => {
    handler = (url) => (url.pathname.endsWith("/registrations") ? json(200, { items: [] }) : undefined);
    renderAt(`/shopfloor/bundles/${BUNDLE}`);
    expect(await screen.findByText("No registrations in this bundle")).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname.includes("/workers/"))).toBe(false);
  });

  it("shows a load failure with its code and retries", async () => {
    handler = () => json(400, { code: "COM-0001", detail: "request failed validation" });
    renderAt(`/shopfloor/bundles/${BUNDLE}`);
    expect(await screen.findByText(/request failed validation/)).toBeInTheDocument();
    handler = (url) => (url.pathname.endsWith("/registrations") ? json(200, { items: [] }) : undefined);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No registrations in this bundle")).toBeInTheDocument();
  });
});
