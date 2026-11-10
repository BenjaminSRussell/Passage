import type { ApplyOutcome, QueuedTx } from "../domain/offlineQueue";
import { apiFetch } from "../../../shell/api/http";

/**
 * Maps a replay HTTP status to a queue outcome. 401/408/429 and 5xx are transport
 * (leave the transaction PENDING and retry). 409 is CONFLICT. Other 4xx are REJECTED.
 */
export function outcomeFromReplayStatus(status: number): ApplyOutcome {
  if (status >= 200 && status < 300) return { status: "APPLIED" };
  if (status === 409) return { status: "CONFLICT", reason: "conflict reported by the server" };
  if (status === 401 || status === 408 || status === 429 || status >= 500) {
    throw new Error("server unavailable");
  }
  return { status: "REJECTED", reason: `rejected with ${status}` };
}

/** Why the last replay stopped without the server judging the transaction. */
export interface ReplayStop {
  code: string;
  message: string;
}

/**
 * Maps one replay answer, with its problem body when it has one (services/mobile README, replay
 * statuses). Beyond the status rules:
 * - 403 MOB-NOT-OWNER: the entry was queued by another user. The queue is kept: sign in again as
 *   that user, or a supervisor closes it. Replay pauses; nothing is rejected.
 * - 422 MOB-0108: the server is missing an earlier sequence number. Replay pauses.
 * - 503 MOB-0114: the device clock is more than 300 s off. Replay pauses until the clock is set.
 * - 409 and 422 REJECTED carry the server's reason.
 */
export function outcomeFromReplay(status: number, problem: { code?: string; detail?: string } | null): ApplyOutcome | ReplayStop {
  const code = problem?.code;
  if (status === 403 && code === "MOB-NOT-OWNER") {
    return { code, message: "These confirmations were queued by another user. Sign in as that user, or ask a supervisor to close them." };
  }
  if (status === 422 && code === "MOB-0108") return { code, message: "The server is missing an earlier confirmation. Sync again once the earlier ones are sent." };
  if (status === 503 && code === "MOB-0114") return { code, message: "The device clock is more than five minutes off. Set the time, then sync again." };
  if (status === 409) return { status: "CONFLICT", reason: problem?.detail ?? "conflict reported by the server" };
  if (status === 422) return { status: "REJECTED", reason: problem?.detail ?? "rejected with 422" };
  return outcomeFromReplayStatus(status);
}

export function isStop(outcome: ApplyOutcome | ReplayStop): outcome is ReplayStop {
  return "message" in outcome && !("status" in outcome);
}

export interface ApplierOptions {
  /** The user signed in on the device; the server compares it with the user who queued the entry. */
  userId?: string;
  /** The open mobile session on this device, when there is one. */
  sessionId?: () => string | undefined;
  /** Called with the reason when replay pauses without judging the transaction. */
  onStop?: (stop: ReplayStop) => void;
}

const REPLAY_URL = "/api/v1/mobile/offline:replay";

async function problemOf(res: Response): Promise<{ code?: string; detail?: string } | null> {
  try {
    return (await res.json()) as { code?: string; detail?: string };
  } catch {
    return null;
  }
}

/**
 * Builds the function the offline queue calls for each transaction. The server enforces ordering
 * and idempotency per key (`device:seq`); the body carries the user who queued the entry
 * (`userId`, BR-32.4 ownership), the session and the capture time as RFC 3339.
 */
export function makeApplier(opts: ApplierOptions = {}): (tx: QueuedTx) => Promise<ApplyOutcome> {
  return async (tx) => {
    const sessionId = opts.sessionId?.();
    const queuedBy = typeof tx.payload["userId"] === "string" ? (tx.payload["userId"] as string) : opts.userId;
    const res = await apiFetch(REPLAY_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Idempotency-Key": tx.idempotencyKey },
      body: JSON.stringify({
        idempotencyKey: tx.idempotencyKey,
        command: tx.command,
        payload: tx.payload,
        capturedAt: new Date(tx.capturedAt).toISOString(),
        ...(queuedBy ? { userId: queuedBy } : {}),
        ...(sessionId ? { sessionId } : {}),
      }),
    });
    if (res.status >= 200 && res.status < 300) return { status: "APPLIED" };
    const outcome = outcomeFromReplay(res.status, await problemOf(res));
    if (isStop(outcome)) {
      opts.onStop?.(outcome);
      throw new Error(outcome.message);
    }
    return outcome;
  };
}
