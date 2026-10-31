import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { CapacityBlock, GanttBar, MoveResponse, Operation, OrderPeg, Resource } from "./api/client";
import { dayStart, today } from "./format";
import scheduling from "./module";
import { describeRecurrence, ruleError, validateBlock, type BlockValues } from "./NewCapacityBlockPage";
import { ordersOf, validatePeg } from "./NewOrderPegPage";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const FILL = "3f1c0000-0000-4000-8000-000000000002";
const PARENT = "b1000000-0000-4000-8000-000000000001";
const DEPENDENT = "b2000000-0000-4000-8000-000000000002";
const JOB1 = "0a000000-0000-4000-8000-000000000001";
const PEG = "9e000000-0000-4000-8000-000000000001";

type Reply = { status: number; body?: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

function json(status: number, body?: unknown): Reply {
  return { status, body };
}

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), {
        status: reply.status,
        headers: { "Content-Type": "application/json" },
      });
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

function lastCall(method: string, suffix: string) {
  return calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix)).at(-1);
}

const shifts = [1, 2, 3, 4, 5].map((weekday) => ({ weekday, startMinute: 480, endMinute: 960 }));
const resources: Resource[] = [
  { id: MIX, code: "MIX-1", name: "Mixing vessel", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" },
  { id: FILL, code: "FILL-1", name: "Filler", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" },
];
const day = dayStart(today());
const at = (h: number) => new Date(Date.parse(day) + h * 3_600_000).toISOString().replace(".000Z", "Z");

describe("Moves that propagate to pegged orders (FR-23.8)", () => {
  const bars: GanttBar[] = [
    { jobId: JOB1, orderId: PARENT, operNumber: 10, resourceId: MIX, segment: 1, start: at(8), end: at(9), lateMinutes: 0, firmed: false, setupGroup: null, changeoverMinutes: 0, changeoverCost: null, changeoverCurrency: null, cleaningSetId: null, secondaryResourceIds: [], outsideCapacity: false },
  ];
  const preview: MoveResponse = {
    dryRun: true,
    feasible: true,
    violations: [],
    changes: [{ operationId: JOB1, orderId: PARENT, resourceId: MIX, oldStart: at(8), oldEnd: at(9), newStart: at(12), newEnd: at(13), lateMinutes: 0 }],
    orderId: PARENT,
    orderStart: at(12),
    orderEnd: at(13),
    dependents: [{ orderId: DEPENDENT, oldStart: at(9), oldEnd: at(10), newStart: at(13), newEnd: at(14) }],
    truncated: true,
  };

  function workspace(): Handler {
    return (url, init) => {
      if (url.pathname.endsWith("/resources")) return json(200, { items: resources });
      if (url.pathname.endsWith("/gantt")) return json(200, { items: bars });
      if (url.pathname.endsWith(":move")) {
        const body = JSON.parse(String(init.body)) as { dryRun: boolean };
        return json(200, { ...preview, dryRun: body.dryRun });
      }
      return undefined;
    };
  }

  it("previews the pegged orders that move, warns about truncation and commits with propagate", async () => {
    handler = workspace();
    const { container } = renderAt("/scheduling");
    const table = await findTable({ name: "Jobs in window" });
    fireEvent.click(screen.getByLabelText("Move pegged orders too"));
    await waitFor(() => within(table).getByRole("button", { name: "Move order b1000000 operation 10" }));
    fireEvent.click(within(table).getByRole("button", { name: "Move order b1000000 operation 10" }));
    fireEvent.change(screen.getByLabelText("New start (UTC)"), { target: { value: at(12).slice(0, 16) } });
    fireEvent.click(screen.getByRole("button", { name: "Preview move" }));

    const pegged = await findTable({ name: "Pegged orders that move" });
    expect(within(pegged).getAllByRole("row")).toHaveLength(2);
    expect(pegged).toHaveTextContent("b2000000");
    expect(pegged).toHaveTextContent("4 h");
    expect(screen.getByText(/Propagation stopped at 500 orders or 5 pegging levels \(SCHED-0007\)/)).toBeInTheDocument();
    expect(lastCall("POST", ":move")?.body).toEqual({ start: at(12), resourceId: MIX, dryRun: true, propagate: true });
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Confirm move" }));
    expect(await screen.findByText(/1 pegged order\(s\) moved with it/)).toBeInTheDocument();
    expect(lastCall("POST", ":move")?.body).toEqual({ start: at(12), resourceId: MIX, dryRun: false, propagate: true });
  });

  it("leaves propagate out and shows no pegged table when the box is clear", async () => {
    handler = workspace();
    renderAt("/scheduling");
    const table = await findTable({ name: "Jobs in window" });
    await waitFor(() => within(table).getByRole("button", { name: "Move order b1000000 operation 10" }));
    fireEvent.click(within(table).getByRole("button", { name: "Move order b1000000 operation 10" }));
    fireEvent.click(screen.getByRole("button", { name: "Preview move" }));
    await findTable({ name: "Affected operations" });
    expect(screen.queryByRole("table", { name: "Pegged orders that move" })).toBeNull();
    expect(lastCall("POST", ":move")?.body).not.toHaveProperty("propagate");
  });
});

describe("Order pegs", () => {
  const peg: OrderPeg = { id: PEG, parentOrderId: PARENT, dependentOrderId: DEPENDENT, createdAt: "2026-10-26T06:00:00Z" };
  const operation = (orderId: string, operNumber: number): Operation => ({
    id: `${orderId.slice(0, 8)}-0000-4000-8000-0000000000${operNumber}`,
    orderId,
    operNumber,
    minutes: 60,
    priority: 100,
    resourceIds: [MIX],
    secondaryResourceIds: [],
    setupGroup: null,
    setupMinutes: 0,
    attributes: {},
    networkId: null,
    status: "scheduled",
    createdAt: "2026-10-26T06:00:00Z",
  });
  const operations = [operation(PARENT, 10), operation(PARENT, 20), operation(DEPENDENT, 10)];

  it("validates a peg and lists the orders that have operations", () => {
    expect(validatePeg({ parentOrderId: "", dependentOrderId: DEPENDENT }).errors.parentOrderId).toBeTruthy();
    expect(validatePeg({ parentOrderId: PARENT, dependentOrderId: PARENT }).errors.dependentOrderId).toBe("An order cannot be pegged to itself.");
    expect(validatePeg({ parentOrderId: PARENT, dependentOrderId: DEPENDENT }).input).toEqual({ parentOrderId: PARENT, dependentOrderId: DEPENDENT });
    expect(ordersOf(operations)).toEqual([
      { orderId: PARENT, operations: 2 },
      { orderId: DEPENDENT, operations: 1 },
    ]);
  });

  it("lists pegs, filters by order and removes one", async () => {
    let pegs = [peg];
    handler = (url, init) => {
      if (url.pathname.endsWith(`/order-pegs/${PEG}`) && init.method === "DELETE") {
        pegs = [];
        return json(204);
      }
      if (url.pathname.endsWith("/order-pegs")) return json(200, { items: pegs });
      return undefined;
    };
    const { container } = renderAt("/scheduling/order-pegs");
    const table = await findTable({ name: "Order pegs" });
    await waitFor(() => expect(table).toHaveTextContent("b2000000"));
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Order"), { target: { value: DEPENDENT } });
    await waitFor(() => expect(lastCall("GET", "/order-pegs")?.url.searchParams.get("orderId")).toBe(DEPENDENT));

    fireEvent.click(screen.getByRole("button", { name: "Remove peg of order b2000000 to b1000000" }));
    expect(await screen.findByText("Order b2000000 is no longer pegged to b1000000.")).toBeInTheDocument();
    expect(lastCall("DELETE", `/order-pegs/${PEG}`)).toBeTruthy();
    expect(await screen.findByText("This order has no pegs")).toBeInTheDocument();
  });

  it("shows an empty list and a load failure with retry", async () => {
    let fail = true;
    handler = (url) => {
      if (!url.pathname.endsWith("/order-pegs")) return undefined;
      return fail ? json(503, { code: "COM-0503", detail: "service unavailable" }) : json(200, { items: [] });
    };
    renderAt("/scheduling/order-pegs");
    expect(await screen.findByText("service unavailable (COM-0503)")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No order pegs yet")).toBeInTheDocument();
  });

  it("creates a peg from the orders in scheduling with an Idempotency-Key", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/operations")) return json(200, { items: operations });
      if (url.pathname.endsWith("/order-pegs") && init.method === "POST") return json(201, peg);
      if (url.pathname.endsWith("/order-pegs")) return json(200, { items: [peg] });
      return undefined;
    };
    const { container } = renderAt("/scheduling/order-pegs/new");
    fireEvent.change(await screen.findByLabelText("Parent order"), { target: { value: PARENT } });
    fireEvent.change(screen.getByLabelText("Dependent order"), { target: { value: DEPENDENT } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create peg" }));
    await findTable({ name: "Order pegs" });
    const post = lastCall("POST", "/order-pegs")!;
    expect(post.body).toEqual({ parentOrderId: PARENT, dependentOrderId: DEPENDENT });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(lastCall("GET", "/operations")?.url.searchParams.get("limit")).toBe("1000");
  });

  it("shows a loop refused by the service on the dependent field", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/operations")) return json(200, { items: operations });
      if (url.pathname.endsWith("/order-pegs") && init.method === "POST") return json(422, { code: "SCHED-0121", detail: "loop" });
      return undefined;
    };
    renderAt("/scheduling/order-pegs/new");
    fireEvent.change(await screen.findByLabelText("Parent order"), { target: { value: DEPENDENT } });
    fireEvent.change(screen.getByLabelText("Dependent order"), { target: { value: PARENT } });
    fireEvent.click(screen.getByRole("button", { name: "Create peg" }));
    expect(await screen.findByText("The parent already depends on this order, so the peg would form a loop.")).toBeInTheDocument();
  });

  it("asks for operations of two orders before a peg can be made", async () => {
    handler = (url) => (url.pathname.endsWith("/operations") ? json(200, { items: [operation(PARENT, 10)] }) : undefined);
    renderAt("/scheduling/order-pegs/new");
    expect(await screen.findByText("Add operations for two orders first")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add an operation" })).toHaveAttribute("href", "/scheduling/operations/new");
  });
});

describe("Recurring capacity blocks (FR-23.17)", () => {
  const values: BlockValues = {
    resourceId: MIX,
    code: "ALT-MON",
    kind: "recurring",
    days: [1],
    start: "06:00",
    end: "14:00",
    startAt: "",
    endAt: "",
    rule: "freq=weekly;interval=2;byday=mo",
    startDate: "2027-01-04",
    condition: "",
    priority: "",
    active: true,
  };

  it("checks the rule subset the scheduler expands", () => {
    expect(ruleError("FREQ=WEEKLY;BYDAY=MO,WE")).toBeNull();
    expect(ruleError("RRULE:FREQ=DAILY;COUNT=10")).toBeNull();
    expect(ruleError("")).toMatch(/Enter a rule/);
    expect(ruleError("FREQ=MONTHLY")).toBe("FREQ must be DAILY or WEEKLY.");
    expect(ruleError("FREQ=DAILY;BYDAY=MO")).toBe("BYDAY needs FREQ=WEEKLY.");
    expect(ruleError("FREQ=WEEKLY;BYDAY=1MO")).toMatch(/weekday codes/);
    expect(ruleError("FREQ=DAILY;COUNT=2;UNTIL=20270101")).toBe("Give COUNT or UNTIL, not both.");
    expect(ruleError("FREQ=DAILY;BYHOUR=6")).toMatch(/not supported/);
    expect(ruleError("FREQ=DAILY;INTERVAL=0")).toBe("INTERVAL must be a positive whole number.");
  });

  it("builds a recurrence request and reports missing parts", () => {
    expect(validateBlock(values).input).toEqual({
      resourceId: MIX,
      code: "ALT-MON",
      recurrence: { rule: "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO", startDate: "2027-01-04", startMinute: 360, endMinute: 840 },
      condition: [],
      priority: 0,
      active: true,
    });
    const { errors } = validateBlock({ ...values, rule: "FREQ=HOURLY", startDate: "" });
    expect(errors.rule).toBe("FREQ must be DAILY or WEEKLY.");
    expect(errors.startDate).toBe("Enter the first date.");
    expect(describeRecurrence({ rule: "FREQ=WEEKLY;BYDAY=MO", startDate: "2027-01-04", startMinute: 360, endMinute: 840 })).toBe(
      "FREQ=WEEKLY;BYDAY=MO from 2027-01-04, 06:00–14:00",
    );
  });

  it("creates a recurring block from the form and lists it", async () => {
    const block: CapacityBlock = {
      id: "b9",
      resourceId: MIX,
      code: "ALT-MON",
      recurrence: { rule: "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO", startDate: "2027-01-04", startMinute: 360, endMinute: 840 },
      condition: [],
      priority: 0,
      active: true,
      version: 1,
      createdAt: "2026-10-26T06:00:00Z",
    };
    handler = (url, init) => {
      if (url.pathname.endsWith("/resources")) return json(200, { items: resources });
      if (url.pathname.endsWith("/capacity-blocks") && init.method === "POST") return json(201, block);
      if (url.pathname.endsWith("/capacity-blocks")) return json(200, { items: [block] });
      return undefined;
    };
    const { container } = renderAt("/scheduling/capacity-blocks/new");
    fireEvent.change(await screen.findByLabelText("Resource"), { target: { value: MIX } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "alt-mon" } });
    fireEvent.change(screen.getByLabelText("Repeats"), { target: { value: "recurring" } });
    fireEvent.change(screen.getByLabelText("Rule"), { target: { value: "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO" } });
    fireEvent.change(screen.getByLabelText("First date"), { target: { value: "2027-01-04" } });
    expect(screen.queryByText("Weekdays")).toBeNull();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create block" }));
    const table = await findTable({ name: "Capacity blocks" });
    await waitFor(() => expect(table).toHaveTextContent("Repeats FREQ=WEEKLY;INTERVAL=2;BYDAY=MO from 2027-01-04, 06:00–14:00"));
    expect(lastCall("POST", "/capacity-blocks")?.body).toMatchObject({
      code: "ALT-MON",
      recurrence: { rule: "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO", startDate: "2027-01-04", startMinute: 360, endMinute: 840 },
    });
    expect(lastCall("POST", "/capacity-blocks")?.body).not.toHaveProperty("windows");
  });
});
