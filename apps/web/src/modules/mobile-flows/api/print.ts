// Client for the print service (services/print/api/openapi.yaml, FR-32.7 and FR-32.13).
// Browser calls use /api/v1/print/..., which the web BFF rewrites to /print/v1/... on the service.

export const base = "/api/v1/print";

export const DEVICE_TYPES = ["ZPL_II", "PDF_IPP", "PCL_6"] as const;
export type DeviceType = (typeof DEVICE_TYPES)[number];

export const DPIS = [203, 300, 600] as const;

export const DOCUMENT_TYPES = [
  "LP_LABEL",
  "LOCATION_LABEL",
  "ITEM_LABEL",
  "GS1_CARTON_LABEL",
  "PICK_LIST",
  "COUNT_SHEET",
  "COA",
  "BOL",
] as const;
export type DocumentType = (typeof DOCUMENT_TYPES)[number];

/** Built-in ZPL templates and the data fields each one needs. */
export const TEMPLATES = {
  LP_LABEL: ["lp", "item", "lot", "expiry", "sscc"],
  LOCATION_LABEL: ["location"],
} as const;
export type TemplateCode = keyof typeof TEMPLATES;

export const JOB_STATUSES = ["QUEUED", "PRINTED", "FAILED"] as const;
export type JobStatus = (typeof JOB_STATUSES)[number];

/** Printer and queue codes: 2 to 40 of A-Z, 0-9, _ and -. */
export const CODE = /^[A-Z0-9][A-Z0-9_-]{1,39}$/;

export interface Printer {
  id: string;
  code: string;
  name: string;
  deviceType: DeviceType;
  dpi: number;
  host: string;
  port: number;
  queueCode: string;
  failoverRank: number;
  createdAt: string;
}

export interface NewPrinter {
  code: string;
  name: string;
  deviceType: DeviceType;
  dpi: number;
  host: string;
  /** Omitted for ZPL_II, which defaults to raw TCP 9100. */
  port?: number;
  queueCode: string;
  failoverRank: number;
}

export interface RoutingRule {
  id: string;
  documentType: DocumentType;
  userId?: string;
  zone?: string;
  warehouseId?: string;
  queueCode: string;
  sequence: number;
}

export type NewRoutingRule = Omit<RoutingRule, "id">;

export interface PrintJob {
  id: string;
  templateCode: string;
  queueCode: string;
  printerId?: string;
  printerCode?: string;
  status: JobStatus;
  attempts: number;
  /** MOB-0111 (unreachable) or MOB-0112 (no device) when FAILED. */
  lastError?: string;
  reprintOf?: string;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface NewPrintJob {
  templateCode?: TemplateCode;
  queueCode?: string;
  documentType?: DocumentType;
  zone?: string;
  warehouseId?: string;
  data?: Record<string, string>;
  reprintOf?: string;
}

export interface JobFilter {
  status?: JobStatus | "";
  queueCode?: string;
  printerCode?: string;
  limit?: number;
}

export class PrintApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string>,
  ) {
    super(message);
    this.name = "PrintApiError";
  }
}

export interface Problem {
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof PrintApiError) return { code: error.code, message: error.message, fields: error.fields };
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<PrintApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new PrintApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await send<{ items?: T[] }>(path, {}, signal);
  if (!Array.isArray(body.items)) throw new PrintApiError(0, "COM-0001", "The response has no items", {});
  return body.items;
}

function post<T>(path: string, body: unknown, key: string): Promise<T> {
  return send<T>(path, { method: "POST", headers: { "Idempotency-Key": key }, body: JSON.stringify(body) });
}

export function newKey(): string {
  return crypto.randomUUID();
}

export function listPrinters(signal?: AbortSignal): Promise<Printer[]> {
  return items<Printer>("/printers", signal);
}

/** Registers a printer. Reuse one key per form so a retried submit cannot register twice. */
export function createPrinter(input: NewPrinter, key: string): Promise<Printer> {
  return post<Printer>("/printers", input, key);
}

export function listRoutingRules(signal?: AbortSignal): Promise<RoutingRule[]> {
  return items<RoutingRule>("/routing-rules", signal);
}

export function createRoutingRule(input: NewRoutingRule, key: string): Promise<RoutingRule> {
  return post<RoutingRule>("/routing-rules", input, key);
}

export function listJobs(filter: JobFilter = {}, signal?: AbortSignal): Promise<PrintJob[]> {
  const q = new URLSearchParams();
  if (filter.status) q.set("status", filter.status);
  if (filter.queueCode) q.set("queueCode", filter.queueCode);
  if (filter.printerCode) q.set("printerCode", filter.printerCode);
  if (filter.limit) q.set("limit", String(filter.limit));
  const query = q.toString();
  return items<PrintJob>(`/jobs${query ? `?${query}` : ""}`, signal);
}

export function getJob(id: string, signal?: AbortSignal): Promise<PrintJob> {
  return send<PrintJob>(`/jobs/${encodeURIComponent(id)}`, {}, signal);
}

/** Renders and prints. The job comes back PRINTED or FAILED; a replayed key returns the stored job. */
export function createJob(input: NewPrintJob, key: string): Promise<PrintJob> {
  return post<PrintJob>("/jobs", input, key);
}

/** "Print again" (BR-32.8): a new job with reprintOf, reusing the stored ZPL. */
export function reprintJob(job: PrintJob, key: string): Promise<PrintJob> {
  return post<PrintJob>("/jobs", { queueCode: job.queueCode, reprintOf: job.id }, key);
}
