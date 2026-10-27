// On-device exceptions and supervisor PIN override (FR-32.8, BR-32.7). Pure: no I/O, no React.
// Every exception is logged with user, device, location and time. An override authorizes
// exactly one action within 60 s, for the worker it was issued to, and is logged with the
// supervisor's subject.

export type ExceptionKind = "SHORT_PICK" | "SKIP" | "DAMAGED" | "OVERRIDE";

export const OVERRIDE_WINDOW_MS = 60_000;

/** MOB-0110: the override expired or was already used. */
export const CODE_OVERRIDE_EXPIRED = "MOB-0110";

export interface ExceptionInput {
  kind: ExceptionKind;
  userId: string;
  deviceId: string;
  locationCode: string;
  workLineId: string;
  /** Units short for SHORT_PICK; ignored otherwise. */
  shortQuantity?: number;
  /** Free text for DAMAGED; required. For OVERRIDE, one of the configured override reasons. */
  reason?: string;
}

export interface ExceptionEntry extends ExceptionInput {
  id: string;
  occurredAt: number; // epoch ms
}

export type Validation = { ok: true } | { ok: false; message: string };

/** Checks an exception before it is logged. Override reasons come from the warehouse configuration. */
export function validateException(input: ExceptionInput, overrideReasons: readonly string[]): Validation {
  if (!input.userId || !input.deviceId || !input.locationCode || !input.workLineId) {
    return { ok: false, message: "User, device, location and work line are required." };
  }
  switch (input.kind) {
    case "SHORT_PICK":
      if (!Number.isInteger(input.shortQuantity) || (input.shortQuantity ?? 0) <= 0) {
        return { ok: false, message: "Short pick needs a whole quantity above zero." };
      }
      return { ok: true };
    case "SKIP":
      return { ok: true };
    case "DAMAGED":
      if (!input.reason || input.reason.trim() === "") return { ok: false, message: "Damage needs a reason." };
      return { ok: true };
    case "OVERRIDE":
      if (!input.reason || !overrideReasons.includes(input.reason)) {
        return { ok: false, message: "Override needs a configured reason." };
      }
      return { ok: true };
  }
}

/** Appends a validated exception to the log. Returns the new log; the input array is not mutated. */
export function logException(
  log: readonly ExceptionEntry[],
  input: ExceptionInput,
  id: string,
  now: number,
  overrideReasons: readonly string[],
): { log: ExceptionEntry[]; entry?: ExceptionEntry; error?: string } {
  const v = validateException(input, overrideReasons);
  if (!v.ok) return { log: [...log], error: v.message };
  const entry: ExceptionEntry = { ...input, id, occurredAt: now };
  return { log: [...log, entry], entry };
}

export interface OverrideGrant {
  id: string;
  supervisorId: string; // logged subject of the supervisor who entered the PIN
  workerId: string; // the only user the override applies to
  action: string; // the single action it authorizes, e.g. "SKIP:line-42"
  issuedAt: number;
  usedAt: number | null;
}

/**
 * Issues an override. A supervisor cannot override their own work (BR-32.7, SoD in §6.32.17).
 * The PIN itself is verified by the server; this records the decision.
 */
export function issueOverride(
  id: string,
  supervisorId: string,
  workerId: string,
  action: string,
  now: number,
): { grant?: OverrideGrant; error?: string } {
  if (supervisorId === workerId) return { error: "A supervisor cannot override their own work." };
  if (!action) return { error: "An override must name one action." };
  return { grant: { id, supervisorId, workerId, action, issuedAt: now, usedAt: null } };
}

/**
 * Consumes an override for one action. It fails with MOB-0110 when the window has passed
 * or the grant was already used, and with a plain reason when the worker or action differs.
 * The grant is returned as a new value with usedAt set; the input is not mutated.
 */
export function consumeOverride(
  grant: OverrideGrant,
  workerId: string,
  action: string,
  now: number,
): { grant?: OverrideGrant; code?: string; error?: string } {
  if (grant.usedAt !== null || now - grant.issuedAt > OVERRIDE_WINDOW_MS || now < grant.issuedAt) {
    return { code: CODE_OVERRIDE_EXPIRED, error: "Supervisor PIN expired or already used." };
  }
  if (workerId !== grant.workerId) return { error: "Override was issued to another worker." };
  if (action !== grant.action) return { error: "Override was issued for another action." };
  return { grant: { ...grant, usedAt: now } };
}
