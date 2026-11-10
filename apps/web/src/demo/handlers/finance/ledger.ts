// General ledger (/api/v1/ledger) for the demo: the company ledger and its chart, journal names, manual journal
// batches with their lifecycle (validate, submit, approve, reject, cancel, post), the journal entries posted
// batches become, and recurring journals with their generated runs. Typed against the module's client types.
// Account numbers line up with the budget plan (6100 to 6500 by cost center) and the treasury bank accounts.
import type { BatchAttachment, BatchLine, Entry, JournalBatch, JournalName, Ledger, RecurringJournal, RecurringLine, RecurringRun } from "../../../modules/ledger/api/client";
import { LEGAL_ENTITY } from "../../company";
import { DemoResponse, reply, route } from "../../backend";
import { at, day, newId, table, uid } from "../../store";
import { ME } from "./people";

const BASE = "/api/v1/ledger";
const K_LEDGER = 600;
const K_BATCH = 601;
const K_ENTRY = 602;
const K_RECURRING = 604;
const K_RUN = 605;
const K_ATTACHMENT = 606;

export const LEDGER_ID = uid(K_LEDGER, 1);

/** Chart of accounts (chart CRP). The name is what entry lines show next to the number. */
export const ACCOUNTS: Record<string, string> = {
  "1010": "Operating bank account (OPS-USD)",
  "1020": "Payroll bank account (PAY-USD)",
  "1050": "Petty cash",
  "1200": "Accounts receivable, trade",
  "1300": "Inventory, raw materials",
  "1310": "Inventory, work in process",
  "1320": "Inventory, finished goods",
  "1400": "Prepaid expenses",
  "1500": "Machinery and equipment",
  "1510": "Buildings",
  "1520": "Vehicles",
  "1530": "Computer equipment",
  "1590": "Accumulated depreciation",
  "2000": "Accounts payable, trade",
  "2100": "Accrued liabilities",
  "2150": "Accrued payroll",
  "2200": "Sales tax payable",
  "2400": "Revolving line of credit",
  "3000": "Common stock",
  "3100": "Retained earnings",
  "4000": "Pump sales",
  "4100": "Spare parts sales",
  "4200": "Service and commissioning revenue",
  "5000": "Cost of goods sold",
  "5100": "Freight in",
  "6100": "Salaries and wages",
  "6200": "Utilities",
  "6300": "Maintenance and repairs",
  "6400": "Freight out",
  "6500": "Office and administration",
  "6600": "Depreciation expense",
  "6700": "Insurance",
  "6800": "Travel and entertainment",
  "7000": "Interest expense",
  "7100": "Bank fees",
  "7200": "Interest income",
};

const ledger: Ledger = {
  id: LEDGER_ID,
  code: "CRP-US",
  name: `${LEGAL_ENTITY.name} general ledger`,
  chartCode: "CRP",
  currency: LEGAL_ENTITY.currency,
  dimensionAttributes: ["COSTCENTER"],
  allowReinstate: false,
  version: 3,
};

// --- journal names ---------------------------------------------------------------

function journalName(code: string, name: string, requiresApproval: boolean, extra: Partial<JournalName> = {}): JournalName {
  return {
    code,
    name,
    requiresApproval,
    selfApprovalAllowed: false,
    reversalReasonRequired: true,
    allowReconAccounts: false,
    linesLimit: 500,
    postingRestriction: "none",
    descriptionLanguage: "legal_entity",
    active: true,
    updatedAt: at(-200),
    lockVersion: 1,
    ...extra,
  };
}

const names = table<JournalName & { id: string }>(
  [
    journalName("GEN", "General journal", true),
    journalName("ACCR", "Month-end accruals", false),
    journalName("RECL", "Reclassifications", true),
    journalName("ADJ", "Inventory and cost adjustments", true, { allowReconAccounts: true }),
    journalName("BANK", "Bank charges and interest", false, { postingRestriction: "own_journals_only" }),
  ].map((n) => ({ ...n, id: n.code })),
);

const nameRow = (code: string) => {
  const row = names.get(code);
  if (!row) return undefined;
  const { id: _id, ...rest } = row;
  return rest as JournalName;
};

