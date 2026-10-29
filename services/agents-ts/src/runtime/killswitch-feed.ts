import { AGENT_EVENT_TYPES } from "../domain/events.js";
import { buildKillSwitchChange, type KillSwitchRegistry } from "../domain/killswitch.js";

/** A consumed message: the CloudEvents envelope fields the consumer needs, plus data. */
export interface KillSwitchMessage {
  type: string;
  tenantid?: string;
  data: unknown;
}

export type FeedOutcome = "applied" | "duplicate" | "ignored";

/**
 * Applies the `killswitch.changed` stream to a worker's registry (FR-47.12,
 * BR-47.5). Each worker runs one feed. A message without an envelope tenant or
 * with a malformed payload throws, so the consumer routes it to the DLQ
 * (consumer C.5) instead of silently dropping a kill.
 */
export class KillSwitchFeed {
  private readonly registry: KillSwitchRegistry;

  constructor(registry: KillSwitchRegistry) {
    this.registry = registry;
  }

  /** Handles one message. Other event types are ignored, not errors. */
  handle(message: KillSwitchMessage): FeedOutcome {
    if (message.type !== AGENT_EVENT_TYPES.killSwitchChanged) return "ignored";
    if (typeof message.tenantid !== "string" || message.tenantid === "") {
      throw new Error("killswitch.changed without a tenant id: send to DLQ");
    }
    const change = buildKillSwitchChange(parseChanged(message.data));
    if (change.scope === "TENANT" && change.tenantId !== message.tenantid.toLowerCase()) {
      // A tenant toggle is always addressed to the tenant it halts (killSwitchChangedEvent).
      throw new Error("killswitch.changed for a tenant other than the envelope's: send to DLQ");
    }
    return this.registry.apply(change) ? "applied" : "duplicate";
  }

  /** Called on every poll of the stream, including polls that returned nothing (fail-closed window). */
  heartbeat(): void {
    this.registry.heartbeat();
  }
}

/** Reads the event's snake_case data (events/asyncapi.yaml KillSwitchChangedData). */
function parseChanged(raw: unknown): Parameters<typeof buildKillSwitchChange>[0] {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) throw new Error("killswitch.changed data must be an object");
  const d = raw as Record<string, unknown>;
  const scope = d.scope;
  if (scope !== "GLOBAL" && scope !== "TENANT") throw new Error("killswitch.changed scope must be GLOBAL or TENANT");
  const tenantId = d.tenant_id;
  if (scope === "GLOBAL" && tenantId !== null) throw new Error("killswitch.changed GLOBAL must carry tenant_id null");
  if (scope === "TENANT" && typeof tenantId !== "string") throw new Error("killswitch.changed TENANT needs tenant_id");
  if (typeof d.active !== "boolean") throw new Error("killswitch.changed active must be a boolean");
  if (typeof d.by_user !== "string") throw new Error("killswitch.changed by_user must be a string");
  if (typeof d.changed_at !== "string") throw new Error("killswitch.changed changed_at must be a string");
  if (typeof d.version !== "number") throw new Error("killswitch.changed version must be a number");
  return {
    scope,
    ...(typeof tenantId === "string" ? { tenantId: tenantId.toLowerCase() } : {}),
    active: d.active,
    byUser: d.by_user,
    version: d.version,
    changedAt: d.changed_at,
  };
}
