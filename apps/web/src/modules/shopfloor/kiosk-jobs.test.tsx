import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Job, KioskSession, RegistrationResult } from "./api/client";
import { describeStartResults, KIOSK_DEVICE_ID, stopItems } from "./KioskJobs";
import shopfloor from "./module";

const WORKER = "0195f0a0-0000-7000-8000-000000000001";
const PARTY = "0195f0a0-0000-7000-8000-000000000020";
const J1 = "0195f0a0-0000-7000-8000-0000000000a1";
const J2 = "0195f0a0-0000-7000-8000-0000000000a2";
const J3 = "0195f0a0-0000-7000-8000-0000000000a3";
const BUNDLE = "0195f0a0-0000-7000-8000-0000000000b1";
const RESOURCE = "0195f0a0-0000-7000-8000-000000000010";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;
type Item = Record<string, unknown>;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const job = (id: string, op: string, extra: Partial<Job> = {}): Job => ({
  id,
  orderRef: "MO-1",
  operationRef: op,
  resourceId: RESOURCE,
  status: "released",
  plannedQuantity: "100.0000",
  goodQuantity: "0.0000",
  scrapQuantity: "0.0000",
  sequenceStart: "2026-10-21T08:00:00Z",
  checks: [],
  results: [],
  finishedAt: null,
  version: 1,
  updatedAt: "2026-10-21T07:00:00Z",
  ...extra,
});

const session = (extra: Partial<KioskSession> = {}): KioskSession => ({
  workerId: WORKER,
  partyId: PARTY,
  name: "Ana Silva",
  userId: null,
  clockedInAt: "2026-10-21T07:00:00Z",
  openJobs: [],
  idleTimeoutMinutes: 10,
  ...extra,
});

const accepted = (id: string, extra: Partial<RegistrationResult> = {}): RegistrationResult => ({
  id,
  status: "accepted",
  flags: [],
  autoStopped: [],
  warnings: [],
  ...extra,
});

const registrationPosts = () => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith("/registrations"));
const postedItems = (i: number) => (registrationPosts()[i].body as { items: Item[] }).items;

