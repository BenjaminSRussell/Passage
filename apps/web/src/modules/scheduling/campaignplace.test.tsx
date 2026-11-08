import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Campaign } from "./api/client";
import scheduling from "./module";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const CAMPAIGN = "ca000000-0000-4000-8000-000000000001";
const ORDER_B = "b1000000-0000-4000-8000-00000000000b";
const ORDER_C = "b1000000-0000-4000-8000-00000000000c";
const OP_B = "0b000000-0000-4000-8000-00000000000b";
const OP_C = "0b000000-0000-4000-8000-00000000000c";
const RUN = "7e000000-0000-4000-8000-000000000001";

type Reply = { status: number; body: unknown };

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);

let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
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

const planned: Campaign = {
  id: CAMPAIGN,
  resourceId: MIX,
  campaignKey: "FAMILY-1",
  status: "planned",
  plannedStart: "2027-01-04T08:00:00Z",
  maxOrders: null,
  maxQty: null,
  maxMinutes: null,
  sequenceRule: "nearest-neighbor",
  currentSetupGroup: "A",
  orderCount: 2,
  runMinutes: 210,
  changeoverMinutes: 50,
  cleanoutMinutes: 240,
  plan: {
    changeovers: [],
    cleanouts: [{ afterOrderId: ORDER_C, setupGroup: "C", minutes: 240, reason: "campaign-end" }],
    warnings: [],
    carryIn: { batches: 0, minutes: 0 },
    carryOut: { batches: 0, minutes: 0 },
    optimized: false,
  },
  sequencedAt: "2026-10-26T07:00:00Z",
  startedAt: null,
  plannedEnd: null,
  placedAt: null,
  placement: null,
  version: 4,
  createdAt: "2026-10-26T06:00:00Z",
  orders: [
    { orderId: ORDER_B, setupGroup: "B", quantity: "200", runMinutes: 90, routeSetupMinutes: 15, position: 2, sequence: 1 },
    { orderId: ORDER_C, setupGroup: "C", quantity: "100", runMinutes: 120, routeSetupMinutes: 15, position: 1, sequence: 2 },
  ],
};

const placed: Campaign = {
  ...planned,
  version: 5,
  placedAt: "2026-10-26T08:00:00Z",
  plannedEnd: "2027-01-05T12:00:00Z",
  placement: {
    runId: RUN,
    placedAt: "2026-10-26T08:00:00Z",
    jobs: [
      { orderId: ORDER_B, operationId: OP_B, operNumber: 10, start: "2027-01-04T08:00:00Z", end: "2027-01-04T10:00:00Z", changeoverMinutes: 30, lateMinutes: 0 },
      { orderId: ORDER_C, operationId: OP_C, operNumber: 10, start: "2027-01-04T10:00:00Z", end: "2027-01-04T12:20:00Z", changeoverMinutes: 20, lateMinutes: 45 },
    ],
    cleanouts: [{ afterOrderId: ORDER_C, setupGroup: "C", minutes: 240, reason: "campaign-end", start: "2027-01-05T08:00:00Z", end: "2027-01-05T12:00:00Z" }],
    warnings: [{ code: "SCHED-0012", operationId: OP_C, detail: "setup matrix has no entry B -> C; route setup time 15 min used" }],
  },
};

function serve(current: () => Campaign, extra?: (url: URL, init: RequestInit) => Reply | undefined) {
  handler = (url, init) => {
    const reply = extra?.(url, init);
    if (reply) return reply;
    if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: [{ id: MIX, code: "MIX-1", name: "Mixing vessel", finite: true, timeZone: "UTC", shifts: [], version: 1, createdAt: "2026-10-01T00:00:00Z" }] } };
    if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return { status: 200, body: current() };
    return undefined;
  };
}

function lastCall(method: string, suffix: string) {
  return calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix)).at(-1);
}

