import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { LoadPlan, RoutePlan, RoutePlanSummary, ScreeningHold } from "./api/client";
import { validatePlan, type PlanValues } from "./LoadPlanPage";
import transport from "./module";
import { validateRoute, type RouteValues } from "./NewRoutePlanPage";
import { validateRelease } from "./ScreeningHoldDetailPage";

const LOAD = "0195f0a0-0000-7000-8000-0000000000a1";
const HOLD = "0195f0a0-0000-7000-8000-0000000000f1";
const PLAN = "0195f0a0-0000-7000-8000-0000000000f2";
const HASH = "a".repeat(64);

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const violations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const problem = (status: number, code: string, detail: string, errors?: Record<string, string>) => json(status, { code, detail, status, errors });

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
  return <p data-testid="location">{location.pathname + location.search}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {transport.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));

const hold = (extra: Partial<ScreeningHold> = {}): ScreeningHold => ({
  id: HOLD,
  loadId: LOAD,
  status: "OPEN",
  parties: [
    { role: "SOLD_TO", name: "Acme GmbH", status: "CLEAR" },
    { role: "SHIP_TO", name: "Northwind", status: "POTENTIAL_MATCH", code: "TMS-0133" },
  ],
  createdBy: "planner-1",
  createdAt: "2026-06-01T09:00:00Z",
  version: 1,
  ...extra,
});

