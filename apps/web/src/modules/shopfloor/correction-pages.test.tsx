import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Correction, PayRevision, Registration } from "./api/client";
import { validateCorrection } from "./CorrectRegistrationPage";
import shopfloor from "./module";
import { validateRevision } from "./RevisePayPage";

const WORKER = "0195f0a0-0000-7000-8000-000000000001";
const REG = "0195f0a0-0000-7000-8000-000000000003";

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
  vi.unstubAllGlobals();
});

function renderAt(entry: string | { pathname: string; state: unknown }) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        {shopfloor.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const registration: Registration = {
  id: REG,
  workerId: WORKER,
  kind: "clock-out",
  jobId: null,
  bundleId: null,
  qtyGood: "0",
  qtyScrap: "0",
  scrapReasonId: null,
  startedAt: "2026-10-20T19:30:00Z",
  stoppedAt: null,
  hours: null,
  serverReceivedAt: "2026-10-20T19:31:00Z",
  deviceId: null,
  offline: false,
  note: null,
  clockOffsetSeconds: null,
  flags: [],
};

const correction: Correction = {
  id: "0195f0a0-0000-7000-8000-000000000040",
  registrationId: REG,
  workerId: WORKER,
  reason: "left at 18:30",
  oldStartedAt: "2026-10-20T19:30:00Z",
  oldStoppedAt: null,
  oldHours: null,
  newStartedAt: "2026-10-20T18:30:00Z",
  newStoppedAt: null,
  newHours: null,
  correctedBy: "supervisor-2",
  correctedAt: "2026-10-21T08:00:00Z",
};

describe("Time corrections list", () => {
  it("lists corrections with before, after, reason and user", async () => {
    let asked: URL | undefined;
    handler = (url) => {
      if (!url.pathname.endsWith("/registration-corrections")) return undefined;
      asked = url;
      return json(200, { items: [correction] });
    };
    const { container } = renderAt("/shopfloor/corrections");
    const table = await findTable({ name: "Corrections" });
    await waitFor(() => expect(table).toHaveTextContent("left at 18:30"));
    // The range is the viewer's days, so the zone goes with it.
    expect(asked?.searchParams.get("from")).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(asked?.searchParams.get("timeZone")).toBeTruthy();
    expect(table).toHaveTextContent("2026-10-20 19:30 UTC");
    expect(table).toHaveTextContent("2026-10-20 18:30 UTC");
    expect(table).toHaveTextContent("supervisor-2");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state and the load error with a retry", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [] }));
    renderAt("/shopfloor/corrections");
    expect(await screen.findByText(/Service unavailable/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No corrections in this range")).toBeInTheDocument();
  });
});

describe("Correct a registration", () => {
  it("validates like the service", () => {
    expect(validateCorrection({ startedAt: "2026-10-20T18:30", stoppedAt: "", reason: " " }).errors.reason).toBeDefined();
    expect(validateCorrection({ startedAt: "", stoppedAt: "", reason: "x" }).errors.startedAt).toBeDefined();
    expect(validateCorrection({ startedAt: "2026-10-20T18:30", stoppedAt: "2026-10-20T10:00", reason: "x" }).errors.stoppedAt).toBeDefined();
    expect(validateCorrection({ startedAt: "2026-10-20T08:00", stoppedAt: "2026-10-21T09:00", reason: "x" }).errors.stoppedAt).toBeDefined();
    expect(validateCorrection({ startedAt: "2026-10-20T18:30", stoppedAt: "2026-10-20T19:30", reason: "x" }, registration).errors.stoppedAt).toBeDefined();
    expect(validateCorrection({ startedAt: "2026-10-20T18:30", stoppedAt: "", reason: " left early " }, registration).input).toEqual({
      startedAt: "2026-10-20T18:30:00.000Z",
      reason: "left early",
    });
  });

  it("sends the stored instant, seconds included, for a time left as the form filled it", () => {
    const stored = { ...registration, startedAt: "2026-10-20T19:30:37Z" };
    expect(validateCorrection({ startedAt: "2026-10-20T19:30", stoppedAt: "", reason: "confirmed" }, stored).input?.startedAt).toBe("2026-10-20T19:30:37Z");
    expect(validateCorrection({ startedAt: "2026-10-20T19:31", stoppedAt: "", reason: "moved" }, stored).input?.startedAt).toBe("2026-10-20T19:31:00.000Z");
  });

  it("sends the correction with an Idempotency-Key and offers pay revision", async () => {
    handler = (url, init) => (init.method === "POST" && url.pathname.endsWith(":correct") ? json(200, correction) : undefined);
    const { container } = renderAt({ pathname: `/shopfloor/registrations/${REG}/correct`, state: { registration } });
    expect(screen.getByLabelText("Started (UTC)")).toHaveValue("2026-10-20T19:30");
    expect(screen.queryByLabelText(/Stopped/)).toBeNull();
    expect(pageViolations(container)).toEqual([]);
    fill("Started (UTC)", "2026-10-20T18:30");
    fill("Reason", "left at 18:30");
    fireEvent.click(screen.getByRole("button", { name: "Save correction" }));
    expect(await screen.findByText("Correction saved")).toBeInTheDocument();
    const sent = calls.find((c) => c.method === "POST")!;
    expect(sent.url.pathname).toBe(`/api/v1/shopfloor/registrations/${REG}:correct`);
    expect(sent.body).toMatchObject({ startedAt: "2026-10-20T18:30:00.000Z" });
    expect(header(sent, "Idempotency-Key")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Revise pay" })).toHaveAttribute("href", `/shopfloor/workers/${WORKER}/revise`);
  });

  it("asks for a reason before sending and shows a service refusal", async () => {
    handler = (url, init) =>
      init.method === "POST" && url.pathname.endsWith(":correct") ? json(422, { code: "SF-0127", detail: "a bundled job registration cannot be corrected" }) : undefined;
    renderAt({ pathname: `/shopfloor/registrations/${REG}/correct`, state: { registration } });
    fill("Started (UTC)", "2026-10-20T18:30");
    fireEvent.click(screen.getByRole("button", { name: "Save correction" }));
    expect(await screen.findByText(/Say why the time changes/)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fill("Reason", "left early");
    fireEvent.click(screen.getByRole("button", { name: "Save correction" }));
    expect(await screen.findByText(/cannot be corrected/)).toBeInTheDocument();
  });
});

describe("Revise pay", () => {
  const revision: PayRevision = {
    workerId: WORKER,
    from: "2026-10-20",
    to: "2026-10-20",
    reason: "clock-out corrected",
    days: [
      {
        workDate: "2026-10-20",
        status: "approved",
        revision: 1,
        changed: true,
        reversedLines: [{ type: "overtime-1.5", hours: "-2.50", multiplier: "1.5", amount: "-75.00", currency: "USD", revision: 1, reversal: true }],
        addedLines: [{ type: "overtime-1.5", hours: "1.50", multiplier: "1.5", amount: "45.00", currency: "USD", revision: 1 }],
      },
    ],
  };

  it("validates the range and the reason", () => {
    expect(validateRevision({ from: "2026-10-01", to: "2026-10-02", reason: "" }).reason).toBeDefined();
    expect(validateRevision({ from: "2026-10-02", to: "2026-10-01", reason: "x" }).to).toBeDefined();
    expect(validateRevision({ from: "2026-09-01", to: "2026-10-30", reason: "x" }).to).toBeDefined();
    expect(validateRevision({ from: "2026-10-01", to: "2026-10-02", reason: "x" })).toEqual({});
  });

  it("revises and shows reversed and added lines", async () => {
    handler = (url, init) => (init.method === "POST" && url.pathname.endsWith("/pay:revise") ? json(200, revision) : undefined);
    const { container } = renderAt(`/shopfloor/workers/${WORKER}/revise`);
    expect(pageViolations(container)).toEqual([]);
    fill("Reason", "clock-out corrected");
    fireEvent.click(screen.getByRole("button", { name: "Revise pay" }));
    const table = await findTable({ name: "Revised days" });
    expect(table).toHaveTextContent("overtime-1.5 -2.50 h (-75.00 USD)");
    expect(table).toHaveTextContent("overtime-1.5 1.50 h (45.00 USD)");
    expect(table).toHaveTextContent("Revision 1");
    const sent = calls.find((c) => c.method === "POST")!;
    expect(sent.body).toMatchObject({ workerId: WORKER, reason: "clock-out corrected" });
    expect(header(sent, "Idempotency-Key")).toBeTruthy();
  });

  it("explains when nothing differs", async () => {
    handler = (url, init) =>
      init.method === "POST" && url.pathname.endsWith("/pay:revise") ? json(422, { code: "SF-0129", detail: "the recalculation matches the stored pay" }) : undefined;
    renderAt(`/shopfloor/workers/${WORKER}/revise`);
    fill("Reason", "check");
    fireEvent.click(screen.getByRole("button", { name: "Revise pay" }));
    expect(await screen.findByText(/nothing to revise\./)).toBeInTheDocument();
  });
});
