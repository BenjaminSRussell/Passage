import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findAlert, findStatus, findTable } from "../../test-utils";
import { codesFor, parseViolations, toTransitionInput, type ActionValues } from "./AuditCasesPage";
import { parseDocuments, toAuditInput, type AuditValues } from "./AuditRunPage";
import { approvalText, toPlanInput } from "./BreakGlassPage";
import compliance from "./module";

const POLICY = "33333333-0000-4000-8000-000000000003";
const ME = "11111111-0000-4000-8000-000000000001";
const OTHER = "44444444-0000-4000-8000-000000000004";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown };
let handler: (url: URL) => Reply;
let calls: { method: string; url: URL; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body } = handler(url);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
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

const baseAudit: AuditValues = {
  policyId: POLICY,
  version: "2",
  type: "KEYWORD",
  words: "gift cards, alcohol",
  currency: "",
  limit: "",
  limits: "",
  holidays: "",
  seed: "",
  sampleBy: "percent",
  sampleSize: "",
  documents: "exp-1 | E-7 | MEALS | 250.00 | USD | 2026-10-03 | | Team gift cards",
};

describe("audit rule helpers", () => {
  it("parses one document per line and reports the bad line", () => {
    expect(parseDocuments("inv-1 | V-1 | | 1000.00 | USD | 2026-10-01 | INV-0042\n\ninv-2").documents).toEqual([
      { id: "inv-1", partyId: "V-1", amount: "1000.00", currency: "USD", date: "2026-10-01T00:00:00Z", number: "INV-0042" },
      { id: "inv-2" },
    ]);
    expect(parseDocuments("a | p | c | 1,000").error).toBe("Line 1: the amount must be a number such as 1250.50.");
    expect(parseDocuments("a\na").error).toBe("Line 2: document a is listed twice.");
    expect(parseDocuments("a | p | c | 1 | usd").error).toBe("Line 1: the currency must be a three-letter code such as USD.");
    expect(parseDocuments("a | p | c | 1 | USD | 03/10/2026").error).toBe("Line 1: the date must be YYYY-MM-DD.");
  });

  it("builds the rule spec of each type with decimal strings", () => {
    expect(toAuditInput(baseAudit).input?.spec).toEqual({ type: "KEYWORD", keywords: ["gift cards", "alcohol"] });
    const threshold = toAuditInput({ ...baseAudit, type: "THRESHOLD", currency: "usd", limits: "MEALS = 200\nTRAVEL=1500.50" });
    expect(threshold.input?.spec).toEqual({ type: "THRESHOLD", currency: "USD", limits: { MEALS: "200", TRAVEL: "1500.50" } });
    expect(toAuditInput({ ...baseAudit, type: "THRESHOLD", currency: "USD", limits: "MEALS" }).errors.limits).toBe('Line 1: write "CATEGORY = positive amount".');
    expect(toAuditInput({ ...baseAudit, type: "AGGREGATE", currency: "USD", limit: "0" }).errors.limit).toBe("Enter a positive monthly limit.");
    expect(toAuditInput({ ...baseAudit, type: "SAMPLING", seed: "run-7", sampleBy: "everyK", sampleSize: "10" }).input?.spec).toEqual({ type: "SAMPLING", sample: { seed: "run-7", everyK: 10 } });
    expect(toAuditInput({ ...baseAudit, type: "SAMPLING", seed: "s", sampleSize: "150" }).errors.sampleSize).toBe("Enter a whole percentage from 1 to 100.");
    expect(toAuditInput({ ...baseAudit, type: "WEEKEND_HOLIDAY", holidays: "2026-12-25, 26/12" }).errors.holidays).toBe("26/12 is not a YYYY-MM-DD date.");
    expect(toAuditInput({ ...baseAudit, type: "DUPLICATE", documents: "inv-1 | V-1" }).errors.documents).toBe("Document inv-1 needs a date for this rule.");
    expect(toAuditInput({ ...baseAudit, policyId: "pol-1", version: "0", words: "" }).errors).toEqual({
      policyId: "Enter the policy's UUID.",
      version: "Enter a whole version number from 1.",
      words: "Enter at least one keyword.",
    });
  });
});

