import { findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { AssetDetail, FaultCatalog, MaintenanceRequest, WorkOrderDetail } from "./api/client";
import { previewCriticality, validateWarranty } from "./AssetCare";
import { catalogToText, parseCatalog } from "./FaultCatalogPage";
import { utcFromLocalInput } from "./format";
import eam from "./module";
import { validateDowntime } from "./WorkOrderCare";

// Slice 2 screens: lifecycle, criticality, warranties, faults, downtime, triage and the fault catalog designer.

const PUMP = "0195f0a0-0000-7000-8000-000000000001";
const SITE = "0195f0a0-0000-7000-8000-0000000000aa";
const WO = "0195f0a0-0000-7000-8000-0000000000d1";
const REQ = "0195f0a0-0000-7000-8000-0000000000e1";
const VENDOR = "0195f0a0-0000-7000-8000-0000000000f1";
const M12 = "0195f0a0-0000-7000-8000-000000000012";

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

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {eam.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const sent = (method: string, suffix: string) => calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const isGet = (path: string) => (url: URL, init: RequestInit) => (init.method ?? "GET") === "GET" && url.pathname === `/api/v1/eam${path}`;

const pump: AssetDetail = {
  id: PUMP,
  assetNumber: "AST-1",
  name: "Feed pump",
  assetType: "PUMP",
  siteId: SITE,
  criticality: 3,
  lifecycleState: "ACTIVE",
  version: 2,
  createdAt: "2026-10-01T08:00:00Z",
  counters: [],
  rules: [],
  openWorkOrders: 0,
  partsCost: "0",
  laborCost: "0",
};

const catalog: FaultCatalog = {
  assetType: "PUMP",
  version: 3,
  symptoms: [
    { code: "NOISE", text: "Abnormal noise" },
    { code: "LEAK", text: "Leak" },
  ],
  areas: [
    { code: "BEARING", text: "Bearing" },
    { code: "SEAL", text: "Seal" },
  ],
  types: [{ code: "WEAR", text: "Wear" }],
  causes: [{ code: "FATIGUE", text: "Fatigue" }],
  remedies: [{ code: "REPLACE", text: "Replace" }],
  allowedCombinations: [
    { symptom: "NOISE", area: "BEARING", type: "WEAR" },
    { symptom: "LEAK", area: "SEAL", type: "WEAR" },
  ],
};

const order: WorkOrderDetail = {
  id: WO,
  number: "WO-7",
  assetId: PUMP,
  assetType: "PUMP",
  workOrderType: "CORRECTIVE",
  status: "IN_PROGRESS",
  priority: 2,
  currency: "USD",
  downtimeMinutes: 0,
  partsCost: "0",
  laborCost: "0",
  warrantyCovered: true,
  warrantyId: "w-1",
  version: 5,
  createdAt: "2026-10-21T08:00:00Z",
  labor: [],
  history: [],
  downtime: [],
};

describe("slice 2 rules mirrored in the forms", () => {
  it("previews BR-44.15: worst consequence times likelihood, SAFETY 5 forces 5", () => {
    expect(previewCriticality({ safety: 3, environment: 1, production: 5, quality: 1, cost: 2 }, 4)).toEqual({ risk: 20, criticality: 5 });
    expect(previewCriticality({ safety: 5, environment: 1, production: 1, quality: 1, cost: 1 }, 1)).toEqual({ risk: 5, criticality: 5 });
    expect(previewCriticality({ safety: 1, environment: 1, production: 3, quality: 1, cost: 1 }, 5)).toEqual({ risk: 15, criticality: 4 });
    expect(previewCriticality({ safety: 1, environment: 1, production: 1, quality: 1, cost: 1 }, 4)).toEqual({ risk: 4, criticality: 1 });
  });

  it("checks a warranty: coverage, months, vendor and excluded job types", () => {
    const base = { warrantyType: "oem", vendorPartyId: VENDOR, startBasis: "PURCHASE" as const, startsOn: "2025-11-01", months: "24", partsCovered: true, laborCovered: false, exclusions: "paint, clean" };
    expect(validateWarranty(base).input).toEqual({
      warrantyType: "OEM",
      vendorPartyId: VENDOR,
      startBasis: "PURCHASE",
      startsOn: "2025-11-01",
      months: 24,
      partsCovered: true,
      laborCovered: false,
      exclusions: ["PAINT", "CLEAN"],
    });
    expect(validateWarranty({ ...base, partsCovered: false }).errors.partsCovered).toBeTruthy();
    expect(validateWarranty({ ...base, months: "241" }).errors.months).toBeTruthy();
    expect(validateWarranty({ ...base, vendorPartyId: "acme" }).errors.vendorPartyId).toBeTruthy();
    expect(validateWarranty({ ...base, exclusions: "PAINT, x" }).errors.exclusions).toBeTruthy();
  });

  it("reads downtime inputs as UTC and needs at least one minute", () => {
    expect(utcFromLocalInput("2026-10-21T08:00")).toBe("2026-10-21T08:00:00.000Z");
    expect(utcFromLocalInput("21/10/2026")).toBeNull();
    expect(validateDowntime({ from: "2026-10-21T08:00", to: "2026-10-21T12:00", resourceId: "" }).input).toEqual({
      from: "2026-10-21T08:00:00.000Z",
      to: "2026-10-21T12:00:00.000Z",
    });
    expect(validateDowntime({ from: "2026-10-21T08:00", to: "2026-10-21T08:00", resourceId: "" }).errors.to).toBeTruthy();
    expect(validateDowntime({ from: "2026-10-21T08:00", to: "2026-10-21T09:00", resourceId: "M-12" }).errors.resourceId).toBeTruthy();
  });

  it("parses the designer text like domain.CheckCatalog and round-trips a catalog", () => {
    const text = catalogToText(catalog);
    expect(parseCatalog(text).input).toEqual({
      symptoms: catalog.symptoms,
      areas: catalog.areas,
      types: catalog.types,
      causes: catalog.causes,
      remedies: catalog.remedies,
      allowedCombinations: catalog.allowedCombinations,
    });
    expect(parseCatalog({ ...text, symptoms: "noise Noise" }).errors.symptoms).toMatch(/capital letters/);
    expect(parseCatalog({ ...text, symptoms: "NOISE Noise\nNOISE Again" }).errors.symptoms).toBe("NOISE is listed twice.");
    expect(parseCatalog({ ...text, causes: "" }).errors.causes).toBeTruthy();
    expect(parseCatalog({ ...text, combinations: "NOISE GEAR WEAR" }).errors.combinations).toBe("NOISE GEAR WEAR uses a code that is not listed above.");
    expect(parseCatalog({ ...text, combinations: "NOISE BEARING" }).errors.combinations).toMatch(/SYMPTOM AREA TYPE/);
  });
});

describe("asset detail: lifecycle, criticality and warranties", () => {
  function fakeAsset(detail: AssetDetail, extra: Handler = () => undefined) {
    handler = (url, init) => {
      const hit = extra(url, init);
      if (hit) return hit;
      if (isGet(`/assets/${PUMP}`)(url, init)) return json(200, detail);
      if (isGet("/kpis")(url, init)) return json(200, { failures: 0, mtbfHours: 0, mttrHours: 0, availability: 1 });
      if (isGet("/work-orders")(url, init)) return json(200, { items: [] });
      if (isGet(`/assets/${PUMP}/criticality`)(url, init)) return json(200, { items: [] });
      if (isGet(`/assets/${PUMP}/warranties`)(url, init)) return json(200, { items: [] });
      if (isGet(`/assets/${PUMP}/isolation-points`)(url, init)) return json(200, { items: [] });
      return undefined;
    };
  }

  it("retires an active asset with its version", async () => {
    fakeAsset(pump, (url) =>
      url.pathname === `/api/v1/eam/assets/${PUMP}/lifecycle` ? json(200, { ...pump, lifecycleState: "INACTIVE", version: 3 }) : undefined,
    );
    const { container } = renderAt(`/eam/assets/${PUMP}`);
    fireEvent.click(await screen.findByRole("button", { name: "Retire asset" }));
    expect(await findStatus()).toHaveTextContent("Asset retired.");
    const [call] = sent("POST", "/lifecycle");
    expect(call.body).toEqual({ to: "INACTIVE" });
    expect(header(call, "If-Match")).toBe('"2"');
    expect(screen.queryByRole("button", { name: "Dispose of asset" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("asks before disposing and shows why the service refused", async () => {
    fakeAsset({ ...pump, lifecycleState: "INACTIVE" }, (url) =>
      url.pathname === `/api/v1/eam/assets/${PUMP}/lifecycle` ? json(422, { code: "EAM-AST-001", detail: "the asset has open work orders" }) : undefined,
    );
    renderAt(`/eam/assets/${PUMP}`);
    fireEvent.click(await screen.findByRole("button", { name: "Dispose of asset" }));
    expect(sent("POST", "/lifecycle")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Dispose permanently" }));
    expect(await screen.findByText("Could not change the asset's state")).toBeInTheDocument();
    expect(screen.getByText(/the asset has open work orders/)).toBeInTheDocument();
    expect(sent("POST", "/lifecycle")[0].body).toEqual({ to: "DISPOSED" });
  });

  it("records a criticality assessment from the matrix (AC-44.16)", async () => {
    fakeAsset(pump, (url, init) => {
      if (url.pathname === `/api/v1/eam/assets/${PUMP}/criticality` && init.method === "POST") {
        return json(201, { id: "a1", assetId: PUMP, version: 1, consequence: {}, likelihood: 4, riskScore: 20, criticality: 5, assessedAt: "2026-10-21T08:00:00Z" });
      }
      return undefined;
    });
    renderAt(`/eam/assets/${PUMP}`);
    const form = await screen.findByRole("form", { name: "Assess criticality" });
    fireEvent.change(within(form).getByLabelText("Safety"), { target: { value: "3" } });
    fireEvent.change(within(form).getByLabelText("Production"), { target: { value: "5" } });
    fireEvent.change(within(form).getByLabelText("Cost"), { target: { value: "2" } });
    fireEvent.change(within(form).getByLabelText("Likelihood of failure"), { target: { value: "4" } });
    expect(within(form).getByText("Risk score 20, criticality 5.")).toBeInTheDocument();
    fireEvent.click(within(form).getByRole("button", { name: "Record assessment" }));
    expect(await findStatus()).toHaveTextContent("Assessment version 1 recorded: risk 20, criticality 5.");
    const [call] = sent("POST", "/criticality");
    expect(call.body).toEqual({ consequence: { safety: 3, environment: 1, production: 5, quality: 1, cost: 2 }, likelihood: 4 });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
  });

  it("lists warranties and adds one after checking it", async () => {
    const warranty = {
      id: "w-1",
      assetId: PUMP,
      warrantyType: "OEM",
      vendorPartyId: VENDOR,
      startBasis: "PURCHASE",
      startsOn: "2025-11-01",
      endsOn: "2027-11-01",
      months: 24,
      partsCovered: true,
      laborCovered: false,
      exclusions: ["PAINT"],
      status: "ACTIVE",
      createdAt: "2025-11-01T00:00:00Z",
    };
    fakeAsset(pump, (url, init) => {
      if (url.pathname === `/api/v1/eam/assets/${PUMP}/warranties` && init.method === "POST") return json(201, warranty);
      if (isGet(`/assets/${PUMP}/warranties`)(url, init)) return json(200, { items: [warranty] });
      return undefined;
    });
    renderAt(`/eam/assets/${PUMP}`);
    const table = await findTable({ name: "Warranties" });
    expect(await within(table).findByText("2027-11-01")).toBeInTheDocument();
    expect(within(table).getByText("Parts")).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Add warranty" });
    fireEvent.click(within(form).getByRole("button", { name: "Add warranty" }));
    expect(await within(form).findByText("Enter the vendor's party id (a UUID).")).toBeInTheDocument();
    expect(sent("POST", "/warranties")).toHaveLength(0);
    fireEvent.change(within(form).getByLabelText("Warranty type"), { target: { value: "OEM" } });
    fireEvent.change(within(form).getByLabelText("Vendor party id"), { target: { value: VENDOR } });
    fireEvent.change(within(form).getByLabelText("Start date"), { target: { value: "2025-11-01" } });
    fireEvent.click(within(form).getByRole("button", { name: "Add warranty" }));
    expect(await findStatus()).toHaveTextContent("Warranty OEM recorded.");
    expect(sent("POST", "/warranties")[0].body).toEqual({
      warrantyType: "OEM",
      vendorPartyId: VENDOR,
      startBasis: "PURCHASE",
      startsOn: "2025-11-01",
      months: 24,
      partsCovered: true,
      laborCovered: false,
    });
  });
});

describe("work order detail: fault and downtime", () => {
  function fakeOrder(detail: WorkOrderDetail, cat: FaultCatalog | null, extra: Handler = () => undefined) {
    handler = (url, init) => {
      const hit = extra(url, init);
      if (hit) return hit;
      if (isGet(`/work-orders/${WO}`)(url, init)) return json(200, detail);
      if (isGet("/permits")(url, init)) return json(200, { items: [] });
      if (isGet("/fault-catalogs/PUMP")(url, init)) return cat ? json(200, cat) : json(404, { code: "EAM-NF-001", detail: "no fault catalog" });
      return undefined;
    };
  }

  it("registers a fault from the allowed combinations only", async () => {
    fakeOrder(order, catalog, (url) =>
      url.pathname === `/api/v1/eam/work-orders/${WO}/faults`
        ? json(201, { ...order, fault: { id: "f1", symptom: "LEAK", area: "SEAL", type: "WEAR", cause: "FATIGUE", remedy: "REPLACE", registeredAt: "2026-10-21T09:00:00Z" } })
        : undefined,
    );
    const { container } = renderAt(`/eam/work-orders/${WO}`);
    const form = await screen.findByRole("form", { name: "Register fault" });
    expect(screen.getByText("Covered by a vendor warranty; completion raises a claim")).toBeInTheDocument();
    expect(within(within(form).getByLabelText("Area")).getAllByRole("option").map((o) => o.textContent)).toEqual(["Bearing"]);
    fireEvent.change(within(form).getByLabelText("Symptom"), { target: { value: "LEAK" } });
    expect(within(within(form).getByLabelText("Area")).getAllByRole("option").map((o) => o.textContent)).toEqual(["Seal"]);
    fireEvent.click(within(form).getByRole("button", { name: "Register fault" }));
    expect(await findStatus()).toHaveTextContent("Fault registered.");
    const [call] = sent("POST", "/faults");
    expect(call.body).toEqual({ symptom: "LEAK", area: "SEAL", type: "WEAR", cause: "FATIGUE", remedy: "REPLACE" });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains EAM-FLT-001 on completion and links to the designer when the type has no catalog", async () => {
    fakeOrder(order, null, (url) =>
      url.pathname === `/api/v1/eam/work-orders/${WO}/status`
        ? json(422, { code: "EAM-FLT-001", detail: "register a fault before completing CORRECTIVE work orders" })
        : undefined,
    );
    renderAt(`/eam/work-orders/${WO}`);
    expect(await screen.findByRole("link", { name: "Design the catalog" })).toHaveAttribute("href", "/eam/fault-catalogs?type=PUMP");
    fireEvent.click(screen.getByRole("button", { name: "Complete" }));
    fireEvent.click(screen.getByRole("button", { name: "Complete work order" }));
    expect(await screen.findByText("Register the fault first")).toBeInTheDocument();
  });

  it("records a downtime window in UTC and prefills completion with the recorded minutes", async () => {
    const withWindow: WorkOrderDetail = {
      ...order,
      downtimeMinutes: 240,
      downtime: [{ id: "d1", resourceId: M12, from: "2026-10-21T08:00:00Z", to: "2026-10-21T12:00:00Z", minutes: 240 }],
    };
    let current = order;
    fakeOrder(order, catalog, (url, init) => {
      if (url.pathname === `/api/v1/eam/work-orders/${WO}/downtime`) {
        current = withWindow;
        return json(201, withWindow);
      }
      if (isGet(`/work-orders/${WO}`)(url, init)) return json(200, current);
      if (isGet("/permits")(url, init)) return json(200, { items: [] });
      return undefined;
    });
    renderAt(`/eam/work-orders/${WO}`);
    const form = await screen.findByRole("form", { name: "Record downtime" });
    fireEvent.change(within(form).getByLabelText("Stopped at (UTC)"), { target: { value: "2026-10-21T08:00" } });
    fireEvent.change(within(form).getByLabelText("Running again at (UTC)"), { target: { value: "2026-10-21T08:00" } });
    fireEvent.click(within(form).getByRole("button", { name: "Record downtime" }));
    expect(await within(form).findByText("The stop must end at least one minute after it began.")).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("Running again at (UTC)"), { target: { value: "2026-10-21T12:00" } });
    fireEvent.click(within(form).getByRole("button", { name: "Record downtime" }));
    expect(await findStatus()).toHaveTextContent("Downtime recorded.");
    expect(sent("POST", "/downtime")[0].body).toEqual({ from: "2026-10-21T08:00:00.000Z", to: "2026-10-21T12:00:00.000Z" });
    expect(await within(screen.getByRole("table", { name: "Downtime windows" })).findByText(M12)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Complete" }));
    expect(screen.getByLabelText("Downtime in minutes")).toHaveValue("240");
  });
});

describe("request triage", () => {
  const request: MaintenanceRequest = { id: REQ, assetId: PUMP, source: "USER", severity: 3, description: "Rattle", status: "NEW", createdAt: "2026-10-21T09:00:00Z" };

  it("defers a request with a key and shows the outcome", async () => {
    handler = (url, init) => {
      if (isGet("/requests")(url, init)) return json(200, { items: [request] });
      if (url.pathname === `/api/v1/eam/requests/${REQ}/triage`) return json(200, { ...request, status: "DEFERRED" });
      return undefined;
    };
    const { container } = renderAt("/eam/requests");
    const table = await findTable({ name: "Maintenance requests" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "Defer" }));
    expect(await findStatus()).toHaveTextContent("Request deferred: it is now deferred.");
    const [call] = sent("POST", "/triage");
    expect(call.body).toEqual({ action: "DEFER" });
    expect(header(call, "Idempotency-Key")).toBeTruthy();
  });

  it("offers triage and reject, not conversion, on a deferred request", async () => {
    handler = (url, init) => (isGet("/requests")(url, init) ? json(200, { items: [{ ...request, status: "DEFERRED", triageNote: "next shutdown" }] }) : undefined);
    renderAt("/eam/requests?status=DEFERRED");
    const table = await findTable({ name: "Maintenance requests" });
    expect(within(table).getByText("next shutdown")).toBeInTheDocument();
    expect(within(table).getByRole("button", { name: "Triage" })).toBeInTheDocument();
    expect(within(table).getByRole("button", { name: "Reject" })).toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: "Convert to work order" })).not.toBeInTheDocument();
  });

  it("shows a refused triage with its code", async () => {
    handler = (url, init) => {
      if (isGet("/requests")(url, init)) return json(200, { items: [request] });
      if (url.pathname === `/api/v1/eam/requests/${REQ}/triage`) return json(422, { code: "EAM-REQ-001", detail: "triage REJECT not allowed from CONVERTED" });
      return undefined;
    };
    renderAt("/eam/requests");
    fireEvent.click(within(await findTable({ name: "Maintenance requests" })).getByRole("button", { name: "Reject" }));
    expect(await screen.findByText("Could not update the request")).toBeInTheDocument();
    expect(screen.getByText("EAM-REQ-001")).toBeInTheDocument();
  });
});

describe("fault catalog designer", () => {
  it("lists the catalogs and opens one", async () => {
    handler = (url, init) => {
      if (isGet("/fault-catalogs")(url, init)) return json(200, { items: [{ assetType: "PUMP", version: 3, codes: 7, combinations: 2, updatedAt: "2026-10-21T08:00:00Z" }] });
      if (isGet("/fault-catalogs/PUMP")(url, init)) return json(200, catalog);
      return undefined;
    };
    const { container } = renderAt("/eam/fault-catalogs");
    const table = await findTable({ name: "Fault catalogs" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "PUMP" }));
    expect(await screen.findByText("Allowed combinations for PUMP (version 3)")).toBeInTheDocument();
  });

  it("shows the empty and error states of the list", async () => {
    handler = (url, init) => (isGet("/fault-catalogs")(url, init) ? json(200, { items: [] }) : undefined);
    const first = renderAt("/eam/fault-catalogs");
    expect(await screen.findByText("No fault catalogs yet")).toBeInTheDocument();
    first.unmount();
    handler = () => json(503, { code: "EAM-READY-001", detail: "database not ready" });
    renderAt("/eam/fault-catalogs");
    expect(await screen.findByText("Could not load the catalogs")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("starts a new catalog with If-Match 0 after checking the text", async () => {
    handler = (url, init) => {
      if (isGet("/fault-catalogs")(url, init)) return json(200, { items: [] });
      if (isGet("/fault-catalogs/PUMP")(url, init)) return json(404, { code: "EAM-NF-001", detail: "no fault catalog" });
      if (url.pathname === "/api/v1/eam/fault-catalogs/PUMP" && init.method === "PUT") return json(200, { ...catalog, version: 1 });
      return undefined;
    };
    const { container } = renderAt("/eam/fault-catalogs");
    fireEvent.change(screen.getByLabelText("Asset type"), { target: { value: "pump" } });
    fireEvent.click(screen.getByRole("button", { name: "Open catalog" }));
    expect(await screen.findByText("No catalog for PUMP yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    const form = screen.getByRole("form", { name: "Fault catalog" });
    fireEvent.click(within(form).getByRole("button", { name: "Save catalog" }));
    expect(await within(form).findByText("List 1 to 200 symptoms, one per line.")).toBeInTheDocument();
    expect(sent("PUT", "/fault-catalogs/PUMP")).toHaveLength(0);
    const text = catalogToText(catalog);
    fireEvent.change(within(form).getByLabelText("Symptoms"), { target: { value: text.symptoms } });
    fireEvent.change(within(form).getByLabelText("Areas"), { target: { value: text.areas } });
    fireEvent.change(within(form).getByLabelText("Fault types"), { target: { value: text.types } });
    fireEvent.change(within(form).getByLabelText("Causes"), { target: { value: text.causes } });
    fireEvent.change(within(form).getByLabelText("Remedies"), { target: { value: text.remedies } });
    fireEvent.change(within(form).getByLabelText("Allowed combinations"), { target: { value: text.combinations } });
    fireEvent.click(within(form).getByRole("button", { name: "Save catalog" }));
    expect(await findStatus()).toHaveTextContent("Catalog for PUMP saved as version 1.");
    const [call] = sent("PUT", "/fault-catalogs/PUMP");
    expect(header(call, "If-Match")).toBe('"0"');
    expect((call.body as { allowedCombinations: unknown[] }).allowedCombinations).toEqual(catalog.allowedCombinations);
  });

  it("edits an existing catalog with its version and explains a conflict", async () => {
    handler = (url, init) => {
      if (isGet("/fault-catalogs/PUMP")(url, init)) return json(200, catalog);
      if (init.method === "PUT") return json(412, { code: "conflict_version", detail: "fault catalog version changed" });
      return undefined;
    };
    renderAt("/eam/fault-catalogs?type=PUMP");
    const table = await findTable({ name: "Allowed combinations" });
    expect(within(table).getByText("LEAK · Leak")).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Fault catalog" });
    await waitFor(() => expect(within(form).getByLabelText("Symptoms")).toHaveValue("NOISE Abnormal noise\nLEAK Leak"));
    fireEvent.click(within(form).getByRole("button", { name: "Save catalog" }));
    expect(await screen.findByText("Someone else changed this catalog. Reload it and try again.")).toBeInTheDocument();
    expect(header(sent("PUT", "/fault-catalogs/PUMP")[0], "If-Match")).toBe('"3"');
  });
});
