import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import type { Board, Flow, Kanban, KanbanState, Rule } from "./api/client";
import { REFRESH_MS } from "./BoardPage";
import { validateFlow } from "./FlowPages";
import { nextState, overdue } from "./format";
import lean from "./module";
import { validateRule } from "./RulePages";

const FLOW = "f1000000-0000-4000-8000-000000000001";
const RULE = "a1000000-0000-4000-8000-000000000001";
const EVENT_RULE = "a1000000-0000-4000-8000-000000000002";
const STREAM = "5e000000-0000-4000-8000-000000000001";
const SITE = "51000000-0000-4000-8000-000000000001";
const ITEM = "17000000-0000-4000-8000-000000000001";
const LOC = "10000000-0000-4000-8000-000000000001";
const K1 = "c0000000-0000-4000-8000-000000000001";
const K2 = "c0000000-0000-4000-8000-000000000002";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

function json(status: number, body: unknown): Reply {
  return { status, body };
}

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
        {lean.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

function header(call: { init: RequestInit }, name: string): string | null {
  return new Headers(call.init.headers).get(name);
}

const flow: Flow = {
  id: FLOW,
  valueStreamId: STREAM,
  siteId: SITE,
  name: "Sauce line",
  taktTimeS: 60,
  status: "draft",
  activeFrom: null,
  activeTo: null,
  version: 1,
  activityCount: 2,
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
  activities: [
    { id: "ac000000-0000-4000-8000-000000000001", sequence: 1, name: "Mix", kind: "process", cycleTimeS: 45, outputLocationId: LOC, predecessors: [], pickingRequired: false },
    { id: "ac000000-0000-4000-8000-000000000002", sequence: 2, name: "Fill", kind: "process", cycleTimeS: 30, outputLocationId: null, predecessors: [1], pickingRequired: true },
  ],
};

const rule: Rule = {
  id: RULE,
  code: "MIX-A",
  itemId: ITEM,
  siteId: SITE,
  flowId: FLOW,
  ruleType: "manufacturing",
  replenishment: "fixed",
  fixedQty: "25",
  numberOfKanbans: 3,
  leadTimeH: 8,
  eventSource: null,
  exactQty: false,
  effectiveFrom: "2026-10-01",
  effectiveTo: null,
  version: 1,
  activeKanbans: 1,
  createdAt: "2026-10-20T08:00:00Z",
};

const eventRule: Rule = { ...rule, id: EVENT_RULE, code: "FILL-EV", replenishment: "event", fixedQty: "20", numberOfKanbans: null, eventSource: "sales", activeKanbans: 0 };

function card(id: string, number: string, state: KanbanState, extra: Partial<Kanban> = {}): Kanban {
  return {
    id,
    ruleId: RULE,
    ruleCode: "MIX-A",
    itemId: ITEM,
    ruleType: "manufacturing",
    replenishment: "fixed",
    cardNumber: number,
    qty: "25",
    state,
    due: null,
    eventRefType: null,
    eventRefId: null,
    version: 2,
    createdAt: "2026-10-20T08:00:00Z",
    updatedAt: "2026-10-20T08:00:00Z",
    ...extra,
  };
}

function boardOf(cards: Kanban[]): Board {
  const states: KanbanState[] = ["not-planned", "planned", "prepared", "in-progress", "completed", "emptied"];
  return { columns: states.map((state) => ({ state, kanbans: cards.filter((k) => k.state === state) })), truncated: false };
}

describe("lean module", () => {
  it("is auto-discovered and routes every nav entry", () => {
    expect(lean.id).toBe("lean");
    expect(loadModules().map((m) => m.id)).toContain("lean");
    const paths = lean.routes.map((r) => r.path);
    for (const n of lean.nav) {
      expect(n.to.startsWith("/lean")).toBe(true);
      expect(paths).toContain(n.to);
    }
  });

  it("links every area from the overview", () => {
    const { container } = renderAt("/lean");
    expect(within(screen.getByRole("list", { name: "Lean manufacturing areas" })).getAllByRole("link")).toHaveLength(3);
    expect(pageViolations(container)).toEqual([]);
  });

  it("knows the forward step of each card (BR-24.6)", () => {
    expect(nextState("planned", "fixed")).toBe("prepared");
    expect(nextState("emptied", "fixed")).toBe("not-planned");
    expect(nextState("emptied", "event")).toBeNull();
    const now = Date.parse("2026-10-26T12:00:00Z");
    expect(overdue("2026-10-26T11:00:00Z", "planned", now)).toBe(true);
    expect(overdue("2026-10-26T11:00:00Z", "completed", now)).toBe(false);
    expect(overdue(null, "planned", now)).toBe(false);
  });
});

describe("production flows", () => {
  it("lists flows and filters by status", async () => {
    handler = (url) => {
      if (url.pathname !== "/api/v1/lean/flows") return undefined;
      return url.searchParams.get("status") === "active" ? json(200, { items: [] }) : json(200, { items: [{ ...flow, activities: undefined }] });
    };
    const { container } = renderAt("/lean/flows");
    const table = await screen.findByRole("table", { name: "Production flows" });
    expect(within(table).getByRole("button", { name: "Sauce line" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "active" } });
    expect(await screen.findByText("No flows with this status")).toBeInTheDocument();
  });

  it("numbers activities and checks predecessors before creating", async () => {
    const values = {
      valueStreamId: STREAM,
      siteId: SITE,
      name: "Sauce line",
      taktTimeS: "60",
      activities: [
        { name: "Mix", kind: "process" as const, cycleTimeS: "45", outputLocationId: LOC, predecessors: "", pickingRequired: false },
        { name: "Fill", kind: "process" as const, cycleTimeS: "30", outputLocationId: "", predecessors: "2", pickingRequired: false },
      ],
    };
    expect(validateFlow(values).errors.activity?.["1.predecessors"]).toBe("List other activity numbers (1 to 2), separated by commas.");
    values.activities[1].predecessors = "1";
    expect(validateFlow(values).input?.activities.map((a) => [a.sequence, a.predecessors, a.outputLocationId])).toEqual([
      [1, [], LOC],
      [2, [1], null],
    ]);

    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows" && init.method === "POST") return json(201, flow);
      if (url.pathname === `/api/v1/lean/flows/${FLOW}`) return json(200, flow);
      return undefined;
    };
    const { container } = renderAt("/lean/flows/new");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Sauce line" } });
    fireEvent.change(screen.getByLabelText("Value stream id"), { target: { value: STREAM } });
    fireEvent.change(screen.getByLabelText("Site id"), { target: { value: SITE } });
    fireEvent.change(screen.getByLabelText("Activity name"), { target: { value: "Mix" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create flow" }));
    expect(await screen.findByRole("heading", { name: "Sauce line" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(post.body).toMatchObject({ name: "Sauce line", taktTimeS: null, activities: [{ sequence: 1, name: "Mix", kind: "process", predecessors: [] }] });
  });

  it("activates a draft flow and shows the connectivity refusal (FR-24.8)", async () => {
    let refuse = true;
    handler = (url, init) => {
      if (url.pathname === `/api/v1/lean/flows/${FLOW}`) return json(200, flow);
      if (url.pathname === `/api/v1/lean/flows/${FLOW}:activate` && init.method === "POST") {
        return refuse
          ? json(422, { code: "LEAN-0004", detail: "flow activity Fill has no output location" })
          : json(200, { ...flow, status: "active", activeFrom: "2026-11-02", version: 2 });
      }
      return undefined;
    };
    const { container } = renderAt(`/lean/flows/${FLOW}`);
    const table = await screen.findByRole("table", { name: "Flow activities" });
    expect(within(table).getByText("Missing")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Active from"), { target: { value: "2026-11-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Activate flow" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("flow activity Fill has no output location (LEAN-0004)");
    expect(calls.at(-1)?.body).toEqual({ version: 1, activeFrom: "2026-11-02" });
    refuse = false;
    fireEvent.click(screen.getByRole("button", { name: "Activate flow" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Activate flow" })).toBeNull());
    expect(screen.getByRole("list", { name: "Flow facts" })).toHaveTextContent("Active from 2026-11-02");
  });
});

describe("kanban rules", () => {
  it("lists rules with their active cards", async () => {
    handler = (url) => (url.pathname === "/api/v1/lean/kanban-rules" ? json(200, { items: [rule, eventRule] }) : undefined);
    const { container } = renderAt(`/lean/rules?flowId=${FLOW}`);
    const table = await screen.findByRole("table", { name: "Kanban rules" });
    expect(within(table).getByText("1 of 3")).toBeInTheDocument();
    expect(within(table).getByText("Event")).toBeInTheDocument();
    expect(calls[0].url.searchParams.get("flowId")).toBe(FLOW);
    expect(pageViolations(container)).toEqual([]);
  });

  it("keeps strategy fields to their strategy", () => {
    const base = {
      code: "MIX-A",
      itemId: ITEM,
      siteId: SITE,
      flowId: "",
      ruleType: "manufacturing" as const,
      replenishment: "fixed" as const,
      fixedQty: "25",
      numberOfKanbans: "",
      leadTimeH: "8",
      eventSource: "sales" as const,
      exactQty: true,
      effectiveFrom: "2026-10-01",
    };
    expect(validateRule(base).errors.numberOfKanbans).toBe("Enter 1 to 1000 cards.");
    expect(validateRule({ ...base, numberOfKanbans: "3" }).input).toMatchObject({ numberOfKanbans: 3, eventSource: null, exactQty: false, flowId: null });
    expect(validateRule({ ...base, replenishment: "event" }).input).toMatchObject({ numberOfKanbans: null, eventSource: "sales", exactQty: true });
    expect(validateRule({ ...base, numberOfKanbans: "3", fixedQty: "0" }).errors.fixedQty).toBeTruthy();
  });

  it("suggests the number of kanbans and creates a fixed rule", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [flow] });
      if (url.pathname === "/api/v1/lean/kanban-rules:fixed-count") return json(200, { numberOfKanbans: 5 });
      if (url.pathname === "/api/v1/lean/kanban-rules" && init.method === "POST") return json(201, rule);
      if (url.pathname === `/api/v1/lean/kanban-rules/${RULE}`) return json(200, rule);
      if (url.pathname === "/api/v1/lean/board") return json(200, boardOf([]));
      return undefined;
    };
    const { container } = renderAt(`/lean/rules/new?flowId=${FLOW}`);
    await screen.findByRole("option", { name: "Sauce line" });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "mix-a" } });
    fireEvent.change(screen.getByLabelText("Item id"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Site id"), { target: { value: SITE } });
    fireEvent.change(screen.getByLabelText("Quantity per card"), { target: { value: "50" } });
    fireEvent.change(screen.getByLabelText("Lead time (hours)"), { target: { value: "48" } });
    fireEvent.change(screen.getByLabelText("Daily demand"), { target: { value: "100" } });
    fireEvent.change(screen.getByLabelText("Safety (%)"), { target: { value: "20" } });
    fireEvent.click(screen.getByRole("button", { name: "Suggest number of kanbans" }));
    expect(await screen.findByText("Suggested 5 cards. You can change it.")).toBeInTheDocument();
    expect(screen.getByLabelText("Number of kanbans")).toHaveValue("5");
    expect(calls.find((c) => c.url.pathname.endsWith(":fixed-count"))?.body).toEqual({ dailyDemand: "100", leadTimeDays: "2.0000", safetyPct: "20", fixedQty: "50" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create rule" }));
    expect(await screen.findByRole("heading", { name: "MIX-A" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST" && c.url.pathname === "/api/v1/lean/kanban-rules")!;
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(post.body).toMatchObject({ code: "MIX-A", flowId: FLOW, replenishment: "fixed", fixedQty: "50", numberOfKanbans: 5, leadTimeH: 48, eventSource: null });
  });

  it("generates event kanbans for a sales line (AC-24.6)", async () => {
    let cards: Kanban[] = [];
    handler = (url, init) => {
      if (url.pathname === `/api/v1/lean/kanban-rules/${EVENT_RULE}`) return json(200, eventRule);
      if (url.pathname === "/api/v1/lean/board") return json(200, boardOf(cards));
      if (url.pathname === "/api/v1/lean/kanbans:generate" && init.method === "POST") {
        cards = [1, 2, 3].map((n) => card(`c0000000-0000-4000-8000-00000000001${n}`, `FILL-EV-000${n}`, "planned", { qty: "20", eventRefId: "SO-1/10" }));
        return json(201, { items: cards });
      }
      return undefined;
    };
    const { container } = renderAt(`/lean/rules/${EVENT_RULE}`);
    expect(await screen.findByText("No cards yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Demand quantity"), { target: { value: "50" } });
    fireEvent.change(screen.getByLabelText("Demand reference"), { target: { value: "SO-1/10" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate kanbans" }));
    expect(await screen.findByText("3 cards created: FILL-EV-0001, FILL-EV-0002, FILL-EV-0003.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ ruleId: EVENT_RULE, demandQty: "50", eventRefType: "sales-line", eventRefId: "SO-1/10" });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    const table = await screen.findByRole("table", { name: "Kanban cards" });
    await waitFor(() => expect(within(table).getAllByText("20")).toHaveLength(3));
  });

  it("shows a full fixed rule as LEAN-0002", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/lean/kanban-rules/${RULE}`) return json(200, { ...rule, activeKanbans: 3 });
      if (url.pathname === "/api/v1/lean/board") return json(200, boardOf([]));
      if (init.method === "POST") return json(409, { code: "LEAN-0002", detail: "rule MIX-A already has 3 active kanbans (max 3)" });
      return undefined;
    };
    renderAt(`/lean/rules/${RULE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Generate kanbans" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("rule MIX-A already has 3 active kanbans (max 3) (LEAN-0002)");
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ ruleId: RULE });
  });
});

describe("kanban board", () => {
  it("moves a card one step with its version and shows the result", async () => {
    let cards = [card(K1, "MIX-A-0001", "planned", { due: "2020-01-01T00:00:00Z" }), card(K2, "MIX-A-0002", "in-progress")];
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [flow] });
      if (url.pathname === "/api/v1/lean/kanban-rules") return json(200, { items: [rule] });
      if (url.pathname === "/api/v1/lean/board") return json(200, boardOf(cards));
      if (url.pathname === `/api/v1/lean/kanbans/${K1}:move` && init.method === "POST") {
        cards = [{ ...cards[0], state: "prepared", version: 3 }, cards[1]];
        return json(200, cards[0]);
      }
      return undefined;
    };
    const { container } = renderAt("/lean/board");
    const planned = await screen.findByRole("list", { name: "Planned cards" });
    expect(within(planned).getByText("MIX-A-0001")).toBeInTheDocument();
    expect(within(planned).getByText("Overdue")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Board metrics" })).toHaveTextContent("Overdue 1");
    expect(screen.getByRole("list", { name: "Board metrics" })).toHaveTextContent("Work in progress 1");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Move MIX-A-0001 to Prepared" }));
    expect(await screen.findByText("MIX-A-0001 moved to Prepared.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ to: "prepared", version: 2 });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    await waitFor(() => expect(within(screen.getByRole("list", { name: "Prepared cards" })).getByText("MIX-A-0001")).toBeInTheDocument());
  });

  it("keeps a card in its column when the move is refused (AC-24.7)", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows" || url.pathname === "/api/v1/lean/kanban-rules") return json(200, { items: [] });
      if (url.pathname === "/api/v1/lean/board") return json(200, boardOf([card(K1, "MIX-A-0001", "planned")]));
      if (init.method === "POST") return json(409, { code: "LEAN-0001", detail: "kanban move planned → completed is not allowed" });
      return undefined;
    };
    renderAt("/lean/board");
    fireEvent.change(await screen.findByLabelText("Move MIX-A-0001 to column"), { target: { value: "completed" } });
    fireEvent.click(screen.getByRole("button", { name: "Move MIX-A-0001" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("MIX-A-0001 stays in Planned");
    expect(alert).toHaveTextContent("kanban move planned → completed is not allowed (LEAN-0001)");
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ to: "completed", version: 2 });
    expect(within(screen.getByRole("list", { name: "Planned cards" })).getByText("MIX-A-0001")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Completed cards" })).queryByText("MIX-A-0001")).toBeNull();
  });

  it("filters by rule and shows the empty board", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [flow] });
      if (url.pathname === "/api/v1/lean/kanban-rules") return json(200, { items: [rule] });
      if (url.pathname === "/api/v1/lean/board") return json(200, boardOf([]));
      return undefined;
    };
    const { container } = renderAt(`/lean/board?ruleId=${RULE}`);
    expect(await screen.findByText("No kanbans on this board")).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname === "/api/v1/lean/board")?.url.searchParams.get("ruleId")).toBe(RULE);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a load failure with a retry", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/lean/board") return json(503, { code: "COM-0500", detail: "service unavailable" });
      return json(200, { items: [] });
    };
    renderAt("/lean/board");
    expect(await screen.findByText("service unavailable (COM-0500)")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("refreshes on its own every 30 seconds", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    handler = (url) => (url.pathname === "/api/v1/lean/board" ? json(200, boardOf([card(K1, "MIX-A-0001", "planned")])) : json(200, { items: [] }));
    renderAt("/lean/board");
    await screen.findByRole("list", { name: "Planned cards" });
    const loads = () => calls.filter((c) => c.url.pathname === "/api/v1/lean/board").length;
    const before = loads();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(REFRESH_MS);
    });
    await waitFor(() => expect(loads()).toBe(before + 1));
  });
});
