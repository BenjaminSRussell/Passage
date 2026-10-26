import { z } from "zod";

/** Fetch-compatible function; injectable so tests need no server. */
export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

const defaultFetch: FetchLike = (input, init) => globalThis.fetch(input, init);

export const violationSchema = z.object({
  code: z.string(),
  lineIndex: z.number().int().optional(),
  field: z.string().optional(),
  message: z.string(),
});
export type Violation = z.infer<typeof violationSchema>;

export const trialBalanceRowSchema = z.object({
  account: z.string(),
  name: z.string(),
  debit: z.string(),
  credit: z.string(),
  net: z.string(),
  lineCount: z.number(),
});

export const trialBalanceSchema = z.object({
  ledgerId: z.string(),
  currency: z.string(),
  rows: z.array(trialBalanceRowSchema),
  debit: z.string(),
  credit: z.string(),
  net: z.string(),
  balanced: z.boolean(),
});
export type TrialBalance = z.infer<typeof trialBalanceSchema>;

export const entryLineSchema = z.object({
  id: z.string(),
  lineNumber: z.number(),
  account: z.string(),
  displayName: z.string(),
  debitCredit: z.enum(["D", "C"]),
  amount: z.string(),
  text: z.string().optional(),
  reversalOfLineId: z.string().optional(),
});

export const entrySchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  voucher: z.string(),
  postingSequence: z.number(),
  postingDate: z.string(),
  periodKey: z.string(),
  description: z.string().optional(),
  reversalOfEntryId: z.string().optional(),
  traceId: z.string(),
  totals: z.object({ currency: z.string(), debit: z.string(), credit: z.string() }),
  lines: z.array(entryLineSchema),
});
export type Entry = z.infer<typeof entrySchema>;

/** Error raised for any non-2xx response. `violations` is set for 422 rule rejections. */
export class LedgerApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly violations: Violation[];

  constructor(status: number, code: string, message: string, violations: Violation[] = []) {
    super(message);
    this.name = "LedgerApiError";
    this.status = status;
    this.code = code;
    this.violations = violations;
  }
}

export interface TrialBalanceQuery {
  ledgerId: string;
  periodFrom?: string;
  periodTo?: string;
  accountFrom?: string;
  accountTo?: string;
}

async function readProblem(res: Response): Promise<LedgerApiError> {
  let body: { code?: unknown; detail?: unknown; violations?: unknown } = {};
  try {
    body = (await res.json()) as typeof body;
  } catch {
    // Non-JSON error bodies fall through to the generic error below.
  }
  const code = typeof body.code === "string" ? body.code : "COM-0500";
  const detail = typeof body.detail === "string" ? body.detail : `request failed with status ${res.status}`;
  const parsed = z.array(violationSchema).safeParse(body.violations ?? []);
  return new LedgerApiError(res.status, code, detail, parsed.success ? parsed.data : []);
}

async function getJSON<T>(url: string, schema: z.ZodType<T>, fetchImpl: FetchLike): Promise<T> {
  const res = await fetchImpl(url, { headers: { Accept: "application/json" } });
  if (!res.ok) throw await readProblem(res);
  return schema.parse(await res.json());
}

/** GET /api/v1/ledger/balances (trial balance over a period and account range). */
export function fetchTrialBalance(q: TrialBalanceQuery, fetchImpl: FetchLike = defaultFetch): Promise<TrialBalance> {
  const params = new URLSearchParams({ ledger: q.ledgerId });
  if (q.periodFrom) params.set("period_from", q.periodFrom);
  if (q.periodTo) params.set("period_to", q.periodTo);
  if (q.accountFrom) params.set("account_from", q.accountFrom);
  if (q.accountTo) params.set("account_to", q.accountTo);
  return getJSON(`/api/v1/ledger/balances?${params.toString()}`, trialBalanceSchema, fetchImpl);
}

/** GET /api/v1/ledger/journal-entries/{id} (entry with lines). */
export function fetchEntry(id: string, fetchImpl: FetchLike = defaultFetch): Promise<Entry> {
  return getJSON(`/api/v1/ledger/journal-entries/${encodeURIComponent(id)}`, entrySchema, fetchImpl);
}
