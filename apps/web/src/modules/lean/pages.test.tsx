import { findAlert, findStatus, findTable } from "../../test-utils";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import {
  LeanApiError,
  type BackflushPreview,
  type Board,
  type CostingPeriod,
  type Flow,
  type JobBoard,
  type JobState,
  type Kanban,
  type KanbanJob,
  type KanbanState,
  type Rule,
  type RuleAnalysis,
  type CountCheck,
} from "./api/client";
import { REFRESH_MS } from "./BoardPage";
import { validatePeriod } from "./CostingPage";
import { validateFlow } from "./FlowPages";
import { nextState, overdue, variance } from "./format";
import { answered } from "./commandKeys";
import lean from "./module";
import { shiftDays, validateAnalysisPeriod } from "./RuleAnalysis";
import { validateCountCheck } from "./RuleCountCheck";
import { validateRule } from "./RulePages";
import { validateCompletion, workCells } from "./WorkCellBoardPage";

const FLOW = "f1000000-0000-4000-8000-000000000001";
const RULE = "a1000000-0000-4000-8000-000000000001";
const EVENT_RULE = "a1000000-0000-4000-8000-000000000002";
const STREAM = "5e000000-0000-4000-8000-000000000001";
const SITE = "51000000-0000-4000-8000-000000000001";
const ITEM = "17000000-0000-4000-8000-000000000001";
const LOC = "10000000-0000-4000-8000-000000000001";
const K1 = "c0000000-0000-4000-8000-000000000001";
const K2 = "c0000000-0000-4000-8000-000000000002";

