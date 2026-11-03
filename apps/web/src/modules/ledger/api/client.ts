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

/** Statistical quantity turnover of one unit (BR-11.2); units are never added together. */
export const quantityTotalSchema = z.object({
  unit: z.string(),
  debit: z.string(),
  credit: z.string(),
  net: z.string(),
});
export type QuantityTotal = z.infer<typeof quantityTotalSchema>;

export const trialBalanceRowSchema = z.object({
  account: z.string(),
  name: z.string(),
  debit: z.string(),
  credit: z.string(),
  net: z.string(),
  lineCount: z.number(),
  /** Statistical quantity totals per unit (BR-11.2); absent when no line carries a quantity. */
  quantities: z.array(quantityTotalSchema).optional(),
  /** Values of the group_by attributes for this row (FR-11.14); absent when not grouped. */
  dimensions: z.record(z.string(), z.string()).optional(),
});

export const trialBalanceSchema = z.object({
  ledgerId: z.string(),
  currency: z.string(),
  rows: z.array(trialBalanceRowSchema),
  debit: z.string(),
  credit: z.string(),
  net: z.string(),
  balanced: z.boolean(),
  /** Statistical quantity totals per unit over every row (BR-11.2). */
  quantities: z.array(quantityTotalSchema).optional(),
  /** True until a ledger created before the quantity projection is rebuilt once; its quantity totals are incomplete. */
  quantitiesPending: z.boolean().optional(),
  /** True when a dimension filter was applied, so the totals cover part of the ledger. */
  filtered: z.boolean().optional(),
  groupBy: z.array(z.string()).optional(),
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
  /** Statistical quantity and its unit (BR-11.2); absent on an ordinary line. */
  quantity: z.string().optional(),
  unit: z.string().optional(),
  reversalOfLineId: z.string().optional(),
  negativePosting: z.boolean().optional(),
  /** Derivation steps that filled dimensions on the line (FR-11.22). */
  derivedByStepIds: z.array(z.string()).optional(),
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
  /** A dimension realignment entry or its reversal (FR-11.24); reversed only through its run. */
  isRealignment: z.boolean().optional(),
  realignmentRunId: z.string().optional(),
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

/**
 * Error raised for any non-2xx response. `violations` is set for 422 rule
 * rejections, `fields` for 400 validation errors (field path to message).
 */
export class LedgerApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly violations: Violation[];
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, violations: Violation[] = [], fields: Record<string, string> = {}) {
    super(message);
    this.name = "LedgerApiError";
    this.status = status;
    this.code = code;
    this.violations = violations;
    this.fields = fields;
  }
}

export interface TrialBalanceQuery {
  ledgerId: string;
  periodFrom?: string;
  periodTo?: string;
  accountFrom?: string;
  accountTo?: string;
  /** Keep the combinations holding every attribute=value pair. */
  dimensions?: Record<string, string>;
  /** Split each account row by these dimension attributes (at most 5). */
  groupBy?: string[];
}

async function readProblem(res: Response): Promise<LedgerApiError> {
  let body: { code?: unknown; detail?: unknown; violations?: unknown; errors?: unknown } = {};
  try {
    body = (await res.json()) as typeof body;
  } catch {
    // Non-JSON error bodies fall through to the generic error below.
  }
  const code = typeof body.code === "string" ? body.code : "COM-0500";
  const detail = typeof body.detail === "string" ? body.detail : `request failed with status ${res.status}`;
  const parsed = z.array(violationSchema).safeParse(body.violations ?? []);
  const fields = z.record(z.string(), z.string()).safeParse(body.errors ?? {});
  return new LedgerApiError(res.status, code, detail, parsed.success ? parsed.data : [], fields.success ? fields.data : {});
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
  for (const [attr, value] of Object.entries(q.dimensions ?? {})) params.set(`dim.${attr}`, value);
  if (q.groupBy && q.groupBy.length > 0) params.set("group_by", q.groupBy.join(","));
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

/** The ledger header the structure and dimension screens read (dimension attribute names). */
export const ledgerSchema = z.object({
  id: z.string(),
  code: z.string(),
  name: z.string(),
  chartCode: z.string(),
  currency: z.string(),
  dimensionAttributes: z.array(z.string()),
});
export type Ledger = z.infer<typeof ledgerSchema>;

/** GET /api/v1/ledger/ledgers/{id}; 404 LEDGER_NOT_FOUND when missing or out of scope. */
export function fetchLedger(id: string, fetchImpl: FetchLike = defaultFetch): Promise<Ledger> {
  return getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(id)}`, ledgerSchema, fetchImpl);
}

/** The implicit first segment of every account structure; constraint rows may be conditioned on it. */
export const MAIN_ACCOUNT = "MAIN_ACCOUNT";

/**
 * One constraint row of an account structure (FR-11.3): the tokens `allowed` for `segment`
 * when the `under` conditions (on MAIN_ACCOUNT or earlier segments) match the line.
 */
export const constraintSchema = z.object({
  segment: z.string(),
  under: z.record(z.string(), z.string()).optional(),
  allowed: z.array(z.string()),
});
export type Constraint = z.infer<typeof constraintSchema>;

/** Account structure version (FR-11.3). Active and retired versions are immutable. */
export const structureSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  name: z.string(),
  version: z.number().int(),
  priority: z.number().int(),
  effectiveFrom: z.string(),
  status: z.enum(["draft", "active", "retired"]),
  segments: z.array(z.string()),
  accountFilter: z.array(z.string()),
  constraints: z.array(constraintSchema),
});
export type Structure = z.infer<typeof structureSchema>;

/** Body of PUT /api/v1/ledger/ledgers/{ledger}/account-structures/{id}. */
export interface StructureInput {
  name: string;
  version: number;
  priority: number;
  effectiveFrom: string;
  status: Structure["status"];
  segments: string[];
  accountFilter: string[];
  constraints: Constraint[];
}

/** Registered financial dimension value (FR-11.2). */
export const dimensionValueSchema = z.object({
  attribute: z.string(),
  value: z.string(),
  name: z.string(),
  activeFrom: z.string().optional(),
  activeTo: z.string().optional(),
  suspended: z.boolean(),
});
export type DimensionValue = z.infer<typeof dimensionValueSchema>;

/** Body of PUT /api/v1/ledger/ledgers/{ledger}/dimension-values/{attribute}/{value}. */
export interface DimensionValueInput {
  name: string;
  activeFrom: string | null;
  activeTo: string | null;
  suspended: boolean;
}

const structureListSchema = z.object({ items: z.array(structureSchema) });
const dimensionValueListSchema = z.object({ items: z.array(dimensionValueSchema) });

/** GET /api/v1/ledger/ledgers/{ledger}/account-structures: every version, by name then version. */
export async function fetchStructures(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<Structure[]> {
  const body = await getJSON(
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/account-structures`,
    structureListSchema,
    fetchImpl,
  );
  return body.items;
}

/** GET /api/v1/ledger/ledgers/{ledger}/account-structures/{id}; 404 LEDGER_STRUCTURE_NOT_FOUND when missing. */
export function fetchStructure(ledgerId: string, id: string, fetchImpl: FetchLike = defaultFetch): Promise<Structure> {
  return getJSON(
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/account-structures/${encodeURIComponent(id)}`,
    structureSchema,
    fetchImpl,
  );
}

/** PUT /api/v1/ledger/ledgers/{ledger}/account-structures/{id}: creates or replaces a version (status active activates it). */
export async function putStructure(
  ledgerId: string,
  id: string,
  input: StructureInput,
  fetchImpl: FetchLike = defaultFetch,
): Promise<Structure> {
  const res = await fetchImpl(
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/account-structures/${encodeURIComponent(id)}`,
    {
      method: "PUT",
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: JSON.stringify(input),
    },
  );
  if (!res.ok) throw await readProblem(res);
  return structureSchema.parse(await res.json());
}

