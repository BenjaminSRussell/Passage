import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { loadModules } from "../registry";
import shopfloor from "./module";
import {
  approvePay,
  calculatePay,
  chunk,
  codeDowntime,
  createMachine,
  createRegistrations,
  exportPay,
  getOee,
  listAndon,
  listBundle,
  listDowntimes,
  describeProblem,
  listWorkerDays,
  deleteProcessMessage,
  listProcessMessages,
  createProcessMessage,
  reprocessProcessMessage,
  MAX_BATCH,
  ShopfloorApiError,
  type RegistrationInput,
} from "./api/client";
import { durationMinutes, fixed, formatOffset, formatPayTotal, percent, sumDecimals } from "./format";

const item: RegistrationInput = {
  id: "0190a0b0-0000-7000-8000-000000000001",
  workerId: "0190a0b0-0000-7000-8000-000000000002",
  kind: "clock-in",
  startedAt: "2026-10-21T07:00:00Z",
};

describe("shopfloor module", () => {
  it("uses its folder name as id and keeps every route and nav entry under /shopfloor", () => {
    expect(shopfloor.id).toBe("shopfloor");
    expect(shopfloor.routes.every((r) => r.element != null && r.path?.startsWith("/shopfloor"))).toBe(true);
    expect(shopfloor.nav.every((n) => n.to.startsWith("/shopfloor"))).toBe(true);
  });

  it("has list and create or detail pages for workers, jobs, bundles, exports and machines, the kiosk, and the floor monitoring pages", () => {
    expect(shopfloor.routes.map((r) => r.path)).toEqual([
      "/shopfloor/workers",
      "/shopfloor/workers/new",
      "/shopfloor/workers/:id",
      "/shopfloor/workers/:id/revise",
      "/shopfloor/registrations/:id/correct",
      "/shopfloor/corrections",
      "/shopfloor/auto-clock-outs",
      "/shopfloor/bundles/:id",
      "/shopfloor/time-profiles",
      "/shopfloor/time-profiles/new",
      "/shopfloor/pay-agreements",
      "/shopfloor/pay-agreements/new",
      "/shopfloor/absence-codes",
      "/shopfloor/absence-codes/new",
      "/shopfloor/absences/new",
      "/shopfloor/kiosk",
      "/shopfloor/jobs",
      "/shopfloor/jobs/:id",
      "/shopfloor/clocked-in",
      "/shopfloor/time",
      "/shopfloor/pay-exports",
      "/shopfloor/pay-exports/:id",
      "/shopfloor/machines",
      "/shopfloor/machines/new",
      "/shopfloor/downtimes",
      "/shopfloor/downtime-reasons",
      "/shopfloor/downtime-reasons/new",
      "/shopfloor/process-categories",
      "/shopfloor/process-categories/new",
      "/shopfloor/process-categories/:id",
      "/shopfloor/andon",
      "/shopfloor/oee",
      "/shopfloor/edge-nodes",
      "/shopfloor/edge-nodes/new",
      "/shopfloor/edge-nodes/:id",
      "/shopfloor/control-recipes",
      "/shopfloor/control-recipes/:id",
      "/shopfloor/process-messages",
      "/shopfloor/process-messages/new",
      "/shopfloor/process-messages/:id",
    ]);
  });

  it("guards the navigation with the duties the service enforces", () => {
    expect(shopfloor.nav).toEqual([
      { label: "Kiosk", to: "/shopfloor/kiosk", duty: "shopfloor.kiosk.login" },
      { label: "Dispatch list", to: "/shopfloor/jobs", duty: "shopfloor.time.register" },
      { label: "Workers", to: "/shopfloor/workers", duty: "shopfloor.view" },
      { label: "Time profiles", to: "/shopfloor/time-profiles", duty: "shopfloor.view" },
      { label: "Pay agreements", to: "/shopfloor/pay-agreements", duty: "shopfloor.view" },
      { label: "Absence codes", to: "/shopfloor/absence-codes", duty: "shopfloor.view" },
      { label: "Who is clocked in", to: "/shopfloor/clocked-in", duty: "shopfloor.view" },
      { label: "Time approval", to: "/shopfloor/time", duty: "shopfloor.pay.approve" },
      { label: "Time corrections", to: "/shopfloor/corrections", duty: "shopfloor.view" },
      { label: "Auto clock-outs", to: "/shopfloor/auto-clock-outs", duty: "shopfloor.view" },
      { label: "Payroll export", to: "/shopfloor/pay-exports", duty: "shopfloor.pay.export" },
      { label: "Machines", to: "/shopfloor/machines", duty: "shopfloor.view" },
      { label: "Downtimes", to: "/shopfloor/downtimes", duty: "shopfloor.view" },
      { label: "Downtime reasons", to: "/shopfloor/downtime-reasons", duty: "shopfloor.view" },
      { label: "Andon board", to: "/shopfloor/andon", duty: "shopfloor.view" },
      { label: "OEE", to: "/shopfloor/oee", duty: "shopfloor.view" },
      { label: "Edge fleet", to: "/shopfloor/edge-nodes", duty: "shopfloor.view" },
      { label: "Control recipes", to: "/shopfloor/control-recipes", duty: "shopfloor.view" },
      { label: "Process messages", to: "/shopfloor/process-messages", duty: "shopfloor.view" },
      { label: "Process categories", to: "/shopfloor/process-categories", duty: "shopfloor.view" },
    ]);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("shopfloor");
  });
});

