import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { ExpenseReport, ReportLine } from "./api/client";
import { emptyNewReport, NewReportPage, refusalMessages, ReportDetailPage, ReportsPage, validateLine, validateNewReport, emptyLine } from "./reports";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const REPORT_ID = "0199a1b2-0000-7000-8000-000000000001";
const LE = "0199a1b2-0000-7000-8000-0000000000aa";
const WORKER = "0199a1b2-0000-7000-8000-0000000000bb";

type Call = [string, RequestInit];

function line(over: Partial<ReportLine> = {}): ReportLine {
  return {
    id: "0199a1b2-0000-7000-8000-00000000l001",
    lineNo: 1,
    category: "MEALS",
    txnDate: "2025-03-03",
    amount: "30.00",
    merchant: "Deli",
    attendees: 0,
    hasReceipt: false,
    justification: "",
    paidBy: "EMPLOYEE",
    policyResults: [],
    ...over,
  };
}

function report(over: Partial<ExpenseReport> = {}): ExpenseReport {
  return {
    id: REPORT_ID,
    legalEntityId: LE,
    workerId: WORKER,
    purpose: "Client visit Boston",
    currency: "USD",
    status: "DRAFT",
    total: "0.00",
    reimbursable: "0.00",
    companyPaid: "0.00",
    submittable: false,
    version: 1,
    createdAt: "2025-03-01T09:00:00Z",
    submittedAt: null,
    lines: [],
    ...over,
  };
}

