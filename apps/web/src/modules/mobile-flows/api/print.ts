import { apiFetch } from "../../../shell/api/http";
// Client for the print service (services/print/api/openapi.yaml, FR-32.7 and FR-32.13).
// Browser calls use /api/v1/print/..., which the web BFF rewrites to /print/v1/... on the service.

export const base = "/api/v1/print";

export const DEVICE_TYPES = ["ZPL_II", "PDF_IPP", "PCL_6"] as const;
export type DeviceType = (typeof DEVICE_TYPES)[number];

export const DPIS = [203, 300, 600] as const;

/** Media sizes (FR-32.13): label media for ZPL II devices, sheets for PDF and PCL 6 devices. */
export const LABEL_MEDIA = ["2X1_IN", "3X1_IN", "4X2_IN", "4X6_IN"] as const;
export const SHEET_MEDIA = ["A4", "LETTER"] as const;
export type MediaSize = (typeof LABEL_MEDIA)[number] | (typeof SHEET_MEDIA)[number];

/** The media sizes a device type takes; the first is the service default. */
export function mediaFor(deviceType: DeviceType): readonly MediaSize[] {
  return deviceType === "ZPL_II" ? ["4X6_IN", "4X2_IN", "3X1_IN", "2X1_IN"] : SHEET_MEDIA;
}

export function mediaLabel(media: MediaSize | string): string {
  const inches = /^(\d)X(\d)_IN$/.exec(media);
  if (inches) return `${inches[1]} x ${inches[2]} in label`;
  return media === "LETTER" ? "Letter sheet" : media === "A4" ? "A4 sheet" : media;
}

/**
 * What a device prints, by device type and media (services/print: layouts.go and the
 * dispatch fit check). 2 x 1 and 3 x 1 in labels print the 1 in layout of the LP, location
 * and item labels; the GS1 carton label is 4 x 6 in only, so those devices pass it to the
 * next device in the queue. No job carries PCL yet.
 */
export function printsOn(deviceType: DeviceType, media: MediaSize | string): string {
  if (deviceType === "PDF_IPP") return "PDF documents";
  if (deviceType === "PCL_6") return "Nothing yet: no job carries PCL";
  if (media === "2X1_IN" || media === "3X1_IN") return "LP, location and item labels in the 1 in layout; carton labels go to the next printer";
  return "LP, location, item and carton labels";
}

/** Job payload formats: a ZPL label from a template, or a PDF document for PDF over IPP devices. */
export const JOB_FORMATS = ["ZPL", "PDF"] as const;
export type JobFormat = (typeof JOB_FORMATS)[number];

/** Largest PDF a job may carry (the service refuses more). */
export const MAX_DOCUMENT_BYTES = 2 * 1024 * 1024;

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

export interface TemplateField {
  name: string;
  /** Optional GS1 fields are left out of the barcode when empty. */
  optional?: boolean;
}

/** Built-in ZPL templates and the data fields each one takes, in form order (§6.32.15). */
export const TEMPLATES = {
  LP_LABEL: [{ name: "lp" }, { name: "item" }, { name: "lot" }, { name: "expiry" }, { name: "sscc" }],
  LOCATION_LABEL: [{ name: "location" }],
  ITEM_LABEL: [{ name: "gtin" }, { name: "lot", optional: true }, { name: "expiry", optional: true }, { name: "serial", optional: true }],
  GS1_CARTON_LABEL: [{ name: "sscc" }, { name: "shipToName" }, { name: "poNumber", optional: true }, { name: "shipToPostal", optional: true }],
} as const satisfies Record<string, readonly TemplateField[]>;
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
  mediaSize: MediaSize;
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
  /** Omitted to use the default: 4 x 6 in for ZPL_II, A4 otherwise. */
  mediaSize?: MediaSize;
  host: string;
  /** Omitted to use the default: raw TCP 9100 for ZPL_II and PCL_6, IPP 631 for PDF_IPP. */
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
  /** PDF_DOCUMENT for a PDF job. */
  templateCode: string;
  queueCode: string;
  format: JobFormat;
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

/** Printer state from its last job (§6.32.14): no device probe is made. */
export const PRINTER_STATES = ["IDLE", "BUSY", "READY", "ERROR"] as const;
export type PrinterState = (typeof PRINTER_STATES)[number];

export interface PrinterStatus {
  printerId: string;
  code: string;
  queueCode: string;
  state: PrinterState;
  /** QUEUED jobs on the printer's queue. */
  queueDepth: number;
  lastError?: string;
  lastJobAt?: string;
}

export interface NewPrintJob {
  templateCode?: TemplateCode;
  /** Omitted for a ZPL label. PDF sends documentBase64 to PDF over IPP devices. */
  format?: JobFormat;
  documentBase64?: string;
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
  const res = await apiFetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
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

/** Printer status report (§6.32.14): state, queue depth and last error per printer. */
export function listPrinterStatuses(signal?: AbortSignal): Promise<PrinterStatus[]> {
  return items<PrinterStatus>("/printers/status", signal);
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

/** "Print again" (BR-32.8): a new job with reprintOf, reusing the stored ZPL or PDF. */
export function reprintJob(job: PrintJob, key: string): Promise<PrintJob> {
  return post<PrintJob>("/jobs", { queueCode: job.queueCode, reprintOf: job.id }, key);
}

/** Reads a blob with FileReader, which every supported browser (and jsdom) implements. */
function readBytes(file: Blob): Promise<Uint8Array> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer));
    reader.onerror = () => reject(new Error("The file could not be read."));
    reader.readAsArrayBuffer(file);
  });
}

/** Reads a PDF file as standard base64 for documentBase64. Refuses a file that is not a PDF or is too large. */
export async function pdfToBase64(file: Blob): Promise<string> {
  if (file.size === 0) throw new Error("The file is empty.");
  if (file.size > MAX_DOCUMENT_BYTES) throw new Error("The PDF is larger than 2 MB.");
  const bytes = await readBytes(file);
  const header = String.fromCharCode(...bytes.subarray(0, 5));
  if (header !== "%PDF-") throw new Error("The file is not a PDF document.");
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  return btoa(binary);
}
