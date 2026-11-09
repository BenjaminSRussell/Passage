import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { GanttBar, Resource } from "./api/client";
import { dayStart, today } from "./format";
import scheduling from "./module";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const ORDER = "b1000000-0000-4000-8000-000000000001";
const JOB1 = "0a000000-0000-4000-8000-000000000001";
const JOB2 = "0a000000-0000-4000-8000-000000000002";

type Reply = { status: number; body: unknown };
let handler: (url: URL, body: Record<string, unknown> | undefined) => Reply | undefined;
let moves: Record<string, unknown>[];
let ifMatches: (string | null)[];
let ganttCalls: number;

const day = dayStart(today());
const at = (h: number) => new Date(Date.parse(day) + h * 3_600_000).toISOString().replace(".000Z", "Z");
const shifts = [1, 2, 3, 4, 5].map((weekday) => ({ weekday, startMinute: 480, endMinute: 960 }));
const resources: Resource[] = [{ id: MIX, code: "MIX-1", name: "Mixing vessel", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" }];
const bar = (jobId: string, oper: number, start: string, end: string, extra: Partial<GanttBar> = {}): GanttBar => ({
  jobId, orderId: ORDER, operNumber: oper, resourceId: MIX, segment: 1, start, end, lateMinutes: 0, firmed: false, setupGroup: null, changeoverMinutes: 0,
  changeoverCost: null, changeoverCurrency: null, cleaningSetId: null, secondaryResourceIds: [], outsideCapacity: false, blockBreach: false, ...extra,
});
const bars = [bar(JOB1, 10, at(8), at(9), { placementVersion: "v1hash" }), bar(JOB2, 20, at(9), at(11), { placementVersion: "v1hash" })];

beforeEach(() => {
  moves = [];
  ifMatches = [];
  ganttCalls = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : undefined;
      if (url.pathname.endsWith(":move") && body) {
        moves.push(body);
        ifMatches.push(new Headers(init.headers).get("If-Match"));
      }
      if (url.pathname.endsWith(":firm")) ifMatches.push(new Headers(init.headers).get("If-Match"));
      if (url.pathname.endsWith("/gantt")) ganttCalls++;
      const reply =
        (url.pathname.endsWith("/resources") ? { status: 200, body: { items: resources } } : undefined) ??
        (url.pathname.endsWith("/gantt") ? { status: 200, body: { items: bars } } : undefined) ??
        handler(url, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderWorkspace() {
  return render(
    <MemoryRouter initialEntries={["/scheduling"]}>
      <Routes>
        {scheduling.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const preview = (dryRun: boolean) => ({
  dryRun,
  feasible: true,
  violations: [],
  changes: [{ operationId: JOB1, orderId: ORDER, resourceId: MIX, oldStart: at(8), oldEnd: at(9), newStart: at(10), newEnd: at(11), lateMinutes: 0 }],
  orderId: ORDER,
  orderStart: at(10),
  orderEnd: at(11),
});
const stale = { status: 412, body: { code: "SCHED-0154", detail: "the order's placement changed since you loaded it; reload the Gantt and try again" } };

describe("Stale placement on moves and firms", () => {
  it("sends the bar's placement version as If-Match on the preview and the commit", async () => {
    handler = (url, body) => (url.pathname.endsWith(":move") && body ? { status: 200, body: preview(Boolean(body.dryRun)) } : undefined);
    renderWorkspace();
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => within(table).getByRole("button", { name: "Move order b1000000 operation 10" }));
    fireEvent.click(within(table).getByRole("button", { name: "Move order b1000000 operation 10" }));
    fireEvent.click(screen.getByRole("button", { name: "Preview move" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm move" }));
    expect(await screen.findByText(/Move saved\./)).toBeInTheDocument();
    expect(ifMatches).toEqual(['"v1hash"', '"v1hash"']);
  });

  it("drops a move refused as stale and reloads the Gantt", async () => {
    handler = (url, body) => (url.pathname.endsWith(":move") && body ? (body.dryRun ? { status: 200, body: preview(true) } : stale) : undefined);
    renderWorkspace();
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => within(table).getByRole("button", { name: "Move order b1000000 operation 10" }));
    fireEvent.click(within(table).getByRole("button", { name: "Move order b1000000 operation 10" }));
    fireEvent.click(screen.getByRole("button", { name: "Preview move" }));
    const before = ganttCalls;
    fireEvent.click(await screen.findByRole("button", { name: "Confirm move" }));
    expect(await screen.findByText(/Another change moved this order after the Gantt loaded/)).toBeInTheDocument();
    expect(screen.getByText(/SCHED-0154/)).toBeInTheDocument();
    await waitFor(() => expect(ganttCalls).toBeGreaterThan(before));
    expect(screen.queryByRole("button", { name: "Confirm move" })).not.toBeInTheDocument();
  });

  it("sends the version when firming and reports a stale firm", async () => {
    handler = (url) => (url.pathname.endsWith(":firm") ? stale : undefined);
    renderWorkspace();
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => within(table).getByRole("button", { name: "Firm order b1000000 operation 10" }));
    fireEvent.click(within(table).getByRole("button", { name: "Firm order b1000000 operation 10" }));
    expect(await screen.findByText(/Another change moved this order after the Gantt loaded/)).toBeInTheDocument();
    expect(ifMatches).toEqual(['"v1hash"']);
  });
});