/** GET /api/v1/ledger/ledgers/{ledger}/dimension-values: registered values, by attribute then value. */
export async function fetchDimensionValues(
  ledgerId: string,
  fetchImpl: FetchLike = defaultFetch,
): Promise<DimensionValue[]> {
  const body = await getJSON(
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/dimension-values`,
    dimensionValueListSchema,
    fetchImpl,
  );
  return body.items;
}

/** PUT /api/v1/ledger/ledgers/{ledger}/dimension-values/{attribute}/{value}. */
export async function putDimensionValue(
  ledgerId: string,
  attribute: string,
  value: string,
  input: DimensionValueInput,
  fetchImpl: FetchLike = defaultFetch,
): Promise<DimensionValue> {
  const path = [attribute, value].map(encodeURIComponent).join("/");
  const res = await fetchImpl(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/dimension-values/${path}`, {
    method: "PUT",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw await readProblem(res);
  return dimensionValueSchema.parse(await res.json());
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

/** Named fields a description template placeholder may bind to (FR-11.17), in PRD order. */
export const DESCRIPTION_FIELDS = [
  "voucher",
  "party_name",
  "document_date",
  "amount_txn",
  "currency_txn",
  "source_id",
  "period",
] as const;
export type DescriptionField = (typeof DESCRIPTION_FIELDS)[number];

/** Default description template (FR-11.17). A legal entity template overrides the tenant one. */
export const descriptionTemplateSchema = z.object({
  id: z.string(),
  scope: z.enum(["tenant", "legal_entity"]),
  legalEntityId: z.string().nullable(),
  sourceType: z.string(),
  language: z.string(),
  template: z.string(),
  fieldBindings: z.record(z.string(), z.string()),
  updatedAt: z.string(),
});
export type DescriptionTemplate = z.infer<typeof descriptionTemplateSchema>;

/** Body of PUT /api/v1/ledger/description-templates/{id}. */
export interface DescriptionTemplateInput {
  scope: DescriptionTemplate["scope"];
  legalEntityId?: string;
  sourceType: string;
  language: string;
  template: string;
  fieldBindings: Record<string, string>;
}

/** Journal name code format enforced by the service: ^[A-Za-z0-9_-]{1,20}$. */
export const JOURNAL_NAME_CODE = /^[A-Za-z0-9_-]{1,20}$/;

/** Setup of a kind of manual journal (FR-11.9, AX LedgerJournalName). */
export const journalNameSchema = z.object({
  code: z.string(),
  name: z.string(),
  requiresApproval: z.boolean(),
  selfApprovalAllowed: z.boolean(),
  reversalReasonRequired: z.boolean(),
  allowReconAccounts: z.boolean(),
  linesLimit: z.number().int(),
  active: z.boolean(),
  updatedAt: z.string(),
});
export type JournalName = z.infer<typeof journalNameSchema>;

/** Body of PUT /api/v1/ledger/journal-names/{code}. */
export interface JournalNameInput {
  name: string;
  requiresApproval: boolean;
  selfApprovalAllowed: boolean;
  reversalReasonRequired: boolean;
  allowReconAccounts: boolean;
  linesLimit: number;
  active: boolean;
}

const journalNameListSchema = z.object({ items: z.array(journalNameSchema) });

/** GET /api/v1/ledger/journal-names: every journal name of the tenant, by code. */
export async function fetchJournalNames(fetchImpl: FetchLike = defaultFetch): Promise<JournalName[]> {
  const body = await getJSON("/api/v1/ledger/journal-names", journalNameListSchema, fetchImpl);
  return body.items;
}

/** GET /api/v1/ledger/journal-names/{code}; 404 LEDGER_JOURNAL_NAME_NOT_FOUND when missing. */
export function fetchJournalName(code: string, fetchImpl: FetchLike = defaultFetch): Promise<JournalName> {
  return getJSON(`/api/v1/ledger/journal-names/${encodeURIComponent(code)}`, journalNameSchema, fetchImpl);
}

async function sendJSON<T>(
  method: string,
  url: string,
  schema: z.ZodType<T>,
  fetchImpl: FetchLike,
  body?: unknown,
  headers: Record<string, string> = {},
): Promise<T> {
  const res = await fetchImpl(url, {
    method,
    headers: { Accept: "application/json", "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await readProblem(res);
  return schema.parse(await res.json());
}

/** PUT /api/v1/ledger/journal-names/{code}: creates or replaces a journal name. */
export function putJournalName(code: string, input: JournalNameInput, fetchImpl: FetchLike = defaultFetch): Promise<JournalName> {
  return sendJSON("PUT", `/api/v1/ledger/journal-names/${encodeURIComponent(code)}`, journalNameSchema, fetchImpl, input);
}

/** One line of a manual journal batch as the service returns it. Amounts are decimal strings. */
export const batchLineSchema = z.object({
  lineNumber: z.number().int(),
  account: z.string(),
  dimensions: z.record(z.string(), z.string()).optional(),
  debitCredit: z.enum(["D", "C"]),
  amount: z.string(),
  currency: z.string(),
  text: z.string().optional(),
  sourceLineRef: z.string().optional(),
  quantity: z.string().optional(),
  unit: z.string().optional(),
});
export type BatchLine = z.infer<typeof batchLineSchema>;

/** BR-11.10 statuses. `posting` is a lock inside the posting transaction and is never returned. */
export const BATCH_STATUSES = ["draft", "validated", "in_approval", "approved", "rejected", "posting", "posted", "cancelled"] as const;
export type BatchStatus = (typeof BATCH_STATUSES)[number];

/** Manual journal batch (FR-11.9). `lines` is present on single-batch reads only. */
export const journalBatchSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  legalEntityId: z.string(),
  journalName: z.string(),
  description: z.string(),
  postingDate: z.string(),
  documentDate: z.string().optional(),
  currency: z.string(),
  status: z.enum(BATCH_STATUSES),
  version: z.number().int(),
  requiresApproval: z.boolean(),
  selfApprovalAllowed: z.boolean(),
  submittedBy: z.string().optional(),
  decidedBy: z.string().optional(),
  postedBy: z.string().optional(),
  postedAt: z.string().optional(),
  postedEntryId: z.string().optional(),
  lastError: z.string().optional(),
  totalDebit: z.string(),
  totalCredit: z.string(),
  lineCount: z.number().int(),
  createdBy: z.string(),
  createdAt: z.string(),
  updatedAt: z.string(),
  lines: z.array(batchLineSchema).optional(),
});
export type JournalBatch = z.infer<typeof journalBatchSchema>;

/** Wire form of one line sent to the service (amount as an object, as in the posting API). */
export interface BatchLineInput {
  account: string;
  dimensions?: Record<string, string>;
  debitCredit: "D" | "C";
  amount: { currency: string; value: string };
  text?: string;
  /** Statistical quantity and unit (BR-11.2), sent together. */
  quantity?: string;
  unit?: string;
}

/** Body of POST /api/v1/ledger/journal-batches. */
export interface JournalBatchInput {
  ledgerId: string;
  journalName: string;
  description: string;
  postingDate: string;
  documentDate?: string;
  lines: BatchLineInput[];
}

/** Body of PATCH /api/v1/ledger/journal-batches/{id}: `lines` replaces every line; "" clears documentDate. */
export interface JournalBatchPatch {
  description?: string;
  postingDate?: string;
  documentDate?: string;
  lines?: BatchLineInput[];
}

const journalBatchListSchema = z.object({ items: z.array(journalBatchSchema) });
const postBatchResultSchema = z.object({
  entries: z.array(z.object({ id: z.string(), voucher: z.string() })),
  idempotency: z.object({ key: z.string(), replayed: z.boolean() }),
  notices: z.array(z.object({ code: z.string(), message: z.string() })).optional(),
});
export type PostBatchResult = z.infer<typeof postBatchResultSchema>;

/** The entity tag of a batch version, for If-Match. */
function ifMatch(version: number): Record<string, string> {
  return { "If-Match": `"${version}"` };
}

/** GET /api/v1/ledger/journal-batches: the newest 200, optionally by status or journal name. */
export async function fetchJournalBatches(
  filter: { status?: string; journalName?: string } = {},
  fetchImpl: FetchLike = defaultFetch,
): Promise<JournalBatch[]> {
  const params = new URLSearchParams();
  if (filter.status) params.set("status", filter.status);
  if (filter.journalName) params.set("journalName", filter.journalName);
  const query = params.toString();
  const body = await getJSON(`/api/v1/ledger/journal-batches${query ? `?${query}` : ""}`, journalBatchListSchema, fetchImpl);
  return body.items;
}

/** GET /api/v1/ledger/journal-batches/{id}: header, totals and lines. */
export function fetchJournalBatch(id: string, fetchImpl: FetchLike = defaultFetch): Promise<JournalBatch> {
  return getJSON(`/api/v1/ledger/journal-batches/${encodeURIComponent(id)}`, journalBatchSchema, fetchImpl);
}

/** POST /api/v1/ledger/journal-batches: creates a draft batch. */
export function createJournalBatch(input: JournalBatchInput, fetchImpl: FetchLike = defaultFetch): Promise<JournalBatch> {
  return sendJSON("POST", "/api/v1/ledger/journal-batches", journalBatchSchema, fetchImpl, input);
}

/** PATCH /api/v1/ledger/journal-batches/{id} at the version the user edited; any edit returns the batch to draft. */
export function patchJournalBatch(
  id: string,
  version: number,
  input: JournalBatchPatch,
  fetchImpl: FetchLike = defaultFetch,
): Promise<JournalBatch> {
  return sendJSON("PATCH", `/api/v1/ledger/journal-batches/${encodeURIComponent(id)}`, journalBatchSchema, fetchImpl, input, ifMatch(version));
}

/** The lifecycle actions that answer with the batch. */
export type BatchAction = "submit" | "approve" | "reject" | "cancel";

/** POST /api/v1/ledger/journal-batches/{id}/{action} at the version the user saw. */
export function batchAction(id: string, action: BatchAction, version: number, fetchImpl: FetchLike = defaultFetch): Promise<JournalBatch> {
  return sendJSON("POST", `/api/v1/ledger/journal-batches/${encodeURIComponent(id)}/${action}`, journalBatchSchema, fetchImpl, undefined, ifMatch(version));
}

/**
 * POST /api/v1/ledger/journal-batches/{id}/post. Reuse the key when retrying the
 * same post, so a timeout replays instead of failing on a posted batch.
 */
export function postJournalBatch(
  id: string,
  version: number,
  idempotencyKey: string,
  fetchImpl: FetchLike = defaultFetch,
): Promise<PostBatchResult> {
  return sendJSON("POST", `/api/v1/ledger/journal-batches/${encodeURIComponent(id)}/post`, postBatchResultSchema, fetchImpl, undefined, {
    ...ifMatch(version),
    "Idempotency-Key": idempotencyKey,
  });
}

const descriptionTemplateListSchema = z.object({ items: z.array(descriptionTemplateSchema) });

/** GET /api/v1/ledger/description-templates: tenant templates and those of legal entities in scope. */
export async function fetchDescriptionTemplates(fetchImpl: FetchLike = defaultFetch): Promise<DescriptionTemplate[]> {
  const body = await getJSON("/api/v1/ledger/description-templates", descriptionTemplateListSchema, fetchImpl);
  return body.items;
}

/** GET /api/v1/ledger/description-templates/{id}; 404 LEDGER_DESCRIPTION_TEMPLATE_NOT_FOUND when missing. */
export function fetchDescriptionTemplate(id: string, fetchImpl: FetchLike = defaultFetch): Promise<DescriptionTemplate> {
  return getJSON(`/api/v1/ledger/description-templates/${encodeURIComponent(id)}`, descriptionTemplateSchema, fetchImpl);
}

/** PUT /api/v1/ledger/description-templates/{id}: creates or replaces a template. */
export async function putDescriptionTemplate(
  id: string,
  input: DescriptionTemplateInput,
  fetchImpl: FetchLike = defaultFetch,
): Promise<DescriptionTemplate> {
  const res = await fetchImpl(`/api/v1/ledger/description-templates/${encodeURIComponent(id)}`, {
    method: "PUT",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  if (!res.ok) throw await readProblem(res);
  return descriptionTemplateSchema.parse(await res.json());
}

/** One posted journal line with its entry context (FR-11.14 ledger transactions). */
export const journalLineSchema = z.object({
  id: z.string(),
  entryId: z.string(),
  voucher: z.string(),
  postingSequence: z.number(),
  postingDate: z.string(),
  lineNumber: z.number(),
  account: z.string(),
  displayName: z.string(),
  dimensions: z.record(z.string(), z.string()).optional(),
  debitCredit: z.enum(["D", "C"]),
  amount: z.string(),
  currency: z.string(),
  negativePosting: z.boolean(),
  text: z.string().optional(),
  description: z.string().optional(),
  sourceSystem: z.string(),
  sourceType: z.string(),
  sourceId: z.string(),
  sourceUri: z.string().optional(),
  sourceLineRef: z.string().optional(),
  quantity: z.string().optional(),
  unit: z.string().optional(),
  reversalOfEntryId: z.string().optional(),
  isRealignment: z.boolean().optional(),
  traceId: z.string(),
});
export type JournalLine = z.infer<typeof journalLineSchema>;

/** Filters of GET /api/v1/ledger/journal-lines. Dimensions become dim.{ATTRIBUTE}=value. */
export interface JournalLineQuery {
  ledgerId: string;
  accountFrom?: string;
  accountTo?: string;
  dateFrom?: string;
  dateTo?: string;
  voucher?: string;
  sourceSystem?: string;
  dimensions?: Record<string, string>;
  cursor?: string;
  limit?: number;
}

const journalLinePageSchema = z.object({
  items: z.array(journalLineSchema),
  nextCursor: z.string().optional(),
});
export type JournalLinePage = z.infer<typeof journalLinePageSchema>;

/** GET /api/v1/ledger/journal-lines: one keyset page in posting order. */
export function fetchJournalLines(q: JournalLineQuery, fetchImpl: FetchLike = defaultFetch): Promise<JournalLinePage> {
  const params = new URLSearchParams({ ledger: q.ledgerId });
  if (q.accountFrom) params.set("account_from", q.accountFrom);
  if (q.accountTo) params.set("account_to", q.accountTo);
  if (q.dateFrom) params.set("date_from", q.dateFrom);
  if (q.dateTo) params.set("date_to", q.dateTo);
  if (q.voucher) params.set("voucher", q.voucher);
  if (q.sourceSystem) params.set("source_system", q.sourceSystem);
  for (const [attr, value] of Object.entries(q.dimensions ?? {})) params.set(`dim.${attr}`, value);
  if (q.cursor) params.set("cursor", q.cursor);
  if (q.limit) params.set("limit", String(q.limit));
  return getJSON(`/api/v1/ledger/journal-lines?${params.toString()}`, journalLinePageSchema, fetchImpl);
}

/** Recurring journal frequencies (FR-11.10). */
export const RECURRING_FREQUENCIES = ["monthly", "quarterly", "yearly"] as const;
export type RecurringFrequency = (typeof RECURRING_FREQUENCIES)[number];

/** A template line: a fixed amount, or a percentage of the base amount. */
export const recurringLineSchema = z.object({
  lineNumber: z.number().int(),
  account: z.string(),
  dimensions: z.record(z.string(), z.string()).optional(),
  debitCredit: z.enum(["D", "C"]),
  amountKind: z.enum(["fixed", "percent"]),
  amount: z.string(),
  currency: z.string(),
  text: z.string().optional(),
  /** Fixed statistical quantity (BR-11.2) copied to every generated line, with its unit. */
  quantity: z.string().optional(),
  unit: z.string().optional(),
});
export type RecurringLine = z.infer<typeof recurringLineSchema>;

/** Recurring journal (FR-11.10). `lines` is present on single reads only. */
export const recurringJournalSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  legalEntityId: z.string(),
  journalName: z.string(),
  name: z.string(),
  description: z.string(),
  frequency: z.enum(RECURRING_FREQUENCIES),
  dayOfMonth: z.number().int(),
  startDate: z.string(),
  endDate: z.string().optional(),
  baseAmount: z.string().optional(),
  status: z.enum(["active", "paused"]),
  nextRunDate: z.string().optional(),
  lastRunDate: z.string().optional(),
  version: z.number().int(),
  lineCount: z.number().int(),
  createdBy: z.string(),
  createdAt: z.string(),
  updatedAt: z.string(),
  lines: z.array(recurringLineSchema).optional(),
});
export type RecurringJournal = z.infer<typeof recurringJournalSchema>;

export const recurringRunSchema = z.object({
  id: z.string(),
  recurringId: z.string(),
  scheduledDate: z.string(),
  batchId: z.string(),
  generatedBy: z.string(),
  generatedAt: z.string(),
});
export type RecurringRun = z.infer<typeof recurringRunSchema>;

/** Body of PUT /api/v1/ledger/recurring-journals/{id}; amounts are decimal strings, percent lines carry the percentage. */
export interface RecurringJournalInput {
  ledgerId: string;
  journalName: string;
  name: string;
  description: string;
  frequency: RecurringFrequency;
  dayOfMonth: number;
  startDate: string;
  endDate?: string;
  baseAmount?: string;
  lines: {
    account: string;
    dimensions?: Record<string, string>;
    debitCredit: "D" | "C";
    amountKind: "fixed" | "percent";
    amount: { currency: string; value: string };
    text?: string;
    quantity?: string;
    unit?: string;
  }[];
}

const recurringListSchema = z.object({ items: z.array(recurringJournalSchema) });
const recurringRunListSchema = z.object({ items: z.array(recurringRunSchema) });

/** GET /api/v1/ledger/recurring-journals, optionally only active or paused ones. */
export async function fetchRecurringJournals(status = "", fetchImpl: FetchLike = defaultFetch): Promise<RecurringJournal[]> {
  const body = await getJSON(`/api/v1/ledger/recurring-journals${status ? `?status=${encodeURIComponent(status)}` : ""}`, recurringListSchema, fetchImpl);
  return body.items;
}

/** GET /api/v1/ledger/recurring-journals/{id} with its lines. */
export function fetchRecurringJournal(id: string, fetchImpl: FetchLike = defaultFetch): Promise<RecurringJournal> {
  return getJSON(`/api/v1/ledger/recurring-journals/${encodeURIComponent(id)}`, recurringJournalSchema, fetchImpl);
}

/** PUT /api/v1/ledger/recurring-journals/{id}: creates, or replaces at `version` (If-Match) when given. */
export function putRecurringJournal(
  id: string,
  input: RecurringJournalInput,
  version?: number,
  fetchImpl: FetchLike = defaultFetch,
): Promise<RecurringJournal> {
  return sendJSON("PUT", `/api/v1/ledger/recurring-journals/${encodeURIComponent(id)}`, recurringJournalSchema, fetchImpl, input, version ? ifMatch(version) : {});
}

/** POST .../pause or .../resume at the version the user saw. */
export function recurringStatusAction(id: string, action: "pause" | "resume", version: number, fetchImpl: FetchLike = defaultFetch): Promise<RecurringJournal> {
  return sendJSON("POST", `/api/v1/ledger/recurring-journals/${encodeURIComponent(id)}/${action}`, recurringJournalSchema, fetchImpl, undefined, ifMatch(version));
}

/** POST .../generate: the draft batch of a scheduled date of the schedule (required); a date with a run replays it. */
export function generateRecurringJournal(
  id: string,
  input: { scheduledDate: string; baseAmount?: string },
  fetchImpl: FetchLike = defaultFetch,
): Promise<RecurringRun> {
  return sendJSON("POST", `/api/v1/ledger/recurring-journals/${encodeURIComponent(id)}/generate`, recurringRunSchema, fetchImpl, input);
}

/** GET .../runs: generated batches, newest scheduled date first. */
export async function fetchRecurringRuns(id: string, fetchImpl: FetchLike = defaultFetch): Promise<RecurringRun[]> {
  const body = await getJSON(`/api/v1/ledger/recurring-journals/${encodeURIComponent(id)}/runs`, recurringRunListSchema, fetchImpl);
  return body.items;
}

// --- open items and clearing (FR-11.19, FR-11.20) --------------------------------

export const OPEN_ITEM_STATUSES = ["open", "cleared", "reset_open", "voided"] as const;
export type OpenItemStatus = (typeof OPEN_ITEM_STATUSES)[number];

/** One open item; amount is absolute, debitCredit says the side. */
export const openItemSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  account: z.string(),
  accountName: z.string(),
  dimensions: z.record(z.string(), z.string()),
  sourceType: z.enum(["gl_line", "residual"]),
  amount: z.string(),
  debitCredit: z.enum(["D", "C"]),
  currency: z.string(),
  assignment: z.string().optional(),
  postingDate: z.string(),
  status: z.enum(OPEN_ITEM_STATUSES),
  clearingDocumentId: z.string().optional(),
  residualOfItemId: z.string().optional(),
  entryId: z.string().optional(),
  voucher: z.string().optional(),
  lineNumber: z.number().optional(),
});
export type OpenItem = z.infer<typeof openItemSchema>;

const openItemPageSchema = z.object({ items: z.array(openItemSchema), nextCursor: z.string().optional() });
export type OpenItemPage = z.infer<typeof openItemPageSchema>;

export interface OpenItemQuery {
  ledgerId: string;
  account?: string;
  status?: string;
  assignment?: string;
  cursor?: string;
  limit?: number;
}

/** GET /api/v1/ledger/open-items, one keyset page. */
export function fetchOpenItems(q: OpenItemQuery, fetchImpl: FetchLike = defaultFetch): Promise<OpenItemPage> {
  const params = new URLSearchParams({ ledger: q.ledgerId });
  if (q.account) params.set("account", q.account);
  if (q.status) params.set("status", q.status);
  if (q.assignment) params.set("assignment", q.assignment);
  if (q.cursor) params.set("cursor", q.cursor);
  if (q.limit) params.set("limit", String(q.limit));
  return getJSON(`/api/v1/ledger/open-items?${params.toString()}`, openItemPageSchema, fetchImpl);
}

export const CLEARING_OUTCOMES = ["balanced", "difference", "residual", "unbalanced"] as const;

export const clearingPreviewSchema = z.object({
  ledgerId: z.string(),
  account: z.string(),
  currency: z.string(),
  itemCount: z.number(),
  debit: z.string(),
  credit: z.string(),
  net: z.string(),
  toleranceGroup: z.string().optional(),
  outcome: z.enum(CLEARING_OUTCOMES),
  differenceAmount: z.string(),
  residualAmount: z.string(),
});
export type ClearingPreview = z.infer<typeof clearingPreviewSchema>;

export const clearingDocumentSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  account: z.string(),
  clearingDate: z.string(),
  mode: z.string(),
  itemIds: z.array(z.string()),
  toleranceGroup: z.string().optional(),
  differenceAmount: z.string(),
  differenceEntryId: z.string().optional(),
  residualItemId: z.string().optional(),
  status: z.enum(["cleared", "reset"]),
  resetReasonCode: z.string().optional(),
  resetComment: z.string().optional(),
  resetBy: z.string().optional(),
  resetAt: z.string().optional(),
  createdBy: z.string(),
  createdAt: z.string(),
  items: z.array(openItemSchema).optional(),
});
export type ClearingDocument = z.infer<typeof clearingDocumentSchema>;