// --- journal batches -------------------------------------------------------------

type Side = "D" | "C";
type LineSeed = [account: string, side: Side, amount: number, text?: string, costCenter?: string];

const cents = (s: string): number => Math.round(Number(s) * 100);
const money = (c: number): string => (c / 100).toFixed(2);

function lines(seed: LineSeed[]): BatchLine[] {
  return seed.map(([account, debitCredit, amount, text, costCenter], i) => ({
    lineNumber: i + 1,
    account,
    ...(costCenter ? { dimensions: { COSTCENTER: costCenter } } : {}),
    debitCredit,
    amount: amount.toFixed(2),
    currency: "USD",
    ...(text ? { text } : {}),
  }));
}

function totals(ls: BatchLine[]) {
  const debit = ls.filter((l) => l.debitCredit === "D").reduce((s, l) => s + cents(l.amount), 0);
  const credit = ls.filter((l) => l.debitCredit === "C").reduce((s, l) => s + cents(l.amount), 0);
  return { totalDebit: money(debit), totalCredit: money(credit), lineCount: ls.length };
}

interface BatchSeed {
  n: number;
  journal: string;
  description: string;
  posting: number;
  status: JournalBatch["status"];
  owner: string;
  lines: LineSeed[];
  submittedBy?: string;
  decidedBy?: string;
  postedBy?: string;
  age: number;
}

const entries = new Map<string, Entry>();
let voucherSeq = 4817;
let postingSeq = 128_406;

function makeEntry(batch: JournalBatch): Entry {
  const id = uid(K_ENTRY, voucherSeq);
  const ls = batch.lines ?? [];
  const entry: Entry = {
    id,
    ledgerId: batch.ledgerId,
    voucher: `${batch.journalName}-${String(voucherSeq).padStart(6, "0")}`,
    postingSequence: postingSeq,
    postingDate: batch.postingDate,
    periodKey: batch.postingDate.slice(0, 7),
    description: batch.description,
    traceId: `4bf92f3577b34da6a3ce929d0e0e${String(voucherSeq).padStart(4, "0")}`,
    totals: { currency: batch.currency, debit: batch.totalDebit, credit: batch.totalCredit },
    lines: ls.map((l) => ({
      id: uid(K_ENTRY + 1, voucherSeq * 100 + l.lineNumber),
      lineNumber: l.lineNumber,
      account: l.account,
      displayName: `${l.account} ${ACCOUNTS[l.account] ?? ""}`.trim() + (l.dimensions?.COSTCENTER ? ` / ${l.dimensions.COSTCENTER}` : ""),
      debitCredit: l.debitCredit,
      amount: l.amount,
      ...(l.text ? { text: l.text } : {}),
    })),
  };
  voucherSeq += 1;
  postingSeq += 3;
  entries.set(id, entry);
  return entry;
}

function seedBatch(s: BatchSeed): JournalBatch {
  const name = nameRow(s.journal)!;
  const ls = lines(s.lines);
  const batch: JournalBatch = {
    id: uid(K_BATCH, s.n),
    ledgerId: LEDGER_ID,
    legalEntityId: LEGAL_ENTITY.id,
    journalName: s.journal,
    description: s.description,
    postingDate: day(s.posting),
    currency: "USD",
    status: s.status,
    version: s.status === "draft" ? 1 : s.status === "posted" ? 4 : 2,
    requiresApproval: name.requiresApproval,
    selfApprovalAllowed: name.selfApprovalAllowed,
    ...(s.submittedBy ? { submittedBy: s.submittedBy } : {}),
    ...(s.decidedBy ? { decidedBy: s.decidedBy } : {}),
    ...totals(ls),
    createdBy: s.owner,
    createdAt: at(-s.age),
    updatedAt: at(-s.age + 0.5),
    lines: ls,
  };
  if (s.status === "posted") {
    const postedAt = at(-s.age + 1);
    const entry = makeEntry(batch);
    batch.postedBy = s.postedBy ?? s.owner;
    batch.postedAt = postedAt;
    batch.postedEntryId = entry.id;
  }
  return batch;
}

