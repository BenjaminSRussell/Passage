import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { AssetDetail, IsolationPoint, LotoLock, Permit, PermitDetail, PermitType, WorkOrder, WorkOrderDetail } from "./api/client";
import { nextPermitStatuses, permitLockable } from "./format";
import eam from "./module";
import { validateIsolationPoint } from "./PermitCare";
import { validatePermit } from "./PermitPages";
import { validatePermitType } from "./PermitTypesPage";

// Slice 3 screens: permit register, permit request, permit detail with LOTO locks, permit types and job
// requirements, isolation points on the asset, and the permits card on the work order (FR-44.19, BR-44.11).

const PUMP = "0195f0a0-0000-7000-8000-000000000001";
const SITE = "0195f0a0-0000-7000-8000-0000000000aa";
const WO = "0195f0a0-0000-7000-8000-0000000000d1";
const PMT = "0195f0a0-0000-7000-8000-0000000000b1";
const ISO1 = "0195f0a0-0000-7000-8000-0000000000c1";
const ISO2 = "0195f0a0-0000-7000-8000-0000000000c2";

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

const isGet = (path: string) => (url: URL, init: RequestInit) => (init.method ?? "GET") === "GET" && url.pathname === `/api/v1/eam${path}`;
const isCall = (method: string, path: string) => (url: URL, init: RequestInit) => init.method === method && url.pathname === `/api/v1/eam${path}`;
const sent = (method: string, suffix: string) => calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const elecIso: PermitType = {
  code: "ELECTRICAL_ISOLATION",
  name: "Electrical isolation",
  approverRole: "Area authority",
  maxValidityHours: 12,
  requiresIsolation: true,
  requiresGasTest: false,
};

const permit: Permit = {
  id: PMT,
  number: "EAM-PMT-1",
  permitTypeCode: "ELECTRICAL_ISOLATION",
  workOrderId: WO,
  assetId: PUMP,
  status: "REQUESTED",
  validFrom: "2026-10-21T08:00:00Z",
  validTo: "2026-10-21T16:00:00Z",
  requestedBy: "planner-1",
  locksOpen: 0,
  version: 1,
  createdAt: "2026-10-21T07:00:00Z",
};

const points: IsolationPoint[] = [
  { id: ISO1, assetId: PUMP, energyType: "ELECTRICAL", location: "MCC-1", method: "breaker padlock", verificationStep: "try start", tagCode: "ISO-1", createdAt: "2026-10-01T00:00:00Z" },
  { id: ISO2, assetId: PUMP, energyType: "PNEUMATIC", location: "Air header", method: "valve lock", verificationStep: "bleed and gauge", tagCode: "ISO-2", createdAt: "2026-10-01T00:00:00Z" },
];

const verifiedLock: LotoLock = {
  id: "lock-1",
  permitId: PMT,
  isolationPointId: ISO1,
  technicianId: "tech-1",
  lockNumber: "LOCK-1",
  state: "VERIFIED",
  appliedAt: "2026-10-21T08:05:00Z",
  verifiedAt: "2026-10-21T08:06:00Z",
};

const detailOf = (p: Partial<Permit>, rest: Partial<PermitDetail> = {}): PermitDetail => ({
  permit: { ...permit, ...p },
  isolationPoints: points,
  locks: [],
  unlockedPoints: 2,
  history: [{ from: "", to: "REQUESTED", at: "2026-10-21T07:00:00Z", actor: "planner-1" }],
  ...rest,
});

const openOrder: WorkOrder = {
  id: WO,
  number: "WO-7",
  assetId: PUMP,
  workOrderType: "CORRECTIVE",
  status: "RELEASED",
  priority: 2,
  currency: "USD",
  jobType: "ELEC_REPAIR",
  downtimeMinutes: 0,
  partsCost: "0",
  laborCost: "0",
  warrantyCovered: false,
  version: 3,
  createdAt: "2026-10-21T07:00:00Z",
};