describe("screening holds", () => {
  it("lists open holds with the held parties and filters by status", async () => {
    handler = (url) => (url.pathname === "/api/v1/transport/screening-holds" ? json(200, { items: [hold()] }) : undefined);
    const { container } = renderAt("/transport/screening-holds");
    const table = await findTable({ name: "Screening holds" });
    expect(within(table).getByText("Northwind (potential match)")).toBeInTheDocument();
    expect(within(table).queryByText(/Acme/)).not.toBeInTheDocument();
    expect(calls[0].url.searchParams.get("status")).toBe("OPEN");
    expect(violations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "RELEASED" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("RELEASED"));
  });

  it("shows the empty and error states", async () => {
    handler = () => json(200, { items: [] });
    const first = renderAt("/transport/screening-holds");
    expect(await screen.findByText("No loads on hold")).toBeInTheDocument();
    first.unmount();

    handler = () => problem(503, "COM-0503", "transport is down");
    renderAt("/transport/screening-holds");
    expect(await screen.findByText(/transport is down/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("validates the release reason like the service", () => {
    expect(validateRelease("  ")).toMatch(/why/);
    expect(validateRelease("x".repeat(501))).toMatch(/500/);
    expect(validateRelease("date of birth differs")).toBeUndefined();
  });

  // AC-34.16: the officer releases with a reason; the page then shows who and why.
  it("releases a held load with a reason", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/transport/screening-holds/${HOLD}` && !init.method) return json(200, hold());
      if (url.pathname.endsWith(`/screening-holds/${HOLD}:release`))
        return json(200, hold({ status: "RELEASED", releasedBy: "officer-1", releaseReason: "date of birth differs", releasedAt: "2026-06-01T10:00:00Z", version: 2 }));
      return undefined;
    };
    const { container } = renderAt(`/transport/screening-holds/${HOLD}`);
    expect(await screen.findByText("Load held for trade compliance (TMS-0133)")).toBeInTheDocument();
    expect(violations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Release hold" }));
    expect(await screen.findByText(/Say why/)).toBeInTheDocument();
    expect(posts(":release")).toHaveLength(0);

    fill("Reason", "  date of birth differs  ");
    fireEvent.click(screen.getByRole("button", { name: "Release hold" }));
    expect(await screen.findByText(/Released by officer-1/)).toBeInTheDocument();
    expect(posts(":release")[0].body).toEqual({ reason: "date of birth differs" });
    expect(screen.getByText("date of birth differs")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Release hold" })).not.toBeInTheDocument();
  });

  it("does not offer a release for a blocked party and shows a refused release", async () => {
    handler = (url) =>
      url.pathname === `/api/v1/transport/screening-holds/${HOLD}`
        ? json(200, hold({ parties: [{ role: "CONSIGNEE", name: "Listed Co", status: "BLOCKED", code: "TMS-0133" }] }))
        : undefined;
    const blocked = renderAt(`/transport/screening-holds/${HOLD}`);
    expect(await screen.findByText("Blocked by the screening list")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Release hold" })).not.toBeInTheDocument();
    blocked.unmount();

    handler = (url, init) => {
      if (url.pathname === `/api/v1/transport/screening-holds/${HOLD}` && !init.method) return json(200, hold());
      if (url.pathname.endsWith(":release")) return problem(403, "AUTH-0003", "missing duty transport.screening-hold.release");
      return undefined;
    };
    renderAt(`/transport/screening-holds/${HOLD}`);
    await screen.findByLabelText("Reason");
    fill("Reason", "cleared");
    fireEvent.click(screen.getByRole("button", { name: "Release hold" }));
    expect(await screen.findByText("The hold was not released")).toBeInTheDocument();
    expect(screen.getByText(/transport.screening-hold.release/)).toBeInTheDocument();
  });

  it("shows the error state of a missing hold", async () => {
    handler = () => problem(404, "TMS-0100", "screening hold not found");
    renderAt(`/transport/screening-holds/${HOLD}`);
    expect(await screen.findByText("Could not load the screening hold")).toBeInTheDocument();
  });
});

const summary = (extra: Partial<RoutePlanSummary> = {}): RoutePlanSummary => ({
  id: PLAN,
  requestHash: HASH,
  stopCount: 3,
  vehicleCount: 2,
  routeCount: 1,
  droppedCount: 1,
  totalDistanceKm: 21.4,
  timeLimitS: 20,
  createdBy: "planner-1",
  createdAt: "2026-06-01T09:00:00Z",
  ...extra,
});

const plan = (): RoutePlan => ({
  ...summary(),
  request: {
    depot: { x: 0, y: 0 },
    stops: [
      { id: "S1", point: { x: 5, y: 0 }, earliest: 0, latest: 120, serviceMin: 10, weightKg: 100, volumeL: 0, pallets: 1 },
      { id: "S2", point: { x: 0, y: 5 }, earliest: 60, latest: 240, serviceMin: 10, weightKg: 100, volumeL: 0, pallets: 1 },
      { id: "S3", point: { x: 90, y: 90 }, earliest: 0, latest: 10, serviceMin: 10, weightKg: 100, volumeL: 0, pallets: 1 },
    ],
    vehicles: [
      { id: "V1", capacityKg: 1000, capacityL: 0, pallets: 10 },
      { id: "V2", capacityKg: 1000, capacityL: 0, pallets: 10 },
    ],
  },
  solution: {
    routes: [
      { vehicleId: "V1", stops: [{ stopId: "S1", arrivalMin: 5, lateMin: 0 }, { stopId: "S2", arrivalMin: 130, lateMin: 0 }], distanceKm: 21.4, endMin: 150 },
      { vehicleId: "V2", stops: [], distanceKm: 0, endMin: 0 },
    ],
    dropped: ["S3"],
  },
  replayed: false,
  warnings: [{ code: "TMS-0104", severity: "warning", detail: "1 stops dropped as infeasible; see route report." }],
});

describe("route plans", () => {
  it("lists plans with dropped stops flagged, and the empty and error states", async () => {
    handler = (url) => (url.pathname === "/api/v1/transport/routes" ? json(200, { items: [summary(), summary({ id: HOLD, droppedCount: 0 })] }) : undefined);
    const { container, unmount } = renderAt("/transport/routes");
    const table = await findTable({ name: "Route plans" });
    expect(within(table).getByText("1 dropped")).toBeInTheDocument();
    expect(within(table).getByText("All served")).toBeInTheDocument();
    expect(within(table).getAllByText("21.4 km")).toHaveLength(2);
    expect(violations(container)).toEqual([]);
    unmount();

    handler = () => json(200, { items: [] });
    const empty = renderAt("/transport/routes");
    expect(await screen.findByText("No route plans yet")).toBeInTheDocument();
    empty.unmount();

    handler = () => problem(500, "COM-0500", "internal error");
    renderAt("/transport/routes");
    expect(await screen.findByText("Could not load the route plans")).toBeInTheDocument();
  });

  const values = (extra: Partial<RouteValues> = {}): RouteValues => ({
    depotX: "0",
    depotY: "0",
    timeLimitS: "20",
    speedKmh: "60",
    latenessPerMin: "0",
    stops: [{ id: " S1 ", x: "5", y: "-2.5", earliest: "0", latest: "480", serviceMin: "10", weightKg: "100", volumeL: "0", pallets: "1" }],
    vehicles: [{ id: "V1", capacityKg: "1000", capacityL: "0", pallets: "10", fixedCost: "0", maxRouteMin: "660" }],
    ...extra,
  });

  it("validates like the service and keeps an explicit zero lateness penalty", () => {
    const ok = validateRoute(values());
    expect(ok.errors).toEqual({});
    expect(ok.input).toMatchObject({ latenessPerMin: 0, timeLimitS: 20, stops: [{ id: "S1", point: { x: 5, y: -2.5 }, latest: 480 }] });

    const bad = validateRoute(
      values({
        timeLimitS: "60",
        stops: [
          { id: "S1", x: "5", y: "0", earliest: "100", latest: "10", serviceMin: "0", weightKg: "0", volumeL: "0", pallets: "0" },
          { id: "S1", x: "5", y: "0", earliest: "0", latest: "10", serviceMin: "0", weightKg: "0", volumeL: "0", pallets: "0" },
        ],
        vehicles: [],
      }),
    );
    expect(bad.input).toBeUndefined();
    expect(bad.errors.timeLimitS).toMatch(/5 to 25/);
    expect(bad.errors["stops[0]"]).toMatch(/earliest at or before latest/);
    expect(bad.errors["stops[1]"]).toMatch(/own id/);
    expect(bad.errors.vehicles).toMatch(/1 and 50/);
  });

  it("optimizes and opens the plan, and explains an unavailable optimizer", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/routes:optimize")) return json(201, plan());
      if (url.pathname === `/api/v1/transport/routes/${PLAN}`) return json(200, plan());
      return undefined;
    };
    renderAt("/transport/routes/new");
    fill("Stop 1 stop reference", "S1");
    fill("Stop 1 x km", "5");
    fill("Stop 1 y km", "0");
    fill("Vehicle 1 vehicle reference", "V1");
    fill("Vehicle 1 capacity kg", "1000");
    fireEvent.click(screen.getByRole("button", { name: "Optimize routes" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/transport/routes/${PLAN}`));
    const sent = posts("/routes:optimize")[0].body as { stops: { id: string; point: { x: number } }[]; latenessPerMin: number };
    expect(sent.stops[0]).toMatchObject({ id: "S1", point: { x: 5, y: 0 } });
    expect(sent.latenessPerMin).toBe(50);

    handler = () => problem(503, "TMS-0123", "the route optimizer is not configured (TMS_OPTIMIZER_URL)");
    renderAt("/transport/routes/new");
    fill("Stop 1 stop reference", "S1");
    fill("Stop 1 x km", "5");
    fill("Stop 1 y km", "0");
    fill("Vehicle 1 vehicle reference", "V1");
    fill("Vehicle 1 capacity kg", "1000");
    fireEvent.click(screen.getByRole("button", { name: "Optimize routes" }));
    expect(await screen.findByText("The route optimizer is unavailable")).toBeInTheDocument();
  });

  it("shows each used vehicle's route, the dropped stops and the TMS-0104 warning", async () => {
    handler = (url) => (url.pathname === `/api/v1/transport/routes/${PLAN}` ? json(200, plan()) : undefined);
    const { container } = renderAt(`/transport/routes/${PLAN}`);
    expect(await screen.findByText("Stops dropped (TMS-0104)")).toBeInTheDocument();
    const route = screen.getByRole("table", { name: "Route of vehicle V1" });
    expect(within(route).getByText("60–240 min")).toBeInTheDocument();
    expect(within(route).getByText("130 min")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Route of vehicle V2" })).not.toBeInTheDocument();
    expect(within(screen.getByRole("list")).getByText("S3")).toBeInTheDocument();
    expect(screen.getByText(HASH)).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
  });
});

describe("load plan", () => {
  const values = (extra: Partial<PlanValues> = {}): PlanValues => ({
    lengthMm: "13600",
    widthMm: "2480",
    maxHeightMm: "2700",
    maxWeightKg: "20000",
    pallets: [{ id: "P1", lengthMm: "1200", widthMm: "1000", heightMm: "900", weightKg: "500", stackable: "no", stopSeq: "2", maxStackKg: "" }],
    ...extra,
  });

  it("validates like the service", () => {
    const ok = validatePlan(values());
    expect(ok.input?.pallets[0]).toEqual({ id: "P1", lengthMm: 1200, widthMm: 1000, heightMm: 900, weightKg: 500, stackable: false, stopSeq: 2 });
    const bad = validatePlan(
      values({
        widthMm: "0",
        pallets: [
          { id: "P1", lengthMm: "1200", widthMm: "1000", heightMm: "0", weightKg: "1", stackable: "yes", stopSeq: "1", maxStackKg: "" },
          { id: "P1", lengthMm: "1200", widthMm: "1000", heightMm: "900", weightKg: "1", stackable: "yes", stopSeq: "1", maxStackKg: "" },
        ],
      }),
    );
    expect(bad.errors.widthMm).toMatch(/width/);
    expect(bad.errors["pallets[0]"]).toMatch(/above zero/);
    expect(bad.errors["pallets[1]"]).toMatch(/own id/);
  });

  // AC-34.18: the plan reports floor positions used; the page draws the top view.
  it("plans a load and shows the top view, floor positions, sequence and axle estimate", async () => {
    const result: LoadPlan = {
      placements: [
        { palletIds: ["P2", "P1"], xMm: 0, yMm: 0, lengthMm: 1200, widthMm: 1000, heightMm: 1800, weightKg: 1000 },
        { palletIds: ["P3"], xMm: 1000, yMm: 0, lengthMm: 1200, widthMm: 1000, heightMm: 900, weightKg: 400 },
      ],
      unplaced: ["P9"],
      floorPositionsUsed: 2,
      lengthUsedMm: 1200,
      layers: 2,
      totalWeightKg: 1400,
      loadSequence: ["P2", "P1", "P3"],
      axle: { kingpinKg: 1338, axleKg: 62 },
    };
    handler = (url) => (url.pathname.endsWith("/load-plans:stack") ? json(200, result) : undefined);
    const { container } = renderAt("/transport/load-plans");
    fill("Pallet 1 pallet reference", "P1");
    fill("Pallet 1 height mm", "900");
    fill("Pallet 1 weight kg", "500");
    fireEvent.click(screen.getByRole("button", { name: "Plan the load" }));
    expect(await screen.findByRole("img", { name: /2 floor positions used/ })).toBeInTheDocument();
    expect(screen.getByText(/P9 did not fit/)).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Floor positions" })).getByText("P2 / P1")).toBeInTheDocument();
    const stats = screen.getByRole("list", { name: "Load plan" });
    expect(stats.textContent).toContain("Kingpin (kg)");
    expect(within(stats).getByText("1338")).toBeInTheDocument();
    expect(posts("/load-plans:stack")[0].body).toMatchObject({ trailer: { lengthMm: 13600 }, pallets: [{ id: "P1", stackable: true, heightMm: 900 }] });
    expect(violations(container)).toEqual([]);
  });

  it("explains an unavailable planner", async () => {
    handler = () => problem(503, "TMS-0123", "the load planner is not configured (TMS_OPTIMIZER_URL)");
    renderAt("/transport/load-plans");
    fill("Pallet 1 pallet reference", "P1");
    fill("Pallet 1 height mm", "900");
    fill("Pallet 1 weight kg", "500");
    fireEvent.click(screen.getByRole("button", { name: "Plan the load" }));
    expect(await screen.findByText("The load planner is unavailable")).toBeInTheDocument();
  });
});
