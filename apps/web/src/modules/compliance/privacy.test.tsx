import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findAlert, findStatus, findTable } from "../../test-utils";
import type { SubjectRequest } from "./api/client";
import { parseEntries } from "./CampaignPage";
import { decisionText } from "./ExportPolicyPage";
import { nextStatus, toStepInput, type StepValues } from "./IncidentStepPage";
import compliance from "./module";
import { toIntakeInput, type IntakeValues } from "./PrivacyRequestNewPage";
import { nextActions, parseRecord, toActionInput, type ActionValues } from "./PrivacyRequestPage";

const REQ = "77777777-0000-4000-8000-000000000007";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
let handler: (url: URL, init: RequestInit) => Reply;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = handler(url, init);
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
      </Routes>
    </MemoryRouter>,
  );
}

const problem = (status: number, code: string, detail: string): Reply => ({ status, body: { type: "x", title: "x", status, code, detail } });

const inDays = (days: number) => new Date(Date.now() + days * 86_400_000).toISOString();

function request(patch: Partial<SubjectRequest> = {}): SubjectRequest {
  return {
    requestId: REQ,
    type: "ERASURE",
    regulation: "GDPR",
    subjectRef: "party:42",
    receivedAt: inDays(-27),
    deadlineAt: inDays(3),
    state: "RECEIVED",
    holdIds: [],
    late: false,
    createdBy: "intake-1",
    createdAt: inDays(-27),
    updatedAt: inDays(-27),
    version: 1,
    ...patch,
  };
}

describe("privacy helpers", () => {
  const now = new Date("2026-11-02T09:00:00Z");

  it("builds the intake and refuses a longer deadline than the regulation", () => {
    const v: IntakeValues = { type: "ACCESS", regulation: "CCPA", subjectRef: " contact:7 ", receivedOn: "2026-11-01", deadlineDays: "" };
    expect(toIntakeInput(v, now).input).toEqual({ type: "ACCESS", regulation: "CCPA", subjectRef: "contact:7", receivedAt: "2026-11-01T00:00:00.000Z" });
    expect(toIntakeInput({ ...v, deadlineDays: "46" }, now).errors.deadlineDays).toContain("1 to 45");
    expect(toIntakeInput({ ...v, regulation: "GDPR", deadlineDays: "31" }, now).errors.deadlineDays).toContain("1 to 30");
    expect(toIntakeInput({ ...v, subjectRef: " ", receivedOn: "2026-12-01" }, now).errors).toEqual({
      subjectRef: "Enter the party, worker, user or contact, for example party:42.",
      receivedOn: "The request cannot arrive in the future.",
    });
  });

  it("offers only the steps the service allows", () => {
    expect(nextActions.RECEIVED).toEqual(["VERIFY", "REJECT"]);
    expect(nextActions.HELD).toEqual(["RELEASE_HOLD", "REJECT"]);
    expect(nextActions.CLOSED).toEqual([]);
  });

  it("checks verification, late close and rejection", () => {
    const base: ActionValues = { action: "VERIFY", attributes: "email, email", emailLink: false, holds: "", justification: "" };
    expect(toActionInput(base, request(), now).errors).toEqual({
      attributes: "Two different attributes must match the record, for example e-mail and postal code.",
      emailLink: "The requester must confirm the e-mail link first.",
    });
    expect(toActionInput({ ...base, attributes: "email, postal_code", emailLink: true }, request(), now).input).toEqual({
      action: "VERIFY",
      matchedAttributes: ["email", "postal_code"],
      emailLinkVerified: true,
    });
    const overdue = request({ deadlineAt: "2026-11-01T00:00:00Z" });
    expect(toActionInput({ ...base, action: "CLOSE" }, overdue, now).errors.justification).toContain("deadline has passed");
    expect(toActionInput({ ...base, action: "CLOSE" }, request({ deadlineAt: "2026-12-01T00:00:00Z" }), now).input).toEqual({ action: "CLOSE" });
    expect(toActionInput({ ...base, action: "REJECT", justification: "no" }, request(), now).errors.justification).toContain("reason");
    expect(toActionInput({ ...base, action: "DISCOVER", holds: "HOLD-7, HOLD-7\nHOLD-3" }, request(), now).input).toEqual({ action: "DISCOVER", holdIds: ["HOLD-7", "HOLD-3"] });
  });

  it("parses a record for the erasure preview", () => {
    expect(parseRecord("email=ada@example.com\namount = 120.00\nnot a field")).toEqual({ email: "ada@example.com", amount: "120.00" });
  });
});

