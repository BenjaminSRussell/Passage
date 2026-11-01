import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Campaign, CapacityBlock, CleanoutRule, Network, Resource, SetupMatrix } from "./api/client";
import { editable, validateAllocation, validateCampaignOrder } from "./CampaignDetailPage";
import { describeCondition } from "./CapacityBlocksPage";
import { describeTriggers } from "./CleanoutRulesPage";
import scheduling from "./module";
import { validateCampaign } from "./NewCampaignPage";
import { parseCondition, validateBlock, type BlockValues } from "./NewCapacityBlockPage";
import { validateCleanout } from "./NewCleanoutRulePage";
import { validateNetwork } from "./NewNetworkPage";
import { parseEntries } from "./NewSetupMatrixPage";

const MIX = "3f1c0000-0000-4000-8000-000000000001";
const FILL = "3f1c0000-0000-4000-8000-000000000002";
const LINE = "3f1c0000-0000-4000-8000-000000000003";
const CAMPAIGN = "ca000000-0000-4000-8000-000000000001";
const ORDER_B = "b1000000-0000-4000-8000-00000000000b";
const ORDER_C = "b1000000-0000-4000-8000-00000000000c";
const ORDER_D = "b1000000-0000-4000-8000-00000000000d";
const MATRIX = "aa000000-0000-4000-8000-000000000001";
const NETWORK = "ee000000-0000-4000-8000-000000000001";

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

function header(call: { init: RequestInit }, name: string): string | null {
  return new Headers(call.init.headers).get(name);
}

function lastCall(method: string, suffix: string) {
  return calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix)).at(-1);
}