export interface ClearingInput {
  ledgerId: string;
  itemIds: string[];
  clearingDate: string;
  toleranceGroup?: string;
  residualItem?: boolean;
}

/** POST /api/v1/ledger/clearings:simulate: residual and outcome, nothing written. */
export function simulateClearing(input: ClearingInput, fetchImpl: FetchLike = defaultFetch): Promise<ClearingPreview> {
  return sendJSON("POST", "/api/v1/ledger/clearings:simulate", clearingPreviewSchema, fetchImpl, input);
}

/** POST /api/v1/ledger/clearings. One Idempotency-Key per user-intended clearing, so a retry replays. */
export function createClearing(input: ClearingInput, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<ClearingDocument> {
  return sendJSON("POST", "/api/v1/ledger/clearings", clearingDocumentSchema, fetchImpl, input, { "Idempotency-Key": idempotencyKey });
}

const clearingListSchema = z.object({ items: z.array(clearingDocumentSchema) });

/** GET /api/v1/ledger/clearings, newest first, optionally only cleared or reset ones. */
export async function fetchClearings(ledgerId: string, status = "", fetchImpl: FetchLike = defaultFetch): Promise<ClearingDocument[]> {
  const params = new URLSearchParams({ ledger: ledgerId });
  if (status) params.set("status", status);
  return (await getJSON(`/api/v1/ledger/clearings?${params.toString()}`, clearingListSchema, fetchImpl)).items;
}

/** GET /api/v1/ledger/clearings/{id} with its items. */
export function fetchClearing(id: string, fetchImpl: FetchLike = defaultFetch): Promise<ClearingDocument> {
  return getJSON(`/api/v1/ledger/clearings/${encodeURIComponent(id)}`, clearingDocumentSchema, fetchImpl);
}

export interface ResetClearingInput {
  reasonCode: string;
  comment?: string;
}

/** POST /api/v1/ledger/clearings/{id}/reset: the whole document, with its own Idempotency-Key. */
export function resetClearing(id: string, input: ResetClearingInput, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<ClearingDocument> {
  return sendJSON("POST", `/api/v1/ledger/clearings/${encodeURIComponent(id)}/reset`, clearingDocumentSchema, fetchImpl, input, {
    "Idempotency-Key": idempotencyKey,
  });
}

export const TOLERANCE_CODE = /^[A-Z0-9_]{1,10}$/;

export const toleranceGroupSchema = z.object({
  ledgerId: z.string(),
  code: z.string(),
  name: z.string(),
  debitLimit: z.string(),
  creditLimit: z.string(),
  percentLimit: z.string(),
  differenceAccount: z.string(),
  active: z.boolean(),
  version: z.number(),
  createdAt: z.string(),
  updatedAt: z.string(),
});
export type ToleranceGroup = z.infer<typeof toleranceGroupSchema>;

export interface ToleranceGroupInput {
  name: string;
  debitLimit: string;
  creditLimit: string;
  percentLimit: string;
  differenceAccount: string;
  active: boolean;
}

const toleranceListSchema = z.object({ items: z.array(toleranceGroupSchema) });

/** GET /api/v1/ledger/ledgers/{ledger}/tolerance-groups */
export async function fetchToleranceGroups(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<ToleranceGroup[]> {
  return (await getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/tolerance-groups`, toleranceListSchema, fetchImpl)).items;
}

/** GET /api/v1/ledger/ledgers/{ledger}/tolerance-groups/{code} */
export function fetchToleranceGroup(ledgerId: string, code: string, fetchImpl: FetchLike = defaultFetch): Promise<ToleranceGroup> {
  return getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/tolerance-groups/${encodeURIComponent(code)}`, toleranceGroupSchema, fetchImpl);
}

/** PUT .../tolerance-groups/{code}: creates, or replaces at `version` (If-Match) when given. */
export function putToleranceGroup(
  ledgerId: string,
  code: string,
  input: ToleranceGroupInput,
  version?: number,
  fetchImpl: FetchLike = defaultFetch,
): Promise<ToleranceGroup> {
  return sendJSON(
    "PUT",
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/tolerance-groups/${encodeURIComponent(code)}`,
    toleranceGroupSchema,
    fetchImpl,
    input,
    version ? ifMatch(version) : {},
  );
}

export const openItemAccountSchema = z.object({
  ledgerId: z.string(),
  account: z.string(),
  name: z.string(),
  enabled: z.boolean(),
  activationRunId: z.string().optional(),
  openItems: z.number(),
  changedBy: z.string(),
  changedAt: z.string(),
});
export type OpenItemAccount = z.infer<typeof openItemAccountSchema>;

const openItemAccountListSchema = z.object({ items: z.array(openItemAccountSchema) });

/** GET /api/v1/ledger/ledgers/{ledger}/open-item-accounts */
export async function fetchOpenItemAccounts(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<OpenItemAccount[]> {
  return (await getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/open-item-accounts`, openItemAccountListSchema, fetchImpl)).items;
}

/** PUT .../open-item-accounts/{account}: switches open-item management on or off. */
export function putOpenItemAccount(ledgerId: string, account: string, enabled: boolean, fetchImpl: FetchLike = defaultFetch): Promise<OpenItemAccount> {
  return sendJSON(
    "PUT",
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/open-item-accounts/${encodeURIComponent(account)}`,
    openItemAccountSchema,
    fetchImpl,
    { enabled },
  );
}

export const activationRunSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  account: z.string(),
  accountName: z.string(),
  activationDate: z.string(),
  itemsCreated: z.number(),
  lineItemsCreated: z.number(),
  totalDebit: z.string(),
  totalCredit: z.string(),
  createdBy: z.string(),
  createdAt: z.string(),
});
export type ActivationRun = z.infer<typeof activationRunSchema>;

/** The service gives activation runs to PRD "Administer ledger", held as this duty. */
export const DUTY_LEDGER_ADMIN = "ledger.balance.rebuild";

const activationRunListSchema = z.object({ items: z.array(activationRunSchema) });

/** GET /api/v1/ledger/ledgers/{ledger}/open-item-activations */
export async function fetchActivationRuns(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<ActivationRun[]> {
  return (await getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/open-item-activations`, activationRunListSchema, fetchImpl)).items;
}

/** POST .../open-item-accounts/{account}:activate. One Idempotency-Key per user-intended run, so a retry replays. */
export function activateOpenItemAccount(
  ledgerId: string,
  account: string,
  activationDate: string,
  idempotencyKey: string,
  fetchImpl: FetchLike = defaultFetch,
): Promise<ActivationRun> {
  return sendJSON(
    "POST",
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/open-item-accounts/${encodeURIComponent(account)}:activate`,
    activationRunSchema,
    fetchImpl,
    { activationDate },
    { "Idempotency-Key": idempotencyKey },
  );
}

/** Criteria a rule may group by (FR-11.20); dim.{ATTRIBUTE} is accepted as well. */
export const AUTO_CRITERION = /^(assignment|source_line_ref|document_id|amount_txn|posting_date|dim\.[A-Z][A-Z0-9_]{0,19})$/;
export const MAX_AUTO_CRITERIA = 5;

export const autoClearingRuleSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  name: z.string(),
  accountFrom: z.string(),
  accountTo: z.string(),
  criteria: z.array(z.string()),
  allowTolerance: z.boolean(),
  toleranceGroup: z.string().optional(),
  sequence: z.number(),
  active: z.boolean(),
  version: z.number(),
  createdBy: z.string(),
  createdAt: z.string(),
  updatedAt: z.string(),
});
export type AutoClearingRule = z.infer<typeof autoClearingRuleSchema>;

export interface AutoClearingRuleInput {
  name: string;
  accountFrom: string;
  accountTo: string;
  criteria: string[];
  allowTolerance: boolean;
  toleranceGroup?: string;
  sequence: number;
  active: boolean;
}

const autoRuleListSchema = z.object({ items: z.array(autoClearingRuleSchema) });

/** GET /api/v1/ledger/ledgers/{ledger}/auto-clearing-rules, in run order. */
export async function fetchAutoClearingRules(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<AutoClearingRule[]> {
  return (await getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/auto-clearing-rules`, autoRuleListSchema, fetchImpl)).items;
}

