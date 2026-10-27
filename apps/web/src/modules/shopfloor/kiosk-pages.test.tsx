import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { badgeLogin, listClockedIn, type ClockedWorker, type KioskSession, type Machine } from "./api/client";
import { elapsed } from "./format";
import { KIOSK_DEVICE_ID } from "./KioskPage";
import shopfloor from "./module";
import { validateMachine } from "./NewMachinePage";

const WORKER = "0195f0a0-0000-7000-8000-000000000001";
const PARTY = "0195f0a0-0000-7000-8000-000000000020";
const JOB = "0195f0a0-0000-7000-8000-000000000002";
const BUNDLE = "0195f0a0-0000-7000-8000-0000000000b1";
const RESOURCE = "0195f0a0-0000-7000-8000-000000000010";

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
        {shopfloor.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

const session = (extra: Partial<KioskSession> = {}): KioskSession => ({
  workerId: WORKER,
  partyId: PARTY,
  name: "Ana Silva",
  userId: null,
  clockedInAt: null,
  openJobs: [],
  idleTimeoutMinutes: 10,
  ...extra,
});

describe("Kiosk", () => {
  it("signs in by badge, clocks in with a client UUID and the kiosk device, then clocks out", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/sessions:badge")) return json(200, session());
      if (url.pathname.endsWith("/registrations")) return json(200, { items: [{ id: "x", status: "accepted" }] });
      return undefined;
    };
    const { container } = renderAt("/shopfloor/kiosk");
    expect(pageViolations(container)).toEqual([]);
    fill("Badge", "  B-42 ");
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("heading", { name: "Ana Silva" })).toBeInTheDocument();
    expect(screen.getByText("You are clocked out.")).toBeInTheDocument();
    expect(posts("/sessions:badge")[0].body).toEqual({ badgeId: "B-42" });
    expect(new Headers(posts("/sessions:badge")[0].init.headers).get("Idempotency-Key")).toBeNull();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Clock in" }));
    expect(await screen.findByRole("button", { name: "Clock out" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent(/^Clocked in at /);
    const [clockIn] = posts("/registrations");
    const item = (clockIn.body as { items: Record<string, unknown>[] }).items[0];
    expect(item).toMatchObject({ workerId: WORKER, kind: "clock-in", deviceId: KIOSK_DEVICE_ID });
    expect(item.id).toMatch(/^[0-9a-f-]{36}$/);

    fireEvent.click(screen.getByRole("button", { name: "Clock out" }));
    expect(await screen.findByRole("button", { name: "Clock in" })).toBeInTheDocument();
    const clockOut = (posts("/registrations")[1].body as { items: Record<string, unknown>[] }).items[0];
    expect(clockOut.kind).toBe("clock-out");
    expect(clockOut.id).not.toBe(item.id);
  });

  it("shows an unknown badge on the field and a malformed one without calling the service", async () => {
    handler = () => json(422, { code: "SF-0001", detail: "badge is not recognized" });
    renderAt("/shopfloor/kiosk");
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("Scan your badge or type its number.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fill("Badge", "NOPE");
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("This badge is not recognized. Ask your supervisor.")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Ana Silva" })).toBeNull();
  });

  it("retries a failed punch with the same client UUID so it is never recorded twice", async () => {
    let fail = true;
    handler = (url) => {
      if (url.pathname.endsWith("/sessions:badge")) return json(200, session({ clockedInAt: "2026-10-21T07:00:00Z" }));
      if (url.pathname.endsWith("/registrations")) return fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [] });
      if (url.pathname.endsWith("/jobs")) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/shopfloor/kiosk");
    fill("Badge", "B-42");
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByText("Clocked in since 2026-10-21 07:00 UTC.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Clock out" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Service unavailable (COM-0503)");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Clock out" }));
    expect(await screen.findByText("You are clocked out.")).toBeInTheDocument();
    const ids = posts("/registrations").map((c) => (c.body as { items: { id: string }[] }).items[0].id);
    expect(ids).toHaveLength(2);
    expect(ids[1]).toBe(ids[0]);
  });

  it("signs the worker out after the idle timeout, and input restarts it", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    handler = () => json(200, session({ idleTimeoutMinutes: 1 }));
    renderAt("/shopfloor/kiosk");
    fill("Badge", "B-42");
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("heading", { name: "Ana Silva" })).toBeInTheDocument();
    await act(async () => {});
    act(() => {
      vi.advanceTimersByTime(50_000);
    });
    fireEvent.keyDown(window, { key: "a" });
    act(() => {
      vi.advanceTimersByTime(50_000);
    });
    expect(screen.getByRole("heading", { name: "Ana Silva" })).toBeInTheDocument();
    act(() => {
      vi.advanceTimersByTime(11_000);
    });
    expect(screen.queryByRole("heading", { name: "Ana Silva" })).toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent("Signed out after 1 minute without input.");
    expect(screen.getByLabelText("Badge")).toHaveValue("");
  });

  it("signs out on request", async () => {
    handler = () => json(200, session());
    renderAt("/shopfloor/kiosk");
    fill("Badge", "B-42");
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    fireEvent.click(await screen.findByRole("button", { name: "Sign out" }));
    expect(screen.getByRole("button", { name: "Sign in" })).toBeInTheDocument();
  });
});

