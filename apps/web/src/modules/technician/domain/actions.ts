import type { ActionBody, ActionKind, DayVisit, VisitStatus } from "./types";

export const MAX_PHOTOS_PER_ACTION = 5;
export const MAX_PHOTO_EDGE = 1600;
export const MAX_CAPTURE_BYTES = 2 * 1024 * 1024;

/** A random id for an action or capture; the server keeps what it has seen once, so a retry is safe. */
export function newId(): string {
  const c = globalThis.crypto;
  if (c?.randomUUID) return c.randomUUID();
  const b = c.getRandomValues(new Uint8Array(16));
  b[6] = (b[6]! & 0x0f) | 0x40;
  b[8] = (b[8]! & 0x3f) | 0x80;
  const h = [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

export function newAction(kind: ActionKind, now: Date, extra: Partial<ActionBody> = {}): ActionBody {
  return { id: newId(), kind, deviceTime: now.toISOString(), ...extra };
}

/** The status moves a technician may start from each state: the same machine as the server's merge (BR-45.6). */
export function nextMoves(status: VisitStatus): ActionKind[] {
  switch (status) {
    case "DISPATCHED":
      return ["TRAVEL_START", "WORK_START"];
    case "TRAVELING":
      return ["WORK_START"];
    case "IN_PROGRESS":
      return ["HOLD", "COMPLETE"];
    case "ON_HOLD":
      return ["RESUME"];
    default:
      return [];
  }
}

export const MOVE_LABEL: Record<string, string> = {
  TRAVEL_START: "Start travel",
  WORK_START: "Start work",
  HOLD: "Put on hold",
  RESUME: "Resume work",
  COMPLETE: "Complete the job",
};

export const STATUS_LABEL: Record<string, string> = {
  DISPATCHED: "Ready to start",
  TRAVELING: "On the way",
  IN_PROGRESS: "Working",
  ON_HOLD: "On hold",
  COMPLETED: "Done",
  POSTED: "Done and billed",
  SCHEDULED: "Scheduled",
  CANCELED: "Canceled",
  UNSCHEDULED: "Unscheduled",
};

/** Whether parts, checklist, photos and the signature can be captured on a visit in this status. */
export function canCapture(status: VisitStatus): boolean {
  return status === "TRAVELING" || status === "IN_PROGRESS" || status === "ON_HOLD" || status === "DISPATCHED";
}

/** What blocks completing the visit, in plain words; empty when it can be completed. Mirrors SVC-WO-001, 002 and 003. */
export function completionBlockers(v: DayVisit): string[] {
  const out: string[] = [];
  const open = v.checklist.filter((c) => c.required && !c.done);
  if (open.length > 0) out.push(open.length === 1 ? `Tick "${open[0]!.text}"` : `Tick the ${open.length} required checklist items`);
  const parts = v.lines.filter((l) => l.kind === "PART" && Number(l.consumedQty) < Number(l.qty));
  if (parts.length > 0) out.push(parts.length === 1 ? `Record the ${parts[0]!.description ?? "part"} you used` : `Record the ${parts.length} parts you used`);
  if (!v.signature.signed && !v.signature.exempt) out.push("Get the customer's signature");
  return out;
}