describe("Privacy requests", () => {
  it("lists requests by deadline with days left and filters by state and type", async () => {
    handler = () => ({ status: 200, body: { items: [request(), request({ requestId: "88888888-0000-4000-8000-000000000008", subjectRef: "worker:9", state: "CLOSED", late: true })] } });
    const { container } = renderAt("/compliance/privacy/requests");
    const table = await findTable({ name: "Privacy requests" });
    expect(calls[0].url.pathname).toBe("/api/v1/privacy/requests");
    expect(within(table).getByText("party:42")).toBeInTheDocument();
    expect(within(table).getByText("3 d left")).toBeInTheDocument();
    expect(within(table).getByText("Closed late")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("State"), { target: { value: "HELD" } });
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "ERASURE" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await findTable({ name: "Privacy requests" });
    const last = calls[calls.length - 1].url.searchParams;
    expect(last.get("state")).toBe("HELD");
    expect(last.get("type")).toBe("ERASURE");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/compliance/privacy/requests");
    expect(await screen.findByText("No requests match")).toBeInTheDocument();
  });

  it("shows a load failure with a retry", async () => {
    handler = () => problem(403, "AUTH-0003", "missing duty PrivacyOfficer");
    renderAt("/compliance/privacy/requests");
    expect((await findAlert()).textContent).toContain("missing duty PrivacyOfficer (AUTH-0003)");
    handler = () => ({ status: 200, body: { items: [request()] } });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Privacy requests" })).toBeInTheDocument();
  });
});