/** Answers fetch calls in order; each answer is [status, body]. */
function stubFetch(...answers: [number, unknown][]) {
  const fetchMock = vi.fn(async () => {
    const [status, body] = answers.length > 1 ? answers.shift()! : answers[0];
    return new Response(JSON.stringify(body), { status });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function call(fetchMock: ReturnType<typeof vi.fn>, i: number) {
  const [url, init] = fetchMock.mock.calls[i] as unknown as Call;
  const headers = (init.headers ?? {}) as Record<string, string>;
  return { url, method: init.method, headers, body: init.body ? JSON.parse(String(init.body)) : undefined };
}

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/expense/reports" element={<ReportsPage />} />
        <Route path="/expense/reports/new" element={<NewReportPage />} />
        <Route path="/expense/reports/:id" element={<ReportDetailPage />} />
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

async function loadedTable(name: string) {
  return waitFor(() => {
    const table = screen.getByRole("table", { name });
    expect(table).not.toHaveAttribute("aria-busy", "true");
    return table;
  });
}

afterEach(() => vi.unstubAllGlobals());

describe("expense report list", () => {
  it("shows the reports with status and total, and opens one", async () => {
    const fetchMock = stubFetch([200, { items: [report({ total: "42.50" }), report({ id: "r2", purpose: "Offsite", status: "SUBMITTED" })], nextCursor: "" }], [200, report()]);
    const { container } = renderAt("/expense/reports");
    expect(screen.getByRole("table", { name: "Expense reports" })).toHaveAttribute("aria-busy", "true");
    const table = await loadedTable("Expense reports");
    const row = within(table).getByText("Client visit Boston").closest("tr") as HTMLElement;
    expect(within(row).getByText("Draft")).toBeInTheDocument();
    expect(within(row).getByText("42.50 USD")).toBeInTheDocument();
    expect(within(table).getByText("Submitted")).toBeInTheDocument();
    expect(call(fetchMock, 0)).toMatchObject({ url: "/api/v1/expense/reports", method: "GET" });
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
    fireEvent.click(within(row).getByRole("button", { name: "Client visit Boston" }));
    expect(screen.getByTestId("where")).toHaveTextContent(`/expense/reports/${REPORT_ID}`);
    expect(await screen.findByRole("heading", { name: "Client visit Boston" })).toBeInTheDocument();
  });

  it("filters by status and pages with the cursor", async () => {
    const fetchMock = stubFetch(
      [200, { items: [report()], nextCursor: "" }],
      [200, { items: [report({ status: "SUBMITTED" })], nextCursor: REPORT_ID }],
      [200, { items: [report({ id: "r3", purpose: "Second page", status: "SUBMITTED" })], nextCursor: "" }],
    );
    renderAt("/expense/reports");
    await loadedTable("Expense reports");
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "SUBMITTED" } });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(call(fetchMock, 1).url).toBe("/api/v1/expense/reports?status=SUBMITTED");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("Second page")).toBeInTheDocument();
    expect(call(fetchMock, 2).url).toBe(`/api/v1/expense/reports?status=SUBMITTED&cursor=${REPORT_ID}`);
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    stubFetch([200, { items: [], nextCursor: "" }]);
    renderAt("/expense/reports");
    expect(await screen.findByText("No expense reports yet")).toBeInTheDocument();
  });

  it("shows a failure with a retry", async () => {
    const fetchMock = stubFetch([503, { code: "COM-0503", detail: "report persistence is not configured" }], [200, { items: [report()], nextCursor: "" }]);
    renderAt("/expense/reports");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("report persistence is not configured (COM-0503)");
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Client visit Boston")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});

describe("new expense report", () => {
  it("validates before sending", () => {
    expect(Object.keys(validateNewReport({ ...emptyNewReport, currency: "us", legalEntityId: "le-1" }).errors).sort()).toEqual(["currency", "legalEntityId", "purpose", "workerId"]);
    expect(validateNewReport({ purpose: " Trip ", currency: "eur", legalEntityId: LE, workerId: WORKER }).input).toEqual({ purpose: "Trip", currency: "EUR", legalEntityId: LE, workerId: WORKER });
  });

  it("creates the report with an Idempotency-Key and opens it", async () => {
    const fetchMock = stubFetch([201, report()], [200, report()]);
    renderAt("/expense/reports/new");
    fireEvent.change(screen.getByLabelText("Purpose"), { target: { value: "Client visit Boston" } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Worker"), { target: { value: WORKER } });
    fireEvent.click(screen.getByRole("button", { name: "Create report" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent(`/expense/reports/${REPORT_ID}`));
    expect(await screen.findByRole("heading", { name: "Client visit Boston" })).toBeInTheDocument();
    const sent = call(fetchMock, 0);
    expect(sent).toMatchObject({ url: "/api/v1/expense/reports", method: "POST", body: { purpose: "Client visit Boston", currency: "USD", legalEntityId: LE, workerId: WORKER } });
    expect(sent.headers["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("puts service field errors next to the fields and other problems in an alert", async () => {
    const fetchMock = stubFetch([403, { code: "AUTH-0004", detail: "legal entity is outside the caller's scope" }]);
    renderAt("/expense/reports/new");
    fireEvent.change(screen.getByLabelText("Purpose"), { target: { value: "Trip" } });
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Worker"), { target: { value: WORKER } });
    fireEvent.click(screen.getByRole("button", { name: "Create report" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("legal entity is outside the caller's scope (AUTH-0004)");
    expect(screen.getByTestId("where")).toHaveTextContent("/expense/reports/new");
    const firstKey = call(fetchMock, 0).headers["Idempotency-Key"];
    fireEvent.click(screen.getByRole("button", { name: "Create report" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(call(fetchMock, 1).headers["Idempotency-Key"]).toBe(firstKey);
    fireEvent.change(screen.getByLabelText("Purpose"), { target: { value: "Trip two" } });
    fireEvent.click(screen.getByRole("button", { name: "Create report" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(call(fetchMock, 2).headers["Idempotency-Key"]).not.toBe(firstKey);
  });
});

describe("expense report detail", () => {
  const flagged = report({
    version: 3,
    total: "30.00",
    reimbursable: "30.00",
    lines: [
      line({
        policyResults: [{ lineId: "x", ruleId: "EXP-RCPT-025", ruleVersion: 1, outcome: "REQUIRE_RECEIPT", code: "EXP-REC-001", message: "Receipt required for lines of 25.00 or more", satisfied: false }],
      }),
    ],
  });

  it("validates a line", () => {
    expect(Object.keys(validateLine({ ...emptyLine(), txnDate: "3/3/2025", amount: "x", attendees: "-1" }).errors).sort()).toEqual(["amount", "attendees", "txnDate"]);
    expect(validateLine({ ...emptyLine(), txnDate: "2025-03-03", amount: " 12.5 ", merchant: " Cab ", attendees: "2" }).input).toMatchObject({ amount: "12.5", merchant: "Cab", attendees: 2 });
  });

  it("shows totals, lines with their policy badges, and why it cannot be submitted", async () => {
    const fetchMock = stubFetch([200, flagged]);
    const { container } = renderAt(`/expense/reports/${REPORT_ID}`);
    expect(screen.getByLabelText("Loading the report")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "Client visit Boston" })).toBeInTheDocument();
    expect(call(fetchMock, 0).url).toBe(`/api/v1/expense/reports/${REPORT_ID}`);
    const totals = within(screen.getByRole("list", { name: "Report totals" })).getAllByRole("listitem");
    expect(totals[0]).toHaveTextContent("Total 30.00 USD");
    expect(totals[2]).toHaveTextContent("Company paid 0.00 USD");
    const row = within(screen.getByRole("table", { name: "Lines" })).getByText("Deli").closest("tr") as HTMLElement;
    expect(within(row).getByText("Receipt required")).toBeInTheDocument();
    expect(screen.getByText("Cannot submit yet")).toBeInTheDocument();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("adds a line with a key and shows the updated report", async () => {
    const added = report({ version: 2, total: "12.00", reimbursable: "12.00", submittable: true, lines: [line({ amount: "12.00", merchant: "Cab", category: "TRAVEL" })] });
    const fetchMock = stubFetch([200, report()], [201, added]);
    renderAt(`/expense/reports/${REPORT_ID}`);
    expect(await screen.findByText("No lines yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Category"), { target: { value: "TRAVEL" } });
    fireEvent.change(screen.getByLabelText("Date"), { target: { value: "2025-03-03" } });
    fireEvent.change(screen.getByLabelText("Amount"), { target: { value: "12.00" } });
    fireEvent.change(screen.getByLabelText("Merchant"), { target: { value: "Cab" } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    expect(await screen.findByText("Line 1 added.")).toBeInTheDocument();
    expect(screen.getByText("Cab")).toBeInTheDocument();
    const sent = call(fetchMock, 1);
    expect(sent).toMatchObject({
      url: `/api/v1/expense/reports/${REPORT_ID}/lines`,
      method: "POST",
      body: { category: "TRAVEL", txnDate: "2025-03-03", amount: "12.00", merchant: "Cab", attendees: 0, hasReceipt: false, paidBy: "EMPLOYEE" },
    });
    expect(sent.headers["Idempotency-Key"]).toBeTruthy();
    expect(screen.getByLabelText("Amount")).toHaveValue("");
  });

  it("explains a refused submit per line and reloads the stored results", async () => {
    const fetchMock = stubFetch(
      [200, flagged],
      [422, { code: "EXP-REC-001", detail: "report has unsatisfied policy results", errors: { [`${flagged.lines[0].id}/EXP-RCPT-025`]: "Receipt required for lines of 25.00 or more" } }],
      [200, flagged],
    );
    renderAt(`/expense/reports/${REPORT_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Submit report" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The report was not submitted");
    expect(within(alert).getByText("Line 1: Receipt required for lines of 25.00 or more (EXP-RCPT-025)")).toBeInTheDocument();
    const sent = call(fetchMock, 1);
    expect(sent).toMatchObject({ url: `/api/v1/expense/reports/${REPORT_ID}/submit`, method: "POST" });
    expect(sent.headers["If-Match"]).toBe('"3"');
    expect(sent.headers["Idempotency-Key"]).toBeTruthy();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
  });

  it("removes a line with If-Match, then submits and freezes the report", async () => {
    const cleaned = report({ version: 4, submittable: true, total: "12.00", reimbursable: "12.00", lines: [line({ id: "l2", lineNo: 2, amount: "12.00", merchant: "Cab" })] });
    const both = report({ ...cleaned, version: 3, lines: [flagged.lines[0], cleaned.lines[0]] });
    const submitted = report({ ...cleaned, version: 5, status: "SUBMITTED", submittedAt: "2025-03-05T10:00:00Z" });
    const fetchMock = stubFetch([200, both], [200, cleaned], [200, submitted]);
    renderAt(`/expense/reports/${REPORT_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Remove line 1" }));
    expect(await screen.findByText("Line 1 removed.")).toBeInTheDocument();
    expect(call(fetchMock, 1)).toMatchObject({ url: `/api/v1/expense/reports/${REPORT_ID}/lines/${flagged.lines[0].id}`, method: "DELETE", headers: { "If-Match": '"3"' } });
    fireEvent.click(screen.getByRole("button", { name: "Submit report" }));
    expect(await screen.findByText("Report submitted for approval.")).toBeInTheDocument();
    expect(call(fetchMock, 2).headers["If-Match"]).toBe('"4"');
    expect(screen.getByText("Submitted")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Submit report" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add line" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove line 2" })).not.toBeInTheDocument();
  });

  it("shows not found for an unknown report", async () => {
    stubFetch([404, { code: "EXP-REP-001", detail: "expense report not found" }], [200, { items: [], nextCursor: "" }]);
    renderAt(`/expense/reports/${REPORT_ID}`);
    expect(await screen.findByText("Report not found")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Back to reports" }));
    expect(screen.getByTestId("where")).toHaveTextContent(/^\/expense\/reports$/);
    expect(await screen.findByText("No expense reports yet")).toBeInTheDocument();
  });

  it("shows a retry when the report cannot be loaded", async () => {
    stubFetch([500, { code: "COM-0500", detail: "internal error" }], [200, flagged]);
    renderAt(`/expense/reports/${REPORT_ID}`);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("internal error (COM-0500)");
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { name: "Client visit Boston" })).toBeInTheDocument();
  });

  it("orders refusal messages by line", () => {
    const r = report({ lines: [line({ id: "a", lineNo: 1 }), line({ id: "b", lineNo: 2 })] });
    expect(refusalMessages(r, { "b/EXP-DUP-001": "Duplicate", "a/EXP-RCPT-025": "Receipt", "z/EXP-X": "Other" })).toEqual([
      "Line 1: Receipt (EXP-RCPT-025)",
      "Line 2: Duplicate (EXP-DUP-001)",
      "A line: Other (EXP-X)",
    ]);
  });
});
