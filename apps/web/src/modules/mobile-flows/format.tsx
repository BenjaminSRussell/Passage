import { Badge, type BadgeTone } from "../../shell/ui";
import type { DocumentType, JobFormat, JobStatus, PrinterState, PrintJob, RoutingRule } from "./api/print";

export const dash = "—";

export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

export const DOCUMENT_LABELS: Record<DocumentType, string> = {
  LP_LABEL: "LP label",
  LOCATION_LABEL: "Location label",
  ITEM_LABEL: "Item label",
  GS1_CARTON_LABEL: "GS1 carton label",
  PICK_LIST: "Pick list",
  COUNT_SHEET: "Count sheet",
  COA: "Certificate of analysis",
  BOL: "Bill of lading",
};

const STATUS: Record<JobStatus, { label: string; tone: BadgeTone }> = {
  QUEUED: { label: "Queued", tone: "neutral" },
  PRINTED: { label: "Printed", tone: "success" },
  FAILED: { label: "Failed", tone: "danger" },
};

export function JobStatusBadge({ status }: { status: JobStatus }) {
  const s = STATUS[status] ?? { label: status, tone: "neutral" as const };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

const PRINTER_STATE: Record<PrinterState, { label: string; tone: BadgeTone }> = {
  IDLE: { label: "Idle", tone: "neutral" },
  BUSY: { label: "Busy", tone: "info" },
  READY: { label: "Ready", tone: "success" },
  ERROR: { label: "Error", tone: "danger" },
};

export function PrinterStateBadge({ state }: { state: PrinterState }) {
  const s = PRINTER_STATE[state] ?? { label: state, tone: "neutral" as const };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

/** Catalog text for a failed job's code (§6.32.18). A PDF job needs a PDF over IPP printer, a label a ZPL II one. */
export function describeJobError(code?: string, format: JobFormat = "ZPL"): string {
  const what = format === "PDF" ? "document" : "label";
  if (code === "MOB-0111") return `No printer in the queue accepted the ${what} after its attempts (MOB-0111).`;
  if (code === "MOB-0112") return `No ${format === "PDF" ? "PDF over IPP" : "ZPL II"} printer is registered in the queue (MOB-0112).`;
  if (code === "PRN-0007") return "The label does not fit the media of any printer in the queue (PRN-0007). Add a printer with larger labels to the queue.";
  return code ? `Failed with ${code}.` : dash;
}

/** What a job printed: the label template, or "PDF document". */
export function describeJobContent(job: Pick<PrintJob, "format" | "templateCode">): string {
  return job.format === "PDF" ? "PDF document" : job.templateCode;
}

/** The scope a rule matches on, most specific first (BR-32.12). */
export function describeScope(rule: Pick<RoutingRule, "userId" | "zone" | "warehouseId">): string {
  if (rule.userId) return `User ${rule.userId}`;
  if (rule.zone) return `Zone ${rule.zone}`;
  if (rule.warehouseId) return `Warehouse ${rule.warehouseId}`;
  return "Default for the document type";
}
