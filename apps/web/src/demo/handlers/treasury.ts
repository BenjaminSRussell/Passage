// Treasury service (/api/v1/treasury) for the demo: house bank accounts, statements with their lines,
// bank reconciliations (close, reopen, exclude, book a fee) and the cash forecast.
// Typed against the module's own client types so shape drift fails `tsc`.
import type { AccountPositionRow, BankAccount, BankReconciliation, ForecastEntry, LineTransaction, Statement, StatementLine, TransactionKind } from "../../modules/treasury/api/client";
import { LEGAL_ENTITY } from "../company";
import { reply, route } from "../backend";
import { at, day, newId, table, uid } from "../store";

const BASE = "/api/v1/treasury";
const K_ACCOUNT = 360;
const K_STATEMENT = 361;
const K_RECON = 362;

// Money is kept in cents so sums stay exact; the services send decimal strings.
const cents = (s: string): number => Math.round(Number(s) * 100);
const dec = (c: number): string => (c / 100).toFixed(2);

const OPERATING = uid(K_ACCOUNT, 1);
const PAYROLL = uid(K_ACCOUNT, 2);

const accounts = table<BankAccount>([
  { id: OPERATING, legalEntityId: LEGAL_ENTITY.id, accountCode: "OPS-USD", ibanMasked: "US** **** **** 4821", ibanLast4: "4821", bic: "FPBKUS44", currency: "USD", mainAccountId: uid(363, 1), status: "active", version: 3, createdAt: at(-400), updatedAt: at(-30) },
  { id: PAYROLL, legalEntityId: LEGAL_ENTITY.id, accountCode: "PAY-USD", ibanMasked: "US** **** **** 7710", ibanLast4: "7710", bic: "FPBKUS44", currency: "USD", mainAccountId: uid(363, 2), status: "active", version: 2, createdAt: at(-400), updatedAt: at(-60) },
]);

const STATEMENT_PRIOR = uid(K_STATEMENT, 1);
const STATEMENT_CURRENT = uid(K_STATEMENT, 2);

function statement(id: string, n: number, openingDate: string, opening: number, closingDate: string, closing: number, lineCount: number): Statement {
  return {
    id,
    bankAccountId: OPERATING,
    legalEntityId: LEGAL_ENTITY.id,
    statementId: `STMT-2026-${String(n).padStart(3, "0")}`,
    sequence: n,
    format: "camt.053",
    currency: "USD",
    openingDate,
    openingBalance: dec(opening),
    closingDate,
    closingBalance: dec(closing),
    lineCount,
    createdAt: at(-n),
  };
}

function line(no: number, offset: number, amount: number, name: string, ref: string, status: string, btc = "NTRF"): StatementLine {
  return { lineNo: no, bookingDate: day(offset), valueDate: day(offset), amount: dec(amount), currency: "USD", btcCode: btc, endToEndRef: ref, counterpartyName: name, status };
}

const priorLines: StatementLine[] = [
  line(1, -52, 31850_00, "Summit Mining Supply", "E2E-SM-5521", "matched"),
  line(2, -49, -12400_00, "Ironbridge Foundry", "E2E-IB-8812", "matched"),
  line(3, -45, -35_00, "Bank service fee", "FEE-0926", "created", "NCHG"),
];
const currentLines: StatementLine[] = [
  line(1, -12, 24500_00, "Bluewater Municipal Utilities", "E2E-BW-7733", "matched"),
  line(2, -9, -18640_00, "Ironbridge Foundry", "E2E-IB-9120", "matched"),
  line(3, -6, -31200_00, "Volta Motor Works", "E2E-VM-1204", "matched"),
  line(4, -4, -42_50, "Bank service fee", "FEE-1026", "unmatched", "NCHG"),
  line(5, -2, 118_37, "Interest credit", "INT-1026", "unmatched", "NINT"),
  line(6, -1, 5000_00, "Unidentified depositor", "NOTPROVIDED", "unmatched"),
];

const statements = table<Statement>([
  statement(STATEMENT_PRIOR, 9, day(-60), 185_000_00, day(-31), 185_000_00 + 31850_00 - 12400_00 - 35_00, priorLines.length),
  statement(STATEMENT_CURRENT, 10, day(-30), 245_120_40, day(-1), 245_120_40 + 24500_00 - 18640_00 - 31200_00 - 42_50 + 118_37 + 5000_00, currentLines.length),
]);