type Reply = { status: number; body: unknown; text?: string };
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
      if (reply.text !== undefined) return new Response(reply.text, { status: reply.status, headers: { "Content-Type": "text/csv" } });
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
  flowVersion: 1,
  previousFlowId: null,
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
    handlingUnits: null,
    jobCount: 0,
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
    expect(within(screen.getByRole("list", { name: "Lean manufacturing areas" })).getAllByRole("link")).toHaveLength(6);
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
    const table = await findTable({ name: "Production flows" });
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
    fireEvent.change(screen.getByLabelText("Value stream"), { target: { value: STREAM } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
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
    const table = await findTable({ name: "Flow activities" });
    expect(within(table).getByText("Missing")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Active from"), { target: { value: "2026-11-02" } });
    fireEvent.click(screen.getByRole("button", { name: "Activate flow" }));
    expect(await findAlert()).toHaveTextContent("flow activity Fill has no output location");
    expect(calls.at(-1)?.body).toEqual({ version: 1, activeFrom: "2026-11-02" });
    refuse = false;
    fireEvent.click(screen.getByRole("button", { name: "Activate flow" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Activate flow" })).toBeNull());
    expect(screen.getByRole("list", { name: "Flow facts" })).toHaveTextContent("Active from 2026-11-02");
  });
});

describe("flow versions", () => {
  const active: Flow = { ...flow, status: "active", activeFrom: "2026-11-02", version: 2 };

  it("creates the next version of an active flow and opens its draft (FR-24.8)", async () => {
    const next: Flow = { ...flow, id: "f1000000-0000-4000-8000-000000000002", flowVersion: 2, previousFlowId: FLOW, taktTimeS: 50 };
    handler = (url, init) => {
      if (url.pathname === `/api/v1/lean/flows/${FLOW}`) return json(200, active);
      if (url.pathname === `/api/v1/lean/flows/${next.id}`) return json(200, next);
      if (url.pathname === `/api/v1/lean/flows/${FLOW}:new-version` && init.method === "POST") return json(201, next);
      return undefined;
    };
    renderAt(`/lean/flows/${FLOW}`);
    expect(await screen.findByText("Version 1")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Takt time/), { target: { value: "50" } });
    fireEvent.click(screen.getByRole("button", { name: "Create new version" }));
    expect(await screen.findByText("Version 2")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ version: 2, taktTimeS: 50 });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(screen.getByRole("link", { name: "the previous version" })).toHaveAttribute("href", `/lean/flows/${FLOW}`);
    expect(screen.queryByRole("button", { name: "Create new version" })).toBeNull();
  });

  it("checks the takt time and shows a refusal", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/lean/flows/${FLOW}`) return json(200, active);
      if (url.pathname.endsWith(":new-version") && init.method === "POST") return json(409, { code: "LEAN-0115", detail: "production flow Sauce line already has a newer version" });
      return undefined;
    };
    renderAt(`/lean/flows/${FLOW}`);
    const field = await screen.findByLabelText(/Takt time/);
    fireEvent.change(field, { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Create new version" }));
    expect(await screen.findByText("Enter whole seconds, 1 or more.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(field, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Create new version" }));
    expect(await findAlert()).toHaveTextContent("already has a newer version");
  });

  it("activates a new version, refuses a start that is too early, and shows the replaced version's end", async () => {
    const draft: Flow = { ...flow, id: "f1000000-0000-4000-8000-000000000002", flowVersion: 2, previousFlowId: FLOW };
    const expired: Flow = { ...active, status: "expired", activeTo: "2026-11-30", version: 3 };
    handler = (url, init) => {
      if (url.pathname === `/api/v1/lean/flows/${draft.id}`) return json(200, draft);
      if (url.pathname === `/api/v1/lean/flows/${FLOW}`) return json(200, expired);
      if (url.pathname === `/api/v1/lean/flows/${draft.id}:activate` && init.method === "POST") {
        return json(422, { code: "LEAN-0116", detail: "version 2 of Sauce line must start after version 1 started on 2026-11-02" });
      }
      return undefined;
    };
    renderAt(`/lean/flows/${draft.id}`);
    expect(await screen.findByText(/expires the version it replaces/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Active from"), { target: { value: "2026-11-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Activate flow" }));
    expect(await findAlert()).toHaveTextContent("must start after version 1 started on 2026-11-02");
    fireEvent.click(screen.getByRole("link", { name: "the previous version" }));
    expect(await screen.findByText("Version 1")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Flow facts" })).toHaveTextContent("Active to 2026-11-30");
    expect(screen.queryByRole("button", { name: "Create new version" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Activate flow" })).toBeNull();
  });
});

/** The rule page's analysis card: no cycles yet. */
function quietAnalysis(url: URL): Reply | undefined {
  const m = /^\/api\/v1\/lean\/kanban-rules\/([^/]+)\/analysis$/.exec(url.pathname);
  if (!m) return undefined;
  return json(200, {
    ruleId: m[1], ruleCode: "", from: "2026-09-27", to: "2026-10-26", cycles: 0, timedCycles: 0, avgLeadTimeH: null, targetLeadTimeH: 8,
    dueCycles: 0, lateCycles: 0, latePct: null, activeKanbans: 0, maxKanbans: null, overdueOpen: 0,
  });
}

describe("kanban rules", () => {
  it("lists rules with their active cards", async () => {
    handler = (url) => (url.pathname === "/api/v1/lean/kanban-rules" ? json(200, { items: [rule, eventRule] }) : undefined);
    const { container } = renderAt(`/lean/rules?flowId=${FLOW}`);
    const table = await findTable({ name: "Kanban rules" });
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
    fireEvent.change(screen.getByLabelText("Item"), { target: { value: ITEM } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
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
      return quietAnalysis(url);
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
    const table = await findTable({ name: "Kanban cards" });
    await waitFor(() => expect(within(table).getAllByText("20")).toHaveLength(3));
  });

  it("shows a full fixed rule as LEAN-0002", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/lean/kanban-rules/${RULE}`) return json(200, { ...rule, activeKanbans: 3 });
      if (url.pathname === "/api/v1/lean/board") return json(200, boardOf([]));
      if (init.method === "POST") return json(409, { code: "LEAN-0002", detail: "rule MIX-A already has 3 active kanbans (max 3)" });
      return quietAnalysis(url);
    };
    renderAt(`/lean/rules/${RULE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Generate kanbans" }));
    expect(await findAlert()).toHaveTextContent("rule MIX-A already has 3 active kanbans (max 3)");
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
    const alert = await findAlert();
    expect(alert).toHaveTextContent("MIX-A-0001 stays in Planned");
    expect(alert).toHaveTextContent("kanban move planned → completed is not allowed");
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
    expect(await screen.findByText("service unavailable")).toBeInTheDocument();
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

describe("slice 2", () => {
  it("shows takt vs cycle on a board filtered to a flow (FR-24.14)", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/lean/flows" || url.pathname === "/api/v1/lean/kanban-rules") return json(200, { items: [] });
      if (url.pathname === "/api/v1/lean/board")
        return json(200, { ...boardOf([card(K1, "MIX-A-0001", "planned")]), takt: { taktTimeS: 60, cycleTimeS: 75, bottleneckActivity: "Fill", withinTakt: false } });
      return undefined;
    };
    renderAt(`/lean/board?flowId=${FLOW}`);
    const metrics = await screen.findByRole("list", { name: "Board metrics" });
    expect(metrics).toHaveTextContent("Takt vs cycle 60 s / 75 s");
    expect(metrics).toHaveTextContent("Fill is slower than takt.");
    expect(calls.find((c) => c.url.pathname === "/api/v1/lean/board")?.url.searchParams.get("flowId")).toBe(FLOW);
  });

  it("previews subcontract PO lines for AC-24.17", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/lean/subcontracting:preview"
        ? json(200, {
            mode: "po-per-kanban",
            lines: [{ vendorId: "V-PLATING", itemId: "SVC-PLATING", qty: "20", unitPrice: "4.5", currency: "USD", priceDate: "2027-03-10", amount: "90", jobIds: ["KJ-0001"] }],
          })
        : undefined;
    const { container } = renderAt("/lean/subcontracting");
    fireEvent.click(screen.getByRole("button", { name: "Preview PO lines" }));
    const table = await findTable({ name: "Purchase order lines" });
    expect(within(table).getByText("90 USD")).toBeInTheDocument();
    expect(within(table).getByText("KJ-0001")).toBeInTheDocument();
    expect(calls[0].body).toEqual({
      mode: "po-per-kanban",
      agreements: [{ vendorId: "V-PLATING", itemId: "SVC-PLATING", unitPrice: "4.50", currency: "USD", validFrom: "2027-01-01", validTo: null }],
      jobs: [{ jobId: "KJ-0001", vendorId: "V-PLATING", itemId: "SVC-PLATING", qty: "20", completedOn: "2027-03-10" }],
    });
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });

  it("shows LEAN-0005 and checks input before calling", async () => {
    handler = () => json(422, { code: "LEAN-0005", detail: "no valid purchase agreement for service SVC-PLATING on 2027-03-10" });
    renderAt("/lean/subcontracting");
    fireEvent.change(screen.getByLabelText("Completed quantity"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview PO lines" }));
    expect(await screen.findByText("Enter the completed quantity.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Completed quantity"), { target: { value: "20" } });
    fireEvent.change(screen.getByLabelText("Mode"), { target: { value: "periodic-po" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview PO lines" }));
    expect(await findAlert()).toHaveTextContent("no valid purchase agreement for service SVC-PLATING on 2027-03-10");
    expect((calls[0].body as { mode: string }).mode).toBe("periodic-po");
  });
});

const CELL = "ce000000-0000-4000-8000-000000000001";
const J1 = "b0000000-0000-4000-8000-000000000001";
const J2 = "b0000000-0000-4000-8000-000000000002";

function job(id: string, number: string, state: JobState, extra: Partial<KanbanJob> = {}): KanbanJob {
  return {
    id,
    kanbanId: K1,
    cardNumber: number,
    ruleId: RULE,
    ruleCode: "MIX-A",
    itemId: ITEM,
    ruleType: "manufacturing",
    cardState: state === "in-progress" ? "in-progress" : "prepared",
    cardQty: "25",
    due: null,
    activityId: "ac000000-0000-4000-8000-000000000001",
    activityName: "Mix",
    activityKind: "process",
    sequence: 1,
    workCellResourceId: CELL,
    pickingRequired: false,
    state,
    startedAt: null,
    completedAt: null,
    reportedQty: null,
    scrapQty: null,
    handlingUnits: null,
    version: 2,
    updatedAt: "2026-10-26T08:00:00Z",
    ...extra,
  };
}

function jobBoardOf(jobs: KanbanJob[]): JobBoard {
  const states: JobState[] = ["not-planned", "planned", "prepared", "in-progress", "completed"];
  return { workCellId: CELL, columns: states.map((state) => ({ state, jobs: jobs.filter((j) => j.state === state) })), truncated: false };
}

describe("slice 4: kanban jobs", () => {
  it("checks a completion report and finds a flow's work cells", () => {
    expect(validateCompletion({ reportedQty: "19.5", scrapQty: "0", handlingUnits: "2" })).toEqual({});
    expect(validateCompletion({ reportedQty: "0", scrapQty: "2", handlingUnits: "1" })).toEqual({});
    expect(validateCompletion({ reportedQty: "0", scrapQty: "0", handlingUnits: "1" }).reportedQty).toBeTruthy();
    expect(validateCompletion({ reportedQty: "-1", scrapQty: "0", handlingUnits: "1" }).reportedQty).toBeTruthy();
    expect(validateCompletion({ reportedQty: "1", scrapQty: "0.0000001", handlingUnits: "1" }).scrapQty).toBeTruthy();
    expect(validateCompletion({ reportedQty: "1", scrapQty: "0", handlingUnits: "0" }).handlingUnits).toBeTruthy();
    const cells = workCells({
      ...flow,
      activities: flow.activities!.map((a, i) => ({ ...a, workCellResourceId: i === 0 ? CELL : null })),
    });
    expect(cells).toEqual([{ id: CELL, label: "Mix (ce000000)" }]);
  });

  it("asks for a work cell before loading the board", async () => {
    handler = (url) => (url.pathname === "/api/v1/lean/flows" ? json(200, { items: [flow] }) : undefined);
    const { container } = renderAt("/lean/work-cell");
    expect(await screen.findByText("Choose a work cell")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Work cell id"), { target: { value: "cell-7" } });
    fireEvent.click(screen.getByRole("button", { name: "Show board" }));
    expect(await screen.findByText("Enter the work cell's resource id (a UUID).")).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname === "/api/v1/lean/board")).toBe(false);
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });

  it("starts a job with its version and shows the card running (BR-24.6)", async () => {
    let jobs = [job(J1, "MIX-A-0001", "prepared", { due: "2020-01-01T00:00:00Z" })];
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [] });
      if (url.pathname === "/api/v1/lean/board") return json(200, jobBoardOf(jobs));
      if (url.pathname === `/api/v1/lean/kanban-jobs/${J1}:start` && init.method === "POST") {
        jobs = [{ ...jobs[0], state: "in-progress", cardState: "in-progress", version: 3 }];
        return json(200, jobs[0]);
      }
      return undefined;
    };
    const { container } = renderAt(`/lean/work-cell?workCellId=${CELL}`);
    const prepared = await screen.findByRole("list", { name: "Prepared jobs" });
    expect(within(prepared).getByText("MIX-A-0001")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Work-cell metrics" })).toHaveTextContent("Overdue 1");
    expect(calls.find((c) => c.url.pathname === "/api/v1/lean/board")?.url.searchParams.get("workCellId")).toBe(CELL);
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
    fireEvent.click(screen.getByRole("button", { name: "Start MIX-A-0001 · 1 Mix" }));
    expect(await screen.findByText("MIX-A-0001 · 1 Mix started. MIX-A-0001 is in progress.")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ version: 2 });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    await waitFor(() => expect(within(screen.getByRole("list", { name: "In progress jobs" })).getByText("MIX-A-0001")).toBeInTheDocument());
  });

  it("completes a job with quantity, scrap and handling units", async () => {
    let jobs = [job(J1, "MIX-A-0001", "in-progress")];
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [] });
      if (url.pathname === "/api/v1/lean/board") return json(200, jobBoardOf(jobs));
      if (url.pathname === `/api/v1/lean/kanban-jobs/${J1}:complete` && init.method === "POST") {
        jobs = [{ ...jobs[0], state: "completed", cardState: "completed", reportedQty: "24", scrapQty: "1", handlingUnits: 2, version: 3 }];
        return json(200, jobs[0]);
      }
      return undefined;
    };
    renderAt(`/lean/work-cell?workCellId=${CELL}`);
    fireEvent.click(await screen.findByRole("button", { name: "Complete MIX-A-0001 · 1 Mix" }));
    const form = screen.getByRole("form", { name: "Report MIX-A-0001 · 1 Mix" });
    expect(within(form).getByLabelText("Good quantity")).toHaveValue("25");
    fireEvent.change(within(form).getByLabelText("Handling units"), { target: { value: "0" } });
    fireEvent.click(within(form).getByRole("button", { name: "Report completion" }));
    expect(await within(form).findByText("Enter 1 to 100000 handling units.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(within(form).getByLabelText("Good quantity"), { target: { value: "24" } });
    fireEvent.change(within(form).getByLabelText("Scrap"), { target: { value: "1" } });
    fireEvent.change(within(form).getByLabelText("Handling units"), { target: { value: "2" } });
    fireEvent.click(within(form).getByRole("button", { name: "Report completion" }));
    expect(await screen.findByText("MIX-A-0001 · 1 Mix completed. MIX-A-0001 is completed.")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ version: 2, reportedQty: "24", scrapQty: "1", handlingUnits: 2 });
    const completed = await screen.findByRole("list", { name: "Completed jobs" });
    await waitFor(() => expect(within(completed).getByText("24")).toBeInTheDocument());
  });

  it("keeps a job in its column when the service refuses it (AC-24.7)", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [] });
      if (url.pathname === "/api/v1/lean/board") return json(200, jobBoardOf([job(J2, "MIX-A-0002", "planned", { pickingRequired: true, cardState: "planned" })]));
      if (init.method === "POST") return json(409, { code: "LEAN-0001", detail: "the activity needs picking: move the kanban to prepared before starting the job" });
      return undefined;
    };
    renderAt(`/lean/work-cell?workCellId=${CELL}`);
    const planned = await screen.findByRole("list", { name: "Planned jobs" });
    expect(within(planned).getByText("Needs picking: move the card to Prepared before starting.")).toBeInTheDocument();
    fireEvent.click(within(planned).getByRole("button", { name: "Start MIX-A-0002 · 1 Mix" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("MIX-A-0002 · 1 Mix stays in Planned");
    expect(alert).toHaveTextContent("LEAN-0001");
    expect(within(screen.getByRole("list", { name: "Planned jobs" })).getByText("MIX-A-0002")).toBeInTheDocument();
  });

  it("shows the empty and failed work-cell boards", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [] });
      if (url.pathname === "/api/v1/lean/board") return json(200, jobBoardOf([]));
      return undefined;
    };
    const { unmount } = renderAt(`/lean/work-cell?workCellId=${CELL}`);
    expect(await screen.findByText("No jobs at this work cell")).toBeInTheDocument();
    unmount();
    handler = (url) => (url.pathname === "/api/v1/lean/board" ? json(503, { code: "COM-0500", detail: "service unavailable" }) : json(200, { items: [] }));
    renderAt(`/lean/work-cell?workCellId=${CELL}`);
    expect(await screen.findByText("service unavailable")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("offers the work cells of a chosen flow", async () => {
    const cellFlow: Flow = { ...flow, activities: flow.activities!.map((a) => ({ ...a, workCellResourceId: CELL })) };
    handler = (url) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [cellFlow] });
      if (url.pathname === `/api/v1/lean/flows/${FLOW}`) return json(200, cellFlow);
      if (url.pathname === "/api/v1/lean/board") return json(200, jobBoardOf([]));
      return undefined;
    };
    renderAt(`/lean/work-cell?flowId=${FLOW}`);
    const pick = await screen.findByLabelText("Work cell of the flow");
    expect(within(pick).getByRole("option", { name: "Mix, Fill (ce000000)" })).toBeInTheDocument();
    fireEvent.change(pick, { target: { value: CELL } });
    await waitFor(() => {
      const load = calls.find((c) => c.url.pathname === "/api/v1/lean/board");
      expect(load?.url.searchParams.get("workCellId")).toBe(CELL);
      expect(load?.url.searchParams.get("flowId")).toBe(FLOW);
    });
  });

  it("sends a card with jobs to its jobs instead of moving it to In progress", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/lean/flows" || url.pathname === "/api/v1/lean/kanban-rules") return json(200, { items: [] });
      if (url.pathname === "/api/v1/lean/board") return json(200, boardOf([card(K1, "MIX-A-0001", "prepared", { jobCount: 2 })]));
      return undefined;
    };
    renderAt("/lean/board");
    const prepared = await screen.findByRole("list", { name: "Prepared cards" });
    expect(within(prepared).getByText(/2 jobs: the card moves on as they are started and completed/)).toBeInTheDocument();
    expect(within(prepared).queryByRole("button", { name: "Move MIX-A-0001 to In progress" })).toBeNull();
    const picker = within(prepared).getByLabelText("Move MIX-A-0001 to column");
    expect(within(picker).queryByRole("option", { name: "In progress" })).toBeNull();
    expect(within(picker).queryByRole("option", { name: "Completed" })).toBeNull();
    expect(within(prepared).getByRole("link", { name: "MIX-A-0001" })).toHaveAttribute("href", `/lean/kanbans/${K1}`);
  });

  it("shows a kanban with its jobs", async () => {
    const detail = card(K1, "MIX-A-0001", "completed", {
      handlingUnits: 2,
      jobCount: 2,
      jobs: [
        job(J1, "MIX-A-0001", "completed", { reportedQty: "25", scrapQty: "0", handlingUnits: 1, completedAt: "2026-10-26T09:00:00Z" }),
        job(J2, "MIX-A-0001", "completed", { sequence: 2, activityName: "Fill", workCellResourceId: null, reportedQty: "24", scrapQty: "1", handlingUnits: 2 }),
      ],
    });
    handler = (url) => (url.pathname === `/api/v1/lean/kanbans/${K1}` ? json(200, detail) : undefined);
    const { container } = renderAt(`/lean/kanbans/${K1}`);
    expect(await screen.findByRole("heading", { name: "MIX-A-0001" })).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Kanban facts" })).toHaveTextContent("Handling units 2");
    const table = screen.getByRole("table", { name: "Kanban jobs" });
    expect(within(table).getByText("Fill")).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: "Open the work-cell board of Mix" })).toHaveAttribute("href", `/lean/work-cell?workCellId=${CELL}`);
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });

  it("shows a card without jobs and a kanban that cannot load", async () => {
    handler = (url) => (url.pathname === `/api/v1/lean/kanbans/${K1}` ? json(200, card(K1, "MIX-A-0001", "planned", { jobs: [] })) : undefined);
    const { unmount } = renderAt(`/lean/kanbans/${K1}`);
    expect(await screen.findByText("No jobs")).toBeInTheDocument();
    unmount();
    handler = () => json(404, { code: "LEAN-0103", detail: "kanban not found" });
    renderAt(`/lean/kanbans/${K2}`);
    expect(await screen.findByText("kanban not found")).toBeInTheDocument();
  });
});

describe("slice 5: flow costing", () => {
  const P1 = "e0000000-0000-4000-8000-000000000001";
  const P2 = "e0000000-0000-4000-8000-000000000002";
  const periodsPath = `/api/v1/lean/flows/${FLOW}/costing-periods`;
  const open: CostingPeriod = {
    id: P1,
    flowId: FLOW,
    periodStart: "2026-09-01",
    periodEnd: "2026-09-30",
    currency: "USD",
    status: "open",
    materialCost: null,
    conversionCost: null,
    outputValue: null,
    variance: null,
    kanbans: null,
    journalEntryId: null,
    settledAt: null,
    settledBy: null,
    version: 1,
    createdAt: "2026-10-01T08:00:00Z",
    updatedAt: "2026-10-01T08:00:00Z",
  };
  const settled: CostingPeriod = {
    ...open,
    id: P2,
    periodStart: "2026-08-01",
    periodEnd: "2026-08-31",
    status: "settled",
    materialCost: "900.10",
    conversionCost: "300.00",
    outputValue: "1150.25",
    variance: "49.85",
    kanbans: 10,
    settledAt: "2026-09-02T09:00:00Z",
    settledBy: "cost-1",
    version: 2,
  };
  const flowList = () => json(200, { items: [{ ...flow, status: "active", activities: undefined }] });
  const flowDetail = (url: URL) => (url.pathname === `/api/v1/lean/flows/${FLOW}` ? json(200, flow) : undefined);

  it("computes the variance exactly and checks a period before opening it", () => {
    expect(variance("900.10", "300", "1150.25")).toBe("49.85");
    expect(variance("0.1", "0.2", "0.3")).toBe("0");
    expect(variance("100", "50", "200.005")).toBe("-50.005");
    expect(variance("1", "-1", "0")).toBeNull();
    expect(validatePeriod({ periodStart: "2026-09-30", periodEnd: "2026-09-01", currency: "USD" }).periodEnd).toBe("The last day cannot be before the first day.");
    expect(validatePeriod({ periodStart: "", periodEnd: "2026-09-01", currency: "us" })).toMatchObject({ periodStart: expect.any(String), currency: expect.any(String) });
    expect(validatePeriod({ periodStart: "2026-09-01", periodEnd: "2026-09-30", currency: "EUR" })).toEqual({});
  });

  it("asks for a flow, then lists its periods and shows a settled one", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/lean/flows") return flowList();
      if (url.pathname === periodsPath) return json(200, { items: [open, settled] });
      return flowDetail(url);
    };
    const { container } = renderAt("/lean/costing");
    expect(await screen.findByText("Choose a production flow")).toBeInTheDocument();
    await screen.findByRole("option", { name: "Sauce line" });
    fireEvent.change(screen.getByLabelText("Production flow"), { target: { value: FLOW } });
    const table = await findTable({ name: "Costing periods" });
    expect(within(table).getByText("49.85 USD")).toBeInTheDocument();
    expect(within(table).getByText("10")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "2026-08-01 to 2026-08-31" }));
    expect(screen.getByRole("list", { name: "Settlement" })).toHaveTextContent("Variance 49.85 USD");
    expect(screen.getByText(/journal entry pending the ledger integration/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Settle period" })).toBeNull();
    // A settled period can still be backflushed, but nothing is carried into a settlement.
    expect(await screen.findByLabelText("1. Mix")).toBeInTheDocument();
  });

  it("shows the empty and failed period lists", async () => {
    let fail = false;
    handler = (url) => {
      if (url.pathname === "/api/v1/lean/flows") return flowList();
      if (url.pathname === periodsPath) return fail ? json(404, { code: "LEAN-0101", detail: "production flow not found" }) : json(200, { items: [] });
      return undefined;
    };
    renderAt(`/lean/costing?flowId=${FLOW}`);
    expect(await screen.findByText("No costing periods yet")).toBeInTheDocument();
    fail = true;
    cleanupAndRender(`/lean/costing?flowId=${FLOW}`);
    expect(await findAlert()).toHaveTextContent("production flow not found");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No costing periods yet")).toBeInTheDocument();
  });

  it("opens a period with a key and checks the dates first", async () => {
    let periods: CostingPeriod[] = [];
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows") return flowList();
      if (url.pathname === periodsPath && init.method === "POST") {
        periods = [open];
        return json(201, open);
      }
      if (url.pathname === periodsPath) return json(200, { items: periods });
      return undefined;
    };
    renderAt(`/lean/costing?flowId=${FLOW}`);
    await screen.findByText("No costing periods yet");
    fireEvent.change(screen.getByLabelText("First day"), { target: { value: "2026-09-30" } });
    fireEvent.change(screen.getByLabelText("Last day"), { target: { value: "2026-09-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Open period" }));
    expect(await screen.findByText("The last day cannot be before the first day.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("First day"), { target: { value: "2026-09-01" } });
    fireEvent.change(screen.getByLabelText("Last day"), { target: { value: "2026-09-30" } });
    fireEvent.click(screen.getByRole("button", { name: "Open period" }));
    expect(await findStatus()).toHaveTextContent("Opened 2026-09-01 to 2026-09-30.");
    const post = calls.find((c) => c.method === "POST")!;
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({ periodStart: "2026-09-01", periodEnd: "2026-09-30", currency: "USD" });
    expect(await screen.findByRole("button", { name: "2026-09-01 to 2026-09-30" })).toBeInTheDocument();
  });

  it("settles a period with its version, shows LEAN-0003 and then the variance (BR-24.9, AC-24.8)", async () => {
    let refuse = true;
    let periods: CostingPeriod[] = [open];
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows") return flowList();
      if (url.pathname === `${periodsPath}/${P1}:settle` && init.method === "POST") {
        if (refuse) return json(409, { code: "LEAN-0003", detail: "period cannot be settled: 1 kanbans open (MIX-A-0002)" });
        const done = { ...settled, id: P1, periodStart: "2026-09-01", periodEnd: "2026-09-30" };
        periods = [done];
        return json(200, done);
      }
      if (url.pathname === periodsPath) return json(200, { items: periods });
      return flowDetail(url);
    };
    const { container } = renderAt(`/lean/costing?flowId=${FLOW}`);
    fireEvent.click(await screen.findByRole("button", { name: "2026-09-01 to 2026-09-30" }));
    await screen.findByLabelText("1. Mix");
    fireEvent.change(screen.getByLabelText("Material cost"), { target: { value: "900.10" } });
    fireEvent.change(screen.getByLabelText("Conversion cost"), { target: { value: "300" } });
    fireEvent.click(screen.getByRole("button", { name: "Settle period" }));
    expect(await screen.findByText("Enter an amount of zero or more in USD.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Output value"), { target: { value: "1150.25" } });
    expect(screen.getByText("Variance 49.85 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Settle period" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Cards with jobs in this period are still open");
    expect(alert).toHaveTextContent("1 kanbans open (MIX-A-0002)");
    const first = calls.filter((c) => c.method === "POST").at(-1)!;
    expect(first.body).toEqual({ version: 1, materialCost: "900.10", conversionCost: "300", outputValue: "1150.25" });
    refuse = false;
    fireEvent.click(screen.getByRole("button", { name: "Settle period" }));
    expect(await findStatus()).toHaveTextContent("Settled 2026-09-01 to 2026-09-30 with a variance of 49.85 USD.");
    const second = calls.filter((c) => c.method === "POST").at(-1)!;
    expect(header(second, "Idempotency-Key")).not.toBe(header(first, "Idempotency-Key"));
    expect(await screen.findByRole("list", { name: "Settlement" })).toHaveTextContent("Cards 10");
  });
});

describe("slice 6: rule analysis, backflush and export", () => {
  const P1 = "e0000000-0000-4000-8000-000000000001";
  const MIX = "ac000000-0000-4000-8000-000000000001";
  const FILL = "ac000000-0000-4000-8000-000000000002";
  const analysisPath = `/api/v1/lean/kanban-rules/${RULE}/analysis`;
  const periodsPath = `/api/v1/lean/flows/${FLOW}/costing-periods`;
  const analysis: RuleAnalysis = {
    ruleId: RULE,
    ruleCode: "MIX-A",
    from: "2026-09-27",
    to: "2026-10-26",
    cycles: 12,
    timedCycles: 10,
    avgLeadTimeH: "8.50",
    targetLeadTimeH: 8,
    dueCycles: 12,
    lateCycles: 3,
    latePct: "25.00",
    activeKanbans: 3,
    maxKanbans: 3,
    overdueOpen: 1,
  };
  const open: CostingPeriod = {
    id: P1,
    flowId: FLOW,
    periodStart: "2026-09-01",
    periodEnd: "2026-09-30",
    currency: "USD",
    status: "open",
    materialCost: null,
    conversionCost: null,
    outputValue: null,
    variance: null,
    kanbans: null,
    journalEntryId: null,
    settledAt: null,
    settledBy: null,
    version: 1,
    createdAt: "2026-10-01T08:00:00Z",
    updatedAt: "2026-10-01T08:00:00Z",
  };
  const preview: BackflushPreview = {
    periodId: P1,
    flowId: FLOW,
    periodStart: "2026-09-01",
    periodEnd: "2026-09-30",
    currency: "USD",
    status: "open",
    conversionCost: "415.07",
    activities: [
      { activityId: MIX, sequence: 1, name: "Mix", kind: "process", cycleTimeS: 45, jobs: 10, processedQty: "250", hours: "3.1250", ratePerHour: "90", cost: "281.25" },
      { activityId: FILL, sequence: 2, name: "Fill", kind: "process", cycleTimeS: 30, jobs: 9, processedQty: "221.5", hours: "1.8458", ratePerHour: "72.5", cost: "133.82" },
    ],
  };

  function ruleHandler(reply: () => Reply): Handler {
    return (url) => {
      if (url.pathname === `/api/v1/lean/kanban-rules/${RULE}`) return json(200, rule);
      if (url.pathname === "/api/v1/lean/board") return json(200, boardOf([]));
      if (url.pathname === analysisPath) return reply();
      return undefined;
    };
  }

  it("checks an analysis period like the service", () => {
    expect(shiftDays("2026-10-26", -29)).toBe("2026-09-27");
    expect(shiftDays("2024-02-28", 1)).toBe("2024-02-29");
    expect(validateAnalysisPeriod({ from: "2026-09-01", to: "2026-09-30" })).toEqual({});
    expect(validateAnalysisPeriod({ from: "2026-09-30", to: "2026-09-01" }).to).toBe("The last day cannot be before the first day.");
    // 366 days is the most; a leap year fits, one more day does not.
    expect(validateAnalysisPeriod({ from: "2024-01-01", to: "2024-12-31" })).toEqual({});
    expect(validateAnalysisPeriod({ from: "2025-08-31", to: "2026-09-01" }).from).toBe("Choose a period of at most 366 days.");
    expect(validateAnalysisPeriod({ from: "", to: "x" })).toMatchObject({ from: expect.any(String), to: expect.any(String) });
  });

  it("shows a rule's analysis for the last 30 days and for a chosen period (PRD 4.24.14)", async () => {
    handler = ruleHandler(() => json(200, analysis));
    const { container } = renderAt(`/lean/rules/${RULE}`);
    const tiles = await screen.findByRole("list", { name: "Rule analysis" });
    expect(tiles).toHaveTextContent("Cycles 12");
    expect(tiles).toHaveTextContent("Average lead time 8.50 h");
    expect(tiles).toHaveTextContent("Target 8 h");
    expect(tiles).toHaveTextContent("Late 25.00 %");
    expect(tiles).toHaveTextContent("3 of 12 with a due time");
    expect(tiles).toHaveTextContent("Active cards 3 of 3");
    expect(tiles).toHaveTextContent("Overdue now 1");
    expect(screen.getByText(/2 older cycles have no planning time/)).toBeInTheDocument();
    const first = calls.find((c) => c.url.pathname === analysisPath)!;
    const to = first.url.searchParams.get("to")!;
    expect(to).toBe(new Date().toISOString().slice(0, 10));
    expect(first.url.searchParams.get("from")).toBe(shiftDays(to, -29));
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("First day"), { target: { value: "2026-09-30" } });
    fireEvent.change(screen.getByLabelText("Last day"), { target: { value: "2026-09-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Analyse" }));
    expect(await screen.findByText("The last day cannot be before the first day.")).toBeInTheDocument();
    expect(calls.filter((c) => c.url.pathname === analysisPath)).toHaveLength(1);
    fireEvent.change(screen.getByLabelText("First day"), { target: { value: "2026-09-01" } });
    fireEvent.change(screen.getByLabelText("Last day"), { target: { value: "2026-09-30" } });
    fireEvent.click(screen.getByRole("button", { name: "Analyse" }));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname === analysisPath)).toHaveLength(2));
    const second = calls.filter((c) => c.url.pathname === analysisPath)[1];
    expect(second.url.searchParams.get("from")).toBe("2026-09-01");
    expect(second.url.searchParams.get("to")).toBe("2026-09-30");
  });

  it("shows an analysis without cycles and one that cannot load", async () => {
    let fail = false;
    handler = ruleHandler(() =>
      fail
        ? json(400, { code: "COM-0001", detail: "request failed validation" })
        : json(200, { ...analysis, cycles: 0, timedCycles: 0, avgLeadTimeH: null, dueCycles: 0, lateCycles: 0, latePct: null, maxKanbans: null, overdueOpen: 0 }),
    );
    renderAt(`/lean/rules/${RULE}`);
    expect(await screen.findByText("No completed cycles in this period")).toBeInTheDocument();
    const tiles = screen.getByRole("list", { name: "Rule analysis" });
    expect(tiles).toHaveTextContent("Average lead time —");
    expect(tiles).toHaveTextContent("Late —");
    expect(tiles).toHaveTextContent("Active cards 3");
    fail = true;
    cleanupAndRender(`/lean/rules/${RULE}`);
    expect(await findAlert()).toHaveTextContent("request failed validation");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No completed cycles in this period")).toBeInTheDocument();
  });

  it("previews a period's backflush and carries it into the settlement (FR-24.12)", async () => {
    let refuse = true;
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [{ ...flow, activities: undefined }] });
      if (url.pathname === `/api/v1/lean/flows/${FLOW}`) return json(200, flow);
      if (url.pathname === periodsPath) return json(200, { items: [open] });
      if (url.pathname === `${periodsPath}/${P1}:backflush` && init.method === "POST") {
        return refuse ? json(400, { code: "COM-0001", detail: "activity Fill completed 9 jobs in the period and needs a cost rate" }) : json(200, preview);
      }
      return undefined;
    };
    const { container } = renderAt(`/lean/costing?flowId=${FLOW}`);
    fireEvent.click(await screen.findByRole("button", { name: "2026-09-01 to 2026-09-30" }));
    const mix = await screen.findByLabelText("1. Mix");
    fireEvent.change(mix, { target: { value: "ninety" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview backflush" }));
    expect(await screen.findByText("Enter a rate of zero or more in USD, with at most six decimals.")).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname.endsWith(":backflush"))).toBe(false);

    fireEvent.change(mix, { target: { value: "90" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview backflush" }));
    expect(await findAlert()).toHaveTextContent("Fill completed 9 jobs in the period and needs a cost rate");
    const sent = calls.find((c) => c.url.pathname.endsWith(":backflush"))!;
    // A read-only preview: only the rates given, and no Idempotency-Key.
    expect(sent.body).toEqual({ rates: [{ activityId: MIX, ratePerHour: "90" }] });
    expect(header(sent, "Idempotency-Key")).toBeNull();

    refuse = false;
    fireEvent.change(screen.getByLabelText("2. Fill"), { target: { value: "72.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview backflush" }));
    const table = await findTable({ name: "Conversion by activity" });
    expect(within(table).getByText("281.25 USD")).toBeInTheDocument();
    expect(within(table).getByText("1.8458")).toBeInTheDocument();
    expect(screen.getByText("Conversion cost 415.07 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Use as conversion cost" }));
    expect(screen.getByLabelText("Conversion cost")).toHaveValue("415.07");
  });

  it("exports the periods as CSV and shows a failed export", async () => {
    let fail = false;
    const csv = "period_start,period_end,status\n2026-09-01,2026-09-30,open\n";
    handler = (url) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [{ ...flow, activities: undefined }] });
      if (url.pathname === periodsPath) return json(200, { items: [open] });
      if (url.pathname === `${periodsPath}:export`) return fail ? json(403, { code: "AUTH-0003", detail: "missing duty lean.read" }) : { status: 200, body: null, text: csv };
      return undefined;
    };
    renderAt(`/lean/costing?flowId=${FLOW}`);
    await findTable({ name: "Costing periods" });
    fireEvent.click(screen.getByRole("button", { name: "Export periods" }));
    const link = await screen.findByRole("link", { name: "Download CSV" });
    expect(link).toHaveAttribute("href", `data:text/csv;charset=utf-8,${encodeURIComponent(csv)}`);
    expect(link).toHaveAttribute("download", "flow-costing-periods.csv");
    const sent = calls.find((c) => c.url.pathname.endsWith(":export"))!;
    expect(header(sent, "Accept")).toBe("text/csv");
    fail = true;
    fireEvent.click(screen.getByRole("button", { name: "Export periods" }));
    expect(await findAlert()).toHaveTextContent("missing duty lean.read");
    expect(screen.queryByRole("link", { name: "Download CSV" })).toBeNull();
  });
});

function cleanupAndRender(path: string) {
  cleanup();
  return renderAt(path);
}

describe("slice 7: retrying board commands", () => {
  function keysOf(path: string): (string | null)[] {
    return calls.filter((c) => c.method === "POST" && c.url.pathname === path).map((c) => header(c, "Idempotency-Key"));
  }

  it("keeps a command's key only while its outcome is unknown", () => {
    expect(answered(new LeanApiError(409, "LEAN-0001", "refused"))).toBe(true);
    expect(answered(new LeanApiError(422, "idempotency_key_reuse", "reused"))).toBe(true);
    expect(answered(new LeanApiError(503, "HTTP-503", "unavailable"))).toBe(false);
    expect(answered(new TypeError("Failed to fetch"))).toBe(false);
  });

  it("retries a start whose answer was lost with the same key, and the service replays it", async () => {
    let jobs = [job(J1, "MIX-A-0001", "prepared")];
    let posts = 0;
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [] });
      if (url.pathname === "/api/v1/lean/board") return json(200, jobBoardOf(jobs));
      if (url.pathname === `/api/v1/lean/kanban-jobs/${J1}:start` && init.method === "POST") {
        posts += 1;
        if (posts === 1) throw new TypeError("Failed to fetch");
        jobs = [{ ...jobs[0], state: "in-progress", cardState: "in-progress", version: 3 }];
        return json(200, jobs[0]);
      }
      return undefined;
    };
    renderAt(`/lean/work-cell?workCellId=${CELL}`);
    fireEvent.click(await screen.findByRole("button", { name: "Start MIX-A-0001 · 1 Mix" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("MIX-A-0001 · 1 Mix stays in Prepared");
    expect(alert).toHaveTextContent("Failed to fetch");
    await waitFor(() => expect(screen.getByRole("button", { name: "Start MIX-A-0001 · 1 Mix" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Start MIX-A-0001 · 1 Mix" }));
    expect(await screen.findByText("MIX-A-0001 · 1 Mix started. MIX-A-0001 is in progress.")).toBeInTheDocument();
    const [first, second] = keysOf(`/api/v1/lean/kanban-jobs/${J1}:start`);
    expect(first).toBeTruthy();
    expect(second).toBe(first);
  });

  it("sends a new key once the service has refused a start", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [] });
      if (url.pathname === "/api/v1/lean/board") return json(200, jobBoardOf([job(J2, "MIX-A-0002", "planned", { pickingRequired: true, cardState: "planned" })]));
      if (init.method === "POST") return json(409, { code: "LEAN-0001", detail: "the activity needs picking" });
      return undefined;
    };
    renderAt(`/lean/work-cell?workCellId=${CELL}`);
    fireEvent.click(await screen.findByRole("button", { name: "Start MIX-A-0002 · 1 Mix" }));
    expect(await findAlert()).toHaveTextContent("LEAN-0001");
    await waitFor(() => expect(screen.getByRole("button", { name: "Start MIX-A-0002 · 1 Mix" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Start MIX-A-0002 · 1 Mix" }));
    await waitFor(() => expect(keysOf(`/api/v1/lean/kanban-jobs/${J2}:start`)).toHaveLength(2));
    const [first, second] = keysOf(`/api/v1/lean/kanban-jobs/${J2}:start`);
    expect(second).not.toBe(first);
  });

  it("keeps the completion key for the same report and changes it when the report changes", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows") return json(200, { items: [] });
      if (url.pathname === "/api/v1/lean/board") return json(200, jobBoardOf([job(J1, "MIX-A-0001", "in-progress")]));
      if (init.method === "POST") throw new TypeError("Failed to fetch");
      return undefined;
    };
    renderAt(`/lean/work-cell?workCellId=${CELL}`);
    fireEvent.click(await screen.findByRole("button", { name: "Complete MIX-A-0001 · 1 Mix" }));
    const form = screen.getByRole("form", { name: "Report MIX-A-0001 · 1 Mix" });
    const submit = within(form).getByRole("button", { name: "Report completion" });
    fireEvent.click(submit);
    await waitFor(() => expect(keysOf(`/api/v1/lean/kanban-jobs/${J1}:complete`)).toHaveLength(1));
    await waitFor(() => expect(submit).toBeEnabled());
    fireEvent.click(submit);
    await waitFor(() => expect(keysOf(`/api/v1/lean/kanban-jobs/${J1}:complete`)).toHaveLength(2));
    await waitFor(() => expect(submit).toBeEnabled());
    fireEvent.change(within(form).getByLabelText("Scrap"), { target: { value: "1" } });
    fireEvent.click(submit);
    await waitFor(() => expect(keysOf(`/api/v1/lean/kanban-jobs/${J1}:complete`)).toHaveLength(3));
    const [first, retry, changed] = keysOf(`/api/v1/lean/kanban-jobs/${J1}:complete`);
    expect(retry).toBe(first);
    expect(changed).not.toBe(first);
    expect(calls.filter((c) => c.method === "POST").map((c) => (c.body as { scrapQty: string }).scrapQty)).toEqual(["0", "0", "1"]);
  });

  it("retries a card move after a 503 with the same key", async () => {
    let cards = [card(K1, "MIX-A-0001", "planned")];
    let posts = 0;
    handler = (url, init) => {
      if (url.pathname === "/api/v1/lean/flows" || url.pathname === "/api/v1/lean/kanban-rules") return json(200, { items: [] });
      if (url.pathname === "/api/v1/lean/board") return json(200, boardOf(cards));
      if (url.pathname === `/api/v1/lean/kanbans/${K1}:move` && init.method === "POST") {
        posts += 1;
        if (posts === 1) return json(503, { code: "HTTP-503", detail: "upstream unavailable" });
        cards = [{ ...cards[0], state: "prepared", version: 3 }];
        return json(200, cards[0]);
      }
      return undefined;
    };
    renderAt("/lean/board");
    fireEvent.click(await screen.findByRole("button", { name: "Move MIX-A-0001 to Prepared" }));
    expect(await findAlert()).toHaveTextContent("MIX-A-0001 stays in Planned");
    await waitFor(() => expect(screen.getByRole("button", { name: "Move MIX-A-0001 to Prepared" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Move MIX-A-0001 to Prepared" }));
    expect(await screen.findByText("MIX-A-0001 moved to Prepared.")).toBeInTheDocument();
    const [first, second] = keysOf(`/api/v1/lean/kanbans/${K1}:move`);
    expect(first).toBeTruthy();
    expect(second).toBe(first);
  });
});

describe("slice 8: fixed-kanban count check", () => {
  const countPath = `/api/v1/lean/kanban-rules/${RULE}/count-check`;
  const reported: CountCheck = {
    ruleId: RULE,
    ruleCode: "MIX-A",
    dailyDemand: "10",
    safetyPct: "10",
    leadTimeH: 8,
    fixedQty: "25",
    configured: 3,
    suggested: 1,
    deviationPct: "66.67",
    limitPct: 25,
    reportable: true,
  };

  function countHandler(reply: (url: URL) => Reply, shown: Rule = rule): Handler {
    return (url) => {
      if (url.pathname === `/api/v1/lean/kanban-rules/${shown.id}`) return json(200, shown);
      if (url.pathname === "/api/v1/lean/board") return json(200, boardOf([]));
      if (url.pathname === countPath) return reply(url);
      return quietAnalysis(url);
    };
  }

  it("checks the inputs like the service", () => {
    expect(validateCountCheck({ dailyDemand: "12.5", safetyPct: "" })).toEqual({});
    expect(validateCountCheck({ dailyDemand: "0", safetyPct: "999.5" })).toEqual({});
    expect(validateCountCheck({ dailyDemand: "", safetyPct: "" }).dailyDemand).toBeDefined();
    expect(validateCountCheck({ dailyDemand: "-1", safetyPct: "" }).dailyDemand).toBeDefined();
    expect(validateCountCheck({ dailyDemand: "1.1234567", safetyPct: "" }).dailyDemand).toBeDefined();
    expect(validateCountCheck({ dailyDemand: "1", safetyPct: "1000" }).safetyPct).toBeDefined();
    expect(validateCountCheck({ dailyDemand: "1", safetyPct: "abc" }).safetyPct).toBeDefined();
  });

  it("reports a fixed rule whose count is more than 25 % off the suggestion (PRD 4.24.16)", async () => {
    handler = countHandler(() => json(200, reported));
    const { container } = renderAt(`/lean/rules/${RULE}`);
    expect(await screen.findByText(/Enter the expected daily demand/)).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Daily demand"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Check count" }));
    expect(await screen.findByText(/Enter the daily demand: 0 or more/)).toBeInTheDocument();
    expect(calls.filter((c) => c.url.pathname === countPath)).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Daily demand"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText(/Safety/), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Check count" }));
    const tiles = await screen.findByRole("list", { name: "Kanban count check" });
    expect(tiles).toHaveTextContent("Configured cards 3");
    expect(tiles).toHaveTextContent("Suggested cards 1");
    expect(tiles).toHaveTextContent("Deviation 66.67 %");
    expect(screen.getByRole("status")).toHaveTextContent("3 cards are 66.67 % away from the 1 suggested");
    const sent = calls.find((c) => c.url.pathname === countPath)!;
    expect(sent.method).toBe("GET");
    expect(sent.url.searchParams.get("dailyDemand")).toBe("10");
    expect(sent.url.searchParams.get("safetyPct")).toBe("10");
    expect(header(sent, "Idempotency-Key")).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when the count is within the limit and leaves safety out when empty", async () => {
    handler = countHandler(() => json(200, { ...reported, safetyPct: "0", suggested: 3, deviationPct: "0.00", reportable: false }));
    renderAt(`/lean/rules/${RULE}`);
    fireEvent.change(await screen.findByLabelText("Daily demand"), { target: { value: "40" } });
    fireEvent.click(screen.getByRole("button", { name: "Check count" }));
    expect(await findStatus()).toHaveTextContent("within 25 % of the 3 suggested");
    const sent = calls.find((c) => c.url.pathname === countPath)!;
    expect(sent.url.searchParams.has("safetyPct")).toBe(false);
  });

  it("shows a refused check and recovers on the next one", async () => {
    let refuse = true;
    handler = countHandler(() =>
      refuse ? json(422, { code: "LEAN-0113", detail: "kanban rule MIX-A is not a fixed rule" }) : json(200, reported),
    );
    renderAt(`/lean/rules/${RULE}`);
    fireEvent.change(await screen.findByLabelText("Daily demand"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Check count" }));
    expect(await findAlert()).toHaveTextContent("kanban rule MIX-A is not a fixed rule");
    expect(screen.queryByRole("list", { name: "Kanban count check" })).toBeNull();
    refuse = false;
    fireEvent.click(screen.getByRole("button", { name: "Check count" }));
    expect(await screen.findByRole("list", { name: "Kanban count check" })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("offers no count check on an event rule", async () => {
    handler = countHandler(() => json(200, reported), eventRule);
    renderAt(`/lean/rules/${EVENT_RULE}`);
    expect(await screen.findByRole("heading", { name: "FILL-EV" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Check count" })).toBeNull();
  });
});
