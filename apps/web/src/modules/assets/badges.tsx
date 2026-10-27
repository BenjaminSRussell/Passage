import { Badge, type BadgeTone } from "../../shell/ui";
import { sentence } from "./format";

const tones: Record<string, BadgeTone> = {
  NotYetAcquired: "neutral",
  Open: "success",
  Active: "success",
  Sold: "info",
  Transferred: "info",
  Modified: "info",
  Scrapped: "warning",
  Terminated: "neutral",
  Draft: "neutral",
};

/** Asset, book or lease status as a toned badge with its text (never a live region). */
export function StatusBadge({ status }: { status: string }) {
  return <Badge tone={tones[status] ?? "neutral"}>{sentence(status)}</Badge>;
}