const lines = new Map<string, StatementLine[]>([
  [STATEMENT_PRIOR, priorLines],
  [STATEMENT_CURRENT, currentLines],
]);

interface Recon extends Omit<BankReconciliation, "summary" | "difference"> {
  id: string;
}
const recons = table<Recon>([
  {
    id: uid(K_RECON, 1),
    legalEntityId: LEGAL_ENTITY.id,
    bankAccountId: OPERATING,
    statementId: STATEMENT_PRIOR,
    currency: "USD",
    periodFrom: day(-60),
    periodTo: day(-31),
    state: "closed",
    openingBalance: dec(185_000_00),
    closingBalance: dec(185_000_00 + 31850_00 - 12400_00 - 35_00),
    bookBalance: dec(185_000_00 + 31850_00 - 12400_00 - 35_00),
    outstandingAmount: "0.00",
    bookBalanceSource: "stated",
    bookBalanceStatedBy: "ana.ortiz",
    bookBalanceStatedAt: at(-30),
    closedBy: "marcus.lee",
    closedAt: at(-29),
    version: 5,
    createdBy: "ana.ortiz",
    createdAt: at(-31),
    updatedAt: at(-29),
  },
  {
    id: uid(K_RECON, 2),
    legalEntityId: LEGAL_ENTITY.id,
    bankAccountId: OPERATING,
    statementId: STATEMENT_CURRENT,
    currency: "USD",
    periodFrom: day(-30),
    periodTo: day(-1),
    state: "open",
    openingBalance: dec(245_120_40),
    closingBalance: dec(245_120_40 + 24500_00 - 18640_00 - 31200_00 - 42_50 + 118_37 + 5000_00),
    bookBalance: dec(245_120_40 + 24500_00 - 18640_00 - 31200_00),
    outstandingAmount: "0.00",
    bookBalanceSource: "stated",
    bookBalanceStatedBy: "ana.ortiz",
    bookBalanceStatedAt: at(-0.5),
    version: 2,
    createdBy: "ana.ortiz",
    createdAt: at(-1),
    updatedAt: at(-0.5),
  },
]);

const MATCHED = new Set(["matched", "created"]);

function view(r: Recon): BankReconciliation {
  const ls = lines.get(r.statementId) ?? [];
  const sum = (rows: StatementLine[]) => rows.reduce((s, l) => s + cents(l.amount), 0);
  const matched = ls.filter((l) => MATCHED.has(l.status));
  const excluded = ls.filter((l) => l.status === "excluded");
  const unmatched = ls.filter((l) => !MATCHED.has(l.status) && l.status !== "excluded");
  const difference = cents(r.closingBalance) - (cents(r.bookBalance) + cents(r.outstandingAmount) + sum(excluded));
  return {
    ...r,
    difference: dec(difference),
    summary: {
      lines: ls.length,
      matchedCount: matched.length,
      matchedTotal: dec(sum(matched)),
      excludedCount: excluded.length,
      excludedTotal: dec(sum(excluded)),
      unmatchedCount: unmatched.length,
      unmatchedTotal: dec(sum(unmatched)),
    },
  };
}

const notFound = (what: string) => reply.problem(404, "COM-0404", `${what} not found`);
const versioned = (r: Recon) => reply.versioned(view(r), r.version);

route("GET", `${BASE}/bank-accounts`, ({ query }) => ({ items: accounts.all().filter((a) => !query.get("legalEntityId") || a.legalEntityId === query.get("legalEntityId")).sort((a, b) => a.accountCode.localeCompare(b.accountCode)) }));
route("GET", `${BASE}/bank-accounts/{id}/statements`, ({ params }) => ({ items: statements.all().filter((s) => s.bankAccountId === params.id).sort((a, b) => b.closingDate.localeCompare(a.closingDate)) }));
route("GET", `${BASE}/bank-accounts/{id}`, ({ params }) => accounts.get(params.id) ?? notFound("bank account"));
route("GET", `${BASE}/statements/{id}/lines`, ({ params, query }) => {
  const after = Number(query.get("afterLineNo") ?? 0);
  const limit = Number(query.get("limit") ?? 100);
  const rest = (lines.get(params.id) ?? []).filter((l) => l.lineNo > after);
  const page = rest.slice(0, limit);
  return { items: page, nextAfterLineNo: rest.length > page.length ? page[page.length - 1].lineNo : null };
});
route("GET", `${BASE}/statements/{id}`, ({ params }) => statements.get(params.id) ?? notFound("statement"));