// Month-end close for last month plus this month's work in progress.
const monthEnd = -new Date().getDate();
const batches = table<JournalBatch>(
  (
    [
      {
        n: 1,
        journal: "ACCR",
        description: "Payroll accrual, last week of the month",
        posting: monthEnd,
        status: "posted",
        owner: "ana.ortiz",
        age: -monthEnd + 1,
        lines: [
          ["6100", "D", 96_400, "Hourly and salaried, plant", "CR-PLANT"],
          ["6100", "D", 38_200, "Hourly, Dallas distribution center", "DAL-DC"],
          ["6100", "D", 21_650, "Office salaries", "ADMIN"],
          ["2150", "C", 156_250, "Accrued payroll"],
        ],
      },
      {
        n: 2,
        journal: "ACCR",
        description: "Utilities accrual for meters read after month end",
        posting: monthEnd,
        status: "posted",
        owner: "ana.ortiz",
        age: -monthEnd + 1,
        lines: [
          ["6200", "D", 21_480, "Electricity and gas, Cedar Rapids", "CR-PLANT"],
          ["6200", "D", 8_215, "Electricity, Dallas", "DAL-DC"],
          ["2100", "C", 29_695, "Accrued utilities"],
        ],
      },
      {
        n: 3,
        journal: "GEN",
        description: "Warranty reserve true-up, year to date",
        posting: monthEnd,
        status: "posted",
        owner: "dana.kim",
        submittedBy: "dana.kim",
        decidedBy: "marcus.lee",
        postedBy: "dana.kim",
        age: -monthEnd + 2,
        lines: [
          ["5000", "D", 12_600, "Seal failures on CP-200 units shipped in Q3", "CR-PLANT"],
          ["2100", "C", 12_600, "Warranty reserve"],
        ],
      },
      {
        n: 4,
        journal: "RECL",
        description: "Reclassify freight on Ironbridge castings to raw material inventory",
        posting: -2,
        status: "in_approval",
        owner: "dana.kim",
        submittedBy: "dana.kim",
        age: 3,
        lines: [
          ["1300", "D", 3_860, "Inbound freight, volute and impeller castings", "CR-PLANT"],
          ["5100", "C", 3_860, "Freight in", "CR-PLANT"],
        ],
      },
      {
        n: 5,
        journal: "ADJ",
        description: "Cycle count adjustment, Dallas finished goods",
        posting: -1,
        status: "approved",
        owner: "ana.ortiz",
        submittedBy: "ana.ortiz",
        decidedBy: ME.username,
        age: 2,
        lines: [
          ["5000", "D", 1_742.5, "Two SP-150 pumps short on count", "DAL-DC"],
          ["1320", "C", 1_742.5, "Finished goods, DAL-FG", "DAL-DC"],
        ],
      },
      {
        n: 6,
        journal: "BANK",
        description: "Bank fee and interest from statement STMT-2026-010",
        posting: -1,
        status: "draft",
        owner: ME.username,
        age: 0.4,
        lines: [
          ["7100", "D", 42.5, "Account maintenance fee", "ADMIN"],
          ["1010", "C", 42.5, "Operating account"],
          ["1010", "D", 118.37, "Interest credit"],
          ["7200", "C", 118.37, "Interest income", "ADMIN"],
        ],
      },
      {
        n: 7,
        journal: "GEN",
        description: "Accrue fourth-quarter bonus pool",
        posting: -3,
        status: "rejected",
        owner: "ana.ortiz",
        submittedBy: "ana.ortiz",
        decidedBy: "marcus.lee",
        age: 4,
        lines: [
          ["6100", "D", 45_000, "Bonus pool, management", "ADMIN"],
          ["2150", "C", 45_000, "Accrued bonuses"],
        ],
      },
      {
        n: 8,
        journal: "GEN",
        description: "Capitalize CNC spindle rebuild on MCH-01",
        posting: -5,
        status: "validated",
        owner: "dana.kim",
        age: 5,
        lines: [
          ["1500", "D", 18_400, "Spindle rebuild extends service life", "CR-PLANT"],
          ["6300", "C", 18_400, "Reverse maintenance expense booked by AP", "CR-PLANT"],
        ],
      },
      {
        n: 9,
        journal: "ACCR",
        description: "Prepaid insurance amortization",
        posting: monthEnd,
        status: "posted",
        owner: "ana.ortiz",
        age: -monthEnd + 1,
        lines: [
          ["6700", "D", 4_250, "Property and liability policy, 1/12", "ADMIN"],
          ["1400", "C", 4_250, "Prepaid insurance"],
        ],
      },
      {
        n: 10,
        journal: "GEN",
        description: "Correct cost center on September maintenance invoice",
        posting: monthEnd - 30,
        status: "cancelled",
        owner: "ana.ortiz",
        age: -monthEnd + 32,
        lines: [
          ["6300", "D", 2_310, "Hydraulic press service", "CR-PLANT"],
          ["6300", "C", 2_310, "Hydraulic press service", "DAL-DC"],
        ],
      },
    ] as BatchSeed[]
  ).map(seedBatch),
);