const clocked = (extra: Partial<ClockedWorker> = {}): ClockedWorker => ({
  workerId: WORKER,
  name: "Ana Silva",
  badgeId: "B-42",
  active: true,
  clockedInAt: "2026-10-21T07:00:00Z",
  jobs: [{ jobId: JOB, bundleId: BUNDLE, startedAt: "2026-10-21T07:30:00Z" }],
  ...extra,
});

describe("Who is clocked in", () => {
  it("lists clocked-in workers with their open jobs and the attendance summary", async () => {
    handler = (url) =>
      url.pathname.endsWith("/clocked-in")
        ? json(200, { items: [clocked(), clocked({ workerId: "0195f0a0-0000-7000-8000-000000000009", name: "Ben Ode", badgeId: null, active: false, jobs: [] })] })
        : undefined;
    const { container } = renderAt("/shopfloor/clocked-in");
    const table = await screen.findByRole("table", { name: "Clocked-in workers" });
    await waitFor(() => expect(table).toHaveTextContent("Ana Silva"));
    expect(table).toHaveTextContent(`${JOB} (bundle)`);
    expect(table).toHaveTextContent("2026-10-21 07:00 UTC");
    expect(table).toHaveTextContent("No open job");
    expect(within(table).getByText("Inactive")).toBeInTheDocument();
    const summary = screen.getByRole("list", { name: "Attendance summary" });
    expect(summary).toHaveTextContent("Clocked in 2");
    expect(summary).toHaveTextContent("On a job 1");
    expect(summary).toHaveTextContent("Without a job 1");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty board and the load error with a retry", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [] }));
    renderAt("/shopfloor/clocked-in");
    expect(await screen.findByText(/Service unavailable \(COM-0503\)/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Nobody is clocked in")).toBeInTheDocument();
  });
});

describe("Machine idle threshold", () => {
  const base = { machineId: "M-101", resourceId: RESOURCE, siteId: "", idealCycleSeconds: "60" };

  it("is optional and bounded to 30 to 3600 whole seconds like the service", () => {
    expect(validateMachine({ ...base, idleThresholdSeconds: "" }).input).toEqual({ ...base, siteId: null });
    expect(validateMachine({ ...base, idleThresholdSeconds: " 120 " }).input?.idleThresholdSeconds).toBe(120);
    for (const v of ["29", "3601", "1.5", "-30", "abc"]) {
      expect(validateMachine({ ...base, idleThresholdSeconds: v }).errors.idleThresholdSeconds).toBeDefined();
    }
  });

  it("is sent with the registration when entered", async () => {
    const machine: Machine = { id: "m1", ...base, siteId: null, idleThresholdSeconds: 600, lastSeq: 0, createdAt: "2026-10-21T07:00:00Z" };
    handler = (url, init) => (url.pathname.endsWith("/machines") ? json(init.method === "POST" ? 201 : 200, init.method === "POST" ? machine : { items: [machine] }) : undefined);
    renderAt("/shopfloor/machines/new");
    fill("Machine code", "M-101");
    fill("Resource id", RESOURCE);
    fill("Ideal cycle time (seconds)", "60");
    fill("Idle threshold (seconds)", "600");
    fireEvent.click(screen.getByRole("button", { name: "Register machine" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Machines" })).toHaveTextContent("600 s"));
    expect(posts("/machines")[0].body).toEqual({ ...base, siteId: null, idleThresholdSeconds: 600 });
  });
});

describe("kiosk client", () => {
  it("posts the badge without an Idempotency-Key and reads the clocked-in list", async () => {
    handler = (url) => (url.pathname.endsWith("/sessions:badge") ? json(200, session()) : json(200, { items: [clocked()] }));
    expect(await badgeLogin("B-42")).toEqual(session());
    expect(await listClockedIn()).toEqual([clocked()]);
    expect(calls.map((c) => `${c.method} ${c.url.pathname}`)).toEqual(["POST /api/v1/shopfloor/sessions:badge", "GET /api/v1/shopfloor/clocked-in"]);
  });

  it("formats time present in hours and minutes", () => {
    const start = "2026-10-21T07:00:00Z";
    expect(elapsed(start, Date.parse("2026-10-21T07:45:30Z"))).toBe("45 min");
    expect(elapsed(start, Date.parse("2026-10-21T14:05:00Z"))).toBe("7 h 05 min");
    expect(elapsed("nope")).toBe("—");
  });
});
