import { Badge, type BadgeTone } from "../../shell/ui";
import type { ClaimStatus, SlaState } from "./api/client";
import { sentence } from "./format";

const slaTone: Record<SlaState, BadgeTone> = { RUNNING: "info", PAUSED: "neutral", MET: "success", BREACHED: "danger" };

/** The SLA clock state of a case (BR-45.2); a breach stays visible as danger. */
export function SlaBadge({ state }: { state: SlaState }) {
  return <Badge tone={slaTone[state] ?? "neutral"}>{sentence(state)}</Badge>;
}

const coverageTone = { BILLABLE: "info", WARRANTY: "warning", AGREEMENT: "success" } as const;

/** Who pays for a posted line (BR-45.4). */
export function CoverageBadge({ coverage }: { coverage: keyof typeof coverageTone }) {
  return <Badge tone={coverageTone[coverage] ?? "neutral"}>{sentence(coverage)}</Badge>;
}

/** Priority 1 is the most urgent. */
export function PriorityBadge({ priority }: { priority: number }) {
  return <Badge tone={priority === 1 ? "danger" : priority === 2 ? "warning" : "neutral"}>{`P${priority}`}</Badge>;
}

const claimTone: Record<ClaimStatus, BadgeTone> = {
  DRAFT: "neutral",
  SUBMITTED: "info",
  ACCEPTED: "success",
  PARTIALLY_ACCEPTED: "warning",
  REJECTED: "danger",
  SETTLED: "success",
  CLOSED: "neutral",
};

/** A supplier warranty claim's status (FR-45.15). */
export function ClaimStatusBadge({ status }: { status: ClaimStatus }) {
  return <Badge tone={claimTone[status] ?? "neutral"}>{sentence(status)}</Badge>;
}
