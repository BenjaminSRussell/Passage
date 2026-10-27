// Client for the intercompany service (services/intercompany/api/openapi.yaml).
// Browser calls use /api/v1/interco/..., which the web BFF rewrites to the
// service's /v1/interco/... paths and signs with the user's tenant and duties.
// Amounts are decimal strings; the browser never does arithmetic on a float.

export const base = "/api/v1/interco";

export type Side = "debit" | "credit";

/** One IC accounting matrix row (BR-40.4). */
export interface MatrixRow {
  entityA: string;
  entityB: string;
  /** Receivable account in entityA for entityB. */
  dueFrom: string;
  /** Payable account in entityB for entityA. */
  dueTo: string;
}

export interface JournalPreviewRequest {
  entity: string;
  account: string;
  side: Side;
  amount: string;
  currency: string;
  offsetEntity: string;
  offsetAccount: string;
  counterCurrency?: string;
  rate?: string;
  matrix: MatrixRow[];
}

export interface JournalLeg {
  entity: string;
  account: string;
  side: Side;
  amount: string;
  currency: string;
}

export interface JournalPreview {
  pairId: string;
  balanced: boolean;
  legs: JournalLeg[];
}

export type ReconStatus = "Matched" | "Timing" | "Mismatch";

export interface ReconItem {
  id?: string;
  icPairId?: string;
  entityA: string;
  entityB: string;
  period: string;
  currency: string;
  amountA: string;
  amountB: string;
  nextPeriodA?: string;
  nextPeriodB?: string;
  fxApplies?: boolean;
}

export interface ClassifiedItem {
  id: string;
  icPairId?: string;
  entityA: string;
  entityB: string;
  period: string;
  currency: string;
  amountA: string;
  amountB: string;
  difference: string;
  status: ReconStatus;
  toleranceApplied: boolean;
}

export interface ClassifyResult {
  fxTolerancePct: string;
  items: ClassifiedItem[];
  summary: { matched: number; timing: number; mismatch: number };
}

/** A failed request, normalised from the service's RFC 9457 problem. */
export interface Problem {
  status: number;
  code: string;
  message: string;
  /** Validation messages keyed by request path, such as matrix[0].dueTo. */
  fields: Record<string, string>;
}

export class IntercompanyApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, code: string, message: string, fields: Record<string, string> = {}) {
    super(message);
    this.name = "IntercompanyApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof IntercompanyApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<IntercompanyApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields =
    body?.errors && typeof body.errors === "object" && !Array.isArray(body.errors) ? (body.errors as Record<string, string>) : {};
  return new IntercompanyApiError(res.status, code, message, fields);
}

async function post<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
  const res = await fetch(`${base}${path}`, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify(body),
    credentials: "same-origin",
    signal,
  });
  if (!res.ok) {
    throw await toError(res);
  }
  return (await res.json()) as T;
}

/** Derive the due-to / due-from journal for one IC line (BR-40.4, AC-40.4). Nothing is posted. */
export function previewJournal(req: JournalPreviewRequest, signal?: AbortSignal): Promise<JournalPreview> {
  return post<JournalPreview>("/journals:preview", req, signal);
}

/** Classify IC balance pairs as Matched, Timing or Mismatch (FR-40.6, BR-40.5). Nothing is stored. */
export function classifyItems(req: { fxTolerancePct?: string; items: ReconItem[] }, signal?: AbortSignal): Promise<ClassifyResult> {
  return post<ClassifyResult>("/reconciliations:classify", req, signal);
}