describe("shopfloor client", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  function reply(status: number, body: unknown) {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
  }

  it("posts registrations as one batch without an Idempotency-Key (each item is keyed by its id)", async () => {
    reply(200, { items: [{ id: item.id, status: "accepted" }] });
    expect(await createRegistrations([item])).toEqual([{ id: item.id, status: "accepted" }]);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/shopfloor/registrations");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBeNull();
    expect(JSON.parse(init.body as string)).toEqual({ items: [item] });
  });

  it("sends the kiosk clock with an offline replay so the server can correct skew (BR-25.7)", async () => {
    reply(200, { items: [{ id: item.id, status: "accepted" }], clockOffsetSeconds: 420, clockCorrected: true });
    await createRegistrations([item], "2026-10-21T07:53:00Z");
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({ deviceSentAt: "2026-10-21T07:53:00Z", items: [item] });
  });

  it("reads a bundle by its id", async () => {
    reply(200, { items: [] });
    expect(await listBundle("0190a0b0-0000-7000-8000-0000000000b1")).toEqual([]);
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/shopfloor/registrations?bundleId=0190a0b0-0000-7000-8000-0000000000b1");
  });

  it("sends the caller's Idempotency-Key on pay commands", async () => {
    const range = { workerId: item.workerId, from: "2026-10-19", to: "2026-10-25", timeZone: "Europe/Berlin" };
    reply(200, { ...range, days: [] });
    reply(200, { workerId: item.workerId, from: range.from, to: range.to, days: [] });
    await calculatePay(range, "calc-key");
    await approvePay(range, "approve-key");
    const calls = fetchMock.mock.calls as [string, RequestInit][];
    expect(calls.map(([url]) => url)).toEqual(["/api/v1/shopfloor/pay:calculate", "/api/v1/shopfloor/pay:approve"]);
    expect(calls.map(([, init]) => new Headers(init.headers).get("Idempotency-Key"))).toEqual(["calc-key", "approve-key"]);
    expect(JSON.parse(calls[0][1].body as string)).toEqual(range);
  });

  it("maps a problem response to the catalog code, detail and field errors", async () => {
    reply(422, { code: "SF-0006", detail: "Day cannot be approved: 1 flags open.", errors: { workerId: "must be a UUID" } });
    const err = await approvePay({ workerId: item.workerId, from: "2026-10-20", to: "2026-10-20" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ShopfloorApiError);
    expect(describeProblem(err)).toEqual({ code: "SF-0006", message: "Day cannot be approved: 1 flags open.", fields: { workerId: "must be a UUID" } });
    expect((err as ShopfloorApiError).status).toBe(422);
  });

  it("falls back to the HTTP status when the body is not a problem", async () => {
    fetchMock.mockResolvedValueOnce(new Response("bad gateway", { status: 502 }));
    const err = await listWorkerDays({ from: "2026-10-20", to: "2026-10-20" }).catch((e: unknown) => e);
    expect(describeProblem(err)).toMatchObject({ code: "HTTP-502" });
  });

  it("drops empty filters from list queries", async () => {
    reply(200, { items: [] });
    await listWorkerDays({ from: "2026-10-20", to: "2026-10-26", status: "" });
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/shopfloor/worker-days?from=2026-10-20&to=2026-10-26");
  });

  it("refuses batches above the server limit before sending", async () => {
    const many = Array.from({ length: MAX_BATCH + 1 }, () => item);
    await expect(createRegistrations(many)).rejects.toBeInstanceOf(RangeError);
    await expect(createRegistrations([])).rejects.toBeInstanceOf(RangeError);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("shopfloor floor client", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    fetchMock = vi.fn(async () => new Response(JSON.stringify({ items: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("reads andon, downtimes and the OEE series with their query parameters", async () => {
    await listAndon({ siteId: "s1" });
    await listDowntimes({ open: true });
    await getOee({ resourceIds: ["r1", "r2"], from: "2026-10-21T08:00:00.000Z", to: "2026-10-21T09:00:00.000Z", bucket: "60m" });
    expect(fetchMock.mock.calls.map((c) => (c as unknown as [string])[0])).toEqual([
      "/api/v1/shopfloor/andon?siteId=s1",
      "/api/v1/shopfloor/downtimes?open=true",
      "/api/v1/shopfloor/oee?resourceIds=r1%2Cr2&from=2026-10-21T08%3A00%3A00.000Z&to=2026-10-21T09%3A00%3A00.000Z&bucket=60m",
    ]);
  });

  it("sends an Idempotency-Key on machine, downtime code and export commands", async () => {
    await createMachine({ machineId: "M-101", resourceId: "r1", idealCycleSeconds: "60" }, "m-key");
    await codeDowntime("d/1", { reasonCode: "JAM" }, "d-key");
    await exportPay({ from: "2026-10-20", to: "2026-10-26" }, "e-key");
    const calls = fetchMock.mock.calls as unknown as [string, RequestInit][];
    expect(calls.map(([url]) => url)).toEqual([
      "/api/v1/shopfloor/machines",
      "/api/v1/shopfloor/downtimes/d%2F1:code",
      "/api/v1/shopfloor/pay:export",
    ]);
    expect(calls.map(([, init]) => new Headers(init.headers).get("Idempotency-Key"))).toEqual(["m-key", "d-key", "e-key"]);
    expect(JSON.parse(calls[1][1].body as string)).toEqual({ reasonCode: "JAM" });
  });
});

describe("shopfloor process message client", () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    fetchMock = vi.fn(async () => new Response(JSON.stringify({ items: [] }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("lists with the monitor filters and drops empty ones", async () => {
    const page = await listProcessMessages({ status: "error", category: "quality-result", destination: "", limit: 20 });
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/shopfloor/process-messages?status=error&category=quality-result&limit=20");
    expect(page).toEqual({ items: [], nextCursor: null });
  });

  it("passes the cursor for the next page and returns nextCursor", async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ items: [], nextCursor: "c-2" }), { status: 200 }));
    const page = await listProcessMessages({ cursor: "c-1" });
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/shopfloor/process-messages?cursor=c-1");
    expect(page.nextCursor).toBe("c-2");
  });

  it("deletes with a reason and an Idempotency-Key", async () => {
    await deleteProcessMessage("m/1", { reason: "duplicate" }, "d-key");
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/shopfloor/process-messages/m%2F1:delete");
    expect(init.method).toBe("POST");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("d-key");
    expect(JSON.parse(init.body as string)).toEqual({ reason: "duplicate" });
  });

  it("records without an Idempotency-Key (the source message id is the key) and reprocesses with one", async () => {
    await createProcessMessage({
      source: "manual",
      sourceMessageId: "s1",
      categoryCode: "custom",
      orderId: item.workerId,
      occurredAt: "2026-10-21T08:00:00.000Z",
      values: [],
    });
    await reprocessProcessMessage("m/1", { destination: "quality", reason: "QMS back" }, "r-key");
    const calls = fetchMock.mock.calls as unknown as [string, RequestInit][];
    expect(calls.map(([url]) => url)).toEqual(["/api/v1/shopfloor/process-messages", "/api/v1/shopfloor/process-messages/m%2F1:reprocess"]);
    expect(calls.map(([, init]) => new Headers(init.headers).get("Idempotency-Key"))).toEqual([null, "r-key"]);
    expect(JSON.parse(calls[1][1].body as string)).toEqual({ destination: "quality", reason: "QMS back" });
  });
});

describe("chunk", () => {
  it("splits an offline queue into server-sized batches in order", () => {
    const queue = Array.from({ length: 1201 }, (_, i) => i);
    const batches = chunk(queue);
    expect(batches.map((b) => b.length)).toEqual([500, 500, 201]);
    expect(batches.flat()).toEqual(queue);
  });

  it("rejects a non-positive size", () => {
    expect(() => chunk([1], 0)).toThrow(RangeError);
  });
});

describe("clock offset formatting", () => {
  it("signs the correction and uses minutes where it can", () => {
    expect(formatOffset(420)).toBe("+7 min");
    expect(formatOffset(-420)).toBe("−7 min");
    expect(formatOffset(-45)).toBe("−45 s");
    expect(formatOffset(390)).toBe("+6 min 30 s");
    expect(formatOffset(null)).toBe("—");
  });
});

describe("decimal formatting", () => {
  it("rounds decimal strings half away from zero without floats", () => {
    expect(fixed("10.5", 2)).toBe("10.50");
    expect(fixed("2.345", 2)).toBe("2.35");
    expect(fixed("9.995", 2)).toBe("10.00");
    expect(fixed("-1.005", 2)).toBe("-1.01");
    expect(fixed("0.1", 0)).toBe("0");
  });

  it("sums amounts exactly where float addition would drift", () => {
    expect(sumDecimals(["0.1", "0.2"], 2)).toBe("0.30");
    expect(sumDecimals(["160.00", "60.00", "-0.01"], 2)).toBe("219.99");
  });

  it("totals pay lines per currency", () => {
    const line = (amount: string, currency: string) => ({ type: "base", hours: "1", multiplier: "1", amount, currency });
    expect(formatPayTotal([line("160.000000", "USD"), line("60.000000", "USD")])).toBe("220.00 USD");
    expect(formatPayTotal([line("1", "USD"), line("2", "EUR")])).toBe("1.00 USD, 2.00 EUR");
    expect(formatPayTotal([])).toBe("—");
  });
});

describe("floor formatting", () => {
  it("shows decimal ratios as percentages without floats", () => {
    expect(percent("0.8750")).toBe("87.5 %");
    expect(percent("0.7521", 2)).toBe("75.21 %");
    expect(percent("1")).toBe("100.0 %");
    expect(percent("0")).toBe("0.0 %");
    expect(percent("0.00005", 2)).toBe("0.01 %");
    expect(percent(null)).toBe("—");
  });

  it("measures downtime in whole minutes, to now while open", () => {
    expect(durationMinutes("2026-10-21T08:00:00Z", "2026-10-21T08:22:59Z")).toBe("22 min");
    expect(durationMinutes("2026-10-21T08:00:00Z", null, Date.parse("2026-10-21T08:11:00Z"))).toBe("11 min");
    expect(durationMinutes("nope")).toBe("—");
  });
});