const attachments = new Map<string, BatchAttachment[]>([
  [
    uid(K_BATCH, 3),
    [
      { id: uid(K_ATTACHMENT, 1), batchId: uid(K_BATCH, 3), kind: "attachment", name: "Warranty claims Q3.xlsx", documentType: "supporting_calculation", retentionClass: "financial_record_7y", retainedUntil: day(7 * 365), contentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", sizeBytes: 48_210, createdBy: "dana.kim", createdAt: at(-monthEnd - 2) },
      { id: uid(K_ATTACHMENT, 2), batchId: uid(K_BATCH, 3), kind: "note", note: "Reserve rate stays at 1.8% of CP-200 sales; the true-up covers the seal batch recalled in August.", createdBy: "marcus.lee", createdAt: at(-monthEnd - 1.5) },
    ],
  ],
  [uid(K_BATCH, 7), [{ id: uid(K_ATTACHMENT, 3), batchId: uid(K_BATCH, 7), kind: "note", note: "Rejected: the bonus pool is approved by the board in December. Accrue then.", createdBy: "marcus.lee", createdAt: at(-3.5) }]],
  [uid(K_BATCH, 4), [{ id: uid(K_ATTACHMENT, 4), batchId: uid(K_BATCH, 4), kind: "attachment", name: "Ironbridge freight bill 88214.pdf", documentType: "invoice_image", retentionClass: "financial_record_7y", contentType: "application/pdf", sizeBytes: 182_440, createdBy: "dana.kim", createdAt: at(-3) }]],
]);

// --- recurring journals ------------------------------------------------------------

type RecurringSeed = [account: string, side: Side, kind: "fixed" | "percent", amount: number, text: string, costCenter?: string];

function recurringLines(seed: RecurringSeed[]): RecurringLine[] {
  return seed.map(([account, debitCredit, amountKind, amount, text, costCenter], i) => ({
    lineNumber: i + 1,
    account,
    ...(costCenter ? { dimensions: { COSTCENTER: costCenter } } : {}),
    debitCredit,
    amountKind,
    amount: amount.toFixed(2),
    currency: "USD",
    text,
  }));
}

const monthDay = (monthOffset: number, dayOfMonth: number): string => {
  const d = new Date();
  return new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + monthOffset, dayOfMonth)).toISOString().slice(0, 10);
};
const firstOfNext = monthDay(1, 1);
const lastOfThis = monthDay(1, 0);
const lastOfPrev = day(monthEnd);

