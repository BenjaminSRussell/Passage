import { Badge, type BadgeTone } from "../../shell/ui";
import { listProcessCategories, type Characteristic, type ProcessCategory, type ProcessDelivery, type ProcessDestination, type ProcessStatus } from "./api/client";
import { useLoad } from "./useLoad";

export const CATEGORY_LABELS: Record<string, string> = {
  "phase-start": "Phase start",
  "phase-end": "Phase end",
  consumption: "Consumption",
  yield: "Yield",
  "process-data": "Process data",
  "quality-result": "Quality result",
  deviation: "Deviation",
  "order-status": "Order status",
  custom: "Custom",
};

export const CATEGORIES = Object.keys(CATEGORY_LABELS) as ProcessCategory[];

/** The label of a category: the built-in name, else the name the tenant gave it, else the code. */
export function categoryLabel(code: string, name?: string): string {
  return CATEGORY_LABELS[code] ?? (name && name !== code ? name : code);
}

export interface CategoryOption {
  code: string;
  label: string;
}

/**
 * The categories a message form or the monitor offers: the tenant's master rows and the built-in
 * defaults they do not override. The message form offers active rows only (an inactive category
 * refuses new messages); the monitor also filters by inactive ones, whose stored messages remain.
 * Until the list loads, or if it fails, the built-in codes.
 */
export function useCategoryOptions(activeOnly = true): CategoryOption[] {
  const state = useLoad((signal) => listProcessCategories(activeOnly ? { active: true } : {}, signal), `process-category-options|${activeOnly}`);
  const rows = state.data;
  if (!rows) return CATEGORIES.map((code) => ({ code, label: CATEGORY_LABELS[code] }));
  return rows.map((row) => ({ code: row.code, label: categoryLabel(row.code, row.name) }));
}

export const DESTINATION_LABELS: Record<ProcessDestination, string> = {
  ebr: "Batch record (EBR)",
  journal: "Production journal",
  quality: "Quality results",
  kafka: "Kafka topic",
  webhook: "Webhook",
};

export const DESTINATIONS = Object.keys(DESTINATION_LABELS) as ProcessDestination[];

const STATUS: Record<ProcessStatus, { label: string; tone: BadgeTone }> = {
  received: { label: "Received", tone: "neutral" },
  "partially-delivered": { label: "Partially delivered", tone: "warning" },
  delivered: { label: "Delivered", tone: "success" },
  error: { label: "Error", tone: "danger" },
};

export const STATUSES = Object.keys(STATUS) as ProcessStatus[];

export function statusLabel(status: ProcessStatus): string {
  return STATUS[status]?.label ?? status;
}

export function MessageStatus({ status }: { status: ProcessStatus }) {
  const s = STATUS[status] ?? { label: status, tone: "neutral" as const };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

const DELIVERY: Record<ProcessDelivery["status"], { label: string; tone: BadgeTone }> = {
  pending: { label: "Pending", tone: "neutral" },
  delivered: { label: "Delivered", tone: "success" },
  error: { label: "Error", tone: "danger" },
};

export function DeliveryStatus({ status }: { status: ProcessDelivery["status"] }) {
  const s = DELIVERY[status] ?? { label: status, tone: "neutral" as const };
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

/** "7.9 pH" with the limits, e.g. "6.5 to 7.5". */
export function limits(c: Characteristic): string {
  if (c.min && c.max) return `${c.min} to ${c.max}`;
  if (c.min) return `at least ${c.min}`;
  if (c.max) return `at most ${c.max}`;
  return "—";
}
