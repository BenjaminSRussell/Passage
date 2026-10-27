import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Asset, AssetDetail, MaintenanceRequest, Plan, WorkOrder, WorkOrderDetail } from "./api/client";
import { validateAsset } from "./AssetsPage";
import eam from "./module";
import { validatePlan } from "./PlansPage";
import { validateWorkOrder } from "./WorkOrdersPage";

const PUMP = "0195f0a0-0000-7000-8000-000000000001";
const SITE = "0195f0a0-0000-7000-8000-0000000000aa";
const HOURS = "0195f0a0-0000-7000-8000-0000000000c1";
const WO = "0195f0a0-0000-7000-8000-0000000000d1";
const REQ = "0195f0a0-0000-7000-8000-0000000000e1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

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
  vi.unstubAllGlobals();
});

function Where() {
  const location = useLocation();
  return <p data-testid="where">{location.pathname + location.search}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {eam.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const get = (path: string) => (url: URL, init: RequestInit) => (init.method ?? "GET") === "GET" && url.pathname === `/api/v1/eam${path}`;

const pump: Asset = {
  id: PUMP,
  assetNumber: "AST-1",
  name: "Feed pump",
  assetType: "PUMP",
  siteId: SITE,
  criticality: 4,
  lifecycleState: "ACTIVE",
  version: 2,
  createdAt: "2026-10-01T08:00:00Z",
};

const pumpDetail: AssetDetail = {
  ...pump,
  counters: [{ id: HOURS, assetId: PUMP, name: "Run hours", unit: "h", cumulative: true, source: "MANUAL", lastReading: 12300, lastReadingAt: "2026-10-21T00:00:00Z", avgDailyUsage: 6.5 }],
  rules: [],
  openWorkOrders: 1,
  partsCost: "350.000000",
  laborCost: "360.000000",
};

const order: WorkOrder = {
  id: WO,
  number: "WO-7",
  assetId: PUMP,
  workOrderType: "CORRECTIVE",
  status: "RELEASED",
  priority: 2,
  currency: "USD",
  dueAt: "2026-10-30T00:00:00Z",
  downtimeMinutes: 0,
  partsCost: "0",
  laborCost: "360.000000",
  warrantyCovered: false,
  version: 3,
  createdAt: "2026-10-21T08:00:00Z",
};

const orderDetail: WorkOrderDetail = {
  ...order,
  assetType: "PUMP",
  downtime: [],
  labor: [{ id: "l1", hours: "8.00", rate: "45.000000", cost: "360.000000", createdAt: "2026-10-21T09:00:00Z" }],
  history: [{ from: "CREATED", to: "SCHEDULED", at: "2026-10-21T08:10:00Z", actor: "planner-1" }],
};

describe("workspace", () => {
  it("shows the tiles counted by the service and links to each area", async () => {
    handler = (url, init) => (get("/workspace")(url, init) ? json(200, { requestsToTriage: 2, dueIn7Days: 3, overdue: 1, openWorkOrders: 9 }) : undefined);
    const { container } = renderAt("/eam");
    const tiles = await screen.findByRole("list", { name: "Asset management tiles" });
    expect(within(tiles).getByText("Requests to triage")).toBeInTheDocument();
    expect(tiles).toHaveTextContent("Requests to triage 2");
    expect(tiles).toHaveTextContent("Open work orders 9");
    expect(screen.getByRole("link", { name: "Open work orders" })).toHaveAttribute("href", "/eam/work-orders");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the error with a retry", async () => {
    let fail = true;
    handler = (url, init) =>
      get("/workspace")(url, init) ? (fail ? json(503, { code: "COM-0500", detail: "EAM is down" }) : json(200, { requestsToTriage: 0, dueIn7Days: 0, overdue: 0, openWorkOrders: 0 })) : undefined;
    renderAt("/eam");
    expect(await screen.findByRole("alert")).toHaveTextContent("EAM is down (COM-0500)");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("list", { name: "Asset management tiles" })).toHaveTextContent("Overdue 0");
  });
});

describe("assets", () => {
  it("lists assets, filters by state through the URL, pages and opens one", async () => {
    handler = (url, init) => {
      if (!get("/assets")(url, init)) return undefined;
      if (url.searchParams.get("cursor") === "next") return json(200, { items: [{ ...pump, id: "a2", assetNumber: "AST-0", name: "Old fan" }] });
      return json(200, { items: [pump], nextCursor: "next" });
    };
    const { container } = renderAt("/eam/assets");
    const table = await screen.findByRole("table", { name: "Assets" });
    expect(within(table).getByText("Feed pump")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Load more assets" }));
    expect(await screen.findByText("Old fan")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "ACTIVE" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("ACTIVE"));
    fireEvent.click(within(await screen.findByRole("table", { name: "Assets" })).getByRole("button", { name: "AST-1" }));
    expect(screen.getByTestId("where")).toHaveTextContent(`/eam/assets/${PUMP}`);
    expect(await screen.findByText("Could not load the asset")).toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    handler = (url, init) => (get("/assets")(url, init) ? json(200, { items: [] }) : undefined);
    renderAt("/eam/assets");
    expect(await screen.findByText("No assets yet")).toBeInTheDocument();
  });

  it("checks a new asset before sending", () => {
    const { errors, input } = validateAsset({ name: "", assetType: "pump!", siteId: "nope", criticality: "9", productionResourceId: "x" });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["assetType", "criticality", "name", "productionResourceId", "siteId"]);
    expect(validateAsset({ name: " Pump ", assetType: "pump", siteId: SITE, criticality: "4", productionResourceId: "" }).input).toEqual({
      name: "Pump",
      assetType: "PUMP",
      siteId: SITE,
      criticality: 4,
    });
  });

  it("creates an asset with one Idempotency-Key across retries and opens it", async () => {
    let fail = true;
    handler = (url, init) => {
      if (url.pathname === "/api/v1/eam/assets" && init.method === "POST") return fail ? json(503, { code: "COM-0500", detail: "busy" }) : json(201, { ...pump, lifecycleState: "CREATED" });
      return undefined;
    };
    renderAt("/eam/assets/new");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Feed pump" } });
    fireEvent.change(screen.getByLabelText("Asset type"), { target: { value: "pump" } });
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: SITE } });
    fireEvent.click(screen.getByRole("button", { name: "Create asset" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("busy (COM-0500)");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Create asset" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/eam/assets/${PUMP}`));
    const [first, second] = posts("/assets");
    expect(second.body).toEqual({ name: "Feed pump", assetType: "PUMP", siteId: SITE, criticality: 3 });
    expect(header(second, "Idempotency-Key")).toBe(header(first, "Idempotency-Key"));
  });
});

describe("asset detail", () => {
  function fakeAsset(detail: AssetDetail, extra: Handler = () => undefined) {
    handler = (url, init) => {
      const hit = extra(url, init);
      if (hit) return hit;
      if (get(`/assets/${PUMP}`)(url, init)) return json(200, detail);
      if (get("/kpis")(url, init)) return json(200, { failures: 3, mtbfHours: 900, mttrHours: 3.3333, availability: 0.996 });
      if (get("/work-orders")(url, init)) return json(200, { items: [order] });
      if (get(`/assets/${PUMP}/criticality`)(url, init)) return json(200, { items: [] });
      if (get(`/assets/${PUMP}/warranties`)(url, init)) return json(200, { items: [] });
      return undefined;
    };
  }

  it("shows details, counters, cost, KPIs and recent orders", async () => {
    fakeAsset(pumpDetail);
    const { container } = renderAt(`/eam/assets/${PUMP}`);
    expect(await screen.findByRole("heading", { name: "Feed pump" })).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Counters" })).getByText("Run hours")).toBeInTheDocument();
    expect(screen.getByText("710.00")).toBeInTheDocument();
    expect(await screen.findByText("900 h")).toBeInTheDocument();
    expect(screen.getByText("99.6%")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "WO-7" })).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname === "/api/v1/eam/work-orders")?.url.searchParams.get("asset")).toBe(PUMP);
    expect(pageViolations(container)).toEqual([]);
  });

  it("activates a created asset with its version", async () => {
    let state: AssetDetail = { ...pumpDetail, lifecycleState: "CREATED", version: 1 };
    fakeAsset(pumpDetail, (url, init) => {
      if (url.pathname === `/api/v1/eam/assets/${PUMP}/activate`) {
        state = { ...state, lifecycleState: "ACTIVE", version: 2 };
        return json(200, state);
      }
      if (get(`/assets/${PUMP}`)(url, init)) return json(200, state);
      return undefined;
    });
    renderAt(`/eam/assets/${PUMP}`);
    fireEvent.click(await screen.findByRole("button", { name: "Activate asset" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Asset activated.");
    expect(header(posts("/activate")[0], "If-Match")).toBe('"1"');
    expect(await screen.findByRole("link", { name: "New work order" })).toHaveAttribute("href", `/eam/work-orders/new?asset=${PUMP}`);
  });

  it("shows a rejected reading with its code (EAM-CTR-001)", async () => {
    fakeAsset(pumpDetail, (url) =>
      url.pathname === `/api/v1/eam/counters/${HOURS}/readings` ? json(422, { code: "EAM-CTR-001", detail: "reading 12250 is below last reading 12300" }) : undefined,
    );
    renderAt(`/eam/assets/${PUMP}`);
    fireEvent.change(await screen.findByLabelText("Reading"), { target: { value: "12250" } });
    fireEvent.click(screen.getByRole("button", { name: "Record reading" }));
    expect(await screen.findByText("Reading rejected")).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("(EAM-CTR-001)");
    expect(posts("/readings")[0].body).toEqual({ value: 12250, reset: false });
  });

  it("adds a condition rule to a counter", async () => {
    fakeAsset(pumpDetail, (url) => (url.pathname === `/api/v1/eam/counters/${HOURS}/rules` ? json(201, { id: "r1" }) : undefined));
    renderAt(`/eam/assets/${PUMP}`);
    fireEvent.change(await screen.findByLabelText("Rule name"), { target: { value: "Bearing hot" } });
    fireEvent.change(screen.getByLabelText("Threshold"), { target: { value: "85" } });
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Rule Bearing hot added.");
    expect(posts("/rules")[0].body).toEqual({ name: "Bearing hot", operator: ">", threshold: 85, severity: 3, cooldownMinutes: 60 });
  });

  it("explains a missing asset", async () => {
    handler = (url, init) => (get(`/assets/${PUMP}`)(url, init) ? json(404, { code: "EAM-NF-001", detail: "asset not found" }) : undefined);
    renderAt(`/eam/assets/${PUMP}`);
    expect(await screen.findByText("We couldn't find this asset")).toBeInTheDocument();
  });
});

describe("work orders", () => {
  it("lists work orders with their cost and filters by status", async () => {
    handler = (url, init) => (get("/work-orders")(url, init) ? json(200, { items: [order] }) : undefined);
    const { container } = renderAt("/eam/work-orders");
    const table = await screen.findByRole("table", { name: "Work orders" });
    expect(within(table).getByText("360.00 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "RELEASED" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("RELEASED"));
  });

  it("checks a new work order: emergency orders send no priority", () => {
    const base = { assetId: PUMP, workOrderType: "EMERGENCY" as const, priority: "4", currency: "usd", jobType: "", dueAt: "" };
    expect(validateWorkOrder(base).input).toEqual({ assetId: PUMP, workOrderType: "EMERGENCY", currency: "USD" });
    expect(validateWorkOrder({ ...base, workOrderType: "CORRECTIVE" }).input?.priority).toBe(4);
    expect(Object.keys(validateWorkOrder({ ...base, assetId: "", currency: "x", jobType: "bad code" }).errors).sort()).toEqual(["assetId", "currency", "jobType"]);
  });

  it("creates a work order on an active asset and opens it", async () => {
    handler = (url, init) => {
      if (get("/assets")(url, init)) return json(200, { items: [pump] });
      if (url.pathname === "/api/v1/eam/work-orders" && init.method === "POST") return json(201, order);
      return undefined;
    };
    renderAt(`/eam/work-orders/new?asset=${PUMP}`);
    expect(await screen.findByLabelText("Asset")).toHaveValue(PUMP);
    expect(calls[0].url.searchParams.get("status")).toBe("ACTIVE");
    fireEvent.change(screen.getByLabelText("Job type"), { target: { value: "repair" } });
    fireEvent.click(screen.getByRole("button", { name: "Create work order" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/eam/work-orders/${WO}`));
    expect(posts("/work-orders")[0].body).toEqual({ assetId: PUMP, workOrderType: "CORRECTIVE", currency: "USD", jobType: "REPAIR" });
    expect(header(posts("/work-orders")[0], "Idempotency-Key")).toBeTruthy();
  });

  it("says when there is no active asset", async () => {
    handler = (url, init) => (get("/assets")(url, init) ? json(200, { items: [] }) : undefined);
    renderAt("/eam/work-orders/new");
    expect(await screen.findByText("No active assets")).toBeInTheDocument();
  });
});

describe("work order detail", () => {
  function fakeOrder(detail: WorkOrderDetail, extra: Handler = () => undefined) {
    handler = (url, init) => extra(url, init) ?? (get(`/work-orders/${WO}`)(url, init) ? json(200, detail) : undefined);
  }

  it("shows labor, history and the next status actions", async () => {
    fakeOrder(orderDetail);
    const { container } = renderAt(`/eam/work-orders/${WO}`);
    expect(await screen.findByRole("heading", { name: "Work order WO-7" })).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Labor" })).getByText("8.00")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Status history" })).getByText("planner-1")).toBeInTheDocument();
    const actions = screen.getByRole("group", { name: "Status actions" });
    expect(within(actions).getByRole("button", { name: "Move to in progress" })).toBeInTheDocument();
    expect(within(actions).queryByRole("button", { name: "Complete" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("completes with downtime, sending the version and a key", async () => {
    fakeOrder({ ...orderDetail, status: "IN_PROGRESS", version: 5 }, (url) =>
      url.pathname === `/api/v1/eam/work-orders/${WO}/status` ? json(200, { ...order, status: "COMPLETED" }) : undefined,
    );
    renderAt(`/eam/work-orders/${WO}`);
    fireEvent.click(await screen.findByRole("button", { name: "Complete" }));
    fireEvent.change(screen.getByLabelText("Downtime in minutes"), { target: { value: "90" } });
    fireEvent.click(screen.getByRole("button", { name: "Complete work order" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Work order moved to completed.");
    const [call] = posts("/status");
    expect(call.body).toEqual({ to: "COMPLETED", downtimeMinutes: 90 });
    expect(header(call, "If-Match")).toBe('"5"');
    expect(header(call, "Idempotency-Key")).toBeTruthy();
  });

  it("explains a version conflict", async () => {
    fakeOrder(orderDetail, (url) =>
      url.pathname === `/api/v1/eam/work-orders/${WO}/status` ? json(412, { code: "conflict_version", detail: "work order version changed" }) : undefined,
    );
    renderAt(`/eam/work-orders/${WO}`);
    fireEvent.click(await screen.findByRole("button", { name: "Move to in progress" }));
    expect(await screen.findByText("Someone else changed this work order. Reload it and try again.")).toBeInTheDocument();
  });

  it("books labor as decimal strings and rejects bad hours before sending", async () => {
    fakeOrder(orderDetail, (url) => (url.pathname === `/api/v1/eam/work-orders/${WO}/labor` ? json(201, order) : undefined));
    renderAt(`/eam/work-orders/${WO}`);
    fireEvent.change(await screen.findByLabelText("Hours"), { target: { value: "0" } });
    fireEvent.change(screen.getByLabelText("Hourly rate"), { target: { value: "45.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Book labor" }));
    expect(await screen.findByText("Enter hours above 0, with up to 2 decimals.")).toBeInTheDocument();
    expect(posts("/labor")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Hours"), { target: { value: "2.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Book labor" }));
    expect(await screen.findByRole("status")).toHaveTextContent("2.5 h booked.");
    expect(posts("/labor")[0].body).toEqual({ hours: "2.5", rate: "45.00" });
  });

  it("offers no labor form on a completed order", async () => {
    fakeOrder({ ...orderDetail, status: "COMPLETED" });
    renderAt(`/eam/work-orders/${WO}`);
    expect(await screen.findByText("Labor can only be booked on an open work order.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Hours")).not.toBeInTheDocument();
  });
});

describe("requests", () => {
  const request: MaintenanceRequest = { id: REQ, assetId: PUMP, source: "IOT", severity: 1, description: "bearing hot breached: reading 87", readingValue: 87, status: "NEW", createdAt: "2026-10-21T09:00:00Z" };

  it("lists new requests and converts one to a work order in one action", async () => {
    handler = (url, init) => {
      if (get("/requests")(url, init)) return json(200, { items: [request] });
      if (url.pathname === `/api/v1/eam/requests/${REQ}/convert`) return json(201, { ...order, workOrderType: "EMERGENCY", priority: 1 });
      return undefined;
    };
    const { container } = renderAt("/eam/requests");
    const table = await screen.findByRole("table", { name: "Maintenance requests" });
    expect(calls[0].url.searchParams.get("status")).toBe("NEW");
    expect(within(table).getByText("IoT rule")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "Convert to work order" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/eam/work-orders/${WO}`));
    expect(header(posts("/convert")[0], "Idempotency-Key")).toBeTruthy();
  });

  it("shows every status when the filter is cleared and links converted requests", async () => {
    handler = (url, init) => (get("/requests")(url, init) ? json(200, { items: [{ ...request, status: "CONVERTED", workOrderId: WO }] }) : undefined);
    renderAt("/eam/requests?status=ALL");
    expect(await screen.findByRole("link", { name: "Open work order" })).toHaveAttribute("href", `/eam/work-orders/${WO}`);
    expect(calls[0].url.searchParams.has("status")).toBe(false);
  });

  it("raises a request against an asset", async () => {
    handler = (url, init) => {
      if (get("/assets")(url, init)) return json(200, { items: [pump] });
      if (url.pathname === "/api/v1/eam/requests" && init.method === "POST") return json(201, request);
      return undefined;
    };
    renderAt("/eam/requests/new");
    fireEvent.change(await screen.findByLabelText("Asset"), { target: { value: PUMP } });
    fireEvent.click(screen.getByRole("button", { name: "Create request" }));
    expect(await screen.findByText("Describe the problem in up to 1,000 characters.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Leaking seal" } });
    fireEvent.click(screen.getByRole("button", { name: "Create request" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/eam/requests"));
    expect(posts("/requests")[0].body).toEqual({ assetId: PUMP, source: "USER", severity: 3, description: "Leaking seal" });
  });
});

describe("plans", () => {
  const plan: Plan = {
    id: "p1",
    code: "PM-PUMP",
    name: "Pump PM",
    currency: "USD",
    active: true,
    lines: [
      {
        id: "l1",
        assetId: PUMP,
        kind: "TIME_OR_COUNTER",
        intervalValue: 90,
        intervalUnit: "DAYS",
        counterId: HOURS,
        counterInterval: 500,
        jobType: "OVERHAUL",
        earlyTolerancePct: 10,
        horizonDays: 90,
        startAt: "2026-08-01T00:00:00Z",
        lastCompletedReading: 12000,
        nextDueAt: "2026-10-30T00:00:00Z",
        nextDueReading: 12500,
        nextDueForecastAt: "2026-11-21T00:00:00Z",
      },
    ],
  };

  it("lists plans and shows the next due point of each line", async () => {
    handler = (url, init) => (get("/plans")(url, init) ? json(200, { items: [plan] }) : undefined);
    const { container } = renderAt("/eam/plans");
    const table = await screen.findByRole("table", { name: "Maintenance plans" });
    expect(within(table).getByText("2026-10-30")).toBeInTheDocument();
    fireEvent.click(within(table).getByRole("button", { name: "PM-PUMP" }));
    const lines = screen.getByRole("table", { name: "Lines of PM-PUMP" });
    expect(within(lines).getByText("90 days or 500 on the counter")).toBeInTheDocument();
    expect(within(lines).getByText("12500")).toBeInTheDocument();
    expect(within(lines).getByText("2026-11-21")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("checks plan lines: counter lines need a counter and an interval", () => {
    const line = { assetId: PUMP, kind: "COUNTER" as const, intervalValue: "", intervalUnit: "DAYS" as const, counterId: "", counterInterval: "0", jobType: "lube", horizonDays: "90", startAt: "" };
    const { errors, input } = validatePlan({ code: "pm-1", name: "PM", currency: "USD", lines: [line] });
    expect(input).toBeUndefined();
    expect(Object.keys(errors.lines[0]).sort()).toEqual(["counterId", "counterInterval"]);
    const ok = validatePlan({ code: "pm-1", name: "PM", currency: "usd", lines: [{ ...line, kind: "TIME", intervalValue: "30" }] });
    expect(ok.input).toEqual({ code: "PM-1", name: "PM", currency: "USD", lines: [{ assetId: PUMP, kind: "TIME", jobType: "LUBE", intervalValue: 30, intervalUnit: "DAYS", horizonDays: 90 }] });
  });

  it("creates a counter-based plan with the asset's counter", async () => {
    handler = (url, init) => {
      if (get("/assets")(url, init)) return json(200, { items: [pump] });
      if (get(`/assets/${PUMP}`)(url, init)) return json(200, pumpDetail);
      if (url.pathname === "/api/v1/eam/plans" && init.method === "POST") return json(201, { id: "p1", code: "PM-PUMP", lineIds: ["l1"] });
      return undefined;
    };
    renderAt("/eam/plans/new");
    fireEvent.change(await screen.findByLabelText("Plan code"), { target: { value: "pm-pump" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Pump PM" } });
    fireEvent.change(screen.getByLabelText("Asset"), { target: { value: PUMP } });
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "COUNTER" } });
    fireEvent.change(screen.getByLabelText("Job type"), { target: { value: "lube" } });
    await screen.findByRole("option", { name: "Run hours (h)" });
    fireEvent.change(screen.getByLabelText("Counter"), { target: { value: HOURS } });
    fireEvent.change(screen.getByLabelText("Counter interval"), { target: { value: "500" } });
    fireEvent.click(screen.getByRole("button", { name: "Create plan" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/eam/plans"));
    expect(posts("/plans")[0].body).toEqual({
      code: "PM-PUMP",
      name: "Pump PM",
      currency: "USD",
      lines: [{ assetId: PUMP, kind: "COUNTER", jobType: "LUBE", counterId: HOURS, counterInterval: 500, horizonDays: 90 }],
    });
  });

  it("shows a duplicate code as a field error", async () => {
    handler = (url, init) => {
      if (get("/assets")(url, init)) return json(200, { items: [pump] });
      if (url.pathname === "/api/v1/eam/plans" && init.method === "POST") return json(409, { code: "EAM-DUP-001", detail: "exists" });
      return undefined;
    };
    renderAt("/eam/plans/new");
    fireEvent.change(await screen.findByLabelText("Plan code"), { target: { value: "PM-PUMP" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Pump PM" } });
    fireEvent.change(screen.getByLabelText("Asset"), { target: { value: PUMP } });
    fireEvent.change(screen.getByLabelText("Every"), { target: { value: "30" } });
    fireEvent.change(screen.getByLabelText("Job type"), { target: { value: "LUBE" } });
    fireEvent.click(screen.getByRole("button", { name: "Create plan" }));
    expect(await screen.findByText("A plan with this code already exists.")).toBeInTheDocument();
  });
});
