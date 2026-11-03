import { findAlert, findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { loadModules } from "../registry";
import { validateAgreement, emptyAgreementLine, type AgreementValues } from "./AgreementsPage";
import { validateCase } from "./CasesPage";
import { defaultKpiValues, validateKpis } from "./KpisPage";
import service from "./module";
import { validateProfile, type ProfileValues } from "./SlaProfilesPage";
import { validateWorkOrder, emptyWorkLine } from "./WorkOrdersPage";

const CUSTOMER = "11111111-0000-4000-8000-000000000001";
const LE = "22222222-0000-4000-8000-000000000002";
const PROFILE = "33333333-0000-4000-8000-000000000003";
const AGREEMENT = "44444444-0000-4000-8000-000000000004";
const CASE = "55555555-0000-4000-8000-000000000005";
const WO = "66666666-0000-4000-8000-000000000006";
const TERRITORY = "77777777-0000-4000-8000-000000000007";

// The shell supplies the h1, landmarks and skip link; a page rendered alone is
// checked for everything else.
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

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {service.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const profile = {
  id: PROFILE,
  code: "GOLD",
  name: "Gold",
  timeZone: "America/Chicago",
  allDay: false,
  openMinute: 480,
  closeMinute: 1080,
  pauseOnParts: false,
  holidays: ["2026-12-25"],
  targets: [{ priority: 2, responseMinutes: 60, resolutionMinutes: 480 }],
};

const serviceCase = {
  id: CASE,
  number: "CS-000042",
  customerPartyId: CUSTOMER,
  legalEntityId: LE,
  channel: "PHONE",
  priority: 2,
  description: "Pump noise",
  agreementId: AGREEMENT,
  slaProfileId: PROFILE,
  entitlementTrace: [`agreement ${AGREEMENT} matched at specificity 1`, "priority rank 5"],
  openedAt: "2026-10-23T21:00:00Z",
  slaResponseDue: "2026-10-23T22:00:00Z",
  slaResponseWarnAt: "2026-10-23T21:45:00Z",
  slaResolutionDue: "2026-10-26T19:00:00Z",
  slaResolutionWarnAt: "2026-10-26T17:00:00Z",
  slaPausedMinutes: 0,
  slaState: "RUNNING",
  slaAlerts: [] as string[],
  status: "NEW",
  version: 1,
};

const agreement = {
  id: AGREEMENT,
  number: "SA-000007",
  customerPartyId: CUSTOMER,
  legalEntityId: LE,
  validFrom: "2026-01-01",
  slaProfileId: PROFILE,
  billingModel: "PREPAID",
  priorityRank: 5,
  status: "ACTIVE",
  lines: [{ includedVisits: 2, includedHours: "1", partsCovered: false, laborCovered: true, travelCovered: false }],
  hoursConsumed: "0.5",
  visitsConsumed: 0,
  version: 1,
};

function workOrder(overrides: Record<string, unknown> = {}) {
  return {
    id: WO,
    number: "SWO-000003",
    caseId: CASE,
    agreementId: AGREEMENT,
    customerPartyId: CUSTOMER,
    legalEntityId: LE,
    territoryId: TERRITORY,
    estDurationMinutes: 180,
    currency: "USD",
    status: "IN_PROGRESS",
    lines: [{ id: "l1", kind: "LABOR", qty: "3", unitPrice: "95", unitCost: "40" }],
    version: 5,
    ...overrides,
  };
}

describe("service module", () => {
  it("declares a list and a detail or create page for each entity", () => {
    expect(service.id).toBe("service");
    const paths = service.routes.map((r) => r.path);
    for (const path of ["/service", "/service/cases", "/service/cases/new", "/service/cases/:id", "/service/agreements", "/service/agreements/new", "/service/agreements/:id", "/service/work-orders", "/service/work-orders/new", "/service/work-orders/:id", "/service/sla-profiles", "/service/sla-profiles/new", "/service/kpis", "/service/objects", "/service/objects/new", "/service/objects/receive", "/service/objects/:id", "/service/activities", "/service/activities/new", "/service/activities/:id", "/service/knowledge", "/service/knowledge/new", "/service/knowledge/:id"]) {
      expect(paths).toContain(path);
    }
    expect(service.routes.every((r) => r.element !== null)).toBe(true);
  });

  it("gates every nav entry by a PRD duty", () => {
    const prdDuties = new Set(["SvcInquire", "SvcCaseCreate", "ActivityMaintain", "KbRead", "CaseMaintain", "CaseSetupAdmin", "SvcClaimMaintain"]);
    expect(service.nav.every((n) => n.to.startsWith("/service/") && prdDuties.has(n.duty ?? ""))).toBe(true);
    expect(service.nav.find((n) => n.to === "/service/knowledge")?.duty).toBe("KbRead");
    expect(service.nav.find((n) => n.to === "/service/activities")?.duty).toBe("ActivityMaintain");
    expect(service.nav.find((n) => n.to === "/service/cases/new")?.duty).toBe("SvcCaseCreate");
    expect(service.nav.find((n) => n.to === "/service/case-management")?.duty).toBe("CaseMaintain");
    expect(service.nav.find((n) => n.to === "/service/case-setup")?.duty).toBe("CaseSetupAdmin");
    expect(service.nav.find((n) => n.to === "/service/supplier-claims")?.duty).toBe("SvcClaimMaintain");
    expect(service.nav.find((n) => n.to === "/service/case-mailbox")?.duty).toBe("CaseMaintain");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("service");
  });
});

describe("cases", () => {
  it("lists cases with their SLA state and opens one", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/service/cases") return { status: 200, body: { items: [serviceCase] } };
      return { status: 200, body: serviceCase };
    };
    const { container } = renderAt("/service/cases");
    const table = await findTable({ name: "Service cases" });
    expect(within(table).getByText("CS-000042")).toBeInTheDocument();
    expect(within(table).getByText("Running")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("SLA state"), { target: { value: "BREACHED" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("slaState")).toBe("BREACHED"));

    fireEvent.click(await screen.findByRole("button", { name: "CS-000042" }));
    expect(await screen.findByRole("heading", { name: "Service case CS-000042" })).toBeInTheDocument();
  });

  it("shows the empty state and the error state with a retry", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const first = renderAt("/service");
    expect(await screen.findByText("No service cases yet")).toBeInTheDocument();
    first.unmount();

    let failing = true;
    handler = () => (failing ? { status: 503, body: { code: "COM-0500", detail: "internal error" } } : { status: 200, body: { items: [serviceCase] } });
    renderAt("/service/cases");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load service cases");
    expect(alert).toHaveTextContent("COM-0500");
    failing = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Service cases" })).toBeInTheDocument();
  });

  it("validates the open-case form like the service does", () => {
    const base = { customerPartyId: CUSTOMER, legalEntityId: LE, serviceObjectId: "", channel: "PHONE" as const, priority: 2 as const, description: "", fallbackSlaProfileId: "" };
    expect(validateCase(base).input).toEqual({ customerPartyId: CUSTOMER, legalEntityId: LE, channel: "PHONE", priority: 2 });
    const bad = validateCase({ ...base, customerPartyId: "acme", serviceObjectId: "x", description: "a".repeat(4001) });
    expect(Object.keys(bad.errors).sort()).toEqual(["customerPartyId", "description", "serviceObjectId"]);
  });

  it("opens a case with an Idempotency-Key and lands on its detail", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/service/sla-profiles") return { status: 200, body: { items: [profile] } };
      if (init.method === "POST") return { status: 201, body: serviceCase };
      return { status: 200, body: serviceCase };
    };
    renderAt("/service/cases/new");
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    await screen.findByRole("option", { name: "GOLD — Gold" });
    fireEvent.change(screen.getByLabelText("Fallback SLA profile"), { target: { value: PROFILE } });
    fireEvent.click(screen.getByRole("button", { name: "Open case" }));

    expect(await screen.findByRole("heading", { name: "Service case CS-000042" })).toBeInTheDocument();
    const create = calls.find((c) => c.method === "POST");
    expect(create?.url.pathname).toBe("/api/v1/service/cases");
    expect(create?.headers.get("Idempotency-Key")).toMatch(/[0-9a-f-]{36}/);
    expect(create?.body).toEqual({ customerPartyId: CUSTOMER, legalEntityId: LE, channel: "PHONE", priority: 3, fallbackSlaProfileId: PROFILE });
  });

  it("explains a case that no agreement covers", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") return { status: 422, body: { code: "SVC-ENT-002", detail: "no agreement matched and no fallback SLA profile was supplied" } };
      return { status: 200, body: { items: [] } };
    };
    renderAt("/service/cases/new");
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Open case" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("No agreement covers this case");
    expect(alert).toHaveTextContent("SVC-ENT-002");
  });

  it("shows the SLA clocks, the entitlement trace and the case's work orders", async () => {
    handler = (url) => {
      if (url.pathname === "/api/v1/service/work-orders") return { status: 200, body: { items: [workOrder()] } };
      return { status: 200, body: serviceCase };
    };
    const { container } = renderAt(`/service/cases/${CASE}`);
    const sla = await screen.findByLabelText("SLA clocks");
    expect(sla).toHaveTextContent("2026-10-26 19:00 UTC");
    expect(screen.getByRole("list", { name: "Entitlement trace" })).toHaveTextContent("priority rank 5");
    expect(screen.getByRole("link", { name: "Create work order" })).toHaveAttribute("href", `/service/work-orders/new?caseId=${CASE}`);
    expect(await findTable({ name: "Case work orders" })).toHaveTextContent("SWO-000003");
    expect(calls.find((c) => c.url.pathname === "/api/v1/service/work-orders")?.url.searchParams.get("caseId")).toBe(CASE);
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers the moves the case status machine allows and pauses with If-Match and an Idempotency-Key", async () => {
    const paused = { ...serviceCase, status: "WAITING_CUSTOMER", slaState: "PAUSED", slaPausedAt: "2026-10-26T15:00:00Z", respondedAt: "2026-10-23T21:10:00Z", version: 2 };
    handler = (url, init) => {
      if (url.pathname === "/api/v1/service/work-orders") return { status: 200, body: { items: [] } };
      if (init.method === "POST") return { status: 200, body: paused, etag: '"2"' };
      return { status: 200, body: serviceCase, etag: '"1"' };
    };
    const { container } = renderAt(`/service/cases/${CASE}`);
    for (const name of ["Start work", "Wait for customer", "Wait for parts", "Resolve", "Cancel case"]) {
      expect(await screen.findByRole("button", { name })).toBeInTheDocument();
    }
    expect(screen.queryByRole("button", { name: "Close" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Wait for customer" }));
    expect(await findStatus()).toHaveTextContent("Case is now waiting customer; SLA paused.");
    const move = calls.find((c) => c.method === "POST");
    expect(move?.url.pathname).toBe(`/api/v1/service/cases/${CASE}:transition`);
    expect(move?.headers.get("If-Match")).toBe('"1"');
    expect(move?.headers.get("Idempotency-Key")).toMatch(/[0-9a-f-]{36}/);
    expect(move?.body).toEqual({ to: "WAITING_CUSTOMER" });
    expect(screen.getByLabelText("SLA clocks")).toHaveTextContent("Since 2026-10-26 15:00 UTC");
    expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Wait for customer" })).toBeNull();
  });

  it("shows the SLA alerts already emitted and hides the work-order link once the case is closed", async () => {
    const closed = { ...serviceCase, status: "CLOSED", slaState: "BREACHED", slaAlerts: ["RESOLUTION_WARNING", "RESOLUTION_BREACH"], resolvedAt: "2026-10-27T15:00:00Z", version: 6 };
    handler = (url) => (url.pathname === "/api/v1/service/work-orders" ? { status: 200, body: { items: [] } } : { status: 200, body: closed, etag: '"6"' });
    renderAt(`/service/cases/${CASE}`);
    const sla = await screen.findByLabelText("SLA clocks");
    expect(sla).toHaveTextContent("Resolution at 75 %");
    expect(sla).toHaveTextContent("Resolution breached");
    expect(sla).toHaveTextContent("2026-10-27 15:00 UTC");
    expect(screen.queryByRole("link", { name: "Create work order" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();
    expect(await screen.findByText("The case is no longer open.")).toBeInTheDocument();
  });

  it("asks before canceling a case and sends nothing when kept", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/service/work-orders") return { status: 200, body: { items: [] } };
      if (init.method === "POST") return { status: 200, body: { ...serviceCase, status: "CANCELED", version: 2 } };
      return { status: 200, body: serviceCase };
    };
    renderAt(`/service/cases/${CASE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel case" }));
    const dialog = screen.getByRole("alertdialog", { name: "Cancel this case?" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Keep the case" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "Cancel case" }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Cancel case" }));
    expect(await findStatus()).toHaveTextContent("Case is now canceled");
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ to: "CANCELED" });
  });

  it("reloads a case changed by someone else and explains why", async () => {
    let gets = 0;
    handler = (url, init) => {
      if (url.pathname === "/api/v1/service/work-orders") return { status: 200, body: { items: [] } };
      if (init.method === "POST") return { status: 412, body: { code: "conflict_version", detail: "current version is 2" } };
      gets += 1;
      return { status: 200, body: gets === 1 ? serviceCase : { ...serviceCase, status: "IN_PROGRESS", version: 2 } };
    };
    renderAt(`/service/cases/${CASE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Resolve" }));
    expect(await findAlert()).toHaveTextContent("Someone else changed this case; it was reloaded");
    expect(await screen.findByRole("button", { name: "Wait for customer" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start work" })).toBeNull();
  });

  it("reports a case that cannot be loaded", async () => {
    handler = () => ({ status: 404, body: { code: "SVC-CASE-001", detail: "service case not found" } });
    renderAt(`/service/cases/${CASE}`);
    expect(await findAlert()).toHaveTextContent("SVC-CASE-001");
  });
});

describe("agreements", () => {
  const values = (): AgreementValues => ({
    customerPartyId: CUSTOMER,
    legalEntityId: LE,
    validFrom: "2026-01-01",
    validTo: "",
    slaProfileId: PROFILE,
    billingModel: "PREPAID",
    priorityRank: "5",
    status: "ACTIVE",
    lines: [emptyAgreementLine()],
  });

  it("validates dates, rank and lines like the service does", () => {
    expect(validateAgreement(values()).input?.lines).toEqual([{ includedVisits: 0, includedHours: "0", partsCovered: false, laborCovered: true, travelCovered: false }]);
    const bad = validateAgreement({ ...values(), validTo: "2025-12-31", priorityRank: "0", lines: [{ ...emptyAgreementLine(), includedHours: "-1" }] });
    expect(Object.keys(bad.errors).sort()).toEqual(["lines[0]", "priorityRank", "validTo"]);
  });

  it("creates an agreement against a chosen SLA profile", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/service/sla-profiles") return { status: 200, body: { items: [profile] } };
      if (init.method === "POST") return { status: 201, body: agreement };
      return { status: 200, body: agreement };
    };
    renderAt("/service/agreements/new");
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    await screen.findByRole("option", { name: "GOLD — Gold" });
    fireEvent.change(screen.getByLabelText("SLA profile"), { target: { value: PROFILE } });
    fireEvent.change(screen.getByLabelText("Included hours"), { target: { value: "1.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Create agreement" }));

    expect(await screen.findByRole("heading", { name: "Service agreement SA-000007" })).toBeInTheDocument();
    const create = calls.find((c) => c.method === "POST");
    expect(create?.body).toMatchObject({ customerPartyId: CUSTOMER, slaProfileId: PROFILE, status: "ACTIVE", lines: [{ includedHours: "1.5", laborCovered: true }] });
    expect(screen.getByLabelText("Agreement")).toHaveTextContent("0.5");
    expect(screen.getByRole("table", { name: "Agreement lines" })).toHaveTextContent("Any object");
  });

  it("lists agreements and shows the empty state", async () => {
    handler = () => ({ status: 200, body: { items: [agreement], nextCursor: AGREEMENT } });
    const view = renderAt("/service/agreements");
    await waitFor(() => expect(screen.getByRole("table", { name: "Service agreements" })).toHaveTextContent("SA-000007"));
    handler = () => ({ status: 200, body: { items: [] } });
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("cursor")).toBe(AGREEMENT));
    view.unmount();
    renderAt("/service/agreements");
    expect(await screen.findByText("No service agreements yet")).toBeInTheDocument();
  });
});

describe("work orders", () => {
  it("validates the work-order form like the service does", () => {
    const base = { customerPartyId: CUSTOMER, legalEntityId: LE, territoryId: TERRITORY, caseId: "", agreementId: "", estDurationMinutes: "60", currency: "USD", lines: [emptyWorkLine()] };
    expect(validateWorkOrder(base).input?.lines).toEqual([{ kind: "LABOR", qty: "1", unitPrice: "0", unitCost: "0" }]);
    const bad = validateWorkOrder({ ...base, currency: "usd", estDurationMinutes: "0", lines: [{ ...emptyWorkLine(), qty: "0" }] });
    expect(Object.keys(bad.errors).sort()).toEqual(["currency", "estDurationMinutes", "lines[0]"]);
  });

  it("starts a work order from a case with the case's customer and legal entity", async () => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/service/cases/${CASE}`) return { status: 200, body: serviceCase };
      if (init.method === "POST") return { status: 201, body: workOrder({ status: "UNSCHEDULED", version: 1 }), etag: '"1"' };
      return { status: 200, body: workOrder({ status: "UNSCHEDULED", version: 1 }), etag: '"1"' };
    };
    renderAt(`/service/work-orders/new?caseId=${CASE}`);
    await waitFor(() => expect(screen.getByLabelText("Customer")).toHaveValue(CUSTOMER));
    expect(screen.getByLabelText("Legal entity")).toHaveValue(LE);
    fireEvent.change(screen.getByLabelText("Territory"), { target: { value: TERRITORY } });
    fireEvent.change(screen.getByLabelText("Unit price"), { target: { value: "95.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Create work order" }));

    expect(await screen.findByRole("heading", { name: "Service work order SWO-000003" })).toBeInTheDocument();
    const create = calls.find((c) => c.method === "POST");
    expect(create?.body).toMatchObject({ caseId: CASE, customerPartyId: CUSTOMER, territoryId: TERRITORY, lines: [{ kind: "LABOR", unitPrice: "95.00" }] });
    expect(create?.body).not.toHaveProperty("agreementId");
  });

  it("offers only the moves the status machine allows and sends If-Match", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") return { status: 200, body: workOrder({ status: "COMPLETED", version: 6 }), etag: '"6"' };
      return { status: 200, body: workOrder(), etag: '"5"' };
    };
    const { container } = renderAt(`/service/work-orders/${WO}`);
    expect(await screen.findByRole("button", { name: "Complete" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Put on hold" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Dispatch" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Post to billing" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Complete" }));
    expect(await findStatus()).toHaveTextContent("Work order is now completed.");
    const move = calls.find((c) => c.method === "POST");
    expect(move?.url.pathname).toBe(`/api/v1/service/work-orders/${WO}:transition`);
    expect(move?.headers.get("If-Match")).toBe('"5"');
    expect(move?.body).toEqual({ to: "COMPLETED" });
    expect(screen.getByRole("button", { name: "Post to billing" })).toBeInTheDocument();
  });

  it("posts a completed work order and shows coverage per line", async () => {
    const posting = {
      currency: "USD",
      billableTotal: "190.00",
      warrantyCost: "0",
      entitlementConsumed: "1",
      lines: [
        { lineId: "l1", kind: "LABOR", coverage: "AGREEMENT", qty: "1", amount: "0", warrantyCost: "0", agreementHours: "1" },
        { lineId: "l1", kind: "LABOR", coverage: "BILLABLE", qty: "2", amount: "190.00", warrantyCost: "0", agreementHours: "0" },
      ],
    };
    handler = (_url, init) => {
      if (init.method === "POST") return { status: 200, body: workOrder({ status: "POSTED", version: 7, posting }), etag: '"7"' };
      return { status: 200, body: workOrder({ status: "COMPLETED", version: 6 }), etag: '"6"' };
    };
    renderAt(`/service/work-orders/${WO}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post to billing" }));
    expect(await findStatus()).toHaveTextContent("Posted: 190.00 USD billable.");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/service/work-orders/${WO}:post`);
    expect(post?.headers.get("If-Match")).toBe('"6"');
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    const posted = screen.getByRole("table", { name: "Posted lines" });
    expect(within(posted).getByText("Agreement")).toBeInTheDocument();
    expect(within(posted).getByText("Billable")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Posting totals" })).toHaveTextContent("Included hours used 1");
    expect(screen.queryByRole("button", { name: "Post to billing" })).toBeNull();
  });

  it("reports a version conflict and reloads", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") return { status: 412, body: { code: "conflict_version", detail: "current version is 6" } };
      return { status: 200, body: workOrder(), etag: '"5"' };
    };
    renderAt(`/service/work-orders/${WO}`);
    fireEvent.click(await screen.findByRole("button", { name: "Put on hold" }));
    expect(await findAlert()).toHaveTextContent("Someone else changed this work order");
    await waitFor(() => expect(calls.filter((c) => c.method === "GET" && c.url.pathname === `/api/v1/service/work-orders/${WO}`).length).toBe(2));
  });

  it("opens a follow-up visit for a completed order and lands on it", async () => {
    const CHILD = "88888888-0000-4000-8000-000000000008";
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: workOrder({ id: CHILD, number: "SWO-000004", status: "UNSCHEDULED", lines: [], followUpOf: WO, version: 1 }), etag: '"1"' };
      if (url.pathname.endsWith(CHILD)) return { status: 200, body: workOrder({ id: CHILD, number: "SWO-000004", status: "UNSCHEDULED", lines: [], followUpOf: WO, version: 1 }), etag: '"1"' };
      return { status: 200, body: workOrder({ status: "COMPLETED", version: 6, completedAt: "2026-10-27T15:00:00Z", firstTimeFix: true }), etag: '"6"' };
    };
    renderAt(`/service/work-orders/${WO}`);
    expect(await screen.findByLabelText("Work order")).toHaveTextContent("Yes");
    fireEvent.click(screen.getByRole("button", { name: "Open follow-up visit" }));
    expect(await screen.findByRole("heading", { name: /SWO-000004/ })).toBeInTheDocument();
    const follow = calls.find((c) => c.method === "POST");
    expect(follow?.url.pathname).toBe(`/api/v1/service/work-orders/${WO}:follow-up`);
    expect(follow?.headers.get("If-Match")).toBe('"6"');
    expect(follow?.headers.get("Idempotency-Key")).toMatch(/[0-9a-f-]{36}/);
    expect(screen.getByRole("link", { name: shortOf(WO) })).toHaveAttribute("href", `/service/work-orders/${WO}`);
  });

  it("offers no follow-up before the order is finished", async () => {
    handler = () => ({ status: 200, body: workOrder(), etag: '"5"' });
    renderAt(`/service/work-orders/${WO}`);
    expect(await screen.findByRole("button", { name: "Complete" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open follow-up visit" })).toBeNull();
  });

  it("lists work orders with what each billed", async () => {
    handler = () => ({ status: 200, body: { items: [workOrder({ status: "POSTED", posting: { currency: "USD", billableTotal: "190.00", warrantyCost: "0", entitlementConsumed: "1", lines: [] } })] } });
    renderAt("/service/work-orders");
    const table = await findTable({ name: "Service work orders" });
    expect(table).toHaveTextContent("SWO-000003");
    expect(table).toHaveTextContent("190.00 USD");
  });
});

describe("SLA profiles", () => {
  const values = (): ProfileValues => ({
    code: "GOLD",
    name: "Gold",
    timeZone: "America/Chicago",
    allDay: false,
    open: "08:00",
    close: "18:00",
    pauseOnParts: true,
    holidays: "2026-12-25, 2026-12-26",
    targets: { 1: { response: "", resolution: "" }, 2: { response: "60", resolution: "480" }, 3: { response: "", resolution: "" }, 4: { response: "", resolution: "" } },
  });

  it("builds the request with hours as minutes, holidays and only the priorities set", () => {
    expect(validateProfile(values()).input).toEqual({
      code: "GOLD",
      name: "Gold",
      timeZone: "America/Chicago",
      allDay: false,
      openMinute: 480,
      closeMinute: 1080,
      pauseOnParts: true,
      holidays: ["2026-12-25", "2026-12-26"],
      targets: [{ priority: 2, responseMinutes: 60, resolutionMinutes: 480 }],
    });
    const bad = validateProfile({ ...values(), code: "g", open: "18:00", close: "08:00", holidays: "25/12", targets: { ...values().targets, 2: { response: "60", resolution: "30" } } });
    expect(Object.keys(bad.errors).sort()).toEqual(["code", "holidays", "openMinute", "targets.2"]);
    const none = validateProfile({ ...values(), targets: { 1: { response: "", resolution: "" }, 2: { response: "", resolution: "" }, 3: { response: "", resolution: "" }, 4: { response: "", resolution: "" } } });
    expect(none.errors.targets).toBeTruthy();
  });

  it("lists profiles with their hours and targets", async () => {
    handler = () => ({ status: 200, body: { items: [profile] } });
    const { container } = renderAt("/service/sla-profiles");
    const table = await findTable({ name: "SLA profiles" });
    expect(table).toHaveTextContent("Mon–Fri 08:00–18:00");
    expect(table).toHaveTextContent("P2 1 h / 8 h");
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a profile and shows a duplicate code", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 409, body: { code: "SVC-PROF-001", detail: "an SLA profile with this code already exists" } } : { status: 200, body: { items: [] } });
    renderAt("/service/sla-profiles/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "gold" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Gold" } });
    fireEvent.click(screen.getByRole("button", { name: "Create SLA profile" }));
    expect(await findAlert()).toHaveTextContent("That code is taken");
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({ code: "GOLD", openMinute: 480, closeMinute: 1080 });
  });
});

const shortOf = (id: string) => id.split("-")[0];

describe("service KPIs", () => {
  const kpis = {
    from: "2026-10-01T00:00:00Z",
    to: "2026-10-31T00:00:00Z",
    casesOpened: 3,
    responseMet: 1,
    responseMissed: 2,
    responseAttainmentPct: "33.33",
    meanResponseMinutes: "75",
    resolutionMet: 1,
    resolutionBreached: 1,
    resolutionAttainmentPct: "50",
    meanResolutionMinutes: "900",
    workOrdersCompleted: 2,
    firstTimeFix: 1,
    firstTimeFixPct: "50",
  };

  it("turns whole days into the service's window and checks it like the service does", () => {
    expect(validateKpis({ from: "2026-10-01", to: "2026-10-30", territory: "" }).query).toEqual({ from: "2026-10-01T00:00:00Z", to: "2026-10-31T00:00:00Z", territory: undefined });
    expect(validateKpis({ from: "2026-10-01", to: "2026-10-01", territory: TERRITORY }).query).toEqual({ from: "2026-10-01T00:00:00Z", to: "2026-10-02T00:00:00Z", territory: TERRITORY });
    expect(Object.keys(validateKpis({ from: "2026-10-02", to: "2026-10-01", territory: "north" }).errors).sort()).toEqual(["territory", "to"]);
    expect(validateKpis({ from: "2025-01-01", to: "2026-01-02", territory: "" }).errors.to).toMatch(/366 days/);
    expect(validateKpis({ from: "", to: "2026-10-01", territory: "" }).errors.from).toBeTruthy();
    expect(defaultKpiValues(new Date("2026-10-29T12:00:00Z"))).toEqual({ from: "2026-09-30", to: "2026-10-29", territory: "" });
  });

  it("shows SLA attainment and first-time fix for the window and refilters by territory", async () => {
    handler = () => ({ status: 200, body: kpis });
    const { container } = renderAt("/service/kpis");
    const sla = await screen.findByRole("list", { name: "SLA attainment" });
    expect(sla).toHaveTextContent("Response attainment 33.33 %");
    expect(sla).toHaveTextContent("1 met, 2 missed");
    expect(sla).toHaveTextContent("Mean time to resolve 900 min");
    expect(screen.getByRole("list", { name: "First-time fix" })).toHaveTextContent("First-time-fix rate 50 %");
    expect(pageViolations(container)).toEqual([]);
    const first = calls[0]?.url;
    expect(first?.pathname).toBe("/api/v1/service/kpis");
    expect(first?.searchParams.get("from")).toMatch(/^\d{4}-\d{2}-\d{2}T00:00:00Z$/);
    expect(first?.searchParams.has("territory")).toBe(false);

    fireEvent.change(screen.getByLabelText("First day"), { target: { value: "2026-10-01" } });
    fireEvent.change(screen.getByLabelText("Last day"), { target: { value: "2026-10-30" } });
    fireEvent.change(screen.getByLabelText(/Territory/), { target: { value: TERRITORY } });
    fireEvent.click(screen.getByRole("button", { name: "Show KPIs" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("territory")).toBe(TERRITORY));
    expect(calls.at(-1)?.url.searchParams.get("from")).toBe("2026-10-01T00:00:00Z");
    expect(calls.at(-1)?.url.searchParams.get("to")).toBe("2026-10-31T00:00:00Z");
  });

  it("shows an empty window, field errors without a request, and a failed load with a retry", async () => {
    handler = () => ({ status: 200, body: { ...kpis, casesOpened: 0, responseMet: 0, responseMissed: 0, resolutionMet: 0, resolutionBreached: 0, workOrdersCompleted: 0, firstTimeFix: 0, responseAttainmentPct: null, resolutionAttainmentPct: null, meanResponseMinutes: null, meanResolutionMinutes: null, firstTimeFixPct: null } });
    const first = renderAt("/service/kpis");
    expect(await screen.findByText("Nothing measured in this window")).toBeInTheDocument();
    const before = calls.length;
    fireEvent.change(screen.getByLabelText(/Territory/), { target: { value: "north" } });
    fireEvent.click(screen.getByRole("button", { name: "Show KPIs" }));
    expect(await screen.findByText("Enter a territory UUID or leave it empty.")).toBeInTheDocument();
    expect(calls.length).toBe(before);
    first.unmount();

    let failing = true;
    handler = () => (failing ? { status: 403, body: { code: "COM-0403", detail: "missing duty SvcInquire" } } : { status: 200, body: kpis });
    renderAt("/service/kpis");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load the service KPIs");
    expect(alert).toHaveTextContent("COM-0403");
    failing = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("list", { name: "SLA attainment" })).toBeInTheDocument();
  });
});
