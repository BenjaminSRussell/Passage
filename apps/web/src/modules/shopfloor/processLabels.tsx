import { Badge, type BadgeTone } from "../../shell/ui";
import type { Characteristic, ProcessCategory, ProcessDelivery, ProcessDestination, ProcessStatus } from "./api/client";

export const CATEGORY_LABELS: Record<ProcessCategory, string> = {
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
