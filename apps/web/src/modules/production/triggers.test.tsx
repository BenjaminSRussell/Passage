import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { createTriggerPoint, deleteTriggerPoint, listTriggerPoints, type TriggerFiring, type TriggerPoint } from "./api/client";
import { emptyDraft, postedNotice, toJournalInput } from "./JournalsPanel";
import { describeCondition, describeFiring } from "./labels";
import production from "./module";
import { json, order, orderId, problem } from "./testData";
import { emptyTrigger, emptyTriggerOperation, toTriggerInput } from "./TriggerPointsPanel";

afterEach(() => vi.unstubAllGlobals());

type Handler = (url: string, init?: RequestInit) => Response | Promise<Response>;

function stubFetch(handler: Handler) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;

function renderAt(path: string) {
  const at = { pathname: path };
  function Probe() {
    at.pathname = useLocation().pathname;
    return null;
  }
  const view = render(
    <MemoryRouter initialEntries={[path]}>
      <a href="#main">Skip to content</a>
      <nav aria-label="Workspaces" />
      <main id="main">
        <h1>Production</h1>
        <Probe />
        <Routes>
          {production.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </main>
    </MemoryRouter>,
  );
  return { location: at, ...view };
}

const uuid = (n: number) => `0195d6a0-0000-7000-8000-${String(n).padStart(12, "0")}`;
const resource = uuid(301);
const reworkOrderId = uuid(77);

function firing(overrides: Partial<TriggerFiring> = {}): TriggerFiring {
  return {
    id: uuid(601),
    triggerPointId: uuid(401),
    journalId: uuid(201),
    operNumber: 20,
    function: "insert-operations",
    status: "fired",
    feedback: { hours: "2.5", qtyGood: "97", qtyError: "3" },
    insertedOperations: [25, 26],
    actor: "supervisor-1",
    firedAt: "2026-10-26T15:00:00Z",
    ...overrides,
  };
}

function point(overrides: Partial<TriggerPoint> = {}): TriggerPoint {
  return {
    id: uuid(401),
    orderId,
    operNumber: 20,
    sequence: 10,
    condition: "qty_error > 0",
    function: "insert-operations",
    operations: [
      { resourceId: resource, setupTimeH: "0", runTimeHPer: "0.5", scrapPct: "0", laborRate: "40", overheadRate: "0" },
      { resourceId: uuid(302), setupTimeH: "1", runTimeHPer: "0", scrapPct: "0", laborRate: "0", overheadRate: "0" },
    ],
    repeatable: false,
    firedCount: 0,
    createdBy: "engineer-1",
    createdAt: "2026-10-26T13:00:00Z",
    firings: [],
    ...overrides,
  };
}

const routed = (overrides: Parameters<typeof order>[0] = {}) => {
  const base = order();
  const op = base.operations[0];
  return order({ status: "Started", operations: [op, { ...op, operNumber: 20 }, { ...op, operNumber: 30 }], ...overrides });
};

/** Answers a request the test cares about, or undefined to fall through to the detail defaults. */
type Extra = (url: string, init?: RequestInit) => Response | undefined;

function detailFetch(current: ReturnType<typeof order>, extra?: Extra) {
  return stubFetch((url, init) => {
    const handled = extra?.(url, init);
    if (handled) {
      return handled;
    }
    if (url.endsWith("/status-log") || url.endsWith("/journals") || url.endsWith("/as-built") || url.endsWith("/trigger-points")) {
      return json({ orderId, items: [] });
    }
    if (url.includes("reworkOf=") || url.includes("/bom-edits")) {
      return json({ orderId, items: [] });
    }
    if (url.endsWith("/costs")) {
      return json({ orderId, currency: "USD", totals: {}, total: "0", rows: [] });
    }
    return json(current);
  });
}

describe("trigger point API client", () => {
  it("lists, defines and deletes trigger points at the service's routes", async () => {
    const fetchMock = stubFetch((_url, init) => {
      if (init?.method === "POST") return json(point(), 201);
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      return json({ orderId, items: [{ ...point(), firings: null }] });
    });
    const listed = await listTriggerPoints(orderId);
    expect(listed[0].firings).toEqual([]);
    await createTriggerPoint(orderId, { operNumber: 20, sequence: 10, condition: "qty_error > 0", function: "notify" }, "key-1");
    await deleteTriggerPoint(uuid(401));
    const [list, create, remove] = calls(fetchMock);
    expect(list[0]).toBe(`/api/v1/production/orders/${orderId}/trigger-points`);
    expect(create[0]).toBe(`/api/v1/production/orders/${orderId}/trigger-points`);
    expect((create[1]?.headers as Record<string, string>)["Idempotency-Key"]).toBe("key-1");
    expect(remove[0]).toBe(`/api/v1/production/trigger-points/${uuid(401)}`);
    expect(remove[1]?.method).toBe("DELETE");
  });
});

describe("trigger point definitions", () => {
  it("builds the condition and the operations of an insert trigger", () => {
    const draft = { ...emptyTrigger("20"), operations: [{ ...emptyTriggerOperation(), resourceId: resource, runTimeHPer: "0.5" }] };
    expect(toTriggerInput(draft)).toEqual({
      errors: {},
      input: {
        operNumber: 20,
        sequence: 10,
        condition: "qty_error > 0",
        function: "insert-operations",
        repeatable: false,
        operations: [{ resourceId: resource, runTimeHPer: "0.5" }],
      },
    });
  });

  it("sends no operations for notify and accepts a negative or decimal value", () => {
    const { input } = toTriggerInput({ ...emptyTrigger("10"), function: "notify", field: "hours", operator: ">=", value: "1.5", operations: [], repeatable: true });
    expect(input).toEqual({ operNumber: 10, sequence: 10, condition: "hours >= 1.5", function: "notify", repeatable: true });
    expect(toTriggerInput({ ...emptyTrigger("10"), function: "notify", value: "-2", operations: [] }).input?.condition).toBe("qty_error > -2");
  });

  it("refuses what the service refuses", () => {
    const bad = toTriggerInput({
      ...emptyTrigger(""),
      sequence: "10000",
      value: "x",
      function: "create-rework-order",
      operations: [{ ...emptyTriggerOperation(), resourceId: "WC-1", scrapPct: "100", laborRate: "-1" }],
    });
    expect(bad.input).toBeUndefined();
    expect(Object.keys(bad.errors).sort()).toEqual(["op.0.laborRate", "op.0.resourceId", "op.0.scrapPct", "operNumber", "sequence", "value"]);
    expect(toTriggerInput({ ...emptyTrigger("10"), operations: [] }).errors.operations).toBe("Add at least one operation.");
    expect(toTriggerInput({ ...emptyTrigger("10"), sequence: "0", function: "notify", operations: [] }).errors.sequence).toMatch(/1 to 9999/);
  });

  it("describes conditions and firings in words", () => {
    expect(describeCondition("qty_error > 0")).toBe("Error quantity is more than 0");
    expect(describeCondition("hours != 2.5")).toBe("Hours is not 2.5");
    expect(describeCondition("speed > 1")).toBe("speed > 1");
    expect(describeFiring(firing())).toBe("Inserted operations 25, 26");
    expect(describeFiring(firing({ function: "create-rework-order", insertedOperations: [], reworkOrderId }))).toBe("Created a rework order for 3");
    expect(describeFiring(firing({ function: "notify", insertedOperations: [] }))).toBe("Notification sent");
    expect(describeFiring(firing({ status: "failed", reason: "no room for 2 operations", insertedOperations: [] }))).toBe("Failed: no room for 2 operations");
  });
});

describe("Trigger points on the order detail", () => {
  it("lists the trigger points with their firings and opens a rework order a trigger created (AC-21.13)", async () => {
    const points = [
      point({ firedCount: 1, lastFiredAt: "2026-10-26T15:00:00Z", firings: [firing()] }),
      point({
        id: uuid(402),
        sequence: 20,
        function: "create-rework-order",
        operations: [{ resourceId: resource }],
        firedCount: 1,
        lastFiredAt: "2026-10-26T15:00:00Z",
        firings: [firing({ id: uuid(602), triggerPointId: uuid(402), function: "create-rework-order", insertedOperations: [], reworkOrderId })],
      }),
      point({ id: uuid(403), operNumber: 10, sequence: 5, condition: "hours >= 8", function: "notify", operations: [], repeatable: true }),
    ];
    detailFetch(routed(), (url) => (url.endsWith("/trigger-points") ? json({ orderId, items: points }) : undefined));
    const { container, location } = renderAt(`/production/${orderId}`);
    const table = await screen.findByRole("table", { name: "Trigger points of this order" });
    await waitFor(() => expect(within(table).getByText("Hours is at least 8")).toBeInTheDocument());
    expect(within(table).getAllByText("Error quantity is more than 0")).toHaveLength(2);
    expect(within(table).getByText("Every time")).toBeInTheDocument();
    expect(within(table).getAllByText("Kept: it fired")).toHaveLength(2);
    expect(within(table).getByRole("button", { name: "Delete trigger point 10/5" })).toBeInTheDocument();

    const firings = screen.getByRole("table", { name: "Trigger firings" });
    expect(within(firings).getByText("Inserted operations 25, 26")).toBeInTheDocument();
    expect(within(firings).getByText("Created a rework order for 3")).toBeInTheDocument();
    expect(within(firings).getAllByText("2.5 h, 97 good, 3 error")).toHaveLength(2);
    expect(checkAxeAA(container)).toEqual([]);

    await userEvent.click(screen.getByRole("button", { name: "Open the rework order of operation 20" }));
    await waitFor(() => expect(location.pathname).toBe(`/production/${reworkOrderId}`));
  });

  it("shows the empty state, defines an insert trigger with one key per body and reloads", async () => {
    let stored: TriggerPoint[] = [];
    const fetchMock = detailFetch(routed(), (url, init) => {
      if (url.endsWith("/trigger-points") && init?.method === "POST") {
        stored = [point()];
        return json(point(), 201);
      }
      return url.endsWith("/trigger-points") ? json({ orderId, items: stored }) : undefined;
    });
    renderAt(`/production/${orderId}`);
    expect(await screen.findByRole("heading", { name: "No trigger points" })).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Define a trigger point" });
    await userEvent.selectOptions(within(form).getByLabelText("On operation"), "20");

    await userEvent.click(within(form).getByRole("button", { name: "Define trigger point" }));
    expect(await within(form).findByText("Enter the resource's id (a UUID).")).toBeInTheDocument();
    expect(calls(fetchMock).some(([url, init]) => url.endsWith("/trigger-points") && init?.method === "POST")).toBe(false);

    const first = within(form).getByRole("group", { name: "Trigger operation 1" });
    await userEvent.type(within(first).getByLabelText("Resource"), resource);
    await userEvent.type(within(first).getByLabelText(/Run hours per unit/), "0.5");
    await userEvent.click(within(form).getByRole("button", { name: "Add operation" }));
    const second = within(form).getByRole("group", { name: "Trigger operation 2" });
    await userEvent.type(within(second).getByLabelText("Resource"), uuid(302));
    await userEvent.type(within(second).getByLabelText(/Setup hours/), "1");
    await userEvent.click(within(form).getByRole("button", { name: "Define trigger point" }));

    expect(await screen.findByText("Trigger point 10 on operation 20 defined: Error quantity is more than 0.")).toBeInTheDocument();
    const create = calls(fetchMock).find(([url, init]) => url.endsWith("/trigger-points") && init?.method === "POST");
    expect(create?.[0]).toBe(`/api/v1/production/orders/${orderId}/trigger-points`);
    expect(JSON.parse(String(create?.[1]?.body))).toEqual({
      operNumber: 20,
      sequence: 10,
      condition: "qty_error > 0",
      function: "insert-operations",
      repeatable: false,
      operations: [
        { resourceId: resource, runTimeHPer: "0.5" },
        { resourceId: uuid(302), setupTimeH: "1" },
      ],
    });
    expect((create?.[1]?.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/.+/);
    const table = await screen.findByRole("table", { name: "Trigger points of this order" });
    await waitFor(() => expect(within(table).getByText("Insert operations")).toBeInTheDocument());
    expect(within(form).getByLabelText("Sequence")).toHaveValue("20");
  });

  it("shows a refusal, such as a rework trigger without the rework duty", async () => {
    detailFetch(routed(), (url, init) =>
      url.endsWith("/trigger-points") && init?.method === "POST"
        ? problem(403, "AUTH-0003", "a trigger point that creates rework orders needs the duty production.order.rework")
        : undefined,
    );
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Define a trigger point" });
    await userEvent.selectOptions(within(form).getByLabelText("Then"), "create-rework-order");
    await userEvent.type(within(form).getByLabelText("Resource"), resource);
    await userEvent.click(within(form).getByRole("button", { name: "Define trigger point" }));
    const alert = await screen.findByText(/needs the duty production.order.rework/);
    expect(alert.closest("[role=alert]")).toHaveTextContent("The trigger point change was refused");
  });

  it("offers no operations for notify", async () => {
    const fetchMock = detailFetch(routed(), (url, init) =>
      url.endsWith("/trigger-points") && init?.method === "POST" ? json(point({ function: "notify", operations: [] }), 201) : undefined,
    );
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "Define a trigger point" });
    await userEvent.selectOptions(within(form).getByLabelText("Then"), "notify");
    expect(within(form).queryByRole("group", { name: "Trigger operation 1" })).not.toBeInTheDocument();
    await userEvent.selectOptions(within(form).getByLabelText("When"), "hours");
    await userEvent.selectOptions(within(form).getByLabelText("Comparison"), ">=");
    await userEvent.clear(within(form).getByLabelText("Value"));
    await userEvent.type(within(form).getByLabelText("Value"), "8");
    await userEvent.selectOptions(within(form).getByLabelText(/^Fires/), "every");
    await userEvent.click(within(form).getByRole("button", { name: "Define trigger point" }));
    await waitFor(() => expect(calls(fetchMock).some(([url, init]) => url.endsWith("/trigger-points") && init?.method === "POST")).toBe(true));
    const create = calls(fetchMock).find(([url, init]) => url.endsWith("/trigger-points") && init?.method === "POST");
    expect(JSON.parse(String(create?.[1]?.body))).toEqual({ operNumber: 10, sequence: 10, condition: "hours >= 8", function: "notify", repeatable: true });
  });

  it("deletes a trigger point that never fired after confirmation", async () => {
    let stored = [point()];
    const fetchMock = detailFetch(routed(), (url, init) => {
      if (init?.method === "DELETE") {
        stored = [];
        return new Response(null, { status: 204 });
      }
      return url.endsWith("/trigger-points") ? json({ orderId, items: stored }) : undefined;
    });
    renderAt(`/production/${orderId}`);
    await userEvent.click(await screen.findByRole("button", { name: "Delete trigger point 20/10" }));
    expect(screen.getByText("Delete trigger point 10 on operation 20?")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Yes, delete it" }));
    expect(await screen.findByText("Trigger point 10 on operation 20 deleted.")).toBeInTheDocument();
    expect(calls(fetchMock).find(([, init]) => init?.method === "DELETE")?.[0]).toBe(`/api/v1/production/trigger-points/${uuid(401)}`);
    expect(await screen.findByRole("heading", { name: "No trigger points" })).toBeInTheDocument();
  });

  it("shows a load error, and no form on an ended order", async () => {
    detailFetch(routed({ status: "Ended" }), (url) =>
      url.endsWith("/trigger-points") ? problem(500, "internal", "the database is unavailable") : undefined,
    );
    renderAt(`/production/${orderId}`);
    expect(await screen.findByText("Could not load the trigger points")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Define a trigger point" })).not.toBeInTheDocument();
    expect(screen.getByText("An ended or cancelled order takes no more feedback, so no trigger points.")).toBeInTheDocument();
  });
});

describe("Route cards and trigger firings in the journals panel", () => {
  it("sends the error quantity of an operation", () => {
    const draft = { ...emptyDraft("route-card"), proposal: "none" as const, hours: { "20": { hours: "2.5", qtyGood: "97", qtyError: "3" } } };
    expect(toJournalInput(draft).input?.hours).toEqual([{ operNumber: 20, hours: "2.5", qtyGood: "97", qtyError: "3" }]);
    const bad = { ...draft, hours: { "20": { hours: "", qtyGood: "", qtyError: "x" } } };
    expect(toJournalInput(bad).errors["error.20"]).toBe("Enter a quantity with up to 6 decimals.");
  });

  it("says after the post what the trigger points did", () => {
    expect(postedNotice("PJ-000004")).toBe("Journal PJ-000004 posted.");
    expect(postedNotice("PJ-000004", [firing(), firing({ id: "f2", status: "failed", reason: "no room" })])).toBe(
      "Journal PJ-000004 posted. Operation 20: Inserted operations 25, 26.",
    );
  });
});
