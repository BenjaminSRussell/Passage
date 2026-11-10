import type { BadgeTone } from "./StatusPill";

/**
 * The one status standard. A status is shown as a pill with its text label and a tone, never colour alone
 * (WCAG 1.4.1). The tone says what the status means for the person reading the list:
 *
 * - neutral: nothing to do. Not started, waiting, or finished and no longer active (draft, open, closed, cancelled).
 * - info: in progress, the work is moving (in review, running, sent, picking).
 * - success: done well, or in force (approved, posted, paid, released, delivered, active).
 * - warning: a person should look (on hold, overdue, expired, partly done, escalated).
 * - danger: it failed or was refused (failed, rejected, refused, error).
 *
 * Status codes arrive as UPPER_SNAKE, snake_case or lower case; lookup ignores case and separators. Add a new
 * status here once rather than a tone table in a page. Unknown statuses are neutral with a sentence-case label.
 */
const TONES: Record<BadgeTone, readonly string[]> = {
  neutral: [
    "draft", "open", "pending", "new", "invited", "closed", "cancelled", "canceled", "ignored", "empty", "void", "voided",
    "archived", "inactive", "not started", "superseded", "withdrawn",
  ],
  info: [
    "in review", "in progress", "running", "executing", "sent", "replies open", "staged", "acceptable", "submitted",
    "picking", "packing", "scheduled", "queued", "in transit", "processing", "partially received", "released to warehouse",
  ],
  success: [
    "active", "approved", "confirmed", "released", "preferred", "completed", "ended", "finalized", "consolidated",
    "effective", "awarded", "replied", "invoiced", "committed", "accepted", "applied", "posted", "paid", "matched",
    "delivered", "shipped", "received", "reconciled", "closed won", "done",
  ],
  warning: [
    "on hold", "hold", "overdue", "escalated", "expired", "partial", "partially paid", "needs review", "blocked", "late",
    "past due", "disputed", "short", "unmatched",
  ],
  danger: ["failed", "error", "rejected", "refused", "declined", "write off", "written off", "dead letter", "overdrawn"],
};

const key = (status: string): string => status.replace(/[_-]+/g, " ").trim().toLowerCase();

const lookup = new Map<string, BadgeTone>();
for (const [tone, statuses] of Object.entries(TONES) as [BadgeTone, readonly string[]][]) {
  for (const status of statuses) lookup.set(status, tone);
}

/** Sentence-case label for a status code, e.g. IN_REVIEW becomes "In review". */
export function statusLabel(status: string): string {
  const spaced = key(status);
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

/** Display label: a few codes read better under another word ("ended" is shown as "Completed"). */
const LABELS: Record<string, string> = { ended: "Completed", executing: "Running", error: "Failed", canceled: "Cancelled" };

export function standardLabel(status: string): string {
  return LABELS[key(status)] ?? statusLabel(status);
}

export function statusTone(status: string): BadgeTone {
  return lookup.get(key(status)) ?? "neutral";
}

/** Every status the standard names, with its tone, for the catalog and the tests. */
export function standardStatuses(): { status: string; label: string; tone: BadgeTone }[] {
  return [...lookup].map(([status, tone]) => ({ status, label: standardLabel(status), tone }));
}