async function signIn(sess: KioskSession, jobs: Job[] | Reply, onRegister?: (items: Item[]) => Reply) {
  handler = (url, init) => {
    if (url.pathname.endsWith("/sessions:badge")) return json(200, sess);
    if (url.pathname.endsWith("/jobs")) return Array.isArray(jobs) ? json(200, { items: jobs }) : jobs;
    if (url.pathname.endsWith("/registrations") && onRegister) return onRegister((JSON.parse(String(init.body)) as { items: Item[] }).items);
    return undefined;
  };
  const view = render(
    <MemoryRouter initialEntries={["/shopfloor/kiosk"]}>
      <Routes>
        {shopfloor.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
  fireEvent.change(screen.getByLabelText("Badge"), { target: { value: "B-42" } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  await screen.findByRole("heading", { name: "Ana Silva" });
  return view;
}

describe("Kiosk job actions", () => {
  it("starts a job, reports the job the server stopped (SF-0002), then stops it with a closed job-stop", async () => {
    const { container } = await signIn(session(), [job(J1, "OP-10"), job(J2, "OP-20")], (items) =>
      items[0].kind === "job-start"
        ? json(200, {
            items: [
              accepted(String(items[0].id), {
                autoStopped: [
                  { registrationId: "s1", jobId: J1, bundleId: null, startedAt: "2026-10-21T08:00:00Z", stoppedAt: "2026-10-21T09:30:00Z", hours: "1.5", flags: ["auto-stopped"] },
                ],
                warnings: [{ code: "SF-0002", message: "Previous job was stopped automatically." }],
              }),
            ],
          })
        : json(200, { items: items.map((it) => accepted(String(it.id))) }),
    );
    expect(await screen.findByText("No job is running. Select one below to start it.")).toBeInTheDocument();
    const table = await screen.findByRole("table", { name: "Dispatch list" });
    expect(within(table).getAllByRole("row")).toHaveLength(3);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("checkbox", { name: "Select MO-1 OP-20" }));
    fireEvent.click(screen.getByRole("button", { name: "Start job" }));
    const status = await screen.findByRole("status");
    expect(status).toHaveTextContent("Job started");
    expect(status).toHaveTextContent("MO-1 OP-10 was stopped automatically after 1.50 h (SF-0002).");
    const [start] = postedItems(0);
    expect(start).toMatchObject({ workerId: WORKER, kind: "job-start", jobId: J2, bundleId: null, deviceId: KIOSK_DEVICE_ID });
    expect(start).not.toHaveProperty("stoppedAt");
    // The current job links to its detail (the dispatch list row links there too).
    const links = screen.getAllByRole("link", { name: "MO-1 OP-20" });
    expect(links).toHaveLength(2);
    for (const link of links) expect(link).toHaveAttribute("href", `/shopfloor/jobs/${J2}`);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Stop job" }));
    expect(await screen.findByText("No job is running. Select one below to start it.")).toBeInTheDocument();
    const [stop] = postedItems(1);
    expect(stop).toMatchObject({ workerId: WORKER, kind: "job-stop", jobId: J2, bundleId: null, startedAt: start.startedAt, deviceId: KIOSK_DEVICE_ID });
    expect(Date.parse(String(stop.stoppedAt))).toBeGreaterThanOrEqual(Date.parse(String(start.startedAt)));
    expect(screen.getByRole("status")).toHaveTextContent("Work stopped");
    // The dispatch list is reloaded after each action so statuses are current.
    expect(calls.filter((c) => c.url.pathname.endsWith("/jobs"))).toHaveLength(3);
  });

  it("starts two jobs as one bundle and refuses a selection that does not fit the button", async () => {
    await signIn(session(), [job(J1, "OP-10"), job(J2, "OP-20"), job(J3, "OP-30")], (items) => json(200, { items: items.map((it) => accepted(String(it.id))) }));
    await screen.findByRole("table", { name: "Dispatch list" });
    fireEvent.click(screen.getByRole("button", { name: "Start job" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Select one job to start, or use Start as bundle.");
    fireEvent.click(screen.getByRole("checkbox", { name: "Select MO-1 OP-10" }));
    fireEvent.click(screen.getByRole("button", { name: "Start as bundle" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Select two or more jobs to start them as a bundle.");
    expect(registrationPosts()).toHaveLength(0);

    fireEvent.click(screen.getByRole("checkbox", { name: "Select MO-1 OP-30" }));
    fireEvent.click(screen.getByRole("button", { name: "Start as bundle" }));
    expect(await screen.findByRole("heading", { name: "Current bundle" })).toBeInTheDocument();
    const items = postedItems(0);
    expect(items.map((it) => it.jobId)).toEqual([J1, J3]);
    expect(items[0].bundleId).toMatch(/^[0-9a-f-]{36}$/);
    expect(items[1].bundleId).toBe(items[0].bundleId);
    expect(items[1].startedAt).toBe(items[0].startedAt);
    expect(items[1].id).not.toBe(items[0].id);
    expect(screen.getByRole("status")).toHaveTextContent("Started a bundle of 2 jobs");
    expect(screen.getAllByText("Bundle")).toHaveLength(2);
  });

  it("stops an open bundle from the session with one job-stop per job sharing the interval", async () => {
    const open = [
      { jobId: J1, bundleId: BUNDLE, startedAt: "2026-10-21T08:00:00Z" },
      { jobId: J2, bundleId: BUNDLE, startedAt: "2026-10-21T08:00:00Z" },
    ];
    await signIn(session({ openJobs: open }), [job(J1, "OP-10", { status: "started" })], (items) => json(200, { items: items.map((it) => accepted(String(it.id))) }));
    expect(await screen.findByRole("heading", { name: "Current bundle" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Stop bundle" }));
    expect(await screen.findByText("No job is running. Select one below to start it.")).toBeInTheDocument();
    const items = postedItems(0);
    expect(items).toHaveLength(2);
    for (const it of items) {
      expect(it).toMatchObject({ kind: "job-stop", bundleId: BUNDLE, startedAt: "2026-10-21T08:00:00Z", stoppedAt: items[0].stoppedAt });
    }
  });

  it("retries a failed start with the same client UUIDs, so it is never registered twice", async () => {
    let fail = true;
    await signIn(session(), [job(J1, "OP-10")], (items) =>
      fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: items.map((it) => accepted(String(it.id))) }),
    );
    await screen.findByRole("table", { name: "Dispatch list" });
    fireEvent.click(screen.getByRole("checkbox", { name: "Select MO-1 OP-10" }));
    fireEvent.click(screen.getByRole("button", { name: "Start job" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Service unavailable (COM-0503)");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Start job" }));
    expect(await screen.findByRole("heading", { name: "Current job" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Job started"));
    expect(postedItems(1)[0].id).toBe(postedItems(0)[0].id);
    expect(postedItems(1)[0].startedAt).toBe(postedItems(0)[0].startedAt);
  });

  it("shows the dispatch list's error and empty states, and retries the load", async () => {
    await signIn(session(), json(500, { code: "COM-0500", detail: "Internal error" }));
    expect(await screen.findByText("Internal error (COM-0500)")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start job" })).toBeNull();
    handler = (url) => (url.pathname.endsWith("/jobs") ? json(200, { items: [] }) : undefined);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No jobs to start")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start job" })).toBeNull();
  });

  it("asks the worker to clock in before jobs are offered", async () => {
    await signIn(session({ clockedInAt: null }), [job(J1, "OP-10")]);
    expect(screen.getByText("Clock in to start jobs.")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Dispatch list" })).toBeNull();
    expect(calls.some((c) => c.url.pathname.endsWith("/jobs"))).toBe(false);
  });
});

describe("kiosk job helpers", () => {
  it("builds closed stops: a plain job from its own start, a bundle from its latest start", () => {
    let n = 0;
    const items = stopItems(
      WORKER,
      [
        { jobId: J1, bundleId: BUNDLE, startedAt: "2026-10-21T08:00:00Z" },
        { jobId: J2, bundleId: BUNDLE, startedAt: "2026-10-21T08:05:00Z" },
        { jobId: J3, bundleId: null, startedAt: "2026-10-21T07:00:00Z" },
      ],
      "2026-10-21T10:00:00Z",
      () => `id-${++n}`,
    );
    expect(items.map((it) => [it.id, it.kind, it.startedAt, it.stoppedAt])).toEqual([
      ["id-1", "job-stop", "2026-10-21T08:05:00Z", "2026-10-21T10:00:00Z"],
      ["id-2", "job-stop", "2026-10-21T08:05:00Z", "2026-10-21T10:00:00Z"],
      ["id-3", "job-stop", "2026-10-21T07:00:00Z", "2026-10-21T10:00:00Z"],
    ]);
  });

  it("describes a capped auto-stop and a start for a finished job (SF-0004)", () => {
    const jobs = [job(J1, "OP-10")];
    const lines = describeStartResults(
      [
        accepted("r1", {
          flags: ["job-finished-conflict"],
          autoStopped: [{ registrationId: "s", jobId: J2, bundleId: null, startedAt: "x", stoppedAt: "y", hours: "24", flags: ["auto-stopped", "auto-stop-capped"] }],
        }),
      ],
      jobs,
      [J1],
    );
    expect(lines).toEqual([
      `${J2} was stopped automatically after 24.00 h (SF-0002). It had run for more than 24 hours, so a supervisor reviews it.`,
      "MO-1 OP-10 was already finished on the server; your supervisor reviews this start (SF-0004).",
    ]);
  });
});
