import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { PackRunDetail } from "./api/client";
import { parseBankBalances } from "./domain";
import migrationFactory from "./module";
import { validateBankRun, type BankRunValues } from "./NewBankRunPage";
import { validateDecision, type DecisionValues } from "./CutoverDecisionPage";

const RUN = "0195f0a0-0000-7000-8000-0000000000e1";

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
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
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
        {migrationFactory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));

const lines = ["legacyId,bankAccount,currency,balance", "BK-1,OPER-USD,USD,-1250.50", "BK-2,PAY-EUR,eur,900", "BK-3,SAV-USD,USD,50.25"].join("\n");

const values: BankRunValues = {
  sourceSystem: "SAPECC",
  sourceCompany: " 1000 ",
  cutoff: "2026-12-31T00:00",
  freeze: "2026-12-31T18:00",
  quarantineMaxPct: "",
  bankAccounts: "OPER-USD, PAY-EUR\nSAV-USD",
  lines,
};

const bankRun: PackRunDetail = {
  runId: RUN,
  pack: "bank_balances",
  sourceSystem: "AX2012",
  sourceCompany: "USMF",
  cutoffTs: "2026-12-31T00:00:00Z",
  inputHash: "ab".repeat(32),
  status: "LOADED",
  recordsIn: 3,
  recordsLoaded: 2,
  recordsQuarantined: 1,
  alreadyPresent: 0,
  version: 1,
  createdAt: "2027-01-01T04:10:00Z",
  controlTotals: [
    { name: "bank_balances.count", key: { company: "USMF" }, source: "3.000000", target: "2.000000", delta: "-1.000000", tolerance: "0.000000", status: "MISMATCH" },
  ],
  quarantined: [
    { seq: 1, legacyId: "BK-9", rule: "REFERENTIAL", reason: 'bank account "GONE" is not in the bank account master', status: "OPEN", account: "GONE", currency: "USD", amount: "5.000000" },
  ],
} as PackRunDetail;

describe("bank balances extract helpers", () => {
  it("parses pasted lines, skips the header, upper-cases the currency and keeps decimals as strings", () => {
    expect(parseBankBalances(lines)).toEqual({
      errors: [],
      records: [
        { legacyId: "BK-1", bankAccount: "OPER-USD", currency: "USD", balance: "-1250.50" },
        { legacyId: "BK-2", bankAccount: "PAY-EUR", currency: "EUR", balance: "900" },
        { legacyId: "BK-3", bankAccount: "SAV-USD", currency: "USD", balance: "50.25" },
      ],
    });
  });

  it("reports each line the service would quarantine for its shape", () => {
    const bad = parseBankBalances(["BK-1,A,USD,1,9", ",A,USD,1", "BK-3,A,DOLLAR,1", "BK-4,A,USD,1e3", "BK-5,A,USD,1.0000001"].join("\n"));
    expect(bad.records).toEqual([]);
    expect(bad.errors).toEqual([
      "Line 1: expected 4 values (legacyId, bankAccount, currency, balance), found 5.",
      "Line 2: the legacy id and bank account are required.",
      'Line 3: currency "DOLLAR" is not a three-letter code.',
      'Line 4: balance "1e3" is not a decimal such as -1250.00.',
      'Line 5: balance "1.0000001" is not a decimal such as -1250.00.',
    ]);
  });
});

describe("validateBankRun", () => {
  it("builds the request the service takes", () => {
    const { errors, input } = validateBankRun(values);
    expect(errors).toEqual({});
    expect(input).toEqual({
      sourceSystem: "SAPECC",
      sourceCompany: "1000",
      cutoffTs: "2026-12-31T00:00:00Z",
      freezeTs: "2026-12-31T18:00:00Z",
      bankAccounts: ["OPER-USD", "PAY-EUR", "SAV-USD"],
      records: parseBankBalances(lines).records,
    });
  });

  it("refuses what the service would refuse", () => {
    const { errors, input } = validateBankRun({ ...values, sourceCompany: "", cutoff: "2027-01-02T00:00", bankAccounts: " , ", quarantineMaxPct: "11", lines: "BK-1,A,USD,x" });
    expect(input).toBeUndefined();
    expect(errors).toEqual({
      sourceCompany: "Enter the legacy company (DataAreaId or company code).",
      cutoff: "The cutoff can't be after the legacy freeze (MIG-1501).",
      bankAccounts: "List the bank account master the lines are checked against.",
      quarantineMaxPct: "Enter a percentage from 0 to 10.",
      lines: 'Line 1: balance "x" is not a decimal such as -1250.00.',
    });
    expect(validateBankRun({ ...values, lines: "  " }).errors.lines).toBe("Paste at least one line.");
  });
});