route("GET", `${BASE}/bank-reconciliations`, ({ query }) => {
  const items = recons
    .all()
    .filter((r) => (!query.get("legalEntityId") || r.legalEntityId === query.get("legalEntityId")) && (!query.get("bankAccountId") || r.bankAccountId === query.get("bankAccountId")) && (!query.get("state") || r.state === query.get("state")))
    .sort((a, b) => b.periodTo.localeCompare(a.periodTo))
    .map(view);
  return { items };
});

route("POST", `${BASE}/bank-reconciliations`, ({ body }) => {
  const input = body as { statementId: string; bookBalance: string; outstandingAmount?: string };
  const st = statements.get(input.statementId);
  if (!st) return notFound("statement");
  if (recons.all().some((r) => r.statementId === st.id)) return reply.problem(409, "TRS-0504", "this statement already has a reconciliation");
  const created = recons.insert({
    id: newId(),
    legalEntityId: st.legalEntityId,
    bankAccountId: st.bankAccountId,
    statementId: st.id,
    currency: st.currency,
    periodFrom: st.openingDate,
    periodTo: st.closingDate,
    state: "open",
    openingBalance: st.openingBalance,
    closingBalance: st.closingBalance,
    bookBalance: input.bookBalance,
    outstandingAmount: input.outstandingAmount ?? "0.00",
    bookBalanceSource: "stated",
    bookBalanceStatedBy: "dev",
    bookBalanceStatedAt: at(0),
    version: 1,
    createdBy: "dev",
    createdAt: at(0),
    updatedAt: at(0),
  });
  return reply.versioned(view(created), created.version, 201);
});

route("GET", `${BASE}/bank-reconciliations/{id}`, ({ params }) => {
  const r = recons.get(params.id);
  return r ? versioned(r) : notFound("reconciliation");
});

function change(id: string, patch: (r: Recon) => Recon | DemoProblem) {
  const r = recons.get(id);
  if (!r) return notFound("reconciliation");
  const next = patch(r);
  if (next instanceof DemoProblem) return next.response;
  return versioned(recons.update(id, { ...next, version: r.version + 1, updatedAt: at(0) })!);
}

class DemoProblem {
  constructor(readonly response: ReturnType<typeof reply.problem>) {}
}

route("POST", `${BASE}/bank-reconciliations/{id}:set-balances`, ({ params, body }) => {
  const input = body as { bookBalance: string; outstandingAmount: string };
  return change(params.id, (r) =>
    r.state === "closed"
      ? new DemoProblem(reply.problem(409, "TRS-0506", "the period is closed"))
      : { ...r, bookBalance: input.bookBalance, outstandingAmount: input.outstandingAmount, bookBalanceStatedBy: "dev", bookBalanceStatedAt: at(0) },
  );
});

route("POST", `${BASE}/bank-reconciliations/{id}:close`, ({ params }) =>
  change(params.id, (r) => {
    const v = view(r);
    if (v.summary.unmatchedCount > 0) return new DemoProblem(reply.problem(422, "TRS-0507", "some lines are neither matched nor excluded"));
    if (cents(v.difference) !== 0) return new DemoProblem(reply.problem(422, "TRS-0410", `the difference is ${v.difference}, it must be zero`));
    return { ...r, state: "closed", closedBy: "dev", closedAt: at(0) };
  }),
);

route("POST", `${BASE}/bank-reconciliations/{id}:reopen`, ({ params, body }) =>
  change(params.id, (r) =>
    r.state !== "closed"
      ? new DemoProblem(reply.problem(422, "TRS-0510", "only a closed reconciliation can be reopened"))
      : { ...r, state: "reopened", reopenedBy: "dev", reopenedAt: at(0), reopenReason: (body as { reason: string }).reason },
  ),
);

function setLine(reconId: string, lineNo: number, patch: (l: StatementLine) => StatementLine | DemoProblem) {
  const r = recons.get(reconId);
  if (!r) return notFound("reconciliation");
  const ls = lines.get(r.statementId) ?? [];
  const at0 = ls.findIndex((l) => l.lineNo === lineNo);
  if (at0 < 0) return reply.problem(404, "TRS-0511", "statement line not found");
  if (r.state === "closed") return reply.problem(409, "TRS-0506", "the period is closed");
  const next = patch(ls[at0]);
  if (next instanceof DemoProblem) return next.response;
  ls[at0] = next;
  return next;
}