describe("permit module wiring", () => {
  it("registers the permit register, request form, detail and setup pages behind eam.inquire", () => {
    const paths = eam.routes.map((r) => r.path);
    for (const path of ["/eam/permits", "/eam/permits/new", "/eam/permits/:id", "/eam/permit-types"]) expect(paths).toContain(path);
    expect(eam.nav.filter((n) => n.to.startsWith("/eam/permit")).map((n) => n.label)).toEqual(["Permits", "Permit types"]);
  });

  it("mirrors the permit lifecycle and the lockable states of BR-44.11", () => {
    expect(nextPermitStatuses("REQUESTED")).toEqual(["APPROVED", "CANCELED"]);
    expect(nextPermitStatuses("ISSUED")).toEqual(["SUSPENDED", "CLOSED"]);
    expect(nextPermitStatuses("SUSPENDED")).toEqual(["ISSUED", "CLOSED"]);
    expect(nextPermitStatuses("CLOSED")).toEqual([]);
    expect(["REQUESTED", "APPROVED", "ISSUED", "SUSPENDED", "CLOSED"].map((s) => permitLockable(s as Permit["status"]))).toEqual([false, true, true, true, false]);
  });
});

describe("permit form checks", () => {
  const base = { workOrderId: WO, permitTypeCode: "ELECTRICAL_ISOLATION", validFrom: "2026-10-21T08:00", validTo: "2026-10-21T16:00" };

  it("sends the window as UTC instants", () => {
    expect(validatePermit(base, [elecIso]).input).toEqual({
      workOrderId: WO,
      permitTypeCode: "ELECTRICAL_ISOLATION",
      validFrom: "2026-10-21T08:00:00.000Z",
      validTo: "2026-10-21T16:00:00.000Z",
    });
  });

  it("refuses a window past the type's maximum validity, a reversed window and missing choices", () => {
    expect(validatePermit({ ...base, validTo: "2026-10-21T20:01" }, [elecIso]).errors.validTo).toMatch(/at most 12 hours/);
    expect(validatePermit({ ...base, validTo: "2026-10-21T07:00" }, [elecIso]).errors.validTo).toMatch(/end after it starts/);
    expect(Object.keys(validatePermit({ ...base, workOrderId: "", permitTypeCode: "NOPE", validFrom: "" }, [elecIso]).errors).sort()).toEqual([
      "permitTypeCode",
      "validFrom",
      "workOrderId",
    ]);
  });

  it("checks permit types and isolation points as the service does", () => {
    const type = { code: "hot_work", name: "Hot work", approverRole: "Safety officer", maxValidityHours: "8", requiresIsolation: false, requiresGasTest: true };
    expect(validatePermitType(type).input).toEqual({ code: "HOT_WORK", name: "Hot work", approverRole: "Safety officer", maxValidityHours: 8, requiresIsolation: false, requiresGasTest: true });
    expect(Object.keys(validatePermitType({ ...type, code: "x", maxValidityHours: "169", approverRole: "" }).errors).sort()).toEqual(["approverRole", "code", "maxValidityHours"]);
    const point = { energyType: "ELECTRICAL" as const, location: " MCC-1 ", method: "breaker", verificationStep: "try start", tagCode: "ISO-1" };
    expect(validateIsolationPoint(point).input?.location).toBe("MCC-1");
    expect(Object.keys(validateIsolationPoint({ ...point, tagCode: " ", verificationStep: "" }).errors).sort()).toEqual(["tagCode", "verificationStep"]);
  });
});

