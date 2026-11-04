import type { ApplyOutcome } from "../domain/offlineQueue";

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
