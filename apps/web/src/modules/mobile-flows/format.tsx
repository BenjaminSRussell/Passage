import { Badge, type BadgeTone } from "../../shell/ui";
import type { DocumentType, JobStatus, RoutingRule } from "./api/print";

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

/** Catalog text for a failed job's code (§6.32.18). */
export function describeJobError(code?: string): string {
  if (code === "MOB-0111") return "No printer in the queue accepted the label after its attempts (MOB-0111).";
  if (code === "MOB-0112") return "No ZPL printer is registered in the queue (MOB-0112).";
  return code ? `Failed with ${code}.` : dash;
}

/** The scope a rule matches on, most specific first (BR-32.12). */
export function describeScope(rule: Pick<RoutingRule, "userId" | "zone" | "warehouseId">): string {
  if (rule.userId) return `User ${rule.userId}`;
  if (rule.zone) return `Zone ${rule.zone}`;
  if (rule.warehouseId) return `Warehouse ${rule.warehouseId}`;
  return "Default for the document type";
}