describe("Record a privacy request", () => {
  it("posts the intake with an Idempotency-Key and opens the request", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, etag: '"1"', body: request({ type: "ACCESS" }) } : { status: 200, etag: '"1"', body: request({ type: "ACCESS" }) });
    const { container } = renderAt("/compliance/privacy/requests/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Record request" }));
    expect(await screen.findByText("Enter the party, worker, user or contact, for example party:42.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Subject"), { target: { value: "party:42" } });
    fireEvent.click(screen.getByRole("button", { name: "Record request" }));
    expect(await screen.findByRole("heading", { name: "Access request for party:42" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/privacy/requests");
    expect(post?.body).toEqual({ type: "ACCESS", regulation: "GDPR", subjectRef: "party:42" });
    expect(post?.headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("shows the service's refusal", async () => {
    handler = () => problem(422, "PRV-1206", "deadline 40 days is outside 1..30 for GDPR");
    renderAt("/compliance/privacy/requests/new");
    fireEvent.change(screen.getByLabelText("Subject"), { target: { value: "party:42" } });
    fireEvent.click(screen.getByRole("button", { name: "Record request" }));
    expect((await findAlert()).textContent).toContain("(PRV-1206)");
  });
});

describe("Privacy request", () => {
  it("verifies identity at the loaded ETag", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 200, etag: '"2"', body: request({ state: "VERIFIED", version: 2, verifiedAt: inDays(0) }) } : { status: 200, etag: '"1"', body: request() };
    const { container } = renderAt(`/compliance/privacy/requests/${REQ}`);
    expect(await screen.findByRole("heading", { name: "Erasure request for party:42" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Matched attributes"), { target: { value: "email, postal_code" } });
    fireEvent.click(screen.getByLabelText("The requester confirmed the e-mail link"));
    fireEvent.click(screen.getByRole("button", { name: "Verify identity" }));
    expect((await findStatus()).textContent).toBe("Verify identity: the request is now verified.");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/privacy/requests/${REQ}/actions`);
    expect(post?.headers.get("If-Match")).toBe('"1"');
    expect(post?.body).toEqual({ action: "VERIFY", matchedAttributes: ["email", "postal_code"], emailLinkVerified: true });
    // The next step follows the new state.
    expect(screen.getByRole("button", { name: "Record discovery" })).toBeInTheDocument();
  });

  it("shows a refused release while a hold stands and previews the erasure", async () => {
    handler = (url, init) => {
      if (url.pathname.endsWith("/actions")) return problem(422, "PRV-1201", "erasure blocked: subject is under legal hold HOLD-7; no field was changed");
      if (url.pathname.endsWith("/erasure-preview")) return { status: 200, body: { record: { email: "ERASED-1a2b", amount: "120.00", erased: "true" }, pseudonymized: ["email"] } };
      return init.method === "POST" ? problem(500, "COM-0500", "x") : { status: 200, etag: '"3"', body: request({ state: "HELD", holdIds: ["HOLD-7"], version: 3 }) };
    };
    renderAt(`/compliance/privacy/requests/${REQ}`);
    expect(await screen.findByText("HOLD-7", { selector: "dd" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Release from hold" }));
    expect((await findAlert()).textContent).toContain("legal hold HOLD-7");
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ action: "RELEASE_HOLD", holdIds: ["HOLD-7"] });

    fireEvent.change(screen.getByLabelText("Record"), { target: { value: "email=ada@example.com\namount=120.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    const preview = await screen.findByLabelText("Record after erasure");
    expect(within(preview).getByText("Pseudonymized")).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname.endsWith("/erasure-preview"))?.body).toEqual({ record: { email: "ada@example.com", amount: "120.00" } });
  });

  it("shows a missing request", async () => {
    handler = () => problem(404, "PRV-1209", "no subject request with that id");
    renderAt(`/compliance/privacy/requests/${REQ}`);
    expect((await findAlert()).textContent).toContain("no subject request with that id (PRV-1209)");
    expect(screen.getByRole("link", { name: "Back to privacy requests" })).toBeInTheDocument();
  });
});

describe("Certification campaign", () => {
  it("parses decision lines", () => {
    expect(parseEntries("mgr-1, u-1, AP, certify\nmgr-1, u-2, GL\n").entries).toEqual([
      { managerId: "mgr-1", userId: "u-1", roleName: "AP", decision: "CERTIFY" },
      { managerId: "mgr-1", userId: "u-2", roleName: "GL" },
    ]);
    expect(parseEntries("mgr-1, u-1").error).toBe("Line 1: give a manager, a user and a role.");
    expect(parseEntries("m, u, AP, maybe").error).toBe("Line 1: the decision must be certify, revoke or pending.");
    expect(parseEntries("").error).toBe("Enter at least one line.");
  });

  it("plans the window and checks the close", async () => {
    handler = (url) =>
      url.pathname.endsWith(":plan")
        ? { status: 200, body: { startsAt: "2026-10-01T00:00:00Z", closesAt: "2026-10-15T00:00:00Z" } }
        : {
            status: 200,
            body: {
              closed: true,
              revoke: [{ managerId: "mgr-1", userId: "u-2", roleName: "GL", decision: "REVOKE" }],
              flagged: [],
              progress: { total: 2, certified: 1, revoked: 1, pending: 0, percentCertified: 50, overdueByManager: { "mgr-1": 1 } },
              asOf: "2026-11-02T09:00:00Z",
            },
          };
    const { container } = renderAt("/compliance/campaigns");
    expect(screen.getByText("No campaign checked")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Starts on (UTC)"), { target: { value: "2026-10-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Plan window" }));
    expect((await findStatus()).textContent).toBe("The campaign closes 2026-10-15 00:00 UTC.");
    expect(calls[0].body).toEqual({ startsAt: "2026-10-01T00:00:00.000Z" });
    fireEvent.change(screen.getByLabelText("Lines"), { target: { value: "mgr-1, u-1, AP, certify\nmgr-1, u-2, GL" } });
    fireEvent.click(screen.getByLabelText("Auto-revoke roles nobody decided on at the close"));
    fireEvent.click(screen.getByRole("button", { name: "Check close" }));
    const table = await findTable({ name: "Roles to revoke" });
    expect(within(table).getByText("GL")).toBeInTheDocument();
    expect(calls[1].body).toEqual({
      entries: [
        { managerId: "mgr-1", userId: "u-1", roleName: "AP", decision: "CERTIFY" },
        { managerId: "mgr-1", userId: "u-2", roleName: "GL" },
      ],
      autoRevoke: true,
      closesAt: "2026-10-15T00:00:00Z",
    });
    expect(screen.getByText("50%")).toBeInTheDocument();
    expect(screen.getByText("mgr-1: 1")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a refused window", async () => {
    handler = () => problem(422, "SOD-1120", "certification window 90 days is outside 3..60");
    renderAt("/compliance/campaigns");
    fireEvent.change(screen.getByLabelText("Starts on (UTC)"), { target: { value: "2026-10-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Plan window" }));
    expect((await findAlert()).textContent).toContain("(SOD-1120)");
  });
});

describe("Incident step check", () => {
  const now = new Date("2026-11-10T09:00:00Z");
  const base: StepValues = { severity: "SEV2", source: "ALERT", from: "RECOVERED", breach: true, awareAt: "2026-11-02T09:00", regimes: ["GDPR"], notified: { GDPR: "2026-11-04T09:00" }, justification: "" };

  it("moves one step and builds the breach body in UTC", () => {
    expect(nextStatus("")).toBe("TRIAGE");
    expect(nextStatus("RECOVERED")).toBe("CLOSED");
    expect(nextStatus("CLOSED")).toBeNull();
    expect(toStepInput(base, now).input).toEqual({
      severity: "SEV2",
      source: "ALERT",
      from: "RECOVERED",
      to: "CLOSED",
      personalDataBreach: true,
      awareAt: "2026-11-02T09:00:00.000Z",
      regimes: ["GDPR"],
      notifiedAt: { GDPR: "2026-11-04T09:00:00.000Z" },
    });
    expect(toStepInput({ ...base, awareAt: "", regimes: [], notified: {} }, now).errors).toEqual({ awareAt: "Enter when you became aware of the breach.", regimes: "Choose at least one regime." });
    expect(toStepInput({ ...base, notified: { GDPR: "2026-12-01T00:00" } }, now).errors.notified).toBe("A notification time must be a past date and time.");
  });

  it("checks a step and shows the clocks", async () => {
    handler = () => ({
      status: 200,
      body: {
        status: "TRIAGE",
        pageOnCall: true,
        asOf: "2026-11-02T10:00:00Z",
        clocks: [{ regime: "GDPR", recipient: "supervisory authority", basis: "GDPR Art. 33", deadlineAt: "2026-11-05T09:00:00Z", status: "NOT_DUE", met: false, late: false }],
      },
    });
    const { container } = renderAt("/compliance/incidents/step");
    fireEvent.change(screen.getByLabelText("Severity"), { target: { value: "SEV1" } });
    fireEvent.click(screen.getByLabelText("Confirmed personal-data breach"));
    fireEvent.change(screen.getByLabelText("Aware at (UTC)"), { target: { value: "2026-01-02T09:00" } });
    fireEvent.click(screen.getByLabelText("EU GDPR"));
    fireEvent.click(screen.getByRole("button", { name: "Check step" }));
    expect((await findStatus()).textContent).toBe("The incident can move to triage. Sev-1: page the on-call responder.");
    expect(calls[0].url.pathname).toBe("/api/v1/security/incidents:transition");
    expect(calls[0].body).toMatchObject({ severity: "SEV1", to: "TRIAGE", personalDataBreach: true, regimes: ["GDPR"] });
    const table = await findTable({ name: "Notification clocks" });
    expect(within(table).getByText("Not yet sent")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a refused close", async () => {
    handler = () => problem(422, "INC-1302", "GDPR notification missed its deadline");
    renderAt("/compliance/incidents/step");
    fireEvent.change(screen.getByLabelText("Current status"), { target: { value: "RECOVERED" } });
    fireEvent.click(screen.getByRole("button", { name: "Check step" }));
    expect((await findAlert()).textContent).toContain("(INC-1302)");
  });
});

describe("Export policy check", () => {
  it("describes the decision", () => {
    expect(decisionText({ allowed: false, actions: ["APPROVAL", "AUDIT"], requiresReason: false, requiresApproval: true, watermark: false, audit: true })).toBe(
      "The download waits for approval. The export is logged.",
    );
  });

  it("checks a download and a read", async () => {
    handler = (url) =>
      url.pathname.endsWith("export-governance:evaluate")
        ? { status: 200, body: { allowed: true, actions: ["REASON", "WATERMARK"], requiresReason: true, requiresApproval: false, watermark: true, audit: true } }
        : { status: 200, body: { logged: true, fields: ["hr.worker.salary"], retentionDays: 730 } };
    const { container } = renderAt("/compliance/export-policy");
    fireEvent.change(screen.getByLabelText("Rows"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText(/^Reason/), { target: { value: "subject access check for ticket 4412" } });
    fireEvent.click(screen.getByRole("button", { name: "Check download" }));
    expect((await screen.findByText(/The download may go ahead/)).textContent).toContain("watermarked");
    expect(calls[0].body).toEqual({ classification: "PII", channel: "GRID", rows: 12, reason: "subject access check for ticket 4412" });
    fireEvent.change(screen.getByLabelText("Fields read"), { target: { value: "hr.worker.salary, hr.worker.name" } });
    fireEvent.click(screen.getByRole("button", { name: "Check read" }));
    expect(await screen.findByText("Logged and kept 730 days: hr.worker.salary.")).toBeInTheDocument();
    expect(calls[1].body).toEqual({ fields: ["hr.worker.salary", "hr.worker.name"] });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows a denied download", async () => {
    handler = () => problem(422, "SEC-0709", "download denied by export policy for CONFIDENTIAL data");
    renderAt("/compliance/export-policy");
    fireEvent.change(screen.getByLabelText("Rows"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Check download" }));
    expect((await findAlert()).textContent).toContain("(SEC-0709)");
  });
});
