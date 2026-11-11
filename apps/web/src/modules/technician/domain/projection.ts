import type { ActionBody, DayVisit, VisitStatus } from "./types";

/** An action waiting to be sent, with the visit it belongs to. */
export interface PendingAction {
  workOrderId: string;
  action: ActionBody;
}

const FURTHEST: Record<string, number> = { DISPATCHED: 0, TRAVELING: 1, IN_PROGRESS: 2, ON_HOLD: 2, COMPLETED: 3 };
const TARGET: Record<string, VisitStatus> = { TRAVEL_START: "TRAVELING", WORK_START: "IN_PROGRESS", HOLD: "ON_HOLD", RESUME: "IN_PROGRESS", COMPLETE: "COMPLETED" };

/** The status a move leads to, or undefined when the server would supersede or reject it (domain.Merge). */
export function moveTo(from: VisitStatus, kind: string): VisitStatus | undefined {
  const to = TARGET[kind];
  if (!to) return undefined;
  if (kind === "HOLD") return from === "IN_PROGRESS" ? to : undefined;
  if (kind === "RESUME") return from === "ON_HOLD" ? to : undefined;
  if (from === "ON_HOLD") return undefined;
  return (FURTHEST[to] ?? 0) > (FURTHEST[from] ?? -1) ? to : undefined;
}

/**
 * What the visit looks like with the queued actions applied, so the device shows its own work at
 * once, offline or not. It follows the server's rules closely enough for display: actions are taken in
 * device-time order, a status move only goes forward, the latest tick of a checklist item wins, parts add
 * up. The server's answer replaces this once the queue is sent.
 */
export function project(visit: DayVisit, pending: readonly PendingAction[]): DayVisit {
  const mine = pending.filter((p) => p.workOrderId === visit.id).map((p) => p.action).sort((a, b) => a.deviceTime.localeCompare(b.deviceTime));
  if (mine.length === 0) return visit;
  const out: DayVisit = {
    ...visit,
    lines: visit.lines.map((l) => ({ ...l })),
    checklist: visit.checklist.map((c) => ({ ...c })),
    photos: [...visit.photos],
    signature: { ...visit.signature },
  };
  const tickAt = new Map<string, string>();
  for (const a of mine) {
    switch (a.kind) {
      case "TRAVEL_START":
      case "WORK_START":
      case "HOLD":
      case "RESUME":
      case "COMPLETE": {
        const to = moveTo(out.status, a.kind);
        if (to) {
          out.status = to;
          if (a.kind === "COMPLETE") out.completedAt = a.deviceTime;
        }
        break;
      }
      case "PART_USED": {
        const line = out.lines.find((l) => l.id === a.lineId);
        if (line && a.qty) line.consumedQty = String(Number(line.consumedQty) + Number(a.qty));
        break;
      }
      case "CHECKLIST": {
        const item = out.checklist.find((c) => c.id === a.itemId);
        if (item && (tickAt.get(item.id) ?? "") <= a.deviceTime) {
          item.done = a.done === true;
          tickAt.set(item.id, a.deviceTime);
        }
        break;
      }
      case "PHOTO":
        for (const id of a.captureIds ?? []) out.photos.push({ captureId: id, ref: `capture:${id}`, capturedAt: a.deviceTime });
        break;
      case "SIGNATURE":
        out.signature = { ...out.signature, signed: true, signedBy: a.signedBy ?? null, signedAt: a.deviceTime, ref: a.signatureRef ?? null };
        break;
      case "NOTE":
        out.technicianNote = a.text ?? "";
        break;
    }
  }
  return out;
}
