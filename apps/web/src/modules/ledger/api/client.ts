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
  negativePosting: z.boolean().optional(),
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
  reasonCode: z.string().optional(),
  reasonComment: z.string().optional(),
  traceId: z.string(),
  totals: z.object({ currency: z.string(), debit: z.string(), credit: z.string() }),
  lines: z.array(entryLineSchema),
});
export type Entry = z.infer<typeof entrySchema>;

/** Shared financial reason code (FR-11.15). */
export const reasonCodeSchema = z.object({
  code: z.string(),
  name: z.string(),
  appliesTo: z.array(z.string()),
  requiresComment: z.boolean(),
  negativePostingAllowed: z.boolean(),
  alternativePostingDateAllowed: z.boolean(),
  active: z.boolean(),
  updatedAt: z.string(),
});
export type ReasonCode = z.infer<typeof reasonCodeSchema>;

/** Body of PUT /api/v1/ledger/reason-codes/{code}. */
export interface ReasonCodeInput {
  name: string;
  appliesTo: string[];
  requiresComment: boolean;
  negativePostingAllowed: boolean;
  alternativePostingDateAllowed: boolean;
  active: boolean;
}

/** Body of POST /api/v1/ledger/journal-entries/{id}/reverse. */
export interface ReverseInput {
  reasonCode?: string;
  reasonComment?: string;
  postingDate?: string;
  description?: string;
}

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

const reasonCodeListSchema = z.object({ items: z.array(reasonCodeSchema) });

/** GET /api/v1/ledger/reason-codes (shared reason codes, ordered by code). */
export async function fetchReasonCodes(fetchImpl: FetchLike = defaultFetch): Promise<ReasonCode[]> {
  const body = await getJSON("/api/v1/ledger/reason-codes", reasonCodeListSchema, fetchImpl);
  return body.items;
}

/** GET /api/v1/ledger/reason-codes/{code}; 404 LEDGER_REASON_NOT_FOUND when missing. */
export function fetchReasonCode(code: string, fetchImpl: FetchLike = defaultFetch): Promise<ReasonCode> {
  return getJSON(`/api/v1/ledger/reason-codes/${encodeURIComponent(code)}`, reasonCodeSchema, fetchImpl);
}

/** The modules a shared reason code can apply to (FR-11.15), in PRD order. */
export const REASON_MODULES = [
  "ledger",
  "bank",
  "customer",
  "vendor",
  "fixed_asset",
  "inventory_counting",
  "tax",
  "project",
] as const;

/** Reason code format enforced by the service: ^[A-Z0-9_]{1,10}$. */
export const REASON_CODE = /^[A-Z0-9_]{1,10}$/;

/** A fresh Idempotency-Key for one user-intended reversal. */
export function newKey(): string {
  return crypto.randomUUID();
}

/** PUT /api/v1/ledger/reason-codes/{code}: creates or replaces a reason code. */
export async function putReasonCode(
  code: string,
  input: ReasonCodeInput,
  fetchImpl: FetchLike = defaultFetch,
): Promise<ReasonCode> {
  const res = await fetchImpl(`/api/v1/ledger/reason-codes/${encodeURIComponent(code)}`, {
    method: "PUT",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw await readProblem(res);
  return reasonCodeSchema.parse(await res.json());
}

const reverseResultSchema = z.object({ entries: z.array(entrySchema) });

/**
 * POST /api/v1/ledger/journal-entries/{id}/reverse. The caller supplies a fresh
 * Idempotency-Key per attempt so a retry after a timeout replays instead of double-reversing.
 */
export async function reverseEntry(
  id: string,
  input: ReverseInput,
  idempotencyKey: string,
  fetchImpl: FetchLike = defaultFetch,
): Promise<Entry[]> {
  const res = await fetchImpl(`/api/v1/ledger/journal-entries/${encodeURIComponent(id)}/reverse`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json", "Idempotency-Key": idempotencyKey },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw await readProblem(res);
  return reverseResultSchema.parse(await res.json()).entries;
}
