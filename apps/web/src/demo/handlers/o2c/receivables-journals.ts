// Receivables ledger journals and remittance advice (/api/v1/receivables) for the demo: a few journals Cedar Ridge Pumps owes its
// ledger (one of them parked), the sweep and retry that change them, and an advice import that attaches references to nothing real.
import type { AdviceLineResult, GLJournal } from "../../../modules/receivables/api/client";
import { LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";

const AT = "2026-11-02T09:00:00Z";

function journal(n: number, over: Partial<GLJournal> & Pick<GLJournal, "sourceType" | "postingDate" | "debit" | "lines">): GLJournal {
  return {
    id: `00000000-0000-0000-0000-00000000b0${String(n).padStart(2, "0")}`,
    legalEntityId: LEGAL_ENTITY.id,
    sourceId: `00000000-0000-0000-0000-00000000c0${String(n).padStart(2, "0")}`,
    documentId: `PAY-${n}`,
    currency: "USD",
    status: "POSTED",
    journalPostingId: `jp-${n}`,
    attempts: 1,
    parked: false,
    createdAt: AT,
    ...over,
  };
}

const journals: GLJournal[] = [
  journal(1, {
    sourceType: "payment_receipt",
    postingDate: "2026-10-28",
    debit: "12400.00",
    lines: [
      { account: "1000", side: "D", amount: "12400.00", text: "Payment received" },
      { account: "1205", side: "C", amount: "12400.00", text: "Unapplied cash" },
    ],
  }),
  journal(2, {
    sourceType: "payment_apply",
    postingDate: "2026-10-29",
    debit: "12400.00",
    status: "PENDING",
    journalPostingId: null,
    attempts: 0,
    lines: [
      { account: "1205", side: "D", amount: "12276.00", text: "Payment applied" },
      { account: "4100", side: "D", amount: "124.00", text: "Cash discount taken" },
      { account: "1200", side: "C", amount: "12400.00", text: "Open items settled" },
    ],
  }),
  journal(3, {
    sourceType: "dunning_fee",
    postingDate: "2026-09-30",
    debit: "25.00",
    status: "PENDING",
    journalPostingId: null,
    attempts: 5,
    parked: true,
    lastError: "The ledger period for 2026-09-30 is closed.",
    lines: [
      { account: "1200", side: "D", amount: "25.00", text: "Dunning fee" },
      { account: "4300", side: "C", amount: "25.00", text: "Dunning fee income" },
    ],
  }),
];

route("GET", "/api/v1/receivables/gl-journals", ({ query }) => {
  let rows = journals;
  const status = query.get("status");
  if (status) rows = rows.filter((j) => j.status === status);
  if (query.get("parked") === "true") rows = rows.filter((j) => j.parked);
  return { items: rows };
});

route("POST", "/api/v1/receivables/gl-journals:sweep", () => {
  let posted = 0;
  for (const j of journals) {
    if (j.status === "PENDING" && !j.parked) {
      j.status = "POSTED";
      j.journalPostingId = `jp-${j.id.slice(-2)}`;
      j.attempts += 1;
      posted += 1;
    }
  }
  return { posted, failed: 0, parked: journals.filter((j) => j.parked).length };
});

route("POST", "/api/v1/receivables/gl-journals/{id}:retry", ({ params, body }) => {
  const id = params.id;
  const j = journals.find((row) => row.id === id);
  if (!j) return reply.problem(404, "AR-JOURNAL-NOT-FOUND", "No such journal.");
  if (j.status === "POSTED") return reply.problem(409, "AR-JOURNAL-STATE", "The journal is already posted.");
  const date = typeof body === "object" && body !== null ? (body as { postingDate?: string }).postingDate : undefined;
  j.parked = false;
  j.attempts = 0;
  j.lastError = null;
  if (date) j.postingDate = date;
  return j;
});

// Ledger setup: the bank accounts Cedar Ridge Pumps maps, its exchange settings, and the backfill of earlier journals.
interface DemoBank {
  legalEntityId: string;
  currency: string;
  accountRef: string;
  glAccount: string;
  name: string;
}

const banks: DemoBank[] = [
  { legalEntityId: LEGAL_ENTITY.id, currency: "USD", accountRef: "", glAccount: "1010", name: "Operating account USD" },
  { legalEntityId: LEGAL_ENTITY.id, currency: "USD", accountRef: "US00001234", glAccount: "1020", name: "Lockbox account" },
];

route("GET", "/api/v1/receivables/bank-accounts", ({ query }) => {
  const le = query.get("legalEntityId");
  return { items: banks.filter((b) => !le || b.legalEntityId === le) };
});

route("PUT", "/api/v1/receivables/bank-accounts", ({ body }) => {
  const input = (body ?? {}) as DemoBank;
  const at = banks.findIndex((b) => b.legalEntityId === input.legalEntityId && b.currency === input.currency && b.accountRef === input.accountRef);
  if (at >= 0) banks[at] = input;
  else banks.push(input);
  return input;
});

route("DELETE", "/api/v1/receivables/bank-accounts", ({ query }) => {
  const at = banks.findIndex((b) => b.legalEntityId === query.get("legalEntityId") && b.currency === query.get("currency") && b.accountRef === (query.get("accountRef") ?? ""));
  if (at < 0) return reply.problem(404, "AR-BANK-NOT-FOUND", "No such bank account mapping.");
  banks.splice(at, 1);
  return reply.noContent();
});

let fxSetting: Record<string, unknown> = { legalEntityId: LEGAL_ENTITY.id, crossCurrencyEnabled: true, functionalCurrency: "USD", gainAccount: "7100", lossAccount: "7200", tolerancePercent: "0.5" };

route("GET", "/api/v1/receivables/fx-settings", ({ query }) =>
  query.get("legalEntityId") === fxSetting.legalEntityId ? fxSetting : { legalEntityId: query.get("legalEntityId") ?? "", crossCurrencyEnabled: false, functionalCurrency: "", gainAccount: "7100", lossAccount: "7200" },
);

route("PUT", "/api/v1/receivables/fx-settings", ({ body }) => {
  // A PUT replaces the setting: a tolerance left out goes back to the default.
  fxSetting = { legalEntityId: fxSetting.legalEntityId, ...((body ?? {}) as Record<string, unknown>) };
  return fxSetting;
});

route("POST", "/api/v1/receivables/gl-journals:backfill", ({ body }) => {
  const opening = typeof body === "object" && body !== null && Boolean((body as { openingAccount?: string }).openingAccount);
  return { receipts: 14, applications: 11, openingItems: opening ? 2 : 0, openingAlreadyQueued: 0, dunningFees: 3, interest: 2, downPayments: 1, deductions: 1, more: false };
});

route("POST", "/api/v1/receivables/remittance-advices", ({ body }) => {
  const input = (body ?? {}) as { content?: string; format?: string; createMissing?: boolean };
  const refs = [...new Set((input.content ?? "").split("\n").slice(1).map((line) => line.split(",")[0]?.trim()).filter((r): r is string => Boolean(r)))];
  const lines: AdviceLineResult[] = refs.map((paymentRef, i) =>
    i === 0
      ? { paymentRef, outcome: "ATTACHED", paymentId: "00000000-0000-0000-0000-00000000d001" }
      : input.createMissing
        ? { paymentRef, outcome: "ADVISED", paymentId: `00000000-0000-0000-0000-00000000d0${String(i + 1).padStart(2, "0")}`, detail: "no bank credit carries this reference yet; the payment is recorded as advised and books no cash until it does" }
        : { paymentRef, outcome: "UNMATCHED", detail: "no payment carries this reference" },
  );
  const created = lines.filter((l) => l.outcome === "ADVISED").length;
  return { lines, attached: lines.filter((l) => l.outcome === "ATTACHED").length, created, skipped: lines.length - lines.filter((l) => l.outcome === "ATTACHED").length };
});
