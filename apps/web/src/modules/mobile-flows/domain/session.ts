// Device session lock (BR-32.6, FR-32.6). A device session holds the work lock while it is
// active. Heartbeat loss longer than the idle timeout makes the session STALE and releases the
// lock; resuming re-locks only if the work is still open for it. Pure functions over a clock
// value. Work states follow BR-31.3: an ASSIGNED work whose lock times out returns to OPEN.

export type SessionState = "ACTIVE" | "STALE";

export interface DeviceSessionView {
  sessionId: string;
  deviceId: string;
  userId: string;
  currentWorkId: string | null;
  lastSeenAt: number; // epoch ms, from the last heartbeat
}

/** Wave-31 work states (BR-31.3). */
export type WorkStatus =
  | "OPEN"
  | "ASSIGNED"
  | "IN_PROGRESS"
  | "PARTIALLY_COMPLETED"
  | "COMPLETED"
  | "CANCELLED"
  | "BLOCKED_NO_LOCATION";

export interface WorkView {
  workId: string;
  status: WorkStatus;
}

/**
 * OPEN work is free. ASSIGNED work is free once no ACTIVE session holds its lock, because the
 * lock timeout returns it to OPEN (BR-31.3). Every other state is past assignment.
 */
function lockable(status: WorkStatus): boolean {
  return status === "OPEN" || status === "ASSIGNED";
}

export const SESSION_IDLE_MIN_DEFAULT = 30;

/** ACTIVE while the heartbeat is within the idle window, STALE once it is older. */
export function sessionState(session: DeviceSessionView, now: number, idleMin = SESSION_IDLE_MIN_DEFAULT): SessionState {
  const idleMs = idleMin * 60_000;
  return now - session.lastSeenAt > idleMs ? "STALE" : "ACTIVE";
}

/** The ACTIVE session holding this work, if any. STALE sessions never hold a lock. */
export function lockHolder(
  sessions: DeviceSessionView[],
  workId: string,
  now: number,
  idleMin = SESSION_IDLE_MIN_DEFAULT,
): DeviceSessionView | null {
  return (
    sessions.find(
      (s) => s.currentWorkId === workId && sessionState(s, now, idleMin) === "ACTIVE",
    ) ?? null
  );
}

/**
 * A work can be assigned to another user when no ACTIVE session holds it and it is OPEN, or
 * ASSIGNED to a session that went STALE (AC-32.8).
 */
export function isAssignable(
  work: WorkView,
  sessions: DeviceSessionView[],
  now: number,
  idleMin = SESSION_IDLE_MIN_DEFAULT,
): boolean {
  if (!lockable(work.status)) return false;
  return lockHolder(sessions, work.workId, now, idleMin) === null;
}

/**
 * Outcome of a resumed heartbeat. A session resuming a STALE lock re-locks its work only if
 * no other ACTIVE session took it and it is still OPEN (or still ASSIGNED, when the sweep has
 * not yet returned it to OPEN); otherwise the work is released.
 */
export function resume(
  session: DeviceSessionView,
  work: WorkView | null,
  sessions: DeviceSessionView[],
  now: number,
  idleMin = SESSION_IDLE_MIN_DEFAULT,
): { relocked: boolean; reason: string } {
  if (!work || work.workId !== session.currentWorkId) return { relocked: false, reason: "no work held" };
  const holder = lockHolder(sessions, work.workId, now, idleMin);
  if (holder && holder.sessionId !== session.sessionId) {
    return { relocked: false, reason: "work is locked by another active session" };
  }
  if (!lockable(work.status)) return { relocked: false, reason: `work is ${work.status}` };
  return { relocked: true, reason: "re-locked" };
}