/** GET .../auto-clearing-rules/{id} */
export function fetchAutoClearingRule(ledgerId: string, id: string, fetchImpl: FetchLike = defaultFetch): Promise<AutoClearingRule> {
  return getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/auto-clearing-rules/${encodeURIComponent(id)}`, autoClearingRuleSchema, fetchImpl);
}

/** PUT .../auto-clearing-rules/{id}: creates under a client-chosen id, or replaces at `version` (If-Match) when given. */
export function putAutoClearingRule(
  ledgerId: string,
  id: string,
  input: AutoClearingRuleInput,
  version?: number,
  fetchImpl: FetchLike = defaultFetch,
): Promise<AutoClearingRule> {
  return sendJSON(
    "PUT",
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/auto-clearing-rules/${encodeURIComponent(id)}`,
    autoClearingRuleSchema,
    fetchImpl,
    input,
    version ? ifMatch(version) : {},
  );
}

export const autoClearingGroupSchema = z.object({
  no: z.number(),
  ruleId: z.string(),
  account: z.string(),
  itemCount: z.number(),
  net: z.string(),
  status: z.enum(["proposed", "cleared", "skipped"]),
  reasonCode: z.string().optional(),
  reason: z.string().optional(),
  clearingDocumentId: z.string().optional(),
  itemIds: z.array(z.string()).optional(),
});
export type AutoClearingGroup = z.infer<typeof autoClearingGroupSchema>;

