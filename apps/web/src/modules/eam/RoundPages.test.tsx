import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { Asset, MaintenanceRound, WorkOrderDetail, WorkOrderStop } from "./api/client";
import eam from "./module";
import { validatePlan } from "./PlansPage";
import { roundSummary, validateRound } from "./RoundPages";

// Slice 5 screens: round register, round editor, round detail with an extra occurrence, the
// round stops card on a ROUND work order (FR-44.14, BR-44.9) and ROUND lines in the plan editor.

const SITE = "0195f0a0-0000-7000-8000-0000000000aa";
const OTHER_SITE = "0195f0a0-0000-7000-8000-0000000000ab";
const V1 = "0195f0a0-0000-7000-8000-000000000011";
const V2 = "0195f0a0-0000-7000-8000-000000000012";
const FAR = "0195f0a0-0000-7000-8000-000000000013";
const ROUND = "0195f0a0-0000-7000-8000-0000000000b1";
const WO = "0195f0a0-0000-7000-8000-0000000000d1";

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
      const reply = handler(url, init) ?? json(500, { code: "TEST-500", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
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
const isGet = (path: string) => (url: URL, init: RequestInit) => (init.method ?? "GET") === "GET" && url.pathname === `/api/v1/eam${path}`;
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));

const asset = (id: string, n: number, siteId = SITE): Asset => ({
  id,
  assetNumber: `AST-${n}`,
  name: `Valve ${n}`,
  assetType: "VALVE",
  siteId,
  criticality: 3,
  lifecycleState: "ACTIVE",
  version: 2,
  createdAt: "2026-10-01T08:00:00Z",
});
const assets = [asset(V1, 1), asset(V2, 2), asset(FAR, 3, OTHER_SITE)];

const round: MaintenanceRound = {
  id: ROUND,
  code: "WEEKLY-A",
  name: "Weekly area A",
  siteId: SITE,
  active: true,
  stops: [
    { sequence: 1, assetId: V1, jobType: "INSPECT", mandatory: false, travelMinutes: 0 },
    { sequence: 2, assetId: V2, jobType: "INSPECT", mandatory: true, travelMinutes: 5 },
  ],
  travelMinutes: 5,
  createdAt: "2026-10-01T08:00:00Z",
};

describe("round module wiring", () => {
  it("registers the round register, editor and detail and the KPI page, with nav entries", () => {
    const paths = eam.routes.map((r) => r.path);
    for (const path of ["/eam/rounds", "/eam/rounds/new", "/eam/rounds/:id", "/eam/kpis"]) expect(paths).toContain(path);
    expect(eam.nav.find((n) => n.to === "/eam/rounds")).toMatchObject({ label: "Rounds", duty: "eam.inquire" });
    expect(eam.nav.find((n) => n.to === "/eam/kpis")).toMatchObject({ label: "KPIs", duty: "eam.inquire" });
  });
});

describe("round form checks", () => {
  const stop = (assetId: string, jobType = "inspect", travelMinutes = "0", mandatory = false) => ({ assetId, jobType, travelMinutes, mandatory });

  it("numbers the stops in order, upper-cases codes and takes the first stop's site", () => {
    const { input } = validateRound({ code: "weekly-a", name: " Weekly area A ", stops: [stop(V1), stop(V2, "inspect", "5", true)] }, assets);
    expect(input).toEqual({ code: "WEEKLY-A", name: "Weekly area A", siteId: SITE, stops: round.stops });
  });

  it("refuses a stop on another site, a free-text job type and travel over 480 minutes", () => {
    const { errors, input } = validateRound({ code: "W", name: "", stops: [stop(V1, "check it", "481"), stop(FAR), stop("")] }, assets);
    expect(input).toBeUndefined();
    expect(errors.code).toMatch(/2 to 30/);
    expect(errors.name).toMatch(/120 characters/);
    expect(errors.stops[0]).toMatchObject({ jobType: expect.stringMatching(/job type code/), travelMinutes: "Enter 0 to 480 minutes." });
    expect(errors.stops[1].assetId).toBe("Every stop must be on the first stop's site.");
    expect(errors.stops[2].assetId).toBe("Choose the asset.");
  });

  it("summarises stops and travel", () => {
    expect(roundSummary(round)).toBe("2 stops · 5 min travel");
    expect(roundSummary({ stops: [round.stops[0]], travelMinutes: 0 })).toBe("1 stop · 0 min travel");
  });

  it("sends a ROUND plan line with the round and the interval only", () => {
    const line = { assetId: "", roundId: ROUND, kind: "ROUND" as const, intervalValue: "1", intervalUnit: "WEEKS" as const, counterId: "", counterInterval: "", jobType: "", horizonDays: "90", startAt: "" };
    expect(validatePlan({ code: "rounds", name: "Rounds", currency: "USD", lines: [line] }).input).toEqual({
      code: "ROUNDS",
      name: "Rounds",
      currency: "USD",
      lines: [{ roundId: ROUND, kind: "ROUND", intervalValue: 1, intervalUnit: "WEEKS", horizonDays: 90 }],
    });
    expect(validatePlan({ code: "rounds", name: "Rounds", currency: "USD", lines: [{ ...line, roundId: "" }] }).errors.lines[0]).toEqual({ roundId: "Choose the round." });
  });
});

