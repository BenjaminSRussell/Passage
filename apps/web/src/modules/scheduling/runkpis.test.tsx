import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { LateOrder, Run, RunKpis } from "./api/client";
import scheduling from "./module";

const RUN = "5e000000-0000-4000-8000-000000000001";
const MIX = "3f1c0000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };
type Handler = (url: URL) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

let handler: Handler;
let calls: URL[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      calls.push(url);
      const reply = handler(url) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
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

const run: Run = {
  id: RUN,
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
};

const kpis: RunKpis = {
  runId: RUN,
  placedJobs: 3,
  orders: 3,
  lateJobs: 1,
  lateOrders: 1,
  maxLateMinutes: 90,
  changeoverMinutes: 30,
  bookedMinutes: 420,
  spanStart: "2027-03-01T08:00:00Z",
  spanEnd: "2027-03-01T14:00:00Z",
  resources: [{ resourceId: MIX, resourceCode: "MIX-1", jobs: 2, bookedMinutes: 360, openMinutes: 360, utilizationPct: 100 }],
};

const late: LateOrder = {
  orderId: "b1000000-0000-4000-8000-000000000001",
  dueAt: "2027-03-01T08:30:00Z",
  plannedStart: "2027-03-01T08:00:00Z",
  plannedEnd: "2027-03-01T10:00:00Z",
  lateMinutes: 90,
  lateOperations: 1,
  unplacedOperations: 0,
  operations: 2,
  firmed: true,
  dependentOrders: 2,
};

describe("Run detail plan measures", () => {
  it("shows the lateness, changeover and utilization of the jobs the run holds", async () => {
    handler = (url) => (url.pathname.endsWith("/kpis") ? { status: 200, body: kpis } : { status: 200, body: run });
    const { container } = renderAt(`/scheduling/runs/${RUN}`);
    const table = await findTable({ name: "Resource utilization" });
    expect(table).toHaveTextContent("MIX-1");
    expect(table).toHaveTextContent("100.0");
    expect(await screen.findByText("3 of 5 operations")).toBeInTheDocument();
    expect(screen.getByText("1 (1 late jobs)")).toBeInTheDocument();
    expect(screen.getByLabelText("Plan measures")).toHaveTextContent("Latest job late by1 h 30 min");
    expect(screen.getByLabelText("Plan measures")).toHaveTextContent("Changeover time30 min");
    expect(calls.some((c) => c.pathname.endsWith(`/runs/${RUN}/kpis`))).toBe(true);
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("explains a run whose jobs were replaced and a measures failure with retry", async () => {
    handler = (url) => (url.pathname.endsWith("/kpis") ? { status: 200, body: { ...kpis, placedJobs: 0, orders: 0, spanStart: null, spanEnd: null, resources: [] } } : { status: 200, body: run });
    const first = renderAt(`/scheduling/runs/${RUN}`);
    expect(await screen.findByText("No jobs carry this run")).toBeInTheDocument();
    first.unmount();

    handler = (url) => (url.pathname.endsWith("/kpis") ? { status: 500, body: { code: "COM-0500", detail: "internal error" } } : { status: 200, body: run });
    renderAt(`/scheduling/runs/${RUN}`);
    expect(await findAlert()).toHaveTextContent("COM-0500");
    handler = (url) => (url.pathname.endsWith("/kpis") ? { status: 200, body: kpis } : { status: 200, body: run });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Resource utilization" })).toBeInTheDocument();
  });
});

describe("Late orders", () => {
  it("lists late and unplaced orders and asks again for a higher threshold", async () => {
    handler = (url) =>
      url.pathname.endsWith("/late-orders")
        ? { status: 200, body: { items: [late, { ...late, orderId: "b1000000-0000-4000-8000-000000000002", lateMinutes: 0, lateOperations: 0, unplacedOperations: 1, firmed: false, plannedEnd: null, dueAt: null }], truncated: true } }
        : undefined;
    const { container } = renderAt("/scheduling/late-orders");
    const table = await findTable({ name: "Late orders" });
    await waitFor(() => expect(table).toHaveTextContent("1 h 30 min"));
    expect(table).toHaveTextContent("1 of 2");
    expect(table).toHaveTextContent("Unplaced 1");
    expect(table).toHaveTextContent("Firmed");
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent(/1 of 2\s*2/);
    expect(screen.getByText("Showing the first 200 orders")).toBeInTheDocument();
    expect(calls[0].searchParams.get("minLateMinutes")).toBe("1");

    fireEvent.change(screen.getByLabelText("Late by"), { target: { value: "240" } });
    await waitFor(() => expect(calls.some((c) => c.searchParams.get("minLateMinutes") === "240")).toBe(true));
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("explains an empty list and a failure with retry", async () => {
    handler = () => ({ status: 200, body: { items: [], truncated: false } });
    const first = renderAt("/scheduling/late-orders");
    expect(await screen.findByText("No late orders")).toBeInTheDocument();
    first.unmount();

    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "internal error" } });
    renderAt("/scheduling/late-orders");
    expect(await findAlert()).toHaveTextContent("COM-0500");
    handler = () => ({ status: 200, body: { items: [late], truncated: false } });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Late orders" })).toBeInTheDocument();
  });
});
