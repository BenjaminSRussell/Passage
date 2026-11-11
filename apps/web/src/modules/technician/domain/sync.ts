import type { ActionBody, ActionOutcome, CaptureBody } from "./types";
import { outcomeNote, type QueueEntry, type TechQueue } from "./queue";

/** The server could not be reached or had a passing fault: keep the queue and try later. */
export class OfflineError extends Error {}

/** The server answered and refused the request itself (a 4xx): retrying the same entry cannot help. */
export class RefusedError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message);
  }
}

export interface SyncClient {
  postActions(workOrderId: string, technicianId: string, actions: ActionBody[]): Promise<ActionOutcome[]>;
  putCapture(workOrderId: string, technicianId: string, capture: CaptureBody): Promise<void>;
}

export interface SyncReport {
  sent: number;
  rejected: number;
  left: number;
  /** Set when the run stopped early because the server could not be reached. */
  offline: boolean;
}

const BATCH = 100;

/**
 * Sends the queue in the order it was made: an image before the action that names it, consecutive actions of one
 * work order in one batch. Every action the server answers for (applied, superseded or rejected) leaves the queue;
 * a rejection is kept as a note for the technician. A request the server refuses leaves the queue with a note. A
 * lost connection stops the run and keeps everything not yet answered; the ids make a resend safe.
 */
export async function syncQueue(queue: TechQueue, client: SyncClient, now: () => Date = () => new Date()): Promise<SyncReport> {
  const report: SyncReport = { sent: 0, rejected: 0, left: 0, offline: false };
  const note = (workOrderId: string, kind: string, reason: string, code?: string) =>
    queue.note({ workOrderId, kind, code, reason, at: now().toISOString() });
  for (;;) {
    const head = queue.pending[0];
    if (!head) break;
    if (head.kind === "capture") {
      const data = await queue.blob(head.capture.captureId);
      try {
        if (data === null) throw new RefusedError("The photo or signature was lost from this device. Take it again.", 0);
        await client.putCapture(head.workOrderId, head.technicianId, { ...head.capture, data });
        report.sent++;
      } catch (e) {
        if (e instanceof OfflineError) {
          report.offline = true;
          break;
        }
        const reason = e instanceof RefusedError ? e.message : "The photo or signature could not be sent.";
        await note(head.workOrderId, head.capture.kind, reason, e instanceof RefusedError ? e.code : undefined);
        report.rejected++;
      }
      await queue.remove([head.seq]);
      continue;
    }
    const batch: Extract<QueueEntry, { kind: "action" }>[] = [];
    for (const e of queue.pending) {
      if (e.kind !== "action" || e.workOrderId !== head.workOrderId || e.technicianId !== head.technicianId || batch.length >= BATCH) break;
      batch.push(e);
    }
    try {
      const results = await client.postActions(head.workOrderId, head.technicianId, batch.map((b) => b.action));
      for (const r of results) {
        if (r.outcome === "REJECTED") {
          report.rejected++;
          await note(head.workOrderId, r.kind, outcomeNote(r), r.code);
        } else report.sent++;
      }
    } catch (e) {
      if (e instanceof OfflineError) {
        report.offline = true;
        break;
      }
      report.rejected += batch.length;
      await note(head.workOrderId, batch[0]!.action.kind, e instanceof RefusedError ? e.message : "The server did not accept these entries.", e instanceof RefusedError ? e.code : undefined);
    }
    await queue.remove(batch.map((b) => b.seq));
  }
  report.left = queue.size;
  return report;
}
