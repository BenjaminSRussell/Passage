import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findAlert, findStatus, findTable } from "../../test-utils";
import { expiryText } from "./ConflictsPage";
import { validateMitigation } from "./ConflictDetailPage";
import compliance from "./module";
import { parseRoles } from "./RoleCheckPage";

const USER = "11111111-0000-4000-8000-000000000001";
const CONFLICT = "22222222-0000-4000-8000-000000000002";

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
        {compliance.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

function conflict(overrides: Record<string, unknown> = {}) {
  return {
    conflictId: CONFLICT,
    userId: USER,
    ruleId: "P2P-001",
    riskId: "P2P-001",
    severity: "HIGH",
    dutyA: "MaintainVendors",
    dutyB: "ProcessVendorPayments",
    rolesA: ["VendorMaint"],
    rolesB: ["VendorPay"],
    state: "OPEN",
    detectedAt: "2026-10-20T09:00:00Z",
    updatedAt: "2026-10-20T09:00:00Z",
    version: 1,
    mitigations: [],
    ...overrides,
  };
}

const problem = (status: number, code: string, detail: string) => ({ status, body: { type: "x", title: "x", status, code, detail } });

const ukPack = {
  code: "UK",
  version: "2026.1",
  name: "United Kingdom",
  country: "GB",
  currency: "GBP",
  locale: "en-GB",
  taxRegime: "VAT",
  fiscalYearStartMonth: 4,
  holidayRegion: "GB-ENG",
  bankFormats: ["BACS"],
  taxIdScheme: "GB_VAT",
  retentionMinYears: { financial: 6, hr: 6 },
  documentLayouts: ["invoice-en-gb"],
  statutoryReports: [{ code: "UK_MTD_VAT", format: "JSON", mandate: "HMRC Making Tax Digital for VAT", requiredFields: ["periodKey"] }],
};

describe("compliance helpers", () => {
  it("parses one role per line and reports the bad line", () => {
    expect(parseRoles("Maint: MaintainVendors\n\nPay: ProcessVendorPayments, ViewReports")).toEqual({
      roles: [
        { name: "Maint", duties: ["MaintainVendors"] },
        { name: "Pay", duties: ["ProcessVendorPayments", "ViewReports"] },
      ],
    });
    expect(parseRoles("Maint MaintainVendors").error).toBe('Line 1: write "Role: Duty, Duty".');
    expect(parseRoles("Ok: A\nEmpty:").error).toMatch(/^Line 2/);
  });

  it("requires control, reason and a future expiry, and ends the mitigation at the end of that day", () => {
    const now = new Date("2026-11-02T09:00:00Z");
    expect(validateMitigation({ controlRef: "", reason: " ", expiresOn: "2026-11-02" }, now).errors).toEqual({
      controlRef: "Name the compensating control.",
      reason: "Explain why the control covers the risk.",
      expiresOn: "Choose a date after today.",
    });
    expect(validateMitigation({ controlRef: " CTL-1 ", reason: "dual approval", expiresOn: "2027-05-01" }, now).input).toEqual({
      controlRef: "CTL-1",
      reason: "dual approval",
      expiresAt: "2027-05-01T23:59:59Z",
    });
  });

  it("shows the mitigation expiry only while mitigated", () => {
    const now = new Date("2026-11-02T09:00:00Z");
    expect(expiryText({ state: "MITIGATED", expiresAt: "2026-11-14T09:00:00Z" }, now)).toBe("In 12 days");
    expect(expiryText({ state: "MITIGATED", expiresAt: "2026-11-01T09:00:00Z" }, now)).toBe("Expired");
    expect(expiryText({ state: "OPEN" }, now)).toBe("—");
  });
});

describe("SoD conflicts page", () => {
  it("lists OPEN conflicts by default and opens one", async () => {
    handler = (url) => (url.pathname.endsWith(CONFLICT) ? { status: 200, etag: '"1"', body: conflict() } : { status: 200, body: { items: [conflict()] } });
    const { container } = renderAt("/compliance/sod/conflicts");
    const table = await findTable({ name: "SoD conflicts" });
    expect(within(table).getByText("11111111")).toBeInTheDocument();
    expect(within(table).getByText("High")).toBeInTheDocument();
    expect(within(table).getByText("VendorMaint, VendorPay")).toBeInTheDocument();
    expect(calls[0].url.pathname).toBe("/api/v1/sod/conflicts");
    expect(calls[0].url.searchParams.get("state")).toBe("OPEN");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "P2P-001" }));
    expect(await screen.findByRole("heading", { name: "Conflict P2P-001" })).toBeInTheDocument();
    expect(calls[1].url.pathname).toBe(`/api/v1/sod/conflicts/${CONFLICT}`);
  });

  it("filters by severity and any state", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/compliance/sod/conflicts");
    await findTable({ name: "SoD conflicts" });
    fireEvent.change(screen.getByLabelText("State"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Severity"), { target: { value: "MEDIUM" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1].url.searchParams.get("severity")).toBe("MEDIUM");
    expect(calls[1].url.searchParams.has("state")).toBe(false);
    expect(await screen.findByText("No conflicts match")).toBeInTheDocument();
  });

  it("refuses a malformed user filter without calling the service", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/compliance/sod/conflicts");
    await findTable({ name: "SoD conflicts" });
    fireEvent.change(screen.getByLabelText("User"), { target: { value: "bob" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(await screen.findByText("Enter the user's UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(1);
  });

  it("shows the failure and retries", async () => {
    let fail = true;
    handler = () => (fail ? problem(503, "COM-0500", "internal error") : { status: 200, body: { items: [conflict()] } });
    renderAt("/compliance/sod/conflicts");
    expect((await findAlert()).textContent).toContain("Could not load conflicts");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "SoD conflicts" })).toBeInTheDocument();
  });

  it("loads the next page with the cursor", async () => {
    handler = (url) =>
      url.searchParams.get("cursor") === "c2"
        ? { status: 200, body: { items: [conflict({ conflictId: "33333333-0000-4000-8000-000000000003", ruleId: "GL-001" })] } }
        : { status: 200, body: { items: [conflict()], nextCursor: "c2" } };
    renderAt("/compliance/sod/conflicts");
    await findTable({ name: "SoD conflicts" });
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByRole("button", { name: "GL-001" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });
});

describe("SoD conflict detail", () => {
  it("approves a mitigation with If-Match and an Idempotency-Key", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") {
        return { status: 201, etag: '"2"', body: conflict({ state: "MITIGATED", version: 2, expiresAt: "2027-05-01T23:59:59Z", mitigationId: "m-1", decidedBy: "approver-1", mitigations: [{ mitigationId: "m-1", conflictId: CONFLICT, controlRef: "CTL-DUAL-PAY", reason: "dual approval", approverId: "approver-1", approvedAt: "2026-11-02T09:00:00Z", expiresAt: "2027-05-01T23:59:59Z" }] }) };
      }
      return { status: 200, etag: '"1"', body: conflict() };
    };
    const { container } = renderAt(`/compliance/sod/conflicts/${CONFLICT}`);
    expect(await screen.findByRole("heading", { name: "Conflict P2P-001" })).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Accept risk" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Compensating control"), { target: { value: "CTL-DUAL-PAY" } });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "dual approval" } });
    fireEvent.change(screen.getByLabelText("Expires on"), { target: { value: "2099-05-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Approve mitigation" }));

    expect((await findStatus()).textContent).toBe("Conflict mitigated until 2099-05-01.");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sod/conflicts/${CONFLICT}/mitigations`);
    expect(post?.headers.get("If-Match")).toBe('"1"');
    expect(post?.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(post?.body).toEqual({ controlRef: "CTL-DUAL-PAY", reason: "dual approval", expiresAt: "2099-05-01T23:59:59Z" });
    const history = await findTable({ name: "Mitigation history" });
    expect(within(history).getByText("approver-1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve mitigation" })).not.toBeInTheDocument();
  });

  it("reports self-approval from the service and keeps the form", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 422, body: { code: "SOD-1102", detail: "You cannot approve a mitigation for your own conflict." } } : { status: 200, etag: '"1"', body: conflict() });
    renderAt(`/compliance/sod/conflicts/${CONFLICT}`);
    await screen.findByRole("heading", { name: "Conflict P2P-001" });
    fireEvent.change(screen.getByLabelText("Compensating control"), { target: { value: "CTL" } });
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "r" } });
    fireEvent.change(screen.getByLabelText("Expires on"), { target: { value: "2099-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Approve mitigation" }));
    expect((await findAlert()).textContent).toContain("You cannot approve a mitigation for your own conflict. (SOD-1102)");
    expect(screen.getByLabelText("Compensating control")).toHaveValue("CTL");
  });

  it("offers risk acceptance on a MEDIUM conflict only", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 200, etag: '"2"', body: conflict({ severity: "MEDIUM", ruleId: "P2P-004", state: "ACCEPTED_RISK", version: 2, decisionReason: "monthly review" }) } : { status: 200, etag: '"1"', body: conflict({ severity: "MEDIUM", ruleId: "P2P-004" }) };
    renderAt(`/compliance/sod/conflicts/${CONFLICT}`);
    await screen.findByRole("heading", { name: "Conflict P2P-004" });
    fireEvent.click(screen.getByRole("button", { name: "Accept risk" }));
    expect(await screen.findByText("Explain why the risk is acceptable.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Risk acceptance reason"), { target: { value: "monthly review" } });
    fireEvent.click(screen.getByRole("button", { name: "Accept risk" }));
    expect((await findStatus()).textContent).toBe("Risk accepted.");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sod/conflicts/${CONFLICT}/accept-risk`);
    expect(post?.body).toEqual({ reason: "monthly review" });
  });

  it("shows a load failure with a way back", async () => {
    handler = () => problem(404, "SOD-1108", "no SoD conflict with that id");
    renderAt(`/compliance/sod/conflicts/${CONFLICT}`);
    expect((await findAlert()).textContent).toContain("no SoD conflict with that id (SOD-1108)");
    expect(screen.getByRole("link", { name: "Back to conflicts" })).toHaveAttribute("href", "/compliance/sod/conflicts");
  });
});