export const autoClearingRunSchema = z.object({
  id: z.string().optional(),
  ledgerId: z.string(),
  ruleId: z.string().optional(),
  mode: z.enum(["simulate", "clear"]),
  clearingDate: z.string(),
  groupsFound: z.number(),
  groupsCleared: z.number(),
  groupsSkipped: z.number(),
  itemsCleared: z.number(),
  unmatchedItems: z.number(),
  truncated: z.boolean(),
  createdBy: z.string().optional(),
  createdAt: z.string().optional(),
  groups: z.array(autoClearingGroupSchema).optional(),
});
export type AutoClearingRun = z.infer<typeof autoClearingRunSchema>;

export interface AutoClearingRunInput {
  ledgerId: string;
  ruleId?: string;
  mode: "simulate" | "clear";
  clearingDate?: string;
}

/** POST /api/v1/ledger/auto-clearing:run. Simulation needs no key; a clear carries one key per user-intended run. */
export function runAutoClearing(input: AutoClearingRunInput, idempotencyKey?: string, fetchImpl: FetchLike = defaultFetch): Promise<AutoClearingRun> {
  return sendJSON("POST", "/api/v1/ledger/auto-clearing:run", autoClearingRunSchema, fetchImpl, input, idempotencyKey ? { "Idempotency-Key": idempotencyKey } : {});
}