describe("Audit rule run", () => {
  it("runs a duplicate check and lists the later invoice with its evidence", async () => {
    handler = () => ({
      status: 200,
      body: { count: 1, code: "AUD-0701", findings: [{ key: `audit:${POLICY}:v1:inv-2`, ruleType: "DUPLICATE", documentId: "inv-2", score: 100, evidence: { vendorId: "V-1", duplicateOf: "inv-1" } }] },
    });
    const { container } = renderAt("/compliance/audit/run");
    expect(screen.getByText("No run yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Policy id"), { target: { value: POLICY } });
    fireEvent.change(screen.getByLabelText("Documents"), {
      target: { value: "inv-1 | V-1 | | 1000.00 | USD | 2026-10-01 | INV-0042\ninv-2 | V-1 | | 1003.00 | USD | 2026-10-11 | INV 42" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Run rule" }));
    expect((await findStatus()).textContent).toBe("1 document violates the policy (AUD-0701).");
    expect(calls[0].method).toBe("POST");
    expect(calls[0].url.pathname).toBe("/api/v1/compliance/audit-policies:evaluate");
    expect(calls[0].body).toMatchObject({ policyId: POLICY, version: 1, spec: { type: "DUPLICATE" } });
    expect((calls[0].body as { documents: unknown[] }).documents[1]).toEqual({ id: "inv-2", partyId: "V-1", amount: "1003.00", currency: "USD", date: "2026-10-11T00:00:00Z", number: "INV 42" });
    const table = await findTable({ name: "Audit violations" });
    expect(within(table).getByText("inv-2")).toBeInTheDocument();
    expect(within(table).getByText("Repeats inv-1")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the threshold fields and an empty result", async () => {
    handler = () => ({ status: 200, body: { count: 0, findings: [] } });
    renderAt("/compliance/audit/run");
    fireEvent.change(screen.getByLabelText("Policy id"), { target: { value: POLICY } });
    fireEvent.change(screen.getByLabelText("Rule"), { target: { value: "THRESHOLD" } });
    fireEvent.change(screen.getByLabelText("Limit currency"), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText("Limits by category"), { target: { value: "MEALS = 200" } });
    fireEvent.change(screen.getByLabelText("Documents"), { target: { value: "exp-1 | E-1 | MEALS | 150 | USD" } });
    fireEvent.click(screen.getByRole("button", { name: "Run rule" }));
    expect((await findStatus()).textContent).toBe("No document violates this rule.");
    expect(calls[0].body).toMatchObject({ spec: { type: "THRESHOLD", currency: "USD", limits: { MEALS: "200" } } });
    expect(await screen.findByText("Every document passes this rule.")).toBeInTheDocument();
  });

  it("validates before calling the service", async () => {
    handler = () => ({ status: 200, body: {} });
    renderAt("/compliance/audit/run");
    fireEvent.change(screen.getByLabelText("Rule"), { target: { value: "KEYWORD" } });
    fireEvent.click(screen.getByRole("button", { name: "Run rule" }));
    expect(await screen.findByText("Enter the policy's UUID.")).toBeInTheDocument();
    expect(screen.getByText("Enter at least one keyword.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("shows the service refusal with its code", async () => {
    handler = () => problem(422, "AUD-0712", "THRESHOLD limit for category \"MEALS\" must be positive");
    renderAt("/compliance/audit/run");
    fireEvent.change(screen.getByLabelText("Policy id"), { target: { value: POLICY } });
    fireEvent.change(screen.getByLabelText("Rule"), { target: { value: "WEEKEND_HOLIDAY" } });
    fireEvent.click(screen.getByRole("button", { name: "Run rule" }));
    expect((await findAlert()).textContent).toContain("must be positive (AUD-0712)");
    expect(screen.queryByRole("table")).toBeNull();
  });
});

describe("audit case helpers", () => {
  const base: ActionValues = { status: "IN_REVIEW", action: "RESOLVE_ISSUE", resolutionCode: "DUPLICATE_RECOVERED", recovery: "1250.50", currency: "USD", closedOn: "" };

  it("parses violations and requires the vendor on duplicates", () => {
    expect(parseViolations(`v1 | ${POLICY} | duplicate | inv-2 | V-1\nv2 | ${POLICY} | KEYWORD | exp-1 | | case-9`).violations).toEqual([
      { id: "v1", policyId: POLICY, ruleType: "DUPLICATE", documentId: "inv-2", partyId: "V-1" },
      { id: "v2", policyId: POLICY, ruleType: "KEYWORD", documentId: "exp-1", caseId: "case-9" },
    ]);
    expect(parseViolations(`v1 | ${POLICY} | DUPLICATE | inv-2`).error).toBe("Line 1: a DUPLICATE violation needs its vendor.");
    expect(parseViolations("v1 | p").error).toBe('Line 1: write "id | policy | rule type | document | party | case".');
  });

  it("offers the resolution codes of the action and builds the request", () => {
    expect(codesFor("RESOLVE_NO_ISSUE")).toEqual(["NO_ISSUE", "FALSE_POSITIVE", "SAMPLE_CLEARED"]);
    expect(codesFor("START_REVIEW")).toEqual([]);
    expect(toTransitionInput(base).input).toEqual({ status: "IN_REVIEW", action: "RESOLVE_ISSUE", resolutionCode: "DUPLICATE_RECOVERED", recoveryAmount: "1250.50", currency: "USD" });
    expect(toTransitionInput({ ...base, currency: "" }).errors.currency).toBe("Enter the three-letter currency of the recovery.");
    expect(toTransitionInput({ ...base, resolutionCode: "NO_ISSUE" }).errors.resolutionCode).toBe("Choose a resolution code.");
    expect(toTransitionInput({ ...base, status: "OPEN" }).errors.action).toBe("Choose an action allowed from this status.");
    const now = new Date("2026-11-02T09:00:00Z");
    const archive = { ...base, status: "CLOSED" as const, action: "ARCHIVE" as const, closedOn: "2025-10-01" };
    expect(toTransitionInput(archive, now).input).toEqual({ status: "CLOSED", action: "ARCHIVE", closedAt: "2025-10-01T00:00:00.000Z" });
    expect(toTransitionInput({ ...archive, closedOn: "2026-12-01" }, now).errors.closedOn).toBe("The closing day cannot be in the future.");
  });
});

describe("Audit cases", () => {
  it("groups violations into one case per vendor", async () => {
    handler = () => ({ status: 200, body: { cases: [{ key: "DUPLICATE_PAYMENT:V-1", category: "DUPLICATE_PAYMENT", violationIds: ["v1", "v2"], documentIds: ["inv-1", "inv-2"] }] } });
    const { container } = renderAt("/compliance/audit/cases");
    expect(screen.getByText("No cases yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Violations"), { target: { value: `v1 | ${POLICY} | DUPLICATE | inv-1 | V-1\nv2 | ${POLICY} | DUPLICATE | inv-2 | V-1` } });
    fireEvent.click(screen.getByRole("button", { name: "Group into cases" }));
    expect((await findStatus()).textContent).toBe("1 case to open.");
    expect(calls[0].url.pathname).toBe("/api/v1/compliance/audit-cases:group");
    expect((calls[0].body as { violations: unknown[] }).violations).toHaveLength(2);
    const table = await findTable({ name: "Cases to open" });
    expect(within(table).getByText("Duplicate payment")).toBeInTheDocument();
    expect(within(table).getByText("V-1")).toBeInTheDocument();
    expect(within(table).getByText("v1, v2")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("refuses an empty list without calling the service", async () => {
    handler = () => ({ status: 200, body: { cases: [] } });
    renderAt("/compliance/audit/cases");
    fireEvent.click(screen.getByRole("button", { name: "Group into cases" }));
    expect(await screen.findByText("Add at least one violation.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("only offers the actions allowed from the status and shows the result", async () => {
    handler = () => ({ status: 200, body: { status: "RESOLVED_NO_ISSUE", resolutionCode: "FALSE_POSITIVE", recoveryAmount: "0", asOf: "2026-11-02T09:00:00Z" } });
    renderAt("/compliance/audit/cases");
    const action = screen.getByLabelText("Action");
    expect(within(action).getAllByRole("option").map((o) => o.textContent)).toEqual(["Start review"]);
    fireEvent.change(screen.getByLabelText("Current status"), { target: { value: "IN_REVIEW" } });
    expect(within(screen.getByLabelText("Action")).getAllByRole("option").map((o) => o.textContent)).toEqual(["Resolve: no issue", "Resolve: issue found"]);
    fireEvent.change(screen.getByLabelText("Resolution code"), { target: { value: "FALSE_POSITIVE" } });
    fireEvent.click(screen.getByRole("button", { name: "Check action" }));
    expect(await screen.findByText("Case after the action")).toBeInTheDocument();
    expect(calls[0].url.pathname).toBe("/api/v1/compliance/audit-cases:transition");
    expect(calls[0].body).toEqual({ status: "IN_REVIEW", action: "RESOLVE_NO_ISSUE", resolutionCode: "FALSE_POSITIVE" });
    expect(within(screen.getByText("Status", { selector: "dt" }).parentElement as HTMLElement).getByText("Resolved no issue")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Current status"), { target: { value: "ARCHIVED" } });
    expect(screen.getByLabelText("Action")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Check action" })).toBeDisabled();
  });

  it("shows the archive refusal from the service", async () => {
    handler = () => problem(422, "AUD-0715", "case stays in the active tier for 365 days after closing");
    renderAt("/compliance/audit/cases");
    fireEvent.change(screen.getByLabelText("Current status"), { target: { value: "CLOSED" } });
    fireEvent.change(screen.getByLabelText("Closed on (UTC)"), { target: { value: "2026-10-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Check action" }));
    expect((await findAlert()).textContent).toContain("365 days after closing (AUD-0715)");
    expect(calls[0].body).toEqual({ status: "CLOSED", action: "ARCHIVE", closedAt: "2026-10-01T00:00:00.000Z" });
  });
});

describe("Break-glass access", () => {
  const plan = {
    requesterId: ME,
    targetUserId: ME,
    roles: ["APClerk"],
    minutes: 60,
    startsAt: "2026-11-02T09:00:00Z",
    endsAt: "2026-11-02T10:00:00Z",
    reviewDueAt: "2026-11-05T10:00:00Z",
    needsApproval: true,
    approverDuty: "BreakGlassApprove",
    requiresPostReview: false,
  };

  it("never sends a requester and checks the fields", () => {
    const values = { target: "", roles: "APClerk, APClerk ,GLPost", reason: "INCIDENT_RESPONSE" as const, ticket: " INC-1 ", minutes: "" };
    expect(toPlanInput(values).input).toEqual({ roles: ["APClerk", "GLPost"], reasonCode: "INCIDENT_RESPONSE", ticketRef: "INC-1" });
    expect(toPlanInput({ ...values, target: OTHER.toUpperCase(), minutes: "90" }).input).toEqual({
      targetUserId: OTHER,
      roles: ["APClerk", "GLPost"],
      reasonCode: "INCIDENT_RESPONSE",
      ticketRef: "INC-1",
      requestedMinutes: 90,
    });
    expect(toPlanInput({ ...values, minutes: "300" }).errors.minutes).toBe("Enter whole minutes from 15 to 240.");
    expect(toPlanInput({ ...values, target: "bob" }).errors.target).toBe("Enter the user's UUID, or leave empty for yourself.");
  });

  it("describes each approval path", () => {
    expect(approvalText({ ...plan, approverDuty: "PlatformAdmin" })).toBe("Waits for a second PlatformAdmin to approve (protected role).");
    expect(approvalText(plan as Parameters<typeof approvalText>[0])).toBe("Waits for a break-glass approver.");
    expect(approvalText({ ...plan, needsApproval: false, approverDuty: undefined, requiresPostReview: true })).toBe("Starts at once; a reviewer must sign off afterwards.");
  });

  it("plans a session and shows the review deadline", async () => {
    handler = () => ({ status: 200, body: plan });
    const { container } = renderAt("/compliance/break-glass");
    expect(screen.getByText("No session planned")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Roles"), { target: { value: "APClerk" } });
    fireEvent.change(screen.getByLabelText("Ticket reference"), { target: { value: "INC-4411" } });
    fireEvent.click(screen.getByRole("button", { name: "Plan session" }));
    expect((await findStatus()).textContent).toBe("60-minute session planned. Waits for a break-glass approver.");
    expect(calls[0].url.pathname).toBe("/api/v1/security/elevated-sessions:plan");
    expect(calls[0].body).toEqual({ roles: ["APClerk"], reasonCode: "INCIDENT_RESPONSE", ticketRef: "INC-4411" });
    expect(screen.getByText("2026-11-05 10:00 UTC")).toBeInTheDocument();
    expect(screen.getByText("You")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the self-grant refusal (BR-48.19)", async () => {
    handler = () => problem(422, "SEC-0707", "break-glass cannot grant SecurityAdmin to yourself");
    renderAt("/compliance/break-glass");
    fireEvent.change(screen.getByLabelText("Roles"), { target: { value: "SecurityAdmin" } });
    fireEvent.change(screen.getByLabelText("Ticket reference"), { target: { value: "INC-4411" } });
    fireEvent.click(screen.getByRole("button", { name: "Plan session" }));
    expect((await findAlert()).textContent).toContain("cannot grant SecurityAdmin to yourself (SEC-0707)");
    expect(screen.getByText("No session planned")).toBeInTheDocument();
  });

  it("validates before calling the service", async () => {
    handler = () => ({ status: 200, body: plan });
    renderAt("/compliance/break-glass");
    fireEvent.click(screen.getByRole("button", { name: "Plan session" }));
    expect(await screen.findByText("Enter at least one role.")).toBeInTheDocument();
    expect(screen.getByText("Enter the incident or change ticket.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});
