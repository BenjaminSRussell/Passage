import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { sampleDividend, validateDividend } from "./DividendsPage";
import { sampleJournal, validateJournal } from "./GroupJournalPage";
import consolidation from "./module";
import { ac4013, validateReclass } from "./ReclassificationPage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; body: unknown }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method: init.method ?? "GET", url, body });
      const reply = handler(url, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
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
        {consolidation.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const cells = (row: HTMLElement) => within(row).getAllByRole("cell").map((c) => c.textContent);
const bodyRows = (table: HTMLElement) => within(table).getAllByRole("row").slice(1);

describe("dividends page", () => {
  it("previews E6 with the parent and NCI shares", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/consol/dividends:preview"
        ? {
            status: 200,
            body: {
              currency: "USD",
              entries: [
                { rule: "E6", account: "DIVIDEND_INCOME", debit: "44000.00", credit: "0.00" },
                { rule: "E6", account: "NCI_EQUITY", debit: "11000.00", credit: "0.00" },
                { rule: "E6", account: "DIVIDENDS_DECLARED", debit: "0.00", credit: "55000.00" },
              ],
              totals: { debit: "55000.00", credit: "55000.00" },
              rounding: "0.00",
              balanced: true,
            },
          }
        : undefined;
    const { container } = renderAt("/consolidation/dividends");
    expect(screen.getByRole("heading", { name: "No dividend eliminated yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview E6" }));
    await screen.findByText("E6 balances");
    expect(calls[0].body).toEqual({ currency: "USD", ownershipPct: "80", amount: "50000.00", rate: "1.10" });
    expect(screen.getByRole("list", { name: "E6 totals" })).toHaveTextContent("Rounding line None");
    expect(bodyRows(screen.getByRole("table", { name: "E6 entries" })).map(cells)).toEqual([
      ["E6", "Dividend income", "DIVIDEND_INCOME", "44000.00", "—"],
      ["E6", "Non-controlling interest (equity)", "NCI_EQUITY", "11000.00", "—"],
      ["E6", "Dividends declared", "DIVIDENDS_DECLARED", "—", "55000.00"],
    ]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the BR-40.9 rounding line", async () => {
    handler = () => ({
      status: 200,
      body: {
        currency: "USD",
        entries: [
          { rule: "E6", account: "DIVIDEND_INCOME", debit: "5.00", credit: "0.00" },
          { rule: "E6", account: "NCI_EQUITY", debit: "5.00", credit: "0.00" },
          { rule: "E6", account: "DIVIDENDS_DECLARED", debit: "0.00", credit: "10.01" },
          { rule: "E6", account: "ROUNDING", debit: "0.01", credit: "0.00" },
        ],
        totals: { debit: "10.01", credit: "10.01" },
        rounding: "0.01",
        balanced: true,
      },
    });
    renderAt("/consolidation/dividends");
    fireEvent.click(screen.getByRole("button", { name: "Preview E6" }));
    await screen.findByText("E6 balances");
    expect(screen.getByRole("list", { name: "E6 totals" })).toHaveTextContent("Rounding line 0.01");
    expect(bodyRows(screen.getByRole("table", { name: "E6 entries" })).map(cells)[3]).toEqual(["E6", "Rounding difference", "ROUNDING", "0.01", "—"]);
  });

  it("checks the dividend before sending and shows a service error", async () => {
    expect(validateDividend({ ...sampleDividend, ownershipPct: "101" }).errors.ownershipPct).toBeDefined();
    expect(validateDividend({ ...sampleDividend, amount: "0" }).errors.amount).toBeDefined();
    expect(validateDividend({ ...sampleDividend, currency: "usd" }).request?.currency).toBe("USD");
    handler = () => ({ status: 422, body: { code: "CNS-0903", detail: "rule E6 unbalanced by 1.00" } });
    renderAt("/consolidation/dividends");
    fireEvent.click(screen.getByRole("button", { name: "Preview E6" }));
    await waitFor(() => expect(screen.getByText("The dividend was not eliminated")).toBeInTheDocument());
    expect(screen.getByText("The dividend was not eliminated").closest("[role=alert]")).toHaveTextContent("rule E6 unbalanced by 1.00 (CNS-0903)");
    expect(screen.queryByRole("table", { name: "E6 entries" })).not.toBeInTheDocument();
  });
});

describe("reclassification page", () => {
  it("dry-runs AC-40.13 and shows the reversal date", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/consol/reclassification-runs:preview"
        ? {
            status: 200,
            body: {
              currency: "USD",
              periodEnd: "2026-11-30",
              dryRun: true,
              applied: ["VDB", "CASH"],
              reversalDate: "2026-12-01",
              lines: [
                { rule: "VDB", level: "Entity", account: "OTHER_RECEIVABLES", party: "V-100", debit: "5000.00", credit: "0.00", reverses: true },
                { rule: "VDB", level: "Entity", account: "VENDOR_RECLASS", party: "V-100", debit: "0.00", credit: "5000.00", reverses: true },
                { rule: "CASH", level: "Group", account: "CASH", debit: "250.00", credit: "0.00", reverses: false },
                { rule: "CASH", level: "Group", account: "BANK_OVERDRAFTS", debit: "0.00", credit: "250.00", reverses: false },
              ],
              totals: { debit: "5250.00", credit: "5250.00" },
            },
          }
        : undefined;
    const { container } = renderAt("/consolidation/reclassification");
    expect(screen.getByRole("heading", { name: "No dry run yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Run dry run" }));
    await screen.findByRole("list", { name: "Dry run" });
    expect(calls[0].body).toEqual({
      currency: "USD",
      periodEnd: "2026-11-30",
      rules: [
        { code: "VDB", level: "Entity", type: "DebitBalanceVendors", order: 1, sourceAccount: "VENDOR_RECLASS", targetAccount: "OTHER_RECEIVABLES" },
        { code: "CASH", level: "Group", type: "SignBased", order: 2, sourceAccount: "CASH", targetAccount: "BANK_OVERDRAFTS" },
      ],
      openItems: [
        { party: "V-100", account: "VENDOR_RECLASS", amount: "5000.00" },
        { party: "V-200", account: "VENDOR_RECLASS", amount: "-800.00" },
      ],
      balances: [{ account: "CASH", amount: "-250.00" }],
    });
    expect(screen.getByRole("list", { name: "Dry run" })).toHaveTextContent("Reverses on 2026-12-01");
    expect(bodyRows(screen.getByRole("table", { name: "Reclassification lines" })).map(cells)).toEqual([
      ["VDB", "Entity", "OTHER_RECEIVABLES", "V-100", "5000.00", "—", "Next period"],
      ["VDB", "Entity", "VENDOR_RECLASS", "V-100", "—", "5000.00", "Next period"],
      ["CASH", "Group", "CASH", "—", "250.00", "—", "No"],
      ["CASH", "Group", "BANK_OVERDRAFTS", "—", "—", "250.00", "No"],
    ]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an empty result when nothing meets a rule", async () => {
    handler = () => ({ status: 200, body: { currency: "USD", periodEnd: "2026-11-30", dryRun: true, applied: [], lines: [], totals: { debit: "0.00", credit: "0.00" } } });
    renderAt("/consolidation/reclassification");
    fireEvent.click(screen.getByRole("button", { name: "Run dry run" }));
    expect(await screen.findByText("Nothing to reclassify")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Dry run" })).toHaveTextContent("Reverses on No reversal");
  });

  it("checks the rules before sending and leaves empty rows out", async () => {
    const noBalances = validateReclass({ ...ac4013, balances: [{ account: "", amount: "" }] });
    expect(noBalances.request?.balances).toEqual([]);
    expect(validateReclass({ ...ac4013, rules: [ac4013.rules[0], ac4013.rules[0]] }).errors["rules[1].code"]).toBe("Rule codes must be unique.");
    expect(validateReclass({ ...ac4013, rules: [{ ...ac4013.rules[0], targetAccount: "VENDOR_RECLASS" }] }).errors["rules[0].targetAccount"]).toBeDefined();
    expect(validateReclass({ ...ac4013, items: [{ ...ac4013.items[0], amount: "5,000" }] }).errors["openItems[0].amount"]).toBeDefined();
    expect(validateReclass({ ...ac4013, head: { ...ac4013.head, periodEnd: "" } }).errors.periodEnd).toBeDefined();
    handler = () => ({ status: 400, body: { code: "COM-0001", detail: "rule CASH of type SignBased must be at Group level" } });
    renderAt("/consolidation/reclassification");
    fireEvent.click(screen.getByRole("button", { name: "Run dry run" }));
    await waitFor(() => expect(screen.getByText("The dry run failed")).toBeInTheDocument());
    expect(screen.getByText("The dry run failed").closest("[role=alert]")).toHaveTextContent("(COM-0001)");
  });
});

describe("top-side group journals page", () => {
  it("checks the journal, flags CNS-0909 and lists the carry-forward periods", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/consol/group-journals:preview"
        ? {
            status: 200,
            body: {
              currency: "USD",
              level: "Group",
              total: "120000.00",
              balanced: true,
              approvalThreshold: "100000.00",
              approvalRequired: true,
              periods: ["2026-11", "2026-12", "2027-01"],
              continuesPastYearEnd: false,
              warnings: [{ code: "CNS-0909", message: "top-side journal of 120000.00 USD needs approval: the threshold is 100000.00" }],
            },
          }
        : undefined;
    const { container } = renderAt("/consolidation/group-journals");
    expect(screen.getByRole("heading", { name: "No journal checked yet" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Check journal" }));
    await screen.findByRole("list", { name: "Journal check" });
    expect(calls[0].body).toEqual({
      periodKey: "2026-11",
      documentType: "Adjustment",
      currency: "USD",
      reason: "Accrue group audit fee",
      attachmentRef: "ENG-LETTER-2026",
      carryForward: true,
      fiscalYearStartMonth: 10,
      lines: [
        { fsItem: "AUDIT_FEES", debit: "120000.00" },
        { fsItem: "ACCRUALS", credit: "120000.00" },
      ],
    });
    expect(screen.getByRole("list", { name: "Journal check" })).toHaveTextContent("Approval Required");
    expect(screen.getByText("Approval required before posting").parentElement).toHaveTextContent("(CNS-0909)");
    expect(bodyRows(screen.getByRole("table", { name: "Posting periods" })).map(cells)).toEqual([["2026-11"], ["2026-12"], ["2027-01"]]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an unbalanced journal (CNS-0903)", async () => {
    handler = () => ({ status: 422, body: { code: "CNS-0903", detail: "top-side journal unbalanced: debit 120000.00, credit 119000.00" } });
    renderAt("/consolidation/group-journals");
    fireEvent.change(screen.getByLabelText("Credit", { selector: "#gj-line-1-credit" }), { target: { value: "119000.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Check journal" }));
    await waitFor(() => expect(screen.getByText("The journal was not accepted")).toBeInTheDocument());
    expect(screen.getByText("The journal was not accepted").closest("[role=alert]")).toHaveTextContent("(CNS-0903)");
    expect(screen.queryByRole("table", { name: "Posting periods" })).not.toBeInTheDocument();
  });

  it("checks the journal before sending", () => {
    expect(validateJournal({ ...sampleJournal, head: { ...sampleJournal.head, reason: " " } }).errors.reason).toBe("Every top-side entry needs a reason.");
    expect(validateJournal({ ...sampleJournal, head: { ...sampleJournal.head, attachmentRef: "" } }).errors.attachmentRef).toBeDefined();
    expect(validateJournal({ ...sampleJournal, head: { ...sampleJournal.head, fiscalYearStartMonth: "13" } }).errors.fiscalYearStartMonth).toBeDefined();
    expect(validateJournal({ ...sampleJournal, lines: [sampleJournal.lines[0]] }).errors.lines).toBeDefined();
    expect(validateJournal({ ...sampleJournal, lines: [{ ...sampleJournal.lines[0], credit: "1" }, sampleJournal.lines[1]] }).errors["lines[0].debit"]).toBeDefined();
    expect(validateJournal({ ...sampleJournal, head: { ...sampleJournal.head, unitId: "S1", carryForward: "no" } }).request).toMatchObject({ unitId: "S1", carryForward: false });
  });
});
