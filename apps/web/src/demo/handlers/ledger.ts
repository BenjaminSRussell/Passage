// Journal batches (/api/v1/ledger/journal-batches) for the demo: enough for the controller's home tiles and the
// journals list to show journals in draft, waiting for approval, approved and posted. Read-only.
import type { BatchLine, BatchStatus, JournalBatch } from "../../modules/ledger/api/client";
import { LEGAL_ENTITY, USER } from "../company";
import { reply, route } from "../backend";
import { at, day, dec, table, uid } from "../store";

const KIND_BATCH = 470;
const LEDGER_ID = "ledger-crp";

function lines(amount: number, debit: string, credit: string, text: string): BatchLine[] {
  return [
    { lineNumber: 1, account: debit, debitCredit: "D", amount: dec(amount), currency: LEGAL_ENTITY.currency, text },
    { lineNumber: 2, account: credit, debitCredit: "C", amount: dec(amount), currency: LEGAL_ENTITY.currency, text },
  ];
}

function batch(n: number, status: BatchStatus, description: string, amount: number, ageDays: number, debit: string, credit: string): JournalBatch {
  const decided = status === "approved" || status === "posted";
  return {
    id: uid(KIND_BATCH, n),
    ledgerId: LEDGER_ID,
    legalEntityId: LEGAL_ENTITY.id,
    journalName: "GENERAL",
    description,
    postingDate: day(-ageDays),
    currency: LEGAL_ENTITY.currency,
    status,
    version: 1,
    requiresApproval: true,
    selfApprovalAllowed: false,
    submittedBy: status === "draft" ? undefined : USER.id,
    decidedBy: decided ? "controller" : undefined,
    postedBy: status === "posted" ? "controller" : undefined,
    postedAt: status === "posted" ? at(-ageDays) : undefined,
    totalDebit: dec(amount),
    totalCredit: dec(amount),
    lineCount: 2,
    createdBy: USER.id,
    createdAt: at(-ageDays),
    updatedAt: at(-ageDays),
    lines: lines(amount, debit, credit, description),
  };
}

const batches = table<JournalBatch>([
  batch(1, "draft", "Accrue October freight", 4820, 1, "6100", "2150"),
  batch(2, "draft", "Reclassify tooling to fixed assets", 12750, 2, "1500", "6400"),
  batch(3, "in_approval", "Month-end payroll accrual", 86400, 2, "6000", "2200"),
  batch(4, "approved", "Prepaid insurance, November share", 3150, 3, "6300", "1300"),
  batch(5, "posted", "Depreciation, October", 21900, 9, "6500", "1590"),
  batch(6, "posted", "Bank fees, October", 412.6, 11, "6700", "1010"),
]);

route("GET", "/api/v1/ledger/journal-batches", ({ query }) => {
  const status = query.get("status");
  const name = query.get("journalName");
  const items = batches
    .all()
    .filter((b) => (!status || b.status === status) && (!name || b.journalName === name))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .map((b) => ({ ...b, lines: undefined }));
  return { items };
});

route("GET", "/api/v1/ledger/journal-batches/{id}", ({ params }) => batches.get(params.id) ?? reply.problem(404, "COM-0404", "journal batch not found"));