route("POST", `${BASE}/bank-reconciliations/{id}/lines/{lineNo}:exclude`, ({ params }) =>
  setLine(params.id, Number(params.lineNo), (l) => (MATCHED.has(l.status) ? new DemoProblem(reply.problem(409, "TRS-0411", "unmatch the line first")) : { ...l, status: "excluded" })),
);
route("POST", `${BASE}/bank-reconciliations/{id}/lines/{lineNo}:include`, ({ params }) => setLine(params.id, Number(params.lineNo), (l) => ({ ...l, status: "unmatched" })));
route("POST", `${BASE}/bank-reconciliations/{id}/lines/{lineNo}:abandon`, ({ params }) =>
  setLine(params.id, Number(params.lineNo), () => new DemoProblem(reply.problem(409, "TRS-0523", "the line has no pending bank transaction to give up"))),
);

route("POST", `${BASE}/bank-reconciliations/{id}/lines/{lineNo}:create-transaction`, ({ params, body }) => {
  const kind = (body as { kind: TransactionKind }).kind;
  const r = recons.get(params.id);
  let amount = "0.00";
  const out = setLine(params.id, Number(params.lineNo), (l) => {
    const money = cents(l.amount);
    if ((kind === "bank_fee" && money > 0) || (kind === "unknown_receipt" && money < 0)) return new DemoProblem(reply.problem(422, "TRS-0512", "that kind does not fit the direction of the line"));
    amount = l.amount;
    return { ...l, status: "created" };
  });
  if (!r || !("lineNo" in (out as object))) return out;
  recons.update(r.id, { bookBalance: dec(cents(r.bookBalance) + cents(amount)), version: r.version + 1 });
  const tx: LineTransaction = { id: newId(), reconciliationId: r.id, lineNo: Number(params.lineNo), kind, amount, currency: r.currency, postingDate: day(0), status: "posted", ledgerEntryId: newId(), version: 1 };
  return reply.created(tx);
});

// ---- Cash position (FR-36.8) ----
// The freshest of the latest intraday report and the latest statement, the way the service picks it: a report
// counts when it was created after the statement's closing day ended.

interface DemoReport {
  id: string;
  bankAccountId: string;
  reportId: string;
  createdAt: string;
  booked: number;
  available: number;
}

const reports = table<DemoReport>([{ id: uid(369, 1), bankAccountId: OPERATING, reportId: "RPT-OPS-1400", createdAt: at(-0.12), booked: 251_940_12, available: 301_940_12 }]);

const dayEnd = (date: string): number => new Date(`${date}T00:00:00Z`).getTime() + 86_400_000;

route("GET", `${BASE}/cash-position`, ({ query }) => {
  const now = Date.now();
  const rows: AccountPositionRow[] = accounts
    .all()
    .filter((a) => a.legalEntityId === query.get("legalEntityId") && a.status === "active")
    .map((a) => {
      const st = statements
        .all()
        .filter((s) => s.bankAccountId === a.id)
        .sort((x, y) => y.closingDate.localeCompare(x.closingDate))[0];
      const rep = reports
        .all()
        .filter((r) => r.bankAccountId === a.id)
        .sort((x, y) => y.createdAt.localeCompare(x.createdAt))[0];
      const base = { bankAccountId: a.id, accountCode: a.accountCode, ibanMasked: a.ibanMasked, currency: a.currency };
      if (rep && (!st || Date.parse(rep.createdAt) > dayEnd(st.closingDate))) {
        return { ...base, booked: dec(rep.booked), available: dec(rep.available), source: "camt.052" as const, asOf: rep.createdAt, stale: now - Date.parse(rep.createdAt) > 86_400_000 };
      }
      if (st) {
        return { ...base, booked: st.closingBalance, available: st.closingBalance, source: "camt.053" as const, asOf: new Date(dayEnd(st.closingDate)).toISOString(), stale: now - dayEnd(st.closingDate) > 86_400_000 };
      }
      return { ...base, booked: "0.00", available: "0.00", source: "none" as const, stale: true };
    });
  const totals = new Map<string, { booked: number; available: number }>();
  for (const r of rows) {
    const t = totals.get(r.currency) ?? { booked: 0, available: 0 };
    totals.set(r.currency, { booked: t.booked + cents(r.booked), available: t.available + cents(r.available) });
  }
  return { asOf: at(0), staleHours: 24, accounts: rows, totals: [...totals.entries()].map(([currency, t]) => ({ currency, booked: dec(t.booked), available: dec(t.available) })) };
});