const recurring = table<RecurringJournal>([
  {
    id: uid(K_RECURRING, 1),
    ledgerId: LEDGER_ID,
    legalEntityId: LEGAL_ENTITY.id,
    journalName: "ACCR",
    name: "Prepaid insurance amortization",
    description: "Prepaid insurance amortization",
    frequency: "monthly",
    dayOfMonth: 31,
    startDate: `${new Date().getFullYear()}-01-31`,
    endDate: `${new Date().getFullYear()}-12-31`,
    status: "active",
    nextRunDate: lastOfThis,
    lastRunDate: lastOfPrev,
    version: 11,
    lineCount: 2,
    createdBy: "ana.ortiz",
    createdAt: at(-310),
    updatedAt: at(-monthEnd),
    lines: recurringLines([
      ["6700", "D", "fixed", 4_250, "Property and liability policy, 1/12", "ADMIN"],
      ["1400", "C", "fixed", 4_250, "Prepaid insurance"],
    ]),
  },
  {
    id: uid(K_RECURRING, 2),
    ledgerId: LEDGER_ID,
    legalEntityId: LEGAL_ENTITY.id,
    journalName: "ACCR",
    name: "Plant lease, Dallas warehouse",
    description: "Monthly rent, Dallas distribution center",
    frequency: "monthly",
    dayOfMonth: 1,
    startDate: `${new Date().getFullYear() - 2}-07-01`,
    status: "active",
    nextRunDate: firstOfNext,
    lastRunDate: monthDay(0, 1),
    version: 28,
    lineCount: 2,
    createdBy: "ana.ortiz",
    createdAt: at(-860),
    updatedAt: at(-new Date().getDate() + 1),
    lines: recurringLines([
      ["6500", "D", "fixed", 18_750, "Rent, 4100 Commerce St", "DAL-DC"],
      ["2100", "C", "fixed", 18_750, "Accrued rent"],
    ]),
  },
  {
    id: uid(K_RECURRING, 3),
    ledgerId: LEDGER_ID,
    legalEntityId: LEGAL_ENTITY.id,
    journalName: "GEN",
    name: "Shared services allocation",
    description: "Allocate corporate IT and HR cost to the plant and the DC",
    frequency: "monthly",
    dayOfMonth: 28,
    startDate: `${new Date().getFullYear()}-01-28`,
    baseAmount: "36400.00",
    status: "active",
    nextRunDate: monthDay(0, 28),
    lastRunDate: day(monthEnd - 3),
    version: 9,
    lineCount: 3,
    createdBy: "dana.kim",
    createdAt: at(-300),
    updatedAt: at(-monthEnd + 3),
    lines: recurringLines([
      ["6500", "D", "percent", 70, "Share of shared services, plant", "CR-PLANT"],
      ["6500", "D", "percent", 30, "Share of shared services, DC", "DAL-DC"],
      ["6500", "C", "percent", 100, "Shared services allocated out", "ADMIN"],
    ]),
  },
  {
    id: uid(K_RECURRING, 4),
    ledgerId: LEDGER_ID,
    legalEntityId: LEGAL_ENTITY.id,
    journalName: "ACCR",
    name: "Property tax accrual",
    description: "Cedar Rapids property tax, accrued quarterly",
    frequency: "quarterly",
    dayOfMonth: 30,
    startDate: `${new Date().getFullYear()}-03-30`,
    status: "paused",
    lastRunDate: `${new Date().getFullYear()}-09-30`,
    version: 5,
    lineCount: 2,
    createdBy: "ana.ortiz",
    createdAt: at(-240),
    updatedAt: at(-20),
    lines: recurringLines([
      ["6500", "D", "fixed", 27_300, "Linn County property tax, 1/4", "CR-PLANT"],
      ["2100", "C", "fixed", 27_300, "Accrued property tax"],
    ]),
  },
]);

const runs = new Map<string, RecurringRun[]>([
  [
    uid(K_RECURRING, 1),
    [
      { id: uid(K_RUN, 1), recurringId: uid(K_RECURRING, 1), scheduledDate: lastOfPrev, batchId: uid(K_BATCH, 9), generatedBy: "ana.ortiz", generatedAt: at(monthEnd + 0.2) },
    ],
  ],
]);

// --- routes ------------------------------------------------------------------------

const notFound = (code: string, what: string) => reply.problem(404, code, `${what} not found`);