const autoRunListSchema = z.object({ items: z.array(autoClearingRunSchema) });

/** GET /api/v1/ledger/auto-clearing-runs, newest first. */
export async function fetchAutoClearingRuns(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<AutoClearingRun[]> {
  return (await getJSON(`/api/v1/ledger/auto-clearing-runs?ledger=${encodeURIComponent(ledgerId)}`, autoRunListSchema, fetchImpl)).items;
}

/** GET /api/v1/ledger/auto-clearing-runs/{id} with its group log. */
export function fetchAutoClearingRun(id: string, fetchImpl: FetchLike = defaultFetch): Promise<AutoClearingRun> {
  return getJSON(`/api/v1/ledger/auto-clearing-runs/${encodeURIComponent(id)}`, autoClearingRunSchema, fetchImpl);
}

/** Lookup table row (FR-11.22): a source tuple of exact values or `*`, the target value and its validity. */
export const lookupRowSchema = z.object({
  source: z.array(z.string()),
  target: z.string(),
  validFrom: z.string(),
  validTo: z.string().optional(),
});
export type LookupRow = z.infer<typeof lookupRowSchema>;

export const lookupTableSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  name: z.string(),
  sourceFields: z.array(z.string()),
  target: z.string(),
  rowCount: z.number(),
  version: z.number(),
  rows: z.array(lookupRowSchema).optional(),
  createdAt: z.string().optional(),
  updatedAt: z.string().optional(),
});
export type LookupTable = z.infer<typeof lookupTableSchema>;

export interface LookupTableInput {
  name: string;
  sourceFields: string[];
  target: string;
  rows: LookupRow[];
}

const lookupListSchema = z.object({ items: z.array(lookupTableSchema) });

/** GET /api/v1/ledger/ledgers/{ledger}/lookup-tables (without rows). */
export async function fetchLookupTables(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<LookupTable[]> {
  return (await getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/lookup-tables`, lookupListSchema, fetchImpl)).items;
}

/** GET .../lookup-tables/{id} (with rows). */
export function fetchLookupTable(ledgerId: string, id: string, fetchImpl: FetchLike = defaultFetch): Promise<LookupTable> {
  return getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/lookup-tables/${encodeURIComponent(id)}`, lookupTableSchema, fetchImpl);
}

/** PUT .../lookup-tables/{id}: creates, or replaces the table and all rows at `version` (If-Match) when given. */
export function putLookupTable(ledgerId: string, id: string, input: LookupTableInput, version?: number, fetchImpl: FetchLike = defaultFetch): Promise<LookupTable> {
  return sendJSON(
    "PUT",
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/lookup-tables/${encodeURIComponent(id)}`,
    lookupTableSchema,
    fetchImpl,
    input,
    version ? ifMatch(version) : {},
  );
}

/** Document splitting rule (FR-11.23): the dimensions lines inherit for one source type, and the zero-balance clearing account. */
export const splittingRuleSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  sourceType: z.string(),
  splittingDimensions: z.array(z.string()),
  zeroBalanceClearingAccount: z.string().optional(),
  defaultSplittingValue: z.record(z.string(), z.string()),
  active: z.boolean(),
  version: z.number(),
  createdAt: z.string().optional(),
  updatedAt: z.string().optional(),
});
export type SplittingRule = z.infer<typeof splittingRuleSchema>;

export interface SplittingRuleInput {
  sourceType: string;
  splittingDimensions: string[];
  zeroBalanceClearingAccount?: string;
  defaultSplittingValue: Record<string, string>;
  active: boolean;
}

const splittingListSchema = z.object({ items: z.array(splittingRuleSchema) });

/** GET /api/v1/ledger/ledgers/{ledger}/splitting-rules. */
export async function fetchSplittingRules(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<SplittingRule[]> {
  return (await getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/splitting-rules`, splittingListSchema, fetchImpl)).items;
}