describe("go/no-go waivers (CUT-1608)", () => {
  const waived: DecisionValues = {
    decision: "GO",
    reason: "",
    attestations: {},
    waivers: { onhand: { waived: true, reason: " posted manually " }, bank_balances: { waived: false, reason: "x" } },
  };
  const pending = { round: 1, approver: "alice", attestations: [], waivers: [{ pack: "onhand", reason: "posted manually" }], planVersion: 2, approvedAt: "2027-01-01T21:00:00Z" };

  it("sends the checked waivers, trimmed, with the first GO only", () => {
    expect(validateDecision(waived, 2).input?.waivers).toEqual([{ pack: "onhand", reason: "posted manually" }]);
    // The second approver approves the pending waivers; a NO_GO carries none.
    expect(validateDecision(waived, 2, pending).input?.waivers).toBeUndefined();
    expect(validateDecision({ ...waived, decision: "NO_GO", reason: "UAT failed" }, 2).input?.waivers).toBeUndefined();
  });

  it("refuses a waiver without a reason or over 500 characters", () => {
    expect(validateDecision({ ...waived, waivers: { onhand: { waived: true, reason: " " } } }, 2).errors.waivers).toMatch(/CUT-1608/);
    expect(validateDecision({ ...waived, waivers: { onhand: { waived: true, reason: "é".repeat(501) } } }, 2).errors.waivers).toMatch(/500/);
    expect(validateDecision({ ...waived, waivers: { onhand: { waived: true, reason: "é".repeat(500) } } }, 2).errors.waivers).toBeUndefined();
  });
});

describe("Load bank balances page", () => {
  function fill() {
    fireEvent.change(screen.getByLabelText("Legacy company"), { target: { value: "USMF" } });
    fireEvent.change(screen.getByLabelText("Extract cutoff (UTC)"), { target: { value: "2026-12-31T00:00" } });
    fireEvent.change(screen.getByLabelText("Legacy freeze (UTC)"), { target: { value: "2026-12-31T18:00" } });
    fireEvent.change(screen.getByLabelText("Bank account master"), { target: { value: "OPER-USD PAY-EUR SAV-USD" } });
    fireEvent.change(screen.getByLabelText("Bank balances"), { target: { value: lines } });
  }

  it("is reached from the pack runs page, posts the extract with an idempotency key, then opens the run", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/migration/runs") return json(200, { items: [] });
      if (init.method === "POST") return json(201, bankRun);
      if (url.pathname === `/api/v1/migration/runs/${RUN}`) return json(200, bankRun);
      return undefined;
    };
    const list = renderAt("/migration-factory");
    expect(await screen.findByRole("link", { name: "Load bank balances" })).toHaveAttribute("href", "/migration-factory/runs/new-bank");
    list.unmount();

    const { container } = renderAt("/migration-factory/runs/new-bank");
    expect(screen.getByRole("heading", { name: "Load bank balances" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Run bank_balances" }));
    expect(await screen.findByRole("heading", { name: "Pack run bank_balances" })).toBeInTheDocument();
    const [post] = posts("/migration/packs/bank_balances/runs");
    expect(post.body).toEqual({
      sourceSystem: "AX2012",
      sourceCompany: "USMF",
      cutoffTs: "2026-12-31T00:00:00Z",
      freezeTs: "2026-12-31T18:00:00Z",
      bankAccounts: ["OPER-USD", "PAY-EUR", "SAV-USD"],
      records: parseBankBalances(lines).records,
    });
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("starts bank_balances runs from the pack catalog", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/migration/packs"
        ? json(200, { items: [{ pack: "bank_balances", sourceSystems: ["AX2012"], controlTotals: "balance per bank account", available: true }] })
        : undefined;
    renderAt("/migration-factory/packs");
    expect(await screen.findByRole("link", { name: "Start bank_balances run" })).toHaveAttribute("href", "/migration-factory/runs/new-bank");
  });

  it("shows a bad paste on the field and sends nothing", async () => {
    renderAt("/migration-factory/runs/new-bank");
    fill();
    fireEvent.change(screen.getByLabelText("Bank balances"), { target: { value: "BK-1,A,USD,abc" } });
    fireEvent.click(screen.getByRole("button", { name: "Run bank_balances" }));
    expect(await screen.findByText('Line 1: balance "abc" is not a decimal such as -1250.00.')).toBeInTheDocument();
    expect(posts("/runs")).toHaveLength(0);
  });

  it("shows the service's refusal with its code and stays on the form", async () => {
    handler = () => json(422, { code: "MIG-0001", detail: "invalid input: bank account OPER-USD already has an opening balance" });
    renderAt("/migration-factory/runs/new-bank");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Run bank_balances" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load the balances");
    expect(alert).toHaveTextContent(/already has an opening balance/);
    expect(screen.getByRole("button", { name: "Run bank_balances" })).toBeInTheDocument();
  });
});

describe("bank_balances run detail", () => {
  it("shows the staged-only notice, the totals and the quarantined account", async () => {
    handler = (url) => (url.pathname === `/api/v1/migration/runs/${RUN}` ? json(200, bankRun) : undefined);
    const { container } = renderAt(`/migration-factory/runs/${RUN}`);
    expect(await screen.findByText("Staged, not posted to treasury")).toBeInTheDocument();
    const totals = await findTable({ name: "Control totals" });
    expect(totals).toHaveTextContent("bank_balances.count");
    const quarantine = await findTable({ name: "Quarantined records" });
    expect(quarantine).toHaveTextContent("GONE");
    expect(within(quarantine).getByRole("button", { name: /^Release record/ })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });
});
