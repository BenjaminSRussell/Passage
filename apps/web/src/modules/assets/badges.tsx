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
  Proposed: "info",
  Posted: "success",
  Due: "info",
  Exception: "warning",
  Submitted: "info",
  InApproval: "info",
  Approved: "success",
  Released: "success",
  Closed: "neutral",
  Rejected: "danger",
  Postponed: "warning",
  Found: "success",
  Missing: "danger",
  Unknown: "warning",
};

/** Asset, book, lease, run, run line, capex request or count outcome as a toned badge with its text (never a live region). */
export function StatusBadge({ status }: { status: string }) {
  return <Badge tone={tones[status] ?? "neutral"}>{sentence(status)}</Badge>;
}