describe("Campaign placement", () => {
  it("places a sequenced campaign and shows the jobs and the cleanout window", async () => {
    serve(
      () => planned,
      (url, init) => (url.pathname.endsWith(":place") && init.method === "POST" ? { status: 200, body: placed } : undefined),
    );
    const { container } = renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    expect(await screen.findByRole("heading", { name: "FAMILY-1" })).toBeInTheDocument();
    expect(screen.getByText("Not placed", { selector: "h3, h2, [role=heading]" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Take off schedule" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Place on schedule" }));
    expect(await screen.findByText("Campaign placed on the schedule.")).toBeInTheDocument();
    const call = lastCall("POST", ":place")!;
    expect(new Headers(call.init.headers).get("If-Match")).toBe('"4"');
    expect(new Headers(call.init.headers).get("Idempotency-Key")).toBeTruthy();
    const jobs = screen.getByRole("table", { name: "Campaign jobs" });
    expect(jobs).toHaveTextContent("30");
    expect(jobs).toHaveTextContent("45");
    const windows = screen.getByRole("table", { name: "Cleanout windows" });
    expect(windows).toHaveTextContent("Campaign end");
    expect(screen.getByText(/SCHED-0012: setup matrix has no entry B -> C/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Place again" })).toBeInTheDocument();
    // A placed campaign keeps its orders and sequence until it is unplaced (SCHED-0152).
    expect(screen.queryByRole("button", { name: "Sequence" })).not.toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Add an order" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Remove order/ })).not.toBeInTheDocument();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("takes a placed campaign off the schedule", async () => {
    serve(
      () => placed,
      (url, init) => (url.pathname.endsWith(":unplace") && init.method === "POST" ? { status: 200, body: { ...planned, version: 6 } } : undefined),
    );
    renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    expect(await screen.findByRole("table", { name: "Campaign jobs" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Take off schedule" }));
    expect(await screen.findByText("Campaign taken off the schedule.")).toBeInTheDocument();
    expect(new Headers(lastCall("POST", ":unplace")!.init.headers).get("If-Match")).toBe('"5"');
    await waitFor(() => expect(screen.queryByRole("table", { name: "Campaign jobs" })).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Place on schedule" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sequence" })).toBeInTheDocument();
  });

  it("explains why a campaign cannot be placed", async () => {
    serve(
      () => planned,
      (url, init) =>
        url.pathname.endsWith(":place") && init.method === "POST"
          ? { status: 422, body: { code: "SCHED-0150", detail: "order b1000000: no operation of the order can run on the campaign resource; create the operation first" } }
          : undefined,
    );
    renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Place on schedule" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("An order has no single operation to place");
    expect(alert).toHaveTextContent("create the operation first");
    expect(alert).toHaveTextContent("SCHED-0150");
  });

  it("asks for the orders' earlier operations to be scheduled first", async () => {
    serve(
      () => planned,
      (url, init) =>
        url.pathname.endsWith(":place") && init.method === "POST"
          ? { status: 422, body: { code: "SCHED-0153", detail: "operation 10 of order b1000000 comes before the campaign operation 20 and is not scheduled" } }
          : undefined,
    );
    renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Place on schedule" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Schedule the orders' earlier operations first");
    expect(alert).toHaveTextContent("operation 10 of order b1000000");
    // Nothing was placed: the campaign still offers the first placement.
    expect(screen.getByRole("button", { name: "Place on schedule" })).toBeInTheDocument();
  });

  it("lists whether each campaign is on the schedule", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/resources")) return { status: 200, body: { items: [] } };
      if (url.pathname.endsWith("/campaigns")) return { status: 200, body: { items: [placed, { ...planned, id: "ca000000-0000-4000-8000-000000000002" }] } };
      return undefined;
    };
    renderAt("/scheduling/campaigns");
    const table = await screen.findByRole("table", { name: "Campaigns" });
    await waitFor(() => expect(within(table).getAllByRole("row")).toHaveLength(3));
    expect(table).toHaveTextContent("Not placed");
    expect(table).toHaveTextContent("On the schedule until");
  });
});
