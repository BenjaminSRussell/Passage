import { Badge, type BadgeTone } from "../../shell/ui";
import type { DeliveryStatus } from "./api/hubClient";

const LABELS: Record<DeliveryStatus, { label: string; tone: BadgeTone }> = {
  pending: { label: "Pending", tone: "neutral" },
  delivered: { label: "Delivered", tone: "success" },
  dead_letter: { label: "Dead letter", tone: "danger" },
};

export function DeliveryPill({ status }: { status: DeliveryStatus }) {
  const { label, tone } = LABELS[status] ?? { label: status, tone: "neutral" as const };
  return <Badge tone={tone}>{label}</Badge>;
}

/** The dead-letter reason: HUB-0115 retries spent, HUB-0117 refused for good, HUB-0118 pushed with a conflicting legacy key. */
export const DEAD_LETTER_REASONS: Record<string, string> = {
  "HUB-0115": "Retries exhausted",
  "HUB-0117": "Refused (4xx or unresolved reference)",
  "HUB-0118": "Pushed, but the event already has another legacy key",
};

/** Dead letters the hub refuses to replay: the ERP already holds the record, so a replay could only add a duplicate. */
export const NOT_REPLAYABLE_CODES: ReadonlySet<string> = new Set(["HUB-0118"]);

/** Unix seconds as "2026-10-26 08:00 UTC". */
export function formatUnix(seconds: number | null): string {
  return seconds === null ? "—" : `${new Date(seconds * 1000).toISOString().slice(0, 16).replace("T", " ")} UTC`;
}