describe("Role assignment check", () => {
  it("shows a blocked assignment with the rule id", async () => {
    handler = () => ({
      status: 200,
      body: {
        newConflicts: [{ userId: USER, ruleId: "P2P-001", severity: "HIGH", riskId: "P2P-001", dutyA: "MaintainVendors", dutyB: "ProcessVendorPayments", rolesA: ["Maint"], rolesB: ["Pay"], state: "OPEN" }],
        blocked: true,
        blockingRuleIds: ["P2P-001"],
        code: "SOD-1101",
      },
    });
    const { container } = renderAt("/compliance/sod/check");
    fireEvent.change(screen.getByLabelText("User"), { target: { value: USER } });
    fireEvent.change(screen.getByLabelText("Current roles"), { target: { value: "Maint: MaintainVendors" } });
    fireEvent.change(screen.getByLabelText("Roles to assign"), { target: { value: "Pay: ProcessVendorPayments" } });
    fireEvent.click(screen.getByRole("button", { name: "Simulate" }));
    expect((await findAlert()).textContent).toContain("Unmitigated high-severity conflict on P2P-001 (SOD-1101)");
    expect(calls[0].url.pathname).toBe("/api/v1/sod/simulate");
    expect(calls[0].body).toEqual({ userId: USER, currentRoles: [{ name: "Maint", duties: ["MaintainVendors"] }], proposedRoles: [{ name: "Pay", duties: ["ProcessVendorPayments"] }] });
    expect(within(await findTable({ name: "New conflicts" })).getByText("P2P-001")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("validates before simulating", async () => {
    handler = () => ({ status: 200, body: {} });
    renderAt("/compliance/sod/check");
    fireEvent.change(screen.getByLabelText("Current roles"), { target: { value: "no colon" } });
    fireEvent.click(screen.getByRole("button", { name: "Simulate" }));
    expect(await screen.findByText("Enter the user's UUID.")).toBeInTheDocument();
    expect(screen.getByText('Line 1: write "Role: Duty, Duty".')).toBeInTheDocument();
    expect(screen.getByText("Add at least one role to assign.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("records an evaluation of the current roles and links the conflicts", async () => {
    handler = () => ({ status: 200, body: { userId: USER, conflicts: [conflict({ ruleId: "GL-001" })] } });
    renderAt("/compliance/sod/check");
    fireEvent.change(screen.getByLabelText("User"), { target: { value: USER } });
    fireEvent.change(screen.getByLabelText("Current roles"), { target: { value: "GL: PostGeneralJournals, ApproveGeneralJournals" } });
    fireEvent.click(screen.getByRole("button", { name: "Record evaluation of current roles" }));
    expect((await findStatus()).textContent).toBe("Evaluation recorded: 1 active conflict for this user.");
    expect(calls[0].url.pathname).toBe("/api/v1/sod/evaluations");
    expect(calls[0].body).toEqual({ userId: USER, roles: [{ name: "GL", duties: ["PostGeneralJournals", "ApproveGeneralJournals"] }] });
    expect(within(await findTable({ name: "Recorded conflicts" })).getByRole("link", { name: "GL-001" })).toHaveAttribute("href", `/compliance/sod/conflicts/${CONFLICT}`);
  });
});

describe("SoD rules page", () => {
  it("lists the rules", async () => {
    handler = () => ({ status: 200, body: { items: [{ ruleId: "SEC-001", name: "User administration vs any posting duty", dutyA: "MaintainUsersAndRoles", dutyB: "*Post*", severity: "HIGH", riskId: "SEC-001", mitigationAllowed: true, maxMitigationDays: 365, status: "ACTIVE", version: 1 }] } });
    renderAt("/compliance/sod/rules");
    const table = await findTable({ name: "SoD rules" });
    expect(within(table).getByText("*Post*")).toBeInTheDocument();
    expect(within(table).getByText("Up to 365 days")).toBeInTheDocument();
  });

  it("shows an empty rule set", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/compliance/sod/rules");
    expect(await screen.findByText("No rules yet")).toBeInTheDocument();
  });
});

describe("Country packs", () => {
  it("lists packs and opens one", async () => {
    handler = () => ({ status: 200, body: { items: [ukPack] } });
    const { container } = renderAt("/compliance/packs");
    const table = await findTable({ name: "Country packs" });
    expect(within(table).getByText("United Kingdom")).toBeInTheDocument();
    expect(calls[0].url.pathname).toBe("/api/v1/localization/packs");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a load failure", async () => {
    handler = () => problem(403, "AUTH-0003", "missing duty LocalizationAdmin");
    renderAt("/compliance/packs");
    expect((await findAlert()).textContent).toContain("missing duty LocalizationAdmin (AUTH-0003)");
  });

  it("shows a pack's contents and runs the install and tax-id checks", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/install:check")) return problem(422, "LOC-0503", "pack UK localizes GB, the legal entity is in FR");
      if (url.pathname.endsWith("/tax-ids:validate")) return { status: 200, body: { valid: true, scheme: "GB_VAT" } };
      return { status: 200, body: ukPack };
    };
    const { container } = renderAt("/compliance/packs/UK");
    expect(await screen.findByRole("heading", { name: "United Kingdom pack" })).toBeInTheDocument();
    expect(screen.getByText("April")).toBeInTheDocument();
    expect(within(await findTable({ name: "Statutory reports" })).getByText("UK_MTD_VAT")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Legal entity country"), { target: { value: "fr" } });
    fireEvent.click(screen.getByRole("button", { name: "Check install" }));
    expect((await findAlert()).textContent).toBe("pack UK localizes GB, the legal entity is in FR (LOC-0503)");
    const install = calls.find((c) => c.url.pathname.endsWith("/install:check"));
    expect(install?.body).toEqual({ entityCountry: "FR", primaryInstalled: 0, calendarOpenPeriodsOk: true });

    fireEvent.change(screen.getByLabelText("Tax id (GB_VAT)"), { target: { value: "GB123456789" } });
    fireEvent.click(screen.getByRole("button", { name: "Check tax id" }));
    expect((await findStatus()).textContent).toBe("GB123456789 is a valid GB_VAT.");
    expect(calls.find((c) => c.url.pathname.endsWith("/tax-ids:validate"))?.body).toEqual({ scheme: "GB_VAT", value: "GB123456789" });
  });
});