/** GET .../splitting-rules/{id}. */
export function fetchSplittingRule(ledgerId: string, id: string, fetchImpl: FetchLike = defaultFetch): Promise<SplittingRule> {
  return getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/splitting-rules/${encodeURIComponent(id)}`, splittingRuleSchema, fetchImpl);
}

/** PUT .../splitting-rules/{id}: creates, or replaces the rule at `version` (If-Match) when given. */
export function putSplittingRule(ledgerId: string, id: string, input: SplittingRuleInput, version?: number, fetchImpl: FetchLike = defaultFetch): Promise<SplittingRule> {
  return sendJSON(
    "PUT",
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/splitting-rules/${encodeURIComponent(id)}`,
    splittingRuleSchema,
    fetchImpl,
    input,
    version ? ifMatch(version) : {},
  );
}

export const DERIVATION_KINDS = ["table_lookup", "move", "clear", "constant"] as const;
export type DerivationKind = (typeof DERIVATION_KINDS)[number];
export const CONDITION_OPS = ["eq", "ne", "in", "blank", "notblank"] as const;

export const derivationStepSchema = z.object({
  id: z.string().optional(),
  sequence: z.number(),
  kind: z.enum(DERIVATION_KINDS),
  condition: z.object({ field: z.string(), op: z.enum(CONDITION_OPS), values: z.array(z.string()).optional() }).optional(),
  sourceFields: z.array(z.string()),
  target: z.string(),
  lookupTableId: z.string().optional(),
  value: z.string().optional(),
  overwrite: z.boolean(),
});
export type DerivationStep = z.infer<typeof derivationStepSchema>;

export const derivationStrategySchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  version: z.number(),
  lockVersion: z.number(),
  name: z.string(),
  status: z.enum(["draft", "active", "retired"]),
  steps: z.array(derivationStepSchema),
  activatedBy: z.string().optional(),
  activatedAt: z.string().optional(),
  createdAt: z.string().optional(),
  updatedAt: z.string().optional(),
});
export type DerivationStrategy = z.infer<typeof derivationStrategySchema>;

export interface DerivationStrategyInput {
  name: string;
  steps: DerivationStep[];
}

const strategyListSchema = z.object({ items: z.array(derivationStrategySchema) });
const strategyUrl = (ledgerId: string, version: number) =>
  `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/derivation-strategies/${version}`;

/** GET /api/v1/ledger/ledgers/{ledger}/derivation-strategies (newest version first). */
export async function fetchDerivationStrategies(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<DerivationStrategy[]> {
  return (await getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/derivation-strategies`, strategyListSchema, fetchImpl)).items;
}

/** GET .../derivation-strategies/{version}. */
export function fetchDerivationStrategy(ledgerId: string, version: number, fetchImpl: FetchLike = defaultFetch): Promise<DerivationStrategy> {
  return getJSON(strategyUrl(ledgerId, version), derivationStrategySchema, fetchImpl);
}

/** PUT .../derivation-strategies/{version}: the next draft, or a replacement of a draft at `lockVersion` (If-Match). */
export function putDerivationStrategy(
  ledgerId: string,
  version: number,
  input: DerivationStrategyInput,
  lockVersion?: number,
  fetchImpl: FetchLike = defaultFetch,
): Promise<DerivationStrategy> {
  return sendJSON("PUT", strategyUrl(ledgerId, version), derivationStrategySchema, fetchImpl, input, lockVersion ? ifMatch(lockVersion) : {});
}

/** POST .../derivation-strategies/{version}/activate. */
export function activateDerivationStrategy(ledgerId: string, version: number, lockVersion?: number, fetchImpl: FetchLike = defaultFetch): Promise<DerivationStrategy> {
  return sendJSON("POST", `${strategyUrl(ledgerId, version)}/activate`, derivationStrategySchema, fetchImpl, undefined, lockVersion ? ifMatch(lockVersion) : {});
}

export const RULE_OPS = ["eq", "ne", "in", "gt", "gte", "lt", "lte", "between", "blank", "notblank"] as const;
export type RuleOp = (typeof RULE_OPS)[number];

/** One condition of a validation rule (FR-11.21): a field, an operator and its values. */
export const ruleConditionSchema = z.object({
  field: z.string(),
  op: z.enum(RULE_OPS),
  values: z.array(z.string()).optional(),
});
export type RuleCondition = z.infer<typeof ruleConditionSchema>;

export const validationRuleSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  sequence: z.number(),
  name: z.string(),
  level: z.enum(["line", "header"]),
  severity: z.enum(["error", "warning"]),
  prerequisite: z.array(ruleConditionSchema),
  checks: z.array(ruleConditionSchema),
  message: z.string(),
  active: z.boolean(),
  lockVersion: z.number(),
  createdAt: z.string().optional(),
  updatedAt: z.string().optional(),
});
export type ValidationRule = z.infer<typeof validationRuleSchema>;

export type ValidationRuleInput = Pick<ValidationRule, "sequence" | "name" | "level" | "severity" | "prerequisite" | "checks" | "message" | "active">;

const validationRuleListSchema = z.object({ items: z.array(validationRuleSchema) });

/** GET /api/v1/ledger/ledgers/{ledger}/validation-rules, in evaluation order. */
export async function fetchValidationRules(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<ValidationRule[]> {
  return (await getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/validation-rules`, validationRuleListSchema, fetchImpl)).items;
}

/** GET .../validation-rules/{id}. */
export function fetchValidationRule(ledgerId: string, id: string, fetchImpl: FetchLike = defaultFetch): Promise<ValidationRule> {
  return getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/validation-rules/${encodeURIComponent(id)}`, validationRuleSchema, fetchImpl);
}

/** PUT .../validation-rules/{id}: creates, or replaces the rule at `lockVersion` (If-Match) when given. */
export function putValidationRule(ledgerId: string, id: string, input: ValidationRuleInput, lockVersion?: number, fetchImpl: FetchLike = defaultFetch): Promise<ValidationRule> {
  return sendJSON(
    "PUT",
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/validation-rules/${encodeURIComponent(id)}`,
    validationRuleSchema,
    fetchImpl,
    input,
    lockVersion ? ifMatch(lockVersion) : {},
  );
}

/** Advanced rule structure (FR-11.16): when a line matches the filter, its segments and constraint rows join the account structure. */
export const advancedRuleSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  structureId: z.string(),
  name: z.string(),
  priority: z.number().int(),
  accounts: z.array(z.string()),
  dimensions: z.record(z.string(), z.string()),
  segments: z.array(z.string()),
  constraints: z.array(constraintSchema),
  lockVersion: z.number(),
  createdAt: z.string().optional(),
  updatedAt: z.string().optional(),
});
export type AdvancedRule = z.infer<typeof advancedRuleSchema>;

export type AdvancedRuleInput = Pick<AdvancedRule, "structureId" | "name" | "priority" | "accounts" | "dimensions" | "segments" | "constraints">;

const advancedRuleListSchema = z.object({ items: z.array(advancedRuleSchema) });