describe("permit register", () => {
  it("lists permits with their work order, validity and open locks, and opens one", async () => {
    handler = (url, init) => (isGet("/permits")(url, init) ? json(200, { items: [{ ...permit, status: "ISSUED", locksOpen: 3 }] }) : undefined);
    const { container } = renderAt("/eam/permits");
    const table = await findTable({ name: "Permits to work" });
    expect(within(table).getByRole("link", { name: "EAM-PMT-1" })).toHaveAttribute("href", `/eam/permits/${PMT}`);
    expect(within(table).getByText("Issued")).toBeInTheDocument();
    expect(within(table).getByText("3")).toBeInTheDocument();
    expect(within(table).getByText("2026-10-21 16:00")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by work order from the link on the work order and keeps it when requesting", async () => {
    handler = (url, init) => (isGet("/permits")(url, init) ? json(200, { items: [] }) : undefined);
    renderAt(`/eam/permits?workOrder=${WO}`);
    expect(await screen.findByText("No permits match")).toBeInTheDocument();
    expect(sent("GET", "/permits")[0].url.searchParams.get("workOrder")).toBe(WO);
    expect(screen.getByRole("link", { name: "Request permit" })).toHaveAttribute("href", `/eam/permits/new?workOrder=${WO}`);
  });

  it("shows the load error with a retry", async () => {
    handler = (url, init) => (isGet("/permits")(url, init) ? json(403, { code: "AUTH-0003", detail: "missing duty eam.inquire" }) : undefined);
    renderAt("/eam/permits");
    expect(await findAlert()).toHaveTextContent("missing duty eam.inquire (AUTH-0003)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("runs the expiry check with a key and reports jobs left running (EAM-PMT-004)", async () => {
    handler = (url, init) => {
      if (isGet("/permits")(url, init)) return json(200, { items: [] });
      if (isCall("POST", "/permits/expire")(url, init))
        return json(200, { items: [{ permitId: PMT, number: "EAM-PMT-1", workOrderId: WO, validTo: permit.validTo, workOrderInProgress: true, code: "EAM-PMT-004" }] });
      return undefined;
    };
    renderAt("/eam/permits");
    fireEvent.click(await screen.findByRole("button", { name: "Suspend expired permits" }));
    expect(await findStatus()).toHaveTextContent("1 expired permit was suspended; 1 job is still in progress without a valid permit.");
    expect(header(sent("POST", "/permits/expire")[0], "Idempotency-Key")).toBeTruthy();
  });
});

describe("request a permit", () => {
  it("sends the request with one key and opens the new permit", async () => {
    handler = (url, init) => {
      if (isGet("/permit-types")(url, init)) return json(200, { items: [elecIso] });
      if (isGet("/work-orders")(url, init)) return json(200, { items: [openOrder, { ...openOrder, id: "closed", number: "WO-8", status: "CLOSED" }] });
      if (isCall("POST", "/permits")(url, init)) return json(201, permit);
      if (isGet(`/permits/${PMT}`)(url, init)) return json(200, detailOf({}));
      return undefined;
    };
    renderAt(`/eam/permits/new?workOrder=${WO}`);
    const wo = await screen.findByLabelText("Work order");
    expect(wo).toHaveValue(WO);
    expect(within(wo).queryByText(/WO-8/)).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Permit type"), { target: { value: "ELECTRICAL_ISOLATION" } });
    expect(screen.getByText(/Valid for at most 12 hours/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Valid from (UTC)"), { target: { value: "2026-10-21T08:00" } });
    fireEvent.change(screen.getByLabelText("Valid to (UTC)"), { target: { value: "2026-10-21T16:00" } });
    fireEvent.click(screen.getByRole("button", { name: "Request permit" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/eam/permits/${PMT}`));
    const [call] = sent("POST", "/permits");
    expect(call.body).toEqual({ workOrderId: WO, permitTypeCode: "ELECTRICAL_ISOLATION", validFrom: "2026-10-21T08:00:00.000Z", validTo: "2026-10-21T16:00:00.000Z" });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
  });

  it("points to permit setup when no permit type exists", async () => {
    handler = (url, init) => {
      if (isGet("/permit-types")(url, init)) return json(200, { items: [] });
      if (isGet("/work-orders")(url, init)) return json(200, { items: [openOrder] });
      return undefined;
    };
    renderAt("/eam/permits/new");
    expect(await screen.findByText("No permit types yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Set up permit types" })).toHaveAttribute("href", "/eam/permit-types");
  });
});

describe("permit detail", () => {
  it("shows the isolation points, approves with the version and reports separation of duties (EAM-PMT-005)", async () => {
    let attempt = 0;
    handler = (url, init) => {
      if (isGet(`/permits/${PMT}`)(url, init)) return json(200, detailOf({}));
      if (isCall("POST", `/permits/${PMT}/transition`)(url, init)) {
        attempt += 1;
        return attempt === 1
          ? json(422, { code: "EAM-PMT-005", detail: "approver must differ from requester" })
          : json(200, { ...permit, status: "APPROVED", version: 2, approvedBy: "issuer-1" });
      }
      return undefined;
    };
    const { container } = renderAt(`/eam/permits/${PMT}`);
    expect(await screen.findByRole("heading", { name: "Permit EAM-PMT-1" })).toBeInTheDocument();
    const iso = screen.getByRole("table", { name: "Isolation points" });
    expect(within(iso).getByText("ISO-2")).toBeInTheDocument();
    // Locks are refused while the permit is only REQUESTED, so no lock buttons yet.
    expect(within(iso).queryByRole("button", { name: "Apply my lock" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel permit" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Separation of duties")).toBeInTheDocument();
    expect(await findAlert()).toHaveTextContent("approver must differ from requester (EAM-PMT-005)");
    const [first] = sent("POST", "/transition");
    expect(first.body).toEqual({ to: "APPROVED" });
    expect(header(first, "If-Match")).toBe('"1"');

    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await findStatus()).toHaveTextContent("Permit EAM-PMT-1 is now approved.");
    const [, second] = sent("POST", "/transition");
    // The retry of the same action reuses the key, so it cannot apply twice.
    expect(header(second, "Idempotency-Key")).toBe(header(first, "Idempotency-Key"));
  });

  it("applies the caller's lock on an isolation point and lists the locks", async () => {
    handler = (url, init) => {
      if (isGet(`/permits/${PMT}`)(url, init)) return json(200, detailOf({ status: "APPROVED", version: 2, locksOpen: 1 }, { locks: [verifiedLock], unlockedPoints: 1 }));
      if (isCall("POST", `/permits/${PMT}/locks`)(url, init)) return json(201, { ...verifiedLock, id: "lock-2", isolationPointId: ISO2, lockNumber: "LOCK-2", state: "APPLIED" });
      return undefined;
    };
    renderAt(`/eam/permits/${PMT}`);
    const iso = await findTable({ name: "Isolation points" });
    const row = within(iso).getByText("ISO-2").closest("tr")!;
    expect(within(screen.getByRole("table", { name: "Personal locks" })).getByText("LOCK-1")).toBeInTheDocument();
    expect(within(iso).getByText("1 (1 verified)")).toBeInTheDocument();
    fireEvent.click(within(row).getByRole("button", { name: "Apply my lock" }));
    expect(await findStatus()).toHaveTextContent("Lock LOCK-2 on ISO-2 applied.");
    const [call] = sent("POST", "/locks");
    expect(call.body).toEqual({ action: "APPLY", isolationPointId: ISO2 });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
  });

  it("explains a lock gap at issue (EAM-PMT-002) and warns when the asset has no isolation points", async () => {
    handler = (url, init) => {
      if (isGet(`/permits/${PMT}`)(url, init)) return json(200, detailOf({ status: "APPROVED", version: 2 }, { isolationPoints: [], unlockedPoints: 0 }));
      if (isCall("POST", `/permits/${PMT}/transition`)(url, init))
        return json(422, { code: "EAM-PMT-002", detail: "the asset has no isolation points; define them before issuing an isolation permit" });
      return undefined;
    };
    renderAt(`/eam/permits/${PMT}`);
    expect(await screen.findByText("This asset has no isolation points")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Issue" }));
    expect(await screen.findByText("Every isolation point needs a verified lock first")).toBeInTheDocument();
  });

  it("shows not found for an unknown permit", async () => {
    handler = (url, init) => (isGet(`/permits/${PMT}`)(url, init) ? json(404, { code: "EAM-NF-001", detail: "permit not found" }) : undefined);
    renderAt(`/eam/permits/${PMT}`);
    expect(await screen.findByText("We couldn't find this permit")).toBeInTheDocument();
  });
});

describe("permit setup", () => {
  it("saves a permit type and the permit types a job type requires", async () => {
    let types: PermitType[] = [];
    handler = (url, init) => {
      if (isGet("/permit-types")(url, init)) return json(200, { items: types });
      if (isCall("POST", "/permit-types")(url, init)) {
        types = [elecIso];
        return json(200, elecIso);
      }
      if (isGet("/job-types/ELEC_REPAIR/permits")(url, init)) return json(200, { jobType: "ELEC_REPAIR", permitTypeCodes: [] });
      if (isCall("PUT", "/job-types/ELEC_REPAIR/permits")(url, init)) return json(200, { jobType: "ELEC_REPAIR", permitTypeCodes: ["ELECTRICAL_ISOLATION"] });
      return undefined;
    };
    const { container } = renderAt("/eam/permit-types");
    expect(await screen.findByText("No permit types yet")).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Save permit type" });
    fireEvent.change(within(form).getByLabelText("Code"), { target: { value: "electrical_isolation" } });
    fireEvent.change(within(form).getByLabelText("Name"), { target: { value: "Electrical isolation" } });
    fireEvent.change(within(form).getByLabelText(/Approver role/), { target: { value: "Area authority" } });
    fireEvent.click(within(form).getByLabelText(/Requires isolation/));
    fireEvent.click(within(form).getByRole("button", { name: "Save permit type" }));
    expect(await findStatus()).toHaveTextContent("Permit type ELECTRICAL_ISOLATION saved.");
    expect(sent("POST", "/permit-types")[0].body).toEqual(elecIso);
    expect(await within(await findTable({ name: "Permit types" })).findByText("Electrical isolation")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Job type"), { target: { value: "elec_repair" } });
    fireEvent.click(screen.getByRole("button", { name: "Show requirements" }));
    fireEvent.click(await screen.findByLabelText("Electrical isolation (ELECTRICAL_ISOLATION)"));
    fireEvent.click(screen.getByRole("button", { name: "Save requirement" }));
    expect(await screen.findByText("Job type ELEC_REPAIR now needs ELECTRICAL_ISOLATION before work starts.")).toBeInTheDocument();
    expect(sent("PUT", "/permits")[0].body).toEqual({ permitTypeCodes: ["ELECTRICAL_ISOLATION"] });
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("permits on the asset and the work order", () => {
  const pump: AssetDetail = {
    id: PUMP,
    assetNumber: "AST-1",
    name: "Feed pump",
    assetType: "PUMP",
    siteId: SITE,
    criticality: 4,
    lifecycleState: "ACTIVE",
    version: 2,
    createdAt: "2026-10-01T08:00:00Z",
    counters: [],
    rules: [],
    openWorkOrders: 0,
    partsCost: "0",
    laborCost: "0",
  };

  it("adds an isolation point to the asset with a key", async () => {
    let list: IsolationPoint[] = [];
    handler = (url, init) => {
      if (isGet(`/assets/${PUMP}`)(url, init)) return json(200, pump);
      if (isGet("/kpis")(url, init)) return json(200, { failures: 0, mtbfHours: 0, mttrHours: 0, availability: 1 });
      if (isGet("/work-orders")(url, init)) return json(200, { items: [] });
      if (isGet(`/assets/${PUMP}/criticality`)(url, init)) return json(200, { items: [] });
      if (isGet(`/assets/${PUMP}/warranties`)(url, init)) return json(200, { items: [] });
      if (isGet(`/assets/${PUMP}/isolation-points`)(url, init)) return json(200, { items: list });
      if (isCall("POST", `/assets/${PUMP}/isolation-points`)(url, init)) {
        list = [points[0]];
        return json(201, points[0]);
      }
      return undefined;
    };
    renderAt(`/eam/assets/${PUMP}`);
    expect(await screen.findByText("No isolation points yet")).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Add isolation point" });
    fireEvent.click(within(form).getByRole("button", { name: "Add isolation point" }));
    expect(await within(form).findByText(/Enter the tag code/)).toBeInTheDocument();
    expect(sent("POST", "/isolation-points")).toHaveLength(0);
    fireEvent.change(within(form).getByLabelText("Tag code"), { target: { value: "ISO-1" } });
    fireEvent.change(within(form).getByLabelText("Location"), { target: { value: "MCC-1" } });
    fireEvent.change(within(form).getByLabelText("Isolation method"), { target: { value: "breaker padlock" } });
    fireEvent.change(within(form).getByLabelText("Verification step"), { target: { value: "try start" } });
    fireEvent.click(within(form).getByRole("button", { name: "Add isolation point" }));
    expect(await findStatus()).toHaveTextContent("Isolation point ISO-1 added.");
    const [call] = sent("POST", "/isolation-points");
    expect(call.body).toEqual({ energyType: "ELECTRICAL", location: "MCC-1", method: "breaker padlock", verificationStep: "try start", tagCode: "ISO-1" });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
    expect(await within(screen.getByRole("table", { name: "Isolation points" })).findByText("ISO-1")).toBeInTheDocument();
  });

  it("tells the planner which permits the job type needs before the order can start", async () => {
    const detail: WorkOrderDetail = { ...openOrder, assetType: "PUMP", downtime: [], labor: [], history: [] };
    handler = (url, init) => {
      if (isGet(`/work-orders/${WO}`)(url, init)) return json(200, detail);
      if (isGet("/job-types/ELEC_REPAIR/permits")(url, init)) return json(200, { jobType: "ELEC_REPAIR", permitTypeCodes: ["ELECTRICAL_ISOLATION"] });
      if (isGet("/permits")(url, init)) return json(200, { items: [{ ...permit, status: "APPROVED" }] });
      return undefined;
    };
    renderAt(`/eam/work-orders/${WO}`);
    expect(await screen.findByText(/requires an issued, valid ELECTRICAL_ISOLATION permit before the order can move to In progress/)).toBeInTheDocument();
    const table = await findTable({ name: "Permits for this work order" });
    expect(within(table).getByRole("link", { name: "EAM-PMT-1" })).toBeInTheDocument();
    expect(sent("GET", "/permits")[0].url.searchParams.get("workOrder")).toBe(WO);
    expect(screen.getByRole("link", { name: "Request permit" })).toHaveAttribute("href", `/eam/permits/new?workOrder=${WO}`);
  });
});
