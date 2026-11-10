import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { GanttBar, Resource } from "./api/client";
import { dayStart, today } from "./format";
import { jobsOf } from "./gantt";
import { resetOrderCache } from "../mfg-shared/orders";
import scheduling from "./module";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const ORDER = "b1000000-0000-4000-8000-000000000001";
const JOB1 = "0a000000-0000-4000-8000-000000000001";
const JOB2 = "0a000000-0000-4000-8000-000000000002";

type Reply = { status: number; body: unknown };
let handler: (url: URL, body: Record<string, unknown> | undefined) => Reply | undefined;
let moves: Record<string, unknown>[];

const day = dayStart(today());
const at = (h: number) => new Date(Date.parse(day) + h * 3_600_000).toISOString().replace(".000Z", "Z");
const shifts = [1, 2, 3, 4, 5].map((weekday) => ({ weekday, startMinute: 480, endMinute: 960 }));
const resources: Resource[] = [{ id: MIX, code: "MIX-1", name: "Mixing vessel", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" }];
const bar = (jobId: string, oper: number, start: string, end: string, extra: Partial<GanttBar> = {}): GanttBar => ({
  jobId, orderId: ORDER, operNumber: oper, resourceId: MIX, segment: 1, start, end, lateMinutes: 0, firmed: false, setupGroup: null, changeoverMinutes: 0,
  changeoverCost: null, changeoverCurrency: null, cleaningSetId: null, secondaryResourceIds: [], outsideCapacity: false, blockBreach: false, ...extra,
});
const bars = [bar(JOB1, 10, at(8), at(9)), bar(JOB2, 20, at(9), at(11), { blockBreach: true })];

beforeEach(() => {
  moves = [];
  resetOrderCache();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : undefined;
      if (url.pathname.endsWith(":move") && body) moves.push(body);
      const reply =
        (url.pathname.endsWith("/resources") ? { status: 200, body: { items: resources } } : undefined) ??
        (url.pathname.endsWith("/gantt") ? { status: 200, body: { items: bars } } : undefined) ??
        (url.pathname.endsWith(`/production/orders/${ORDER}`) ? { status: 200, body: { id: ORDER, orderNumber: "MO-1001", productId: "" } } : undefined) ??
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

const refused = { dryRun: true, feasible: false, overridden: false, violations: [{ code: "SCHED-0002", detail: "start is outside open capacity" }], changes: [] };
const overriddenChange = { operationId: JOB1, orderId: ORDER, resourceId: MIX, oldStart: at(8), oldEnd: at(9), newStart: at(17), newEnd: at(18), lateMinutes: 0 };

describe("Gantt block breaches", () => {
  it("dots a bar inside a block it does not match and says so in the table and the title", async () => {
    handler = () => undefined;
    const { container } = renderWorkspace();
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => expect(table).toHaveTextContent("Inside a block it does not match"));
    expect(within(table).getAllByRole("row")[1]).not.toHaveTextContent("does not match");
    expect(container.querySelector(`rect[data-job="${JOB2}"] title`)?.textContent).toContain("inside a capacity block it does not match");
    expect(container.querySelector(`rect[data-job="${JOB1}"] title`)?.textContent).not.toContain("capacity block");
    expect(container.querySelector(`rect[data-job="${JOB2}"]`)?.getAttribute("class")).toMatch(/breach/);
    expect(container.querySelector(`rect[data-job="${JOB1}"]`)?.getAttribute("class")).not.toMatch(/breach/);
  });

  it("keeps the breach when a job's segments are merged", () => {
    const split = [bar(JOB1, 10, at(8), at(9)), bar(JOB1, 10, at(10), at(11), { segment: 2, blockBreach: true })];
    expect(jobsOf(split)[0].blockBreach).toBe(true);
    expect(jobsOf([split[0]])[0].blockBreach).toBe(false);
  });
});

describe("Override scheduling constraints", () => {
  it("offers the override after a capacity refusal and commits over it", async () => {
    handler = (url, body) => {
      if (!url.pathname.endsWith(":move") || !body) return undefined;
      if (!body.overrideConstraints) return { status: 200, body: refused };
      return { status: 200, body: { dryRun: body.dryRun, feasible: true, overridden: true, violations: refused.violations, changes: [overriddenChange], orderId: ORDER, orderStart: at(17), orderEnd: at(18) } };
    };
    renderWorkspace();
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => within(table).getByRole("button", { name: "Move order MO-1001 operation 10" }));
    fireEvent.click(within(table).getByRole("button", { name: "Move order MO-1001 operation 10" }));
    fireEvent.click(screen.getByRole("button", { name: "Preview move" }));
    expect(await screen.findByText("This move breaks a constraint and cannot be saved")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Confirm move/ })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Preview with override" }));
    expect(await screen.findByText("This move goes over constraints")).toBeInTheDocument();
    expect(moves.at(-1)).toMatchObject({ dryRun: true, overrideConstraints: true });
    fireEvent.click(screen.getByRole("button", { name: "Confirm move over constraints" }));
    expect(await screen.findByText(/Move saved\. It went over 1 constraint violation\(s\)/)).toBeInTheDocument();
    expect(moves.at(-1)).toMatchObject({ dryRun: false, overrideConstraints: true });
  });

  it("does not offer the override for double booking", async () => {
    handler = (url, body) =>
      url.pathname.endsWith(":move") && body
        ? { status: 200, body: { ...refused, violations: [{ code: "SCHED-0001", detail: "overlaps a booked job on the resource", jobId: JOB2 }] } }
        : undefined;
    renderWorkspace();
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => within(table).getByRole("button", { name: "Move order MO-1001 operation 10" }));
    fireEvent.click(within(table).getByRole("button", { name: "Move order MO-1001 operation 10" }));
    fireEvent.click(screen.getByRole("button", { name: "Preview move" }));
    expect(await screen.findByText("This move breaks a constraint and cannot be saved")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Preview with override" })).not.toBeInTheDocument();
  });

  it("does not offer the override again when the overridden preview is still refused", async () => {
    const booked = { ...refused, violations: [{ code: "SCHED-0005", detail: "secondary resource TOOL-1 is booked by job x and a booking is never overridden", jobId: JOB1 }] };
    handler = (url, body) => (url.pathname.endsWith(":move") && body ? { status: 200, body: body.overrideConstraints ? booked : refused } : undefined);
    renderWorkspace();
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => within(table).getByRole("button", { name: "Move order MO-1001 operation 10" }));
    fireEvent.click(within(table).getByRole("button", { name: "Move order MO-1001 operation 10" }));
    fireEvent.click(screen.getByRole("button", { name: "Preview move" }));
    fireEvent.click(await screen.findByRole("button", { name: "Preview with override" }));
    expect(await screen.findByText(/Even with the override this move cannot be saved/)).toBeInTheDocument();
    expect(screen.getByRole("listitem")).toHaveTextContent("a booking is never overridden (SCHED-0005)");
    expect(screen.queryByRole("button", { name: "Preview with override" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Confirm move/ })).not.toBeInTheDocument();
  });

  it("reports a missing duty with its code and leaves the schedule alone", async () => {
    handler = (url, body) => {
      if (!url.pathname.endsWith(":move") || !body) return undefined;
      if (!body.overrideConstraints) return { status: 200, body: refused };
      return { status: 403, body: { code: "SCHED-0141", detail: "overriding scheduling constraints needs the scheduling.constraints.override duty" } };
    };
    renderWorkspace();
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => within(table).getByRole("button", { name: "Move order MO-1001 operation 10" }));
    fireEvent.click(within(table).getByRole("button", { name: "Move order MO-1001 operation 10" }));
    fireEvent.click(screen.getByRole("button", { name: "Preview move" }));
    fireEvent.click(await screen.findByRole("button", { name: "Preview with override" }));
    expect(await screen.findByText(/needs the scheduling\.constraints\.override duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Confirm move/ })).not.toBeInTheDocument();
  });
});