/** GET /api/v1/ledger/ledgers/{ledger}/advanced-rules, by structure version, priority and name. */
export async function fetchAdvancedRules(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<AdvancedRule[]> {
  return (await getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/advanced-rules`, advancedRuleListSchema, fetchImpl)).items;
}

/** GET .../advanced-rules/{id}. */
export function fetchAdvancedRule(ledgerId: string, id: string, fetchImpl: FetchLike = defaultFetch): Promise<AdvancedRule> {
  return getJSON(`/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/advanced-rules/${encodeURIComponent(id)}`, advancedRuleSchema, fetchImpl);
}

/** PUT .../advanced-rules/{id}: creates, or replaces the rule at `lockVersion` (If-Match) when given. */
export function putAdvancedRule(ledgerId: string, id: string, input: AdvancedRuleInput, lockVersion?: number, fetchImpl: FetchLike = defaultFetch): Promise<AdvancedRule> {
  return sendJSON(
    "PUT",
    `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/advanced-rules/${encodeURIComponent(id)}`,
    advancedRuleSchema,
    fetchImpl,
    input,
    lockVersion ? ifMatch(lockVersion) : {},
  );
}

/** Dimension realignment (FR-11.24): a restatement of posted balances from old to new dimension values. */
export const REALIGNMENT_STATUSES = ["proposed", "posted", "reversed", "cancelled"] as const;
export type RealignmentStatus = (typeof REALIGNMENT_STATUSES)[number];

export const realignmentFilterSchema = z.object({
  accountFrom: z.string().optional(),
  accountTo: z.string().optional(),
  periodFrom: z.string(),
  periodTo: z.string(),
});
export type RealignmentFilter = z.infer<typeof realignmentFilterSchema>;

/** Dimension attribute to old value to new value. */
export type RealignmentMapping = Record<string, Record<string, string>>;

export const realignmentRowSchema = z.object({
  account: z.string(),
  period: z.string(),
  oldDimensions: z.record(z.string(), z.string()),
  newDimensions: z.record(z.string(), z.string()),
  oldCombination: z.string(),
  newCombination: z.string(),
  net: z.string(),
  /** Statistical quantities (BR-11.2) that move with the balance, one per unit of measure. */
  quantities: z.array(z.object({ unit: z.string(), net: z.string() })).optional(),
  lineCount: z.number(),
});
export type RealignmentRow = z.infer<typeof realignmentRowSchema>;

export const realignmentRunSchema = z.object({
  id: z.string(),
  ledgerId: z.string(),
  status: z.enum(REALIGNMENT_STATUSES),
  filter: realignmentFilterSchema,
  mapping: z.record(z.string(), z.record(z.string(), z.string())),
  reasonCode: z.string().optional(),
  reasonComment: z.string().optional(),
  combinationsAffected: z.number(),
  lineCount: z.number(),
  amountMoved: z.string(),
  currency: z.string(),
  preview: z.array(realignmentRowSchema),
  previewTruncated: z.boolean(),
  entryIds: z.array(z.string()),
  reversalEntryIds: z.array(z.string()),
  version: z.number(),
  createdBy: z.string().optional(),
  createdAt: z.string().optional(),
  postedBy: z.string().optional(),
  postedAt: z.string().optional(),
  reversedBy: z.string().optional(),
  reversedAt: z.string().optional(),
});
export type RealignmentRun = z.infer<typeof realignmentRunSchema>;

export interface RealignmentInput {
  filter: RealignmentFilter;
  mapping: RealignmentMapping;
}

export interface RealignmentActionInput {
  reasonCode?: string;
  reasonComment?: string;
  /** Reverse only: dates every reversing entry that day instead of its original date. */
  postingDate?: string;
}

const realignmentListSchema = z.object({ items: z.array(realignmentRunSchema) });

function realignmentBase(ledgerId: string): string {
  return `/api/v1/ledger/ledgers/${encodeURIComponent(ledgerId)}/realignments`;
}

/** GET /api/v1/ledger/ledgers/{ledger}/realignments (without previews). */
export async function fetchRealignments(ledgerId: string, fetchImpl: FetchLike = defaultFetch): Promise<RealignmentRun[]> {
  return (await getJSON(realignmentBase(ledgerId), realignmentListSchema, fetchImpl)).items;
}

/** GET .../realignments/{id}: the run with its affected rows. */
export function fetchRealignment(ledgerId: string, id: string, fetchImpl: FetchLike = defaultFetch): Promise<RealignmentRun> {
  return getJSON(`${realignmentBase(ledgerId)}/${encodeURIComponent(id)}`, realignmentRunSchema, fetchImpl);
}

/** POST .../realignments: simulates a run (nothing is posted). One Idempotency-Key per user-intended run. */
export function createRealignment(ledgerId: string, input: RealignmentInput, idempotencyKey: string, fetchImpl: FetchLike = defaultFetch): Promise<RealignmentRun> {
  return sendJSON("POST", realignmentBase(ledgerId), realignmentRunSchema, fetchImpl, input, { "Idempotency-Key": idempotencyKey });
}

/** POST .../realignments/{id}:post, :reverse or :cancel. Post and reverse need a reason code and an Idempotency-Key. */
export function actOnRealignment(
  action: "post" | "reverse" | "cancel",
  ledgerId: string,
  id: string,
  input: RealignmentActionInput,
  idempotencyKey: string,
  fetchImpl: FetchLike = defaultFetch,
): Promise<RealignmentRun> {
  return sendJSON("POST", `${realignmentBase(ledgerId)}/${encodeURIComponent(id)}:${action}`, realignmentRunSchema, fetchImpl, input, { "Idempotency-Key": idempotencyKey });
}

/** Shipped document types (FR-11.18) and retention classes of voucher attachments. */
export const DOCUMENT_TYPES = ["invoice_image", "approval_evidence", "supporting_calculation", "correspondence", "other"] as const;
export const RETENTION_CLASSES = ["financial_record_10y", "financial_record_7y", "financial_record_6y", "working_paper_2y"] as const;

/** An attachment (kind attachment) or a note (kind note) on a journal batch (FR-11.11). `retainedUntil` is set once the batch is posted. */
export const attachmentSchema = z.object({
  id: z.string(),
  batchId: z.string(),
  kind: z.enum(["attachment", "note"]),
  name: z.string().optional(),
  documentType: z.string().optional(),
  retentionClass: z.string().optional(),
  retainedUntil: z.string().optional(),
  storeRef: z.string().optional(),
  contentType: z.string().optional(),
  sizeBytes: z.number().int().optional(),
  note: z.string().optional(),
  createdBy: z.string(),
  createdAt: z.string(),
});
export type BatchAttachment = z.infer<typeof attachmentSchema>;

export interface BatchAttachmentInput {
  kind: "attachment" | "note";
  name?: string;
  documentType?: string;
  retentionClass?: string;
  storeRef?: string;
  contentType?: string;
  sizeBytes?: number;
  note?: string;
}

const attachmentListSchema = z.object({ items: z.array(attachmentSchema) });

/** GET /api/v1/ledger/journal-batches/{id}/attachments: files and notes, oldest first. */
export async function fetchBatchAttachments(batchId: string, fetchImpl: FetchLike = defaultFetch): Promise<BatchAttachment[]> {
  return (await getJSON(`/api/v1/ledger/journal-batches/${encodeURIComponent(batchId)}/attachments`, attachmentListSchema, fetchImpl)).items;
}

/** GET /api/v1/ledger/journal-entries/{id}/attachments: the files and notes of the batch posted as the entry (FR-11.18). */
export async function fetchEntryAttachments(entryId: string, fetchImpl: FetchLike = defaultFetch): Promise<BatchAttachment[]> {
  return (await getJSON(`/api/v1/ledger/journal-entries/${encodeURIComponent(entryId)}/attachments`, attachmentListSchema, fetchImpl)).items;
}

/** PUT .../attachments/{id}: adds under a client-chosen id, so a retry of the same request is a no-op. */
export function putBatchAttachment(batchId: string, id: string, input: BatchAttachmentInput, fetchImpl: FetchLike = defaultFetch): Promise<BatchAttachment> {
  return sendJSON("PUT", `/api/v1/ledger/journal-batches/${encodeURIComponent(batchId)}/attachments/${encodeURIComponent(id)}`, attachmentSchema, fetchImpl, input);
}

/** DELETE .../attachments/{id}: 409 LEDGER_ATTACHMENT_RETAINED while a posted batch's retention class runs. */
export async function deleteBatchAttachment(batchId: string, id: string, fetchImpl: FetchLike = defaultFetch): Promise<void> {
  const res = await fetchImpl(`/api/v1/ledger/journal-batches/${encodeURIComponent(batchId)}/attachments/${encodeURIComponent(id)}`, {
    method: "DELETE",
    headers: { Accept: "application/json" },
  });
  if (!res.ok) throw await readProblem(res);
}