function ifMatch(headers: Headers): number | null {
  const raw = headers.get("If-Match");
  return raw === null ? null : Number(raw.replace(/"/g, ""));
}

const summary = (b: JournalBatch): JournalBatch => {
  const { lines: _lines, ...rest } = b;
  return rest;
};

route("GET", `${BASE}/ledgers`, () => ({ items: [ledger] }));
route("GET", `${BASE}/ledgers/{id}`, ({ params }) => (params.id === LEDGER_ID ? ledger : notFound("LEDGER_NOT_FOUND", "ledger")));
route("GET", `${BASE}/journal-names`, () => ({ items: names.all().map((n) => nameRow(n.code)!) }));
route("GET", `${BASE}/journal-names/{code}`, ({ params }) => nameRow(params.code) ?? notFound("LEDGER_JOURNAL_NAME_NOT_FOUND", "journal name"));

route("GET", `${BASE}/journal-batches`, ({ query }) => {
  const status = query.get("status");
  const journal = query.get("journalName");
  const rows = batches
    .all()
    .filter((b) => (!status || b.status === status) && (!journal || b.journalName === journal))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  return { items: rows.map(summary) };
});

route("POST", `${BASE}/journal-batches`, ({ body }) => {
  const input = (body ?? {}) as { journalName?: string; description?: string; postingDate?: string; documentDate?: string; lines?: { account: string; dimensions?: Record<string, string>; debitCredit: Side; amount: { value: string }; text?: string }[] };
  const name = nameRow(input.journalName ?? "");
  if (!name) return reply.problem(422, "LEDGER_JOURNAL_NAME_NOT_FOUND", "journal name not found");
  const ls: BatchLine[] = (input.lines ?? []).map((l, i) => ({ lineNumber: i + 1, account: l.account, ...(l.dimensions ? { dimensions: l.dimensions } : {}), debitCredit: l.debitCredit, amount: Number(l.amount.value).toFixed(2), currency: "USD", ...(l.text ? { text: l.text } : {}) }));
  const batch: JournalBatch = {
    id: newId(),
    ledgerId: LEDGER_ID,
    legalEntityId: LEGAL_ENTITY.id,
    journalName: name.code,
    description: input.description ?? "",
    postingDate: input.postingDate ?? day(0),
    ...(input.documentDate ? { documentDate: input.documentDate } : {}),
    currency: "USD",
    status: "draft",
    version: 1,
    requiresApproval: name.requiresApproval,
    selfApprovalAllowed: name.selfApprovalAllowed,
    ...totals(ls),
    createdBy: ME.username,
    createdAt: at(0),
    updatedAt: at(0),
    lines: ls,
  };
  return reply.created(batches.insert(batch));
});

route("GET", `${BASE}/journal-batches/{id}/attachments`, ({ params }) => ({ items: attachments.get(params.id) ?? [] }));
route("PUT", `${BASE}/journal-batches/{id}/attachments/{aid}`, ({ params, body }) => {
  const input = (body ?? {}) as Partial<BatchAttachment>;
  const row: BatchAttachment = { ...input, id: params.aid, batchId: params.id, kind: input.kind ?? "note", createdBy: ME.username, createdAt: at(0) };
  attachments.set(params.id, [...(attachments.get(params.id) ?? []), row]);
  return reply.created(row);
});
route("GET", `${BASE}/journal-entries/{id}/attachments`, ({ params }) => {
  const batch = batches.all().find((b) => b.postedEntryId === params.id);
  return { items: batch ? (attachments.get(batch.id) ?? []) : [] };
});
route("GET", `${BASE}/journal-entries/{id}`, ({ params }) => entries.get(params.id) ?? notFound("LEDGER_ENTRY_NOT_FOUND", "journal entry"));

route("GET", `${BASE}/journal-batches/{id}`, ({ params }) => batches.get(params.id) ?? notFound("LEDGER_BATCH_NOT_FOUND", "journal batch"));

function guard(id: string, headers: Headers): JournalBatch | DemoResponse {
  const batch = batches.get(id);
  if (!batch) return notFound("LEDGER_BATCH_NOT_FOUND", "journal batch");
  const v = ifMatch(headers);
  if (v !== null && v !== batch.version) return reply.problem(412, "LEDGER_BATCH_VERSION_CONFLICT", "the batch changed since you opened it");
  return batch;
}

route("PATCH", `${BASE}/journal-batches/{id}`, ({ params, headers, body }) => {
  const batch = guard(params.id, headers);
  if (batch instanceof DemoResponse) return batch;
  const input = (body ?? {}) as { description?: string; postingDate?: string; documentDate?: string; lines?: { account: string; dimensions?: Record<string, string>; debitCredit: Side; amount: { value: string }; text?: string }[] };
  const ls = input.lines
    ? input.lines.map((l, i) => ({ lineNumber: i + 1, account: l.account, ...(l.dimensions ? { dimensions: l.dimensions } : {}), debitCredit: l.debitCredit, amount: Number(l.amount.value).toFixed(2), currency: "USD", ...(l.text ? { text: l.text } : {}) }))
    : (batch.lines ?? []);
  const next: JournalBatch = {
    ...batch,
    description: input.description ?? batch.description,
    postingDate: input.postingDate ?? batch.postingDate,
    ...(input.documentDate !== undefined ? { documentDate: input.documentDate || undefined } : {}),
    lines: ls,
    ...totals(ls),
    status: "draft",
    submittedBy: undefined,
    decidedBy: undefined,
    version: batch.version + 1,
    updatedAt: at(0),
  };
  return batches.update(batch.id, next);
});

route("POST", `${BASE}/journal-batches/{id}/{action}`, ({ params, headers }) => {
  const batch = guard(params.id, headers);
  if (batch instanceof DemoResponse) return batch;
  const bump = (patch: Partial<JournalBatch>) => batches.update(batch.id, { ...patch, version: batch.version + 1, updatedAt: at(0) });
  switch (params.action) {
    case "submit": {
      if (batch.totalDebit !== batch.totalCredit) {
        return reply.problem(422, "LEDGER_UNBALANCED", "the batch does not balance", undefined);
      }
      return bump(batch.requiresApproval ? { status: "in_approval", submittedBy: ME.username } : { status: "validated" });
    }
    case "approve":
      if (batch.status !== "in_approval") return reply.problem(409, "LEDGER_BATCH_STATUS", "only a batch in approval can be approved");
      if (batch.submittedBy === ME.username && !batch.selfApprovalAllowed) return reply.problem(403, "LEDGER_SELF_APPROVAL", "you submitted this batch, so someone else must approve it");
      return bump({ status: "approved", decidedBy: ME.username });
    case "reject":
      if (batch.status !== "in_approval") return reply.problem(409, "LEDGER_BATCH_STATUS", "only a batch in approval can be rejected");
      return bump({ status: "rejected", decidedBy: ME.username });
    case "cancel":
      return bump({ status: "cancelled" });
    case "post": {
      const postable = batch.status === "approved" || (batch.status === "validated" && !batch.requiresApproval);
      if (!postable) return reply.problem(409, "LEDGER_BATCH_STATUS", "the batch must be validated or approved before it is posted");
      const postedAt = at(0);
      const entry = makeEntry(batch);
      batches.update(batch.id, { status: "posted", postedBy: ME.username, postedAt, postedEntryId: entry.id, version: batch.version + 1, updatedAt: postedAt });
      return { entries: [{ id: entry.id, voucher: entry.voucher }], idempotency: { key: headers.get("Idempotency-Key") ?? newId(), replayed: false } };
    }
    default:
      return reply.problem(404, "COM-0404", "unknown batch action");
  }
});

route("GET", `${BASE}/recurring-journals`, ({ query }) => {
  const status = query.get("status");
  const rows = recurring
    .all()
    .filter((r) => !status || r.status === status)
    .sort((a, b) => (a.nextRunDate ?? "9999").localeCompare(b.nextRunDate ?? "9999") || a.name.localeCompare(b.name))
    .map(({ lines: _lines, ...rest }) => rest);
  return { items: rows };
});
route("GET", `${BASE}/recurring-journals/{id}/runs`, ({ params }) => ({ items: [...(runs.get(params.id) ?? [])].sort((a, b) => b.scheduledDate.localeCompare(a.scheduledDate)) }));
route("GET", `${BASE}/recurring-journals/{id}`, ({ params }) => recurring.get(params.id) ?? notFound("LEDGER_RECURRING_NOT_FOUND", "recurring journal"));

route("PUT", `${BASE}/recurring-journals/{id}`, ({ params, body }) => {
  const input = (body ?? {}) as Omit<RecurringJournal, "lines"> & { lines: { account: string; dimensions?: Record<string, string>; debitCredit: Side; amountKind: "fixed" | "percent"; amount: { value: string }; text?: string }[] };
  const current = recurring.get(params.id);
  const ls: RecurringLine[] = (input.lines ?? []).map((l, i) => ({ lineNumber: i + 1, account: l.account, ...(l.dimensions ? { dimensions: l.dimensions } : {}), debitCredit: l.debitCredit, amountKind: l.amountKind, amount: Number(l.amount.value).toFixed(2), currency: "USD", ...(l.text ? { text: l.text } : {}) }));
  const row: RecurringJournal = {
    id: params.id,
    ledgerId: LEDGER_ID,
    legalEntityId: LEGAL_ENTITY.id,
    journalName: input.journalName,
    name: input.name,
    description: input.description,
    frequency: input.frequency,
    dayOfMonth: input.dayOfMonth,
    startDate: input.startDate,
    ...(input.endDate ? { endDate: input.endDate } : {}),
    ...(input.baseAmount ? { baseAmount: input.baseAmount } : {}),
    status: current?.status ?? "active",
    nextRunDate: current?.nextRunDate ?? input.startDate,
    ...(current?.lastRunDate ? { lastRunDate: current.lastRunDate } : {}),
    version: (current?.version ?? 0) + 1,
    lineCount: ls.length,
    createdBy: current?.createdBy ?? ME.username,
    createdAt: current?.createdAt ?? at(0),
    updatedAt: at(0),
    lines: ls,
  };
  return current ? recurring.update(params.id, row) : reply.created(recurring.insert(row));
});

route("POST", `${BASE}/recurring-journals/{id}/{action}`, ({ params, body }) => {
  const r = recurring.get(params.id);
  if (!r) return notFound("LEDGER_RECURRING_NOT_FOUND", "recurring journal");
  if (params.action === "pause") return recurring.update(r.id, { status: "paused", nextRunDate: undefined, version: r.version + 1, updatedAt: at(0) });
  if (params.action === "resume") return recurring.update(r.id, { status: "active", nextRunDate: firstOfNext, version: r.version + 1, updatedAt: at(0) });
  if (params.action !== "generate") return reply.problem(404, "COM-0404", "unknown action");
  const input = (body ?? {}) as { scheduledDate?: string; baseAmount?: string };
  const scheduledDate = input.scheduledDate ?? r.nextRunDate ?? day(0);
  const existing = (runs.get(r.id) ?? []).find((x) => x.scheduledDate === scheduledDate);
  if (existing) return existing;
  const base = Number(input.baseAmount ?? r.baseAmount ?? "0");
  const ls: BatchLine[] = (r.lines ?? []).map((l) => ({
    lineNumber: l.lineNumber,
    account: l.account,
    ...(l.dimensions ? { dimensions: l.dimensions } : {}),
    debitCredit: l.debitCredit,
    amount: (l.amountKind === "percent" ? (base * Number(l.amount)) / 100 : Number(l.amount)).toFixed(2),
    currency: l.currency,
    ...(l.text ? { text: l.text } : {}),
  }));
  const name = nameRow(r.journalName)!;
  const batch = batches.insert({
    id: newId(),
    ledgerId: LEDGER_ID,
    legalEntityId: LEGAL_ENTITY.id,
    journalName: r.journalName,
    description: r.description,
    postingDate: scheduledDate,
    currency: "USD",
    status: "draft",
    version: 1,
    requiresApproval: name.requiresApproval,
    selfApprovalAllowed: name.selfApprovalAllowed,
    ...totals(ls),
    createdBy: ME.username,
    createdAt: at(0),
    updatedAt: at(0),
    lines: ls,
  });
  const run: RecurringRun = { id: newId(), recurringId: r.id, scheduledDate, batchId: batch.id, generatedBy: ME.username, generatedAt: at(0) };
  runs.set(r.id, [run, ...(runs.get(r.id) ?? [])]);
  recurring.update(r.id, { lastRunDate: scheduledDate, version: r.version + 1 });
  return reply.created(run);
});