const shifts = [1, 2, 3, 4, 5].map((weekday) => ({ weekday, startMinute: 480, endMinute: 960 }));
const resources: Resource[] = [
  { id: MIX, code: "MIX-1", name: "Mixing vessel", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" },
  { id: FILL, code: "FILL-1", name: "Filler", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" },
  { id: LINE, code: "LINE-1", name: "Transfer line", finite: true, timeZone: "UTC", shifts, version: 1, createdAt: "2026-10-01T00:00:00Z" },
];

function withResources(extra: Handler, list: Resource[] = resources): Handler {
  return (url, init) => extra(url, init) ?? (url.pathname.endsWith("/resources") ? json(200, { items: list }) : undefined);
}

const proposed: Campaign = {
  id: CAMPAIGN,
  resourceId: MIX,
  campaignKey: "FAMILY-1",
  status: "proposed",
  plannedStart: "2027-01-04T06:00:00Z",
  maxOrders: 10,
  maxQty: null,
  maxMinutes: null,
  sequenceRule: "nearest-neighbor",
  currentSetupGroup: "A",
  orderCount: 2,
  runMinutes: 0,
  changeoverMinutes: 0,
  cleanoutMinutes: 0,
  plan: null,
  sequencedAt: null,
  startedAt: null,
  version: 3,
  createdAt: "2026-10-26T06:00:00Z",
  orders: [
    { orderId: ORDER_C, setupGroup: "C", quantity: "100", runMinutes: 120, routeSetupMinutes: 15, position: 1, sequence: null },
    { orderId: ORDER_B, setupGroup: "B", quantity: "200", runMinutes: 90, routeSetupMinutes: 15, position: 2, sequence: null },
  ],
};

// AC-23.11: B then C, 50 changeover minutes, a cleanout after C.
const planned: Campaign = {
  ...proposed,
  status: "planned",
  version: 4,
  runMinutes: 210,
  changeoverMinutes: 50,
  cleanoutMinutes: 240,
  sequencedAt: "2026-10-26T07:00:00Z",
  orders: [
    { orderId: ORDER_B, setupGroup: "B", quantity: "200", runMinutes: 90, routeSetupMinutes: 15, position: 2, sequence: 1 },
    { orderId: ORDER_C, setupGroup: "C", quantity: "100", runMinutes: 120, routeSetupMinutes: 15, position: 1, sequence: 2 },
  ],
  plan: {
    matrixId: MATRIX,
    changeovers: [
      { afterOrderId: ORDER_B, fromGroup: "A", toGroup: "B", minutes: 30, cost: "45.5", missing: false },
      { beforeOrderId: ORDER_B, afterOrderId: ORDER_C, fromGroup: "B", toGroup: "C", minutes: 20, cost: "30", missing: false },
    ],
    cleanouts: [{ afterOrderId: ORDER_C, setupGroup: "C", minutes: 240, reason: "campaign-end" }],
    warnings: [],
  },
};

describe("Campaign list", () => {
  it("lists campaigns with the resource code and filters by status", async () => {
    handler = withResources((url) => (url.pathname.endsWith("/campaigns") ? json(200, { items: [planned] }) : undefined));
    const { container } = renderAt("/scheduling/campaigns");
    const table = await findTable({ name: "Campaigns" });
    await waitFor(() => expect(table).toHaveTextContent("MIX-1"));
    expect(table).toHaveTextContent("Planned");
    expect(table).toHaveTextContent("50 min");
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "started" } });
    await waitFor(() => expect(lastCall("GET", "/campaigns")?.url.searchParams.get("status")).toBe("started"));
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains an empty list and shows a failure with retry", async () => {
    handler = withResources((url) => (url.pathname.endsWith("/campaigns") ? json(200, { items: [] }) : undefined));
    renderAt("/scheduling/campaigns");
    expect(await screen.findByText("No campaigns yet")).toBeInTheDocument();

    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty scheduling.read" });
    renderAt("/scheduling/campaigns");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("missing duty scheduling.read (AUTH-0003)");
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("New campaign", () => {
  const base = {
    resourceId: MIX,
    campaignKey: " FAMILY-1 ",
    plannedStart: "2027-01-04T06:00",
    sequenceRule: "nearest-neighbor" as const,
    currentSetupGroup: "",
    maxOrders: "",
    maxQty: "",
    maxMinutes: "",
  };

  it("validates limits and keeps quantities as decimal text", () => {
    expect(validateCampaign({ ...base, resourceId: "", maxOrders: "0", maxQty: "0", currentSetupGroup: "a b" }).errors).toEqual({
      resourceId: "Choose the campaign resource.",
      currentSetupGroup: "Use 1 to 32 capital letters, digits, _ or -.",
      maxOrders: "Enter 1 to 1000.",
      maxQty: "Enter a quantity above zero with up to 6 decimals.",
    });
    expect(validateCampaign({ ...base, maxQty: "1500.25", maxOrders: "5" }).input).toEqual({
      resourceId: MIX,
      campaignKey: "FAMILY-1",
      plannedStart: "2027-01-04T06:00:00Z",
      sequenceRule: "nearest-neighbor",
      maxOrders: 5,
      maxQty: "1500.25",
      orders: [],
    });
  });

  it("creates a campaign with an Idempotency-Key and opens it", async () => {
    handler = withResources((url, init) => {
      if (url.pathname.endsWith("/campaigns") && init.method === "POST") return json(201, { ...proposed, orders: [] });
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return json(200, { ...proposed, orders: [] });
      return undefined;
    });
    const { container } = renderAt("/scheduling/campaigns/new");
    fireEvent.change(await screen.findByLabelText("Resource"), { target: { value: MIX } });
    fireEvent.change(screen.getByLabelText("Campaign key"), { target: { value: "FAMILY-1" } });
    fireEvent.change(screen.getByLabelText("Planned start (UTC)"), { target: { value: "2027-01-04T06:00" } });
    fireEvent.change(screen.getByLabelText("Current setup group"), { target: { value: "a" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create campaign" }));
    expect(await screen.findByRole("heading", { name: "FAMILY-1" })).toBeInTheDocument();
    const post = lastCall("POST", "/campaigns")!;
    expect(post.body).toMatchObject({ resourceId: MIX, campaignKey: "FAMILY-1", currentSetupGroup: "A", orders: [] });
    expect(header(post, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("offers the CP-SAT optimizer as a sequence rule", async () => {
    handler = withResources((url, init) => {
      if (url.pathname.endsWith("/campaigns") && init.method === "POST") return json(201, { ...proposed, sequenceRule: "cp-sat", orders: [] });
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return json(200, { ...proposed, sequenceRule: "cp-sat", orders: [] });
      return undefined;
    });
    renderAt("/scheduling/campaigns/new");
    fireEvent.change(await screen.findByLabelText("Resource"), { target: { value: MIX } });
    fireEvent.change(screen.getByLabelText("Campaign key"), { target: { value: "FAMILY-2" } });
    fireEvent.change(screen.getByLabelText("Planned start (UTC)"), { target: { value: "2027-01-04T06:00" } });
    const rule = screen.getByLabelText("Sequence rule");
    expect(within(rule).getByRole("option", { name: "CP-SAT optimizer" })).toBeInTheDocument();
    fireEvent.change(rule, { target: { value: "cp-sat" } });
    expect(screen.getByText(/fewest changeover minutes \(up to 50 orders\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create campaign" }));
    expect(await screen.findByRole("heading", { name: "FAMILY-1" })).toBeInTheDocument();
    expect(screen.getByText("CP-SAT optimizer")).toBeInTheDocument();
    expect(lastCall("POST", "/campaigns")!.body).toMatchObject({ sequenceRule: "cp-sat" });
  });

  it("tells a new tenant to add a resource first", async () => {
    handler = withResources(() => undefined, []);
    renderAt("/scheduling/campaigns/new");
    expect(await screen.findByText("Add a resource first")).toBeInTheDocument();
  });
});

describe("Campaign detail", () => {
  it("validates an order line", () => {
    expect(validateCampaignOrder({ orderId: "x", setupGroup: "", quantity: "-1", runMinutes: "1.5", routeSetupMinutes: "", dueAt: "" }).errors).toEqual({
      orderId: "Enter the production order id (a UUID).",
      setupGroup: "Use 1 to 32 capital letters, digits, _ or -.",
      quantity: "Enter a quantity above zero with up to 6 decimals.",
      runMinutes: "Enter 0 to 100,000 minutes.",
      routeSetupMinutes: "Enter 0 to 10,080 minutes.",
    });
    expect(editable(proposed)).toBe(true);
    expect(editable({ ...proposed, status: "started" })).toBe(false);
    expect(validateAllocation({ amount: "900.00", currency: "USD", basis: "quantity" }).ok).toBe(true);
    expect(validateAllocation({ amount: "9e2", currency: "usd", basis: "quantity" }).errors).toEqual({
      amount: "Enter an amount such as 900.00.",
      currency: "Enter a three-letter currency code.",
    });
  });

  it("completes a started campaign and cancels another, releasing its orders", async () => {
    const started: Campaign = { ...planned, status: "started", version: 5, startedAt: "2026-10-26T08:00:00Z" };
    handler = withResources((url, init) => {
      if (url.pathname.endsWith(":complete") && init.method === "POST") return json(200, { ...started, status: "completed", version: 6, completedAt: "2026-10-26T09:00:00Z" });
      if (url.pathname.endsWith(":cancel") && init.method === "POST") return json(200, { ...planned, status: "cancelled", version: 5, orders: [], orderCount: 0, cancelledAt: "2026-10-26T09:00:00Z" });
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return json(200, started);
      return undefined;
    });
    renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    expect(await screen.findByRole("heading", { name: "FAMILY-1" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Complete" }));
    expect(await screen.findByText("Campaign completed.")).toBeInTheDocument();
    expect(header(lastCall("POST", ":complete")!, "If-Match")).toBe('"5"');
    expect(screen.queryByRole("button", { name: "Complete" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel campaign" })).not.toBeInTheDocument();
  });

  it("cancels with a reason after the confirmation step and shows the released orders", async () => {
    const cancelled: Campaign = { ...planned, status: "cancelled", version: 5, orders: [], orderCount: 0, cancelledAt: "2026-10-26T09:00:00Z", cancelReason: "Demand dropped" };
    handler = withResources((url, init) => {
      if (url.pathname.endsWith(":cancel") && init.method === "POST") return json(200, cancelled);
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return json(200, planned);
      return undefined;
    });
    const { container } = renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    expect(await screen.findByRole("heading", { name: "FAMILY-1" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Complete" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel campaign" }));
    let form = screen.getByRole("form", { name: "Cancel the campaign" });
    expect(within(form).getByLabelText(/Reason/)).toHaveFocus();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(form).getByRole("button", { name: "Keep campaign" }));
    expect(screen.queryByRole("form", { name: "Cancel the campaign" })).not.toBeInTheDocument();
    expect(lastCall("POST", ":cancel")).toBeUndefined();

    fireEvent.click(screen.getByRole("button", { name: "Cancel campaign" }));
    form = screen.getByRole("form", { name: "Cancel the campaign" });
    fireEvent.change(within(form).getByLabelText(/Reason/), { target: { value: "x".repeat(501) } });
    fireEvent.click(within(form).getByRole("button", { name: "Cancel campaign" }));
    expect(await within(form).findByText("Keep the reason to 500 characters.")).toBeInTheDocument();
    expect(lastCall("POST", ":cancel")).toBeUndefined();

    fireEvent.change(within(form).getByLabelText(/Reason/), { target: { value: "  Demand dropped " } });
    fireEvent.click(within(form).getByRole("button", { name: "Cancel campaign" }));
    expect(await screen.findByText("Campaign cancelled. Its orders are free again.")).toBeInTheDocument();
    const call = lastCall("POST", ":cancel")!;
    expect(header(call, "If-Match")).toBe('"4"');
    expect(call.body).toEqual({ reason: "Demand dropped" });
    expect(screen.getByText("Demand dropped")).toBeInTheDocument();
    expect(screen.getByText("The campaign is cancelled; its orders were released and can join another campaign.")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Cancel the campaign" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel campaign" })).not.toBeInTheDocument();
  });

  it("keeps the cancel form open and offers a reload when the campaign changed meanwhile", async () => {
    handler = withResources((url, init) => {
      if (url.pathname.endsWith(":cancel") && init.method === "POST") return json(412, { code: "SCHED-0115", detail: "campaign was changed by another user; current version is 5" });
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return json(200, planned);
      return undefined;
    });
    renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    expect(await screen.findByRole("heading", { name: "FAMILY-1" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel campaign" }));
    fireEvent.click(within(screen.getByRole("form", { name: "Cancel the campaign" })).getByRole("button", { name: "Cancel campaign" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Someone else changed this campaign");
    expect(within(alert).getByRole("button", { name: "Reload" })).toBeInTheDocument();
    expect(lastCall("POST", ":cancel")!.body).toEqual({});
    expect(screen.getByRole("form", { name: "Cancel the campaign" })).toBeInTheDocument();
  });

  it("explains a refused completion", async () => {
    const started: Campaign = { ...planned, status: "started", version: 5, startedAt: "2026-10-26T08:00:00Z" };
    handler = withResources((url, init) => {
      if (url.pathname.endsWith(":complete") && init.method === "POST") return json(422, { code: "SCHED-0125", detail: "start the campaign before you complete it" });
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return json(200, started);
      return undefined;
    });
    renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    expect(await screen.findByRole("heading", { name: "FAMILY-1" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Complete" }));
    expect(await findAlert()).toHaveTextContent("Start the campaign before you complete it");
    expect(screen.getByRole("button", { name: "Complete" })).toBeInTheDocument();
  });

  it("sequences with If-Match and shows the plan of AC-23.11", async () => {
    handler = withResources((url, init) => {
      if (url.pathname.endsWith(`:sequence`) && init.method === "POST") return json(200, planned);
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return json(200, proposed);
      return undefined;
    });
    const { container } = renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    expect(await screen.findByRole("heading", { name: "FAMILY-1" })).toBeInTheDocument();
    expect(screen.getByText("Not sequenced yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Sequence" }));
    expect(await screen.findByText("Campaign sequenced.")).toBeInTheDocument();
    const seq = lastCall("POST", ":sequence")!;
    expect(header(seq, "If-Match")).toBe('"3"');
    expect(header(seq, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);

    const orders = screen.getByRole("table", { name: "Campaign orders" });
    const rows = within(orders).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("b1000000");
    expect(rows[1]).toHaveTextContent("B");
    const changeovers = screen.getByRole("table", { name: "Changeovers" });
    expect(within(changeovers).getAllByRole("row")).toHaveLength(3);
    expect(changeovers).toHaveTextContent("Leading setup");
    expect(screen.getByRole("table", { name: "Cleanouts" })).toHaveTextContent("Campaign end");
    expect(screen.getByRole("button", { name: "Start" })).toBeInTheDocument();
  });

  it("asks for a reload when another planner changed the campaign", async () => {
    handler = withResources((url, init) => {
      if (url.pathname.endsWith(":start") && init.method === "POST") {
        return json(412, { code: "SCHED-0115", detail: "campaign was changed by another user; current version is 5" });
      }
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return json(200, planned);
      return undefined;
    });
    renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Start" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Someone else changed this campaign");
    expect(header(lastCall("POST", ":start")!, "If-Match")).toBe('"4"');
    fireEvent.click(within(alert).getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "GET" && c.url.pathname.endsWith(`/campaigns/${CAMPAIGN}`))).toHaveLength(2));
  });

  it("adds and removes orders and maps a server field error", async () => {
    const added: Campaign = {
      ...proposed,
      version: 4,
      orderCount: 3,
      orders: [...proposed.orders!, { orderId: ORDER_D, setupGroup: "B", quantity: "5.5", runMinutes: 30, routeSetupMinutes: 0, position: 3, sequence: null }],
    };
    let addReply: Reply = json(400, { code: "COM-0001", detail: "validation failed", errors: { quantity: "must have at most 6 decimals" } });
    handler = withResources((url, init) => {
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}/orders`) && init.method === "POST") return addReply;
      if (init.method === "DELETE") return json(200, { ...added, version: 5, orderCount: 2, orders: added.orders!.slice(1) });
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return json(200, proposed);
      return undefined;
    });
    renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    const form = await screen.findByRole("form", { name: "Add an order" });
    fireEvent.change(within(form).getByLabelText("Order id"), { target: { value: ORDER_D.toUpperCase() } });
    fireEvent.change(within(form).getByLabelText("Setup group"), { target: { value: "b" } });
    fireEvent.change(within(form).getByLabelText("Quantity"), { target: { value: "5.5" } });
    fireEvent.change(within(form).getByLabelText("Run minutes"), { target: { value: "30" } });
    fireEvent.click(within(form).getByRole("button", { name: "Add order" }));
    expect(await within(form).findByText("must have at most 6 decimals")).toBeInTheDocument();
    expect(within(form).getByLabelText("Quantity")).toHaveValue("5.5");

    addReply = json(200, added);
    fireEvent.click(within(form).getByRole("button", { name: "Add order" }));
    expect(await screen.findByText("Order added. Sequence the campaign again.")).toBeInTheDocument();
    const add = lastCall("POST", "/orders")!;
    expect(add.body).toEqual({ orderId: ORDER_D, setupGroup: "B", quantity: "5.5", runMinutes: 30, routeSetupMinutes: 0 });
    expect(header(add, "If-Match")).toBe('"3"');
    expect(within(form).getByLabelText("Quantity")).toHaveValue("");

    fireEvent.click(screen.getByRole("button", { name: `Remove order ${ORDER_C}` }));
    await waitFor(() => expect(lastCall("DELETE", `/orders/${ORDER_C}`)).toBeDefined());
    expect(header(lastCall("DELETE", `/orders/${ORDER_C}`)!, "If-Match")).toBe('"4"');
  });

  it("shows the optimizer outcome and the cleanout counter of a cp-sat plan", async () => {
    const optimized: Campaign = {
      ...planned,
      sequenceRule: "cp-sat",
      plan: {
        ...planned.plan!,
        solverStatus: "OPTIMAL",
        optimized: true,
        carryIn: { batches: 2, minutes: 130 },
        carryOut: { batches: 1, minutes: 90 },
        warnings: [
          { code: "SCHED-0006", detail: "Optimization ended with status OPTIMAL; optimized schedule kept." },
          { code: "SCHED-0012", detail: "Setup matrix has no entry C->Z; route setup time used." },
        ],
      },
    };
    handler = withResources((url) => (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`) ? json(200, optimized) : undefined));
    const { container } = renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    expect(await screen.findByText("Optimizer sequence kept")).toBeInTheDocument();
    expect(screen.getByText("SCHED-0006: Optimization ended with status OPTIMAL; optimized schedule kept.")).toBeInTheDocument();
    // The optimizer message is not repeated among the missing-entry warnings.
    expect(screen.getAllByText(/SCHED-0006/)).toHaveLength(1);
    expect(screen.getByText("SCHED-0012: Setup matrix has no entry C->Z; route setup time used.")).toBeInTheDocument();
    expect(screen.getByText(/starts 2 batches and 2 h 10 min after its last cleanout and is left at 1 batch and 1 h 30 min/)).toBeInTheDocument();
    expect(screen.getByText("CP-SAT optimizer")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when the heuristic was kept and when a start needs a new sequence", async () => {
    const kept: Campaign = {
      ...planned,
      sequenceRule: "cp-sat",
      plan: { ...planned.plan!, solverStatus: "UNAVAILABLE", optimized: false, warnings: [{ code: "SCHED-0006", detail: "Optimization ended with status UNAVAILABLE; heuristic schedule kept." }] },
    };
    handler = withResources((url, init) => {
      if (url.pathname.endsWith(":start") && init.method === "POST") {
        return json(409, { code: "SCHED-0123", detail: "the resource has run 2 batches and 130 minutes since its last cleanout, not the 0 and 0 this campaign was sequenced from; sequence it again" });
      }
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return json(200, kept);
      return undefined;
    });
    renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    expect(await screen.findByText("Nearest-neighbour sequence kept")).toBeInTheDocument();
    expect(screen.queryByText("Changeovers without a matrix entry")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Sequence the campaign again before you start it");
    expect(alert).toHaveTextContent("SCHED-0123");
  });

  it("freezes a started campaign", async () => {
    handler = withResources((url) => (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`) ? json(200, { ...planned, status: "started", startedAt: "2026-10-27T06:00:00Z" }) : undefined));
    renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    expect(await screen.findByText(/orders can no longer join or leave it \(SCHED-0011\)/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sequence" })).toBeNull();
    expect(screen.queryByRole("form", { name: "Add an order" })).toBeNull();
    expect(screen.queryAllByRole("button", { name: /^Remove order/ })).toHaveLength(0);
  });

  it("allocates the cleanout cost of AC-23.12", async () => {
    const three: Campaign = {
      ...planned,
      orders: [
        { orderId: ORDER_B, setupGroup: "B", quantity: "100", runMinutes: 90, routeSetupMinutes: 15, position: 1, sequence: 1 },
        { orderId: ORDER_C, setupGroup: "C", quantity: "200", runMinutes: 120, routeSetupMinutes: 15, position: 2, sequence: 2 },
        { orderId: ORDER_D, setupGroup: "C", quantity: "300", runMinutes: 60, routeSetupMinutes: 15, position: 3, sequence: 3 },
      ],
    };
    handler = withResources((url, init) => {
      if (url.pathname.endsWith(":allocate-cost") && init.method === "POST") {
        return json(200, {
          campaignId: CAMPAIGN,
          basis: "quantity",
          currency: "USD",
          amount: "900.00",
          lines: [
            { orderId: ORDER_B, weight: "100", amount: "150.00" },
            { orderId: ORDER_C, weight: "200", amount: "300.00" },
            { orderId: ORDER_D, weight: "300", amount: "450.00" },
          ],
        });
      }
      if (url.pathname.endsWith(`/campaigns/${CAMPAIGN}`)) return json(200, three);
      return undefined;
    });
    renderAt(`/scheduling/campaigns/${CAMPAIGN}`);
    const form = await screen.findByRole("form", { name: "Allocate changeover cost" });
    fireEvent.change(within(form).getByLabelText("Cost to allocate"), { target: { value: "900.00" } });
    fireEvent.click(within(form).getByRole("button", { name: "Allocate" }));
    const table = await findTable({ name: "Allocation" });
    expect(within(table).getAllByRole("row").map((r) => r.textContent)).toEqual([
      "OrderWeightAmount (USD)",
      "b1000000100150.00",
      "b1000000200300.00",
      "b1000000300450.00",
    ]);
    expect(lastCall("POST", ":allocate-cost")!.body).toEqual({ amount: "900.00", currency: "USD", basis: "quantity" });
  });
});

describe("Setup matrices", () => {
  it("parses changeover lines and rejects bad ones", () => {
    expect(parseEntries("a, b, 30, 45.50\n\nB,C,20,30,WET-CLEAN")).toEqual({
      entries: [
        { fromGroup: "A", toGroup: "B", minutes: 30, cost: "45.50" },
        { fromGroup: "B", toGroup: "C", minutes: 20, cost: "30", cleaningSetId: "WET-CLEAN" },
      ],
    });
    expect(parseEntries("A,A,5,1").error).toBe("Line 1: a group needs no changeover to itself.");
    expect(parseEntries("A,B,5,1\nA,B,6,1").error).toBe("Line 2: A->B is listed twice.");
    expect(parseEntries("A,B,5,-1").error).toBe("Line 1: cost must be a non-negative amount such as 45.50.");
    expect(parseEntries("A,B,99999,1").error).toBe("Line 1: minutes must be 0 to 10080.");
    expect(parseEntries("").error).toBe("List at least one changeover.");
  });

  const matrix: SetupMatrix = {
    id: MATRIX,
    resourceId: MIX,
    version: 2,
    activeFrom: "2027-01-01",
    currency: "USD",
    entries: [{ fromGroup: "PEANUT", toGroup: "PEANUT-FREE", minutes: 240, cost: "900.00", cleaningSetId: "WET-CLEAN" }],
    createdAt: "2026-10-26T06:00:00Z",
  };

  it("saves a matrix version and opens it", async () => {
    handler = withResources((url, init) => {
      if (url.pathname.endsWith("/setup-matrices") && init.method === "POST") return json(201, matrix);
      if (url.pathname.endsWith(`/setup-matrices/${MATRIX}`)) return json(200, matrix);
      return undefined;
    });
    renderAt("/scheduling/setup-matrices/new");
    fireEvent.change(await screen.findByLabelText("Resource"), { target: { value: MIX } });
    fireEvent.change(screen.getByLabelText("Active from"), { target: { value: "2027-01-01" } });
    fireEvent.change(screen.getByLabelText("Changeovers"), { target: { value: "peanut, peanut-free, 240, 900.00, WET-CLEAN" } });
    fireEvent.click(screen.getByRole("button", { name: "Save matrix" }));
    expect(await screen.findByRole("heading", { name: "MIX-1 setup matrix" })).toBeInTheDocument();
    const post = lastCall("POST", "/setup-matrices")!;
    expect(post.body).toEqual({ resourceId: MIX, activeFrom: "2027-01-01", currency: "USD", entries: matrix.entries });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(screen.getByRole("table", { name: "Changeovers" })).toHaveTextContent("4 h");
  });

  it("lists matrices with the resource code and filters by resource", async () => {
    handler = withResources((url) => (url.pathname.endsWith("/setup-matrices") ? json(200, { items: [matrix] }) : undefined));
    const { container } = renderAt("/scheduling/setup-matrices");
    const table = await findTable({ name: "Setup matrices" });
    await waitFor(() => expect(table).toHaveTextContent("MIX-1"));
    fireEvent.change(screen.getByLabelText("Resource"), { target: { value: MIX } });
    await waitFor(() => expect(lastCall("GET", "/setup-matrices")?.url.searchParams.get("resourceId")).toBe(MIX));
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a missing matrix as an error", async () => {
    handler = withResources((url) => (url.pathname.includes("/setup-matrices/") ? json(404, { code: "SCHED-0110", detail: "setup matrix not found" }) : undefined));
    renderAt(`/scheduling/setup-matrices/${MATRIX}`);
    expect(await findAlert()).toHaveTextContent("setup matrix not found (SCHED-0110)");
  });
});

describe("Resource networks", () => {
  const edge = { from: MIX, to: FILL, transfer: "10", maxWait: "120", secondary: "" };
  const values = { code: "MIX-FILL", name: " Mixing to filling ", activeFrom: "2027-01-01", activeTo: "", edges: [edge] };

  it("applies the domain edge rules before calling the service", () => {
    expect(validateNetwork(values).input).toEqual({
      code: "MIX-FILL",
      name: "Mixing to filling",
      activeFrom: "2027-01-01",
      edges: [{ fromResourceId: MIX, toResourceId: FILL, transferMinutes: 10, maxWaitMinutes: 120 }],
    });
    expect(validateNetwork({ ...values, edges: [{ ...edge, to: MIX }] }).errors.edges).toBe("Transition 1: a resource cannot feed itself.");
    expect(validateNetwork({ ...values, edges: [{ ...edge, secondary: FILL }] }).errors.edges).toBe("Transition 1: the transfer resource must be a third resource.");
    expect(validateNetwork({ ...values, edges: [edge, edge] }).errors.edges).toBe("Transition 2: this transition is listed twice.");
    expect(validateNetwork({ ...values, activeTo: "2026-12-31" }).errors.activeTo).toBe("Enter a day on or after Active from.");
    expect(validateNetwork({ ...values, edges: [{ ...edge, maxWait: "" }] }).input?.edges[0].maxWaitMinutes).toBeNull();
    expect(validateNetwork({ ...values, edges: [{ ...edge, transfer: "30", maxWait: "20" }] }).errors.edges).toBe(
      "Transition 1: the maximum wait includes the transfer, so it must be at least the transfer minutes.",
    );
  });

  const network: Network = {
    id: NETWORK,
    code: "MIX-FILL",
    name: "Mixing to filling",
    activeFrom: "2027-01-01",
    activeTo: null,
    edges: [
      { fromResourceId: MIX, toResourceId: FILL, transferMinutes: 10, maxWaitMinutes: 120, secondaryResourceId: LINE },
      { fromResourceId: FILL, toResourceId: LINE, transferMinutes: 0, maxWaitMinutes: null },
    ],
    version: 1,
    createdAt: "2026-10-26T06:00:00Z",
  };

  it("creates a network with two transitions and shows a duplicate code at the field", async () => {
    let reply: Reply = json(409, { code: "SCHED-0108", detail: "a network with this code already exists" });
    handler = withResources((url, init) => {
      if (url.pathname.endsWith("/networks") && init.method === "POST") return reply;
      if (url.pathname.endsWith(`/networks/${NETWORK}`)) return json(200, network);
      return undefined;
    });
    const { container } = renderAt("/scheduling/networks/new");
    fireEvent.change(await screen.findByLabelText("Code"), { target: { value: "mix-fill" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Mixing to filling" } });
    fireEvent.change(screen.getAllByLabelText("From resource")[0], { target: { value: MIX } });
    fireEvent.change(screen.getAllByLabelText("To resource")[0], { target: { value: FILL } });
    fireEvent.change(screen.getAllByLabelText("Maximum wait minutes")[0], { target: { value: "120" } });
    fireEvent.click(screen.getByRole("button", { name: "Add transition" }));
    fireEvent.change(screen.getAllByLabelText("From resource")[1], { target: { value: FILL } });
    fireEvent.change(screen.getAllByLabelText("To resource")[1], { target: { value: LINE } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create network" }));
    expect(await screen.findByText("A network with this code already exists.")).toBeInTheDocument();

    reply = json(201, network);
    fireEvent.click(screen.getByRole("button", { name: "Create network" }));
    expect(await screen.findByRole("heading", { name: "MIX-FILL" })).toBeInTheDocument();
    const post = lastCall("POST", "/networks")!;
    expect((post.body as { edges: unknown[] }).edges).toEqual([
      { fromResourceId: MIX, toResourceId: FILL, transferMinutes: 0, maxWaitMinutes: 120 },
      { fromResourceId: FILL, toResourceId: LINE, transferMinutes: 0, maxWaitMinutes: null },
    ]);
    const table = await findTable({ name: "Allowed transitions" });
    await waitFor(() => expect(table).toHaveTextContent("LINE-1"));
    expect(table).toHaveTextContent("No limit");
  });

  it("explains an empty network list", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/scheduling/networks");
    expect(await screen.findByText("No resource networks yet")).toBeInTheDocument();
  });
});

describe("Cleanout rules", () => {
  const base = { setupGroup: "C", durationMinutes: "240", operationSetId: "", afterBatches: "", afterHours: "", atCampaignEnd: false };

  it("needs at least one trigger", () => {
    expect(validateCleanout(base).errors.atCampaignEnd).toBe("A rule needs a batch limit, an hour limit or a cleanout at campaign end.");
    expect(validateCleanout({ ...base, afterHours: "48" }).input).toEqual({ setupGroup: "C", durationMinutes: 240, atCampaignEnd: false, afterHours: 48 });
  });

  const rule: CleanoutRule = { id: "r1", setupGroup: "C", durationMinutes: 240, afterBatches: 5, afterHours: null, atCampaignEnd: true, version: 1, createdAt: "2026-10-26T06:00:00Z" };

  it("describes triggers in the list", async () => {
    expect(describeTriggers(rule)).toBe("After 5 batches · at campaign end");
    handler = (url) => (url.pathname.endsWith("/cleanout-rules") ? json(200, { items: [rule] }) : undefined);
    const { container } = renderAt("/scheduling/cleanout-rules");
    const table = await findTable({ name: "Cleanout rules" });
    await waitFor(() => expect(table).toHaveTextContent("After 5 batches"));
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a rule and shows a duplicate group at the field", async () => {
    let reply: Reply = json(409, { code: "SCHED-0111", detail: "this setup group already has a cleanout rule" });
    handler = (url, init) => {
      if (url.pathname.endsWith("/cleanout-rules") && init.method === "POST") return reply;
      if (url.pathname.endsWith("/cleanout-rules")) return json(200, { items: [rule] });
      return undefined;
    };
    renderAt("/scheduling/cleanout-rules/new");
    fireEvent.change(screen.getByLabelText("Setup group"), { target: { value: "c" } });
    fireEvent.change(screen.getByLabelText("Duration minutes"), { target: { value: "240" } });
    fireEvent.change(screen.getByLabelText("After batches"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Create rule" }));
    expect(await screen.findByText("This setup group already has a cleanout rule.")).toBeInTheDocument();
    reply = json(201, rule);
    fireEvent.click(screen.getByRole("button", { name: "Create rule" }));
    expect(await screen.findByRole("heading", { name: "Cleanout rules" })).toBeInTheDocument();
    expect(lastCall("POST", "/cleanout-rules")!.body).toEqual({ setupGroup: "C", durationMinutes: 240, atCampaignEnd: true, afterBatches: 5 });
  });
});

describe("Capacity blocks", () => {
  it("parses predicates with the longest operator first", () => {
    expect(parseCondition("color = white\ngrade >= 2\nshade != dark")).toEqual({
      condition: [
        { attribute: "color", op: "=", value: "white" },
        { attribute: "grade", op: ">=", value: "2" },
        { attribute: "shade", op: "!=", value: "dark" },
      ],
    });
    expect(parseCondition("grade > high").error).toBe("Line 1: > compares numbers; the value must be numeric.");
    expect(parseCondition("color white").error).toMatch(/^Line 1: use attribute, an operator/);
    expect(describeCondition([])).toBe("Any job");
  });

  const values: BlockValues = {
    resourceId: MIX,
    code: "MON-WHITE",
    kind: "weekly",
    days: [1],
    start: "06:00",
    end: "14:00",
    startAt: "",
    endAt: "",
    rule: "",
    startDate: "",
    condition: "color = white",
    priority: "",
    active: true,
  };

  it("builds weekly windows or a dated window", () => {
    expect(validateBlock(values).input).toEqual({
      resourceId: MIX,
      code: "MON-WHITE",
      windows: [{ weekday: 1, startMinute: 360, endMinute: 840 }],
      condition: [{ attribute: "color", op: "=", value: "white" }],
      priority: 0,
      active: true,
    });
    const dated = validateBlock({ ...values, kind: "dated", startAt: "2027-01-06T06:00", endAt: "2027-01-06T05:00" });
    expect(dated.errors.endAt).toBe("The block must end after it starts.");
    expect(validateBlock({ ...values, kind: "dated", startAt: "2027-01-06T06:00", endAt: "2027-01-06T14:00" }).input).toMatchObject({
      startAt: "2027-01-06T06:00:00Z",
      endAt: "2027-01-06T14:00:00Z",
    });
  });

  const block: CapacityBlock = {
    id: "b1",
    resourceId: MIX,
    code: "MON-WHITE",
    windows: [{ weekday: 1, startMinute: 360, endMinute: 840 }],
    condition: [{ attribute: "color", op: "=", value: "white" }],
    priority: 10,
    active: true,
    version: 1,
    createdAt: "2026-10-26T06:00:00Z",
  };

  it("creates a block and lists it", async () => {
    handler = withResources((url, init) => {
      if (url.pathname.endsWith("/capacity-blocks") && init.method === "POST") return json(201, block);
      if (url.pathname.endsWith("/capacity-blocks")) return json(200, { items: [block] });
      return undefined;
    });
    const { container } = renderAt("/scheduling/capacity-blocks/new");
    fireEvent.change(await screen.findByLabelText("Resource"), { target: { value: MIX } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "mon-white" } });
    fireEvent.change(screen.getByLabelText("Condition"), { target: { value: "color = white" } });
    fireEvent.change(screen.getByLabelText("Priority"), { target: { value: "10" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create block" }));
    const table = await findTable({ name: "Capacity blocks" });
    await waitFor(() => expect(table).toHaveTextContent("color = white"));
    expect(table).toHaveTextContent("Weekly: Mon 06:00–14:00");
    const post = lastCall("POST", "/capacity-blocks")!;
    expect(post.body).toMatchObject({ code: "MON-WHITE", priority: 10, windows: [{ weekday: 1, startMinute: 360, endMinute: 840 }] });
    expect(header(post, "Idempotency-Key")).toBeTruthy();
  });
});