describe("round register", () => {
  it("lists rounds with stops, mandatory stops and travel, and opens one", async () => {
    handler = (url, init) => (isGet("/rounds")(url, init) ? json(200, { items: [round] }) : undefined);
    const { container } = renderAt("/eam/rounds");
    const table = await findTable({ name: "Maintenance rounds" });
    const row = within(table).getAllByRole("row")[1];
    expect(row).toHaveTextContent("Weekly area A");
    expect(row).toHaveTextContent("5 min");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "WEEKLY-A" }));
    expect(screen.getByTestId("where")).toHaveTextContent(`/eam/rounds/${ROUND}`);
  });

  it("shows an empty register and a failed load", async () => {
    handler = (url, init) => (isGet("/rounds")(url, init) ? json(200, { items: [] }) : undefined);
    const { unmount } = renderAt("/eam/rounds");
    expect(await screen.findByText("No maintenance rounds yet")).toBeInTheDocument();
    unmount();
    handler = (url, init) => (isGet("/rounds")(url, init) ? json(503, { code: "COM-0503", detail: "down" }) : undefined);
    renderAt("/eam/rounds");
    expect(await findAlert()).toHaveTextContent("(COM-0503)");
  });
});

describe("round editor", () => {
  it("orders the stops and creates the round with an idempotency key", async () => {
    handler = (url, init) => {
      if (isGet("/assets")(url, init)) return json(200, { items: assets });
      if (init.method === "POST" && url.pathname === "/api/v1/eam/rounds") return json(201, round);
      return undefined;
    };
    const { container } = renderAt("/eam/rounds/new");
    fireEvent.change(await screen.findByLabelText("Round code"), { target: { value: "weekly-a" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Weekly area A" } });
    fireEvent.change(screen.getByLabelText("Asset", { selector: "#stop-0-asset" }), { target: { value: V2 } });
    fireEvent.change(screen.getByLabelText("Mandatory stop", { selector: "#stop-0-mandatory" }), { target: { value: "yes" } });
    fireEvent.change(screen.getByLabelText(/Travel to the first stop/, { selector: "#stop-0-travel" }), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Add stop" }));
    fireEvent.change(screen.getByLabelText("Asset", { selector: "#stop-1-asset" }), { target: { value: V1 } });
    // Swap them: V1 first with no travel, V2 second, mandatory, 5 minutes on.
    fireEvent.click(screen.getByRole("button", { name: "Move stop 2 up" }));
    fireEvent.change(screen.getByLabelText(/Travel to the first stop/, { selector: "#stop-0-travel" }), { target: { value: "0" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create round" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/eam/rounds/${ROUND}`));
    const [call] = posts("/rounds");
    expect(call.body).toEqual({ code: "WEEKLY-A", name: "Weekly area A", siteId: SITE, stops: round.stops });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
  });

  it("shows a rule the service refuses (EAM-RND-003) and a duplicate code on its field", async () => {
    let reply = json(422, { code: "EAM-RND-003", detail: "stop 1 job type WELD needs a HOT_WORK permit to work" });
    handler = (url, init) => {
      if (isGet("/assets")(url, init)) return json(200, { items: assets });
      if (init.method === "POST" && url.pathname === "/api/v1/eam/rounds") return reply;
      return undefined;
    };
    renderAt("/eam/rounds/new");
    fireEvent.change(await screen.findByLabelText("Round code"), { target: { value: "WELDS" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Welds" } });
    fireEvent.change(screen.getByLabelText("Asset", { selector: "#stop-0-asset" }), { target: { value: V1 } });
    fireEvent.change(screen.getByLabelText(/Job type/, { selector: "#stop-0-job" }), { target: { value: "WELD" } });
    fireEvent.click(screen.getByRole("button", { name: "Create round" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("The round breaks a rule");
    expect(alert).toHaveTextContent("HOT_WORK permit to work (EAM-RND-003)");
    reply = json(409, { code: "EAM-DUP-001", detail: "maintenance round WELDS already exists" });
    fireEvent.click(screen.getByRole("button", { name: "Create round" }));
    expect(await screen.findByText("A round with this code already exists.")).toBeInTheDocument();
  });

  it("asks for an asset first when there are none", async () => {
    handler = (url, init) => (isGet("/assets")(url, init) ? json(200, { items: [] }) : undefined);
    renderAt("/eam/rounds/new");
    expect(await screen.findByText("No assets yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create an asset" })).toHaveAttribute("href", "/eam/assets/new");
  });
});

describe("round detail", () => {
  it("shows the stops and opens an extra occurrence as a ROUND work order", async () => {
    handler = (url, init) => {
      if (isGet(`/rounds/${ROUND}`)(url, init)) return json(200, round);
      if (init.method === "POST" && url.pathname === `/api/v1/eam/rounds/${ROUND}/work-orders`) return json(201, { id: WO, number: "WO-9" });
      return undefined;
    };
    const { container } = renderAt(`/eam/rounds/${ROUND}`);
    expect(await screen.findByRole("heading", { name: "Weekly area A" })).toBeInTheDocument();
    const stops = screen.getByRole("table", { name: "Stops" });
    expect(within(stops).getAllByRole("row")).toHaveLength(3);
    expect(within(stops).getAllByRole("row")[2]).toHaveTextContent("Yes");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Work order currency"), { target: { value: "eur" } });
    fireEvent.click(screen.getByRole("button", { name: "Open round work order" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/eam/work-orders/${WO}`));
    const [call] = posts("/work-orders");
    expect(call.body).toEqual({ currency: "EUR" });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
  });

  it("explains an inactive stop asset (EAM-AST-001) and a missing round", async () => {
    handler = (url, init) => {
      if (isGet(`/rounds/${ROUND}`)(url, init)) return json(200, round);
      if (init.method === "POST") return json(422, { code: "EAM-AST-001", detail: "stop 2 asset AST-2 is INACTIVE; work orders need ACTIVE" });
      return undefined;
    };
    const { unmount } = renderAt(`/eam/rounds/${ROUND}`);
    fireEvent.click(await screen.findByRole("button", { name: "Open round work order" }));
    expect(await findAlert()).toHaveTextContent("Every stop's asset must be active");
    unmount();
    handler = (url, init) => (isGet(`/rounds/${ROUND}`)(url, init) ? json(404, { code: "EAM-NF-001", detail: "maintenance round not found" }) : undefined);
    renderAt(`/eam/rounds/${ROUND}`);
    expect(await screen.findByText("We couldn't find this round")).toBeInTheDocument();
  });
});

describe("round stops on the work order", () => {
  const stop = (sequence: number, mandatory: boolean, extra: Partial<WorkOrderStop> = {}): WorkOrderStop => ({
    sequence,
    assetId: sequence === 1 ? V1 : V2,
    jobType: "INSPECT",
    mandatory,
    travelMinutes: 0,
    ...extra,
  });
  const order: WorkOrderDetail = {
    id: WO,
    number: "WO-9",
    assetId: V1,
    workOrderType: "ROUND",
    status: "IN_PROGRESS",
    priority: 3,
    currency: "USD",
    downtimeMinutes: 0,
    partsCost: "0",
    laborCost: "0",
    warrantyCovered: false,
    version: 4,
    createdAt: "2026-10-21T08:00:00Z",
    assetType: "VALVE",
    labor: [],
    history: [],
    downtime: [],
    stops: [stop(1, false, { outcome: "DONE", recordedAt: "2026-10-21T09:00:00Z" }), stop(2, true)],
  };

  function fakeOrder(detail: WorkOrderDetail, extra: Handler = () => undefined) {
    let current = detail;
    handler = (url, init) => {
      const hit = extra(url, init);
      if (hit) return hit;
      if (init.method === "POST" && url.pathname === `/api/v1/eam/work-orders/${WO}/stops/2`) {
        const saved = stop(2, true, { outcome: "SKIPPED", reason: "flooded", requestId: "req-1", recordedAt: "2026-10-21T10:00:00Z" });
        current = { ...current, stops: [current.stops![0], saved] };
        return json(200, saved);
      }
      if (isGet(`/work-orders/${WO}`)(url, init)) return json(200, current);
      if (isGet("/permits")(url, init)) return json(200, { items: [] });
      if (isGet("/fault-catalogs/VALVE")(url, init)) return json(404, { code: "EAM-NF-001", detail: "fault catalog not found" });
      return undefined;
    };
  }

  it("records a mandatory skip with its reason and says a request was raised", async () => {
    fakeOrder(order);
    const { container } = renderAt(`/eam/work-orders/${WO}`);
    const table = await findTable({ name: "Round stops" });
    expect(within(table).getAllByRole("row")[1]).toHaveTextContent("Done");
    expect(screen.getByText(/1 of 2 stops have no outcome yet/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Skip stop 2" }));
    expect(await screen.findByText("Say why the stop was skipped.")).toBeInTheDocument();
    expect(posts("/stops/2")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText(/Stop 2 · INSPECT · mandatory/), { target: { value: "flooded" } });
    fireEvent.click(screen.getByRole("button", { name: "Skip stop 2" }));
    expect(await findStatus()).toHaveTextContent("Stop 2 skipped. A maintenance request was raised for its asset.");
    const [call] = posts("/stops/2");
    expect(call.body).toEqual({ outcome: "SKIPPED", reason: "flooded" });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
    expect(await screen.findByText("Every stop has an outcome.")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Round stops" })).getByRole("link", { name: "Request raised" })).toBeInTheDocument();
  });

  it("explains a refused completion while stops are open (EAM-RND-001)", async () => {
    fakeOrder(order, (url) =>
      url.pathname === `/api/v1/eam/work-orders/${WO}/status` ? json(422, { code: "EAM-RND-001", detail: "1 round stops have no outcome" }) : undefined,
    );
    renderAt(`/eam/work-orders/${WO}`);
    fireEvent.click(await screen.findByRole("button", { name: "Complete" }));
    fireEvent.click(screen.getByRole("button", { name: "Complete work order" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Record every round stop first");
    expect(alert).toHaveTextContent("(EAM-RND-001)");
  });

  it("asks to start the order before stops are recorded", async () => {
    fakeOrder({ ...order, status: "RELEASED" });
    renderAt(`/eam/work-orders/${WO}`);
    expect(await screen.findByText("Start the work order to record stop outcomes.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Skip stop 2" })).not.toBeInTheDocument();
  });
});

describe("plan editor round lines", () => {
  it("swaps the asset and job type for a round and sends a ROUND line", async () => {
    handler = (url, init) => {
      if (isGet("/assets")(url, init)) return json(200, { items: assets });
      if (isGet("/rounds")(url, init)) return json(200, { items: [round] });
      if (url.pathname === "/api/v1/eam/plans" && init.method === "POST") return json(201, { id: "p1", code: "ROUNDS", lineIds: ["l1"] });
      return undefined;
    };
    renderAt("/eam/plans/new");
    fireEvent.change(await screen.findByLabelText("Plan code"), { target: { value: "rounds" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Rounds" } });
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "ROUND" } });
    expect(screen.queryByLabelText("Asset")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/Job type/)).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("option", { name: "WEEKLY-A Weekly area A" })).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText("Round"), { target: { value: ROUND } });
    fireEvent.change(screen.getByLabelText("Every"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Unit"), { target: { value: "WEEKS" } });
    fireEvent.click(screen.getByRole("button", { name: "Create plan" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/eam/plans"));
    expect(posts("/plans")[0].body).toEqual({
      code: "ROUNDS",
      name: "Rounds",
      currency: "USD",
      lines: [{ roundId: ROUND, kind: "ROUND", intervalValue: 1, intervalUnit: "WEEKS", horizonDays: 90 }],
    });
  });
});