route("POST", `${BASE}/intraday-reports/import`, ({ body }) => {
  const accountId = String((body as { bankAccountId?: string } | undefined)?.bankAccountId ?? "");
  const account = accounts.get(accountId);
  if (!account) return notFound("bank account");
  if (account.status !== "active") return reply.problem(422, "TRS-0412", "reports can only be imported into an active bank account");
  const reportId = `RPT-${account.accountCode}-${reports.all().length + 1}`;
  const stored = reports.insert({ id: newId(), bankAccountId: accountId, reportId, createdAt: at(0), booked: 251_940_12, available: 301_940_12 });
  return reply.created({ id: stored.id, bankAccountId: accountId, reportId, currency: account.currency, createdAtBank: stored.createdAt, balances: [] });
});

// ---- Cash forecast (FR-36.9) ----

const forecastEntries = table<ForecastEntry>([
  { id: uid(364, 1), legalEntityId: LEGAL_ENTITY.id, sourceType: "AR", sourceId: "INV-30118", expectedDate: day(4), amount: "42650.00", currency: "USD", confidence: "0.9" },
  { id: uid(364, 2), legalEntityId: LEGAL_ENTITY.id, sourceType: "AR", sourceId: "INV-30142", expectedDate: day(12), amount: "88200.00", currency: "USD", confidence: "0.8" },
  { id: uid(364, 3), legalEntityId: LEGAL_ENTITY.id, sourceType: "AP", sourceId: "IBF-88317", expectedDate: day(8), amount: "-9215.50", currency: "USD", confidence: "1" },
  { id: uid(364, 4), legalEntityId: LEGAL_ENTITY.id, sourceType: "AP", sourceId: "VMW-120771", expectedDate: day(3), amount: "-31200.00", currency: "USD", confidence: "1" },
  { id: uid(364, 5), legalEntityId: LEGAL_ENTITY.id, sourceType: "Payroll", sourceId: "PAY-2026-11B", expectedDate: day(10), amount: "-96400.00", currency: "USD", confidence: "1" },
  { id: uid(364, 6), legalEntityId: LEGAL_ENTITY.id, sourceType: "Recurring", sourceId: "LEASE-CR", expectedDate: day(20), amount: "-14500.00", currency: "USD", confidence: "1" },
]);

route("POST", `${BASE}/cash-forecast-entries`, ({ body }) => {
  const input = body as Omit<ForecastEntry, "id" | "sourceId" | "confidence"> & { sourceId?: string; confidence?: string };
  return reply.created(forecastEntries.insert({ ...input, id: newId(), sourceId: input.sourceId ?? `MAN-${Math.floor(Math.random() * 9000 + 1000)}`, confidence: input.confidence ?? "1" }));
});

route("GET", `${BASE}/cash-forecast`, ({ query }) => {
  const layout = (query.get("layout") ?? "weekly") as "daily" | "weekly" | "monthly";
  const asOf = query.get("asOf") || day(0);
  const start = new Date(`${asOf}T00:00:00Z`).getTime();
  const DAY = 86_400_000;
  const keyOf = (date: string): string => {
    const t = new Date(`${date}T00:00:00Z`).getTime();
    if (layout === "daily") return date;
    if (layout === "weekly") return new Date(start + Math.floor((t - start) / (7 * DAY)) * 7 * DAY).toISOString().slice(0, 10);
    return `${date.slice(0, 7)}-01`;
  };
  const buckets = new Map<string, { inflow: number; outflow: number }>();
  for (const e of forecastEntries.all().filter((x) => x.legalEntityId === query.get("legalEntityId") && x.expectedDate >= asOf)) {
    const k = keyOf(e.expectedDate);
    const b = buckets.get(k) ?? { inflow: 0, outflow: 0 };
    const c = Math.round(cents(e.amount) * Number(e.confidence));
    if (c >= 0) b.inflow += c;
    else b.outflow += -c;
    buckets.set(k, b);
  }
  let running = 0;
  const rows = [...buckets.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, b]) => {
      running += b.inflow - b.outflow;
      return { key, currency: "USD", inflow: dec(b.inflow), outflow: dec(b.outflow), net: dec(b.inflow - b.outflow), cumulative: dec(running) };
    });
  return { layout, asOf, buckets: rows, outOfHorizon: 0 };
});
