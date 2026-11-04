import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { ruleLines, validateAssign, validateBoard } from "./DispatchBoardPage";
import { validateScheduling, schedulingValues } from "./SchedulingPanel";
import { emptyTechnician, validateTechnician } from "./TechniciansPage";
import service from "./module";

const LE = "22222222-0000-4000-8000-000000000002";
const TERR = "55555555-0000-4000-8000-000000000005";
const TECH = "77777777-0000-4000-8000-000000000007";
const WO = "66666666-0000-4000-8000-000000000006";
const WO2 = "66666666-0000-4000-8000-000000000016";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const DISPATCHER = ["SvcInquire", "SvcWoMaintain", "SvcDispatch"];

function renderAt(path: string, duties: readonly string[] = DISPATCHER) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {service.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
          <Route path="*" element={<p>Elsewhere</p>} />
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const technician = (overrides: Record<string, unknown> = {}) => ({
  id: TECH,
  code: "ANA",
  name: "Ana Ruiz",
  legalEntityId: LE,
  territoryIds: [TERR],
  skills: ["HVAC"],
  timeZone: "America/Chicago",
  shiftDays: [1, 2, 3, 4, 5],
  shiftStartMinute: 480,
  shiftEndMinute: 1020,
  maxDailyMinutes: 480,
  overtimeAllowed: false,
  active: true,
  version: 1,
  ...overrides,
});

const workOrder = (overrides: Record<string, unknown> = {}) => ({
  id: WO,
  number: "SWO-000003",
  customerPartyId: "11111111-0000-4000-8000-000000000001",
  legalEntityId: LE,
  territoryId: TERR,
  estDurationMinutes: 120,
  currency: "USD",
  status: "UNSCHEDULED",
  lines: [],
  requiredSkills: ["HVAC"],
  address: "1 Main St",
  version: 1,
  ...overrides,
});

describe("validators", () => {
  it("validates a technician like the service does", () => {
    const empty = validateTechnician(emptyTechnician(), true);
    expect(Object.keys(empty.errors).sort()).toEqual(["code", "legalEntityId", "name", "territoryIds"]);
    const bad = validateTechnician(
      { ...emptyTechnician(), code: "ana", name: "A", legalEntityId: LE, territoryIds: "nope", skills: "ok, not ok!", timeZone: "Local", days: [], shiftStart: "17:00", shiftEnd: "08:00", maxDailyMinutes: "5", homeLatitude: "95" },
      true,
    );
    expect(Object.keys(bad.errors).sort()).toEqual(["code", "homeLatitude", "maxDailyMinutes", "shiftDays", "shiftStartMinute", "skills", "territoryIds", "timeZone"]);
    const good = validateTechnician({ ...emptyTechnician(), code: "ANA", name: " Ana ", legalEntityId: LE, territoryIds: `${TERR}, ${TERR}`, skills: "hvac, electrical, hvac", days: [5, 1] }, true);
    expect(good.input).toMatchObject({ code: "ANA", name: "Ana", territoryIds: [TERR], skills: ["HVAC", "ELECTRICAL"], shiftDays: [1, 5], shiftStartMinute: 480, shiftEndMinute: 1020 });
    expect(validateTechnician({ ...emptyTechnician(), name: "Ana", legalEntityId: LE, territoryIds: TERR }, false).input).not.toHaveProperty("code");
  });

  it("validates the board day and the assign form", () => {
    expect(Object.keys(validateBoard({ territoryId: "x", date: "soon", timeZone: "Local" }).errors).sort()).toEqual(["date", "territoryId", "timeZone"]);
    expect(validateBoard({ territoryId: TERR, date: "2026-10-26", timeZone: "" }).query).toEqual({ territoryId: TERR, date: "2026-10-26", timeZone: "UTC" });
    expect(Object.keys(validateAssign({ workOrderId: "", technicianId: "", start: "", pinned: false }).errors).sort()).toEqual(["start", "technicianId", "workOrderId"]);
    expect(validateAssign({ workOrderId: WO, technicianId: TECH, start: "2026-10-26T14:00", pinned: true }).start).toBe("2026-10-26T14:00:00Z");
  });

  it("turns a refusal into one line per broken rule", () => {
    expect(ruleLines({ status: 422, code: "SVC-DSP-003", message: "x", fields: { "SVC-DSP-003": "technician lacks skill WELDING", "SVC-DSP-007": "overlaps" } })).toEqual([
      "SVC-DSP-003: technician lacks skill WELDING",
      "SVC-DSP-007: overlaps",
    ]);
    expect(ruleLines({ status: 500, code: "COM-0500", message: "boom", fields: {} })).toEqual(["boom (COM-0500)"]);
  });

  it("validates scheduling data", () => {
    const base = schedulingValues(workOrder() as never);
    expect(validateScheduling(base).input).toMatchObject({ requiredSkills: ["HVAC"], windowStart: null, address: "1 Main St", estDurationMinutes: 120 });
    const bad = validateScheduling({ ...base, skills: "bad skill!", windowStart: "2026-10-26T10:00", windowEnd: "", latitude: "10", longitude: "", estDurationMinutes: "0" });
    expect(Object.keys(bad.errors).sort()).toEqual(["estDurationMinutes", "latitude", "requiredSkills", "windowStart"]);
    expect(validateScheduling({ ...base, windowStart: "2026-10-26T12:00", windowEnd: "2026-10-26T10:00" }).errors.windowEnd).toBeTruthy();
  });
});

describe("technicians", () => {
  it("lists technicians with loading, empty and error states", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const view = renderAt("/service/technicians");
    expect(await screen.findByText("No technicians yet")).toBeInTheDocument();
    view.unmount();

    handler = () => ({ status: 200, body: { items: [technician(), technician({ id: "x", code: "BOB", name: "Bob", active: false, skills: [] })] } });
    const filled = renderAt("/service/technicians");
    const table = await findTable({ name: "Technicians" });
    expect(within(table).getByText("ANA")).toBeInTheDocument();
    expect(within(table).getByText("Inactive")).toBeInTheDocument();
    expect(within(table).getAllByText("Mon, Tue, Wed, Thu, Fri 08:00–17:00")).toHaveLength(2);
    expect(pageViolations(filled.container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/Status/), { target: { value: "true" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("active") === "true")).toBe(true));
    filled.unmount();

    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "down" } });
    renderAt("/service/technicians");
    expect(await findAlert()).toHaveTextContent("COM-0500");
  });

  it("creates a technician with a key and goes to its page", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: technician(), etag: '"1"' } : { status: 200, body: technician(), etag: '"1"' });
    renderAt("/service/technicians/new");
    fireEvent.click(await screen.findByRole("button", { name: "Create technician" }));
    expect(screen.getByText("Use 2 to 20 characters of A–Z, 0–9, _ or -.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText(/^Code/), { target: { value: "ana" } });
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Ana Ruiz" } });
    fireEvent.change(screen.getByLabelText(/^Legal entity/), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText(/^Territories/), { target: { value: TERR } });
    fireEvent.change(screen.getByLabelText(/^Skills/), { target: { value: "hvac" } });
    fireEvent.click(screen.getByRole("button", { name: "Create technician" }));
    expect(await screen.findByRole("heading", { name: "Technician ANA" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/service/technicians");
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toMatchObject({ code: "ANA", name: "Ana Ruiz", skills: ["HVAC"], territoryIds: [TERR], maxDailyMinutes: 480 });
  });

  it("explains a taken code", async () => {
    handler = () => ({ status: 409, body: { code: "SVC-TECH-001", detail: "a technician with this code already exists" } });
    renderAt("/service/technicians/new");
    fireEvent.change(await screen.findByLabelText(/^Code/), { target: { value: "ANA" } });
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Ana" } });
    fireEvent.change(screen.getByLabelText(/^Legal entity/), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText(/^Territories/), { target: { value: TERR } });
    fireEvent.click(screen.getByRole("button", { name: "Create technician" }));
    expect(await findAlert()).toHaveTextContent("That code is taken");
  });

  it("edits a technician at its ETag, and reloads after a version conflict", async () => {
    let version = 1;
    handler = (_url, init) => {
      if (init.method === "PUT") {
        version += 1;
        return { status: 200, body: technician({ name: "Ana R.", version }), etag: `"${version}"` };
      }
      return { status: 200, body: technician({ version }), etag: `"${version}"` };
    };
    renderAt(`/service/technicians/${TECH}`);
    fireEvent.change(await screen.findByLabelText(/^Name/), { target: { value: "Ana R." } });
    expect(screen.getByLabelText(/^Code/)).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Save technician" }));
    expect(await findStatus()).toHaveTextContent("Technician saved.");
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.headers.get("If-Match")).toBe('"1"');
    expect(put?.body).not.toHaveProperty("code");
    expect(put?.body).toMatchObject({ name: "Ana R.", active: true });

    handler = (_url, init) =>
      init.method === "PUT" ? { status: 412, body: { code: "conflict_version", detail: "current version is 5" } } : { status: 200, body: technician({ version: 5 }), etag: '"5"' };
    fireEvent.click(screen.getByRole("button", { name: "Save technician" }));
    expect(await findAlert()).toHaveTextContent("Someone else changed this technician");
  });

  it("shows why a technician cannot be deactivated", async () => {
    handler = (_url, init) =>
      init.method === "PUT" ? { status: 422, body: { code: "SVC-TECH-003", detail: "the technician still has 2 work orders ahead" } } : { status: 200, body: technician(), etag: '"1"' };
    renderAt(`/service/technicians/${TECH}`);
    fireEvent.click(await screen.findByLabelText("Active"));
    fireEvent.click(screen.getByRole("button", { name: "Save technician" }));
    expect(await findAlert()).toHaveTextContent("SVC-TECH-003");
  });

  it("lists the scheduled visits an edit would break", async () => {
    handler = (_url, init) =>
      init.method === "PUT"
        ? { status: 422, body: { code: "SVC-TECH-005", detail: "the change breaks the dispatch rules for scheduled work orders SWO-000004", errors: { "SWO-000004": "SVC-DSP-003 technician lacks skill HVAC" } } }
        : { status: 200, body: technician(), etag: '"1"' };
    renderAt(`/service/technicians/${TECH}`);
    fireEvent.change(await screen.findByLabelText(/^Skills/), { target: { value: "ELECTRICAL" } });
    fireEvent.click(screen.getByRole("button", { name: "Save technician" }));
    expect(await findAlert()).toHaveTextContent("SWO-000004: SVC-DSP-003 technician lacks skill HVAC");
  });

  it("shows a technician read-only, without create or save, to a user without SvcDispatch", async () => {
    handler = (url) =>
      url.pathname.endsWith(TECH) ? { status: 200, body: technician(), etag: '"1"' } : { status: 200, body: { items: [technician()] } };
    const view = renderAt(`/service/technicians/${TECH}`, ["SvcInquire"]);
    expect(await screen.findByRole("heading", { name: "Technician ANA" })).toBeInTheDocument();
    expect(screen.getByText("America/Chicago")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save technician" })).not.toBeInTheDocument();
    expect(pageViolations(view.container)).toEqual([]);
    view.unmount();

    renderAt("/service/technicians", ["SvcInquire"]);
    expect(await screen.findByRole("heading", { name: "Technicians" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New technician" })).not.toBeInTheDocument();
  });

  it("reports a technician that cannot be loaded", async () => {
    handler = () => ({ status: 404, body: { code: "SVC-TECH-002", detail: "technician not found" } });
    renderAt(`/service/technicians/${TECH}`);
    expect(await findAlert()).toHaveTextContent("SVC-TECH-002");
  });
});

function board(overrides: Record<string, unknown> = {}) {
  return {
    territoryId: TERR,
    date: "2026-10-26",
    timeZone: "UTC",
    technicians: [
      {
        technician: technician(),
        bookedMinutes: 60,
        assignments: [
          workOrder({ id: WO2, number: "SWO-000004", status: "SCHEDULED", technicianId: TECH, assignedStart: "2026-10-26T09:00:00Z", assignedEnd: "2026-10-26T10:00:00Z", version: 3 }),
        ],
      },
    ],
    unscheduled: [workOrder()],
    ...overrides,
  };
}

async function openBoard() {
  const view = renderAt("/service/dispatch");
  fireEvent.change(await screen.findByLabelText(/^Territory/), { target: { value: TERR } });
  fireEvent.click(screen.getByRole("button", { name: "Show board" }));
  await screen.findByRole("heading", { name: "ANA · Ana Ruiz" });
  return view;
}

describe("dispatch board", () => {
  it("is view only for a user without SvcDispatch", async () => {
    handler = () => ({ status: 200, body: board() });
    renderAt("/service/dispatch", ["SvcInquire"]);
    fireEvent.change(await screen.findByLabelText(/^Territory/), { target: { value: TERR } });
    fireEvent.click(screen.getByRole("button", { name: "Show board" }));
    await screen.findByRole("heading", { name: "ANA · Ana Ruiz" });
    expect(screen.getByText(/need the SvcDispatch duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Assign" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Unassign SWO-000004" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Publish day" })).not.toBeInTheDocument();
  });

  it("asks for a territory and day before it loads anything", async () => {
    handler = () => ({ status: 200, body: board() });
    renderAt("/service/dispatch");
    expect(await screen.findByText("Pick a territory and a day")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show board" }));
    expect(screen.getByText("Enter a territory UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("shows technicians with their day and the unscheduled work orders", async () => {
    handler = () => ({ status: 200, body: board() });
    const view = await openBoard();
    expect(calls[0]?.url.pathname).toBe("/api/v1/service/dispatch/board");
    expect(calls[0]?.url.searchParams.get("territoryId")).toBe(TERR);
    expect(screen.getByText(/1 h booked of 8 h/)).toBeInTheDocument();
    expect(within(await findTable({ name: "Assignments of ANA" })).getByText("SWO-000004")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Unscheduled work orders" })).getByText("SWO-000003")).toBeInTheDocument();
    expect(pageViolations(view.container)).toEqual([]);
  });

  it("assigns a work order at its ETag and reloads the board", async () => {
    let loads = 0;
    handler = (_url, init) => {
      if (init.method === "POST") return { status: 200, body: workOrder({ status: "SCHEDULED", version: 2 }), etag: '"2"' };
      loads += 1;
      return { status: 200, body: board() };
    };
    await openBoard();
    fireEvent.change(screen.getByLabelText(/^Work order/), { target: { value: WO } });
    fireEvent.change(screen.getByLabelText(/^Technician/), { target: { value: TECH } });
    fireEvent.change(screen.getByLabelText(/^Start/), { target: { value: "2026-10-26T13:00" } });
    fireEvent.click(screen.getByLabelText("Pin the assignment"));
    fireEvent.click(screen.getByRole("button", { name: "Assign" }));
    expect(await findStatus()).toHaveTextContent("SWO-000003 is scheduled with ANA.");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/service/work-orders/${WO}:assign`);
    expect(post?.headers.get("If-Match")).toBe('"1"');
    expect(post?.body).toEqual({ technicianId: TECH, start: "2026-10-26T13:00:00Z", pinned: true });
    await waitFor(() => expect(loads).toBeGreaterThanOrEqual(2));
  });

  it("checks the assign form before it calls the service", async () => {
    handler = () => ({ status: 200, body: board() });
    await openBoard();
    fireEvent.click(screen.getByRole("button", { name: "Assign" }));
    expect(screen.getByText("Pick a work order.")).toBeInTheDocument();
    expect(screen.getByText("Pick a technician.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("lists every rule a refused assignment broke", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 422, body: { code: "SVC-DSP-003", detail: "x", errors: { "SVC-DSP-003": "technician lacks skill WELDING", "SVC-DSP-007": "the visit overlaps another booking" } } }
        : { status: 200, body: board() };
    await openBoard();
    fireEvent.change(screen.getByLabelText(/^Work order/), { target: { value: WO } });
    fireEvent.change(screen.getByLabelText(/^Technician/), { target: { value: TECH } });
    fireEvent.click(screen.getByRole("button", { name: "Assign" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("That assignment breaks a dispatch rule");
    expect(alert).toHaveTextContent("SVC-DSP-003: technician lacks skill WELDING");
    expect(alert).toHaveTextContent("SVC-DSP-007: the visit overlaps another booking");
  });

  it("unassigns a scheduled visit", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 200, body: workOrder({ version: 4 }), etag: '"4"' } : { status: 200, body: board() });
    await openBoard();
    fireEvent.click(await screen.findByRole("button", { name: "Unassign SWO-000004" }));
    expect(await findStatus()).toHaveTextContent("SWO-000004 is unscheduled.");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/service/work-orders/${WO2}:unassign`);
    expect(post?.headers.get("If-Match")).toBe('"3"');
  });

  it("publishes the day with a key and reports the count", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 201, body: { id: "r", legalEntityId: LE, territoryId: TERR, date: "2026-10-26", timeZone: "UTC", status: "PUBLISHED", assignments: [], dispatchedCount: 1, publishedBy: "u", publishedAt: "2026-10-26T08:00:00Z" } }
        : { status: 200, body: board() };
    await openBoard();
    fireEvent.click(screen.getByRole("button", { name: "Publish day" }));
    expect(await findStatus()).toHaveTextContent("Dispatched 1 work order for 2026-10-26.");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/service/dispatch/publish");
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ legalEntityId: LE, territoryId: TERR, date: "2026-10-26", timeZone: "UTC" });
  });

  it("has nothing to publish without scheduled visits, and shows the empty states", async () => {
    handler = () => ({ status: 200, body: board({ technicians: [], unscheduled: [] }) });
    renderAt("/service/dispatch");
    fireEvent.change(await screen.findByLabelText(/^Territory/), { target: { value: TERR } });
    fireEvent.click(screen.getByRole("button", { name: "Show board" }));
    expect(await screen.findByText("No active technician covers this territory")).toBeInTheDocument();
    expect(screen.getByText("Nothing waiting")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Publish day" })).toBeDisabled();
  });

  it("reports a board that cannot be loaded", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "down" } });
    renderAt("/service/dispatch");
    fireEvent.change(await screen.findByLabelText(/^Territory/), { target: { value: TERR } });
    fireEvent.click(screen.getByRole("button", { name: "Show board" }));
    expect(await findAlert()).toHaveTextContent("COM-0500");
  });
});

describe("work order scheduling", () => {
  it("saves scheduling data of an unscheduled work order at its ETag", async () => {
    handler = (_url, init) =>
      init.method === "PUT"
        ? { status: 200, body: workOrder({ requiredSkills: ["HVAC", "WELDING"], version: 2 }), etag: '"2"' }
        : { status: 200, body: workOrder(), etag: '"1"' };
    renderAt(`/service/work-orders/${WO}`);
    fireEvent.change(await screen.findByLabelText(/^Required skills/), { target: { value: "hvac, welding" } });
    fireEvent.click(screen.getByRole("button", { name: "Save scheduling data" }));
    expect(await findStatus()).toHaveTextContent("Scheduling data saved.");
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toBe(`/api/v1/service/work-orders/${WO}/scheduling`);
    expect(put?.headers.get("If-Match")).toBe('"1"');
    expect(put?.body).toMatchObject({ requiredSkills: ["HVAC", "WELDING"], estDurationMinutes: 120, windowStart: null });
  });

  it("is read-only once the work order is scheduled", async () => {
    handler = () => ({
      status: 200,
      body: workOrder({ status: "SCHEDULED", technicianId: TECH, assignedStart: "2026-10-26T09:00:00Z", assignedEnd: "2026-10-26T11:00:00Z", assignmentPinned: true, version: 2 }),
      etag: '"2"',
    });
    renderAt(`/service/work-orders/${WO}`);
    expect(await screen.findByText(/Scheduling data changes only while the work order is unscheduled/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save scheduling data" })).toBeNull();
    expect(screen.getByText("2026-10-26 09:00 UTC")).toBeInTheDocument();
  });

  it("shows the refusal of a scheduling change", async () => {
    handler = (_url, init) =>
      init.method === "PUT" ? { status: 422, body: { code: "SVC-WO-011", detail: "scheduling data of a SCHEDULED work order does not change" } } : { status: 200, body: workOrder(), etag: '"1"' };
    renderAt(`/service/work-orders/${WO}`);
    fireEvent.click(await screen.findByRole("button", { name: "Save scheduling data" }));
    expect(await findAlert()).toHaveTextContent("SVC-WO-011");
  });
});

describe("module", () => {
  it("registers the dispatch screens, gated on SvcInquire", () => {
    const paths = service.routes.map((r) => r.path);
    for (const p of ["/service/dispatch", "/service/technicians", "/service/technicians/new", "/service/technicians/:id"]) expect(paths).toContain(p);
    expect(service.nav.filter((n) => ["/service/dispatch", "/service/technicians"].includes(n.to)).map((n) => n.duty)).toEqual(["SvcInquire", "SvcInquire"]);
  });
});
