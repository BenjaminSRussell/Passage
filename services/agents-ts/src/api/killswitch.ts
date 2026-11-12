import { type AgentEvent, type KillSwitchChangedData, killSwitchChangedEvent } from "../domain/events.js";
import { buildKillSwitchChange, type KillSwitchChange, type KillSwitchRegistry } from "../domain/killswitch.js";
import { ApiError, asObject, isUuid, type Principal, requireDuty } from "./principal.js";

/** Duty for a tenant kill (9.47.17 agents.admin) and for the global kill (platform.global.admin, break-glass). */
export const TENANT_KILL_DUTY = "agents.admin";
export const GLOBAL_KILL_DUTY = "platform.global.admin";

/** Versions are strictly increasing; production backs this with a database sequence. */
export interface KillSwitchVersionSource {
  next(): number;
}

/** Writes an event to the outbox in the toggle's transaction (production) or elsewhere (tests). */
export type Publish = (event: AgentEvent<unknown>) => Promise<void>;

/** A toggle as the shared log stores it: who wrote it (their tenant and user) and what it switches. */
export interface KillSwitchToggle {
  actorTenantId: string;
  scope: "GLOBAL" | "TENANT";
  tenantId?: string;
  active: boolean;
  byUser: string;
  changedAt: string;
}

/**
 * The kill switch shared by every pod (AC-47.6): `kill_switch_change` in production (PgKillSwitchLog).
 * `toggle` is idempotent by scope against the stored state, draws the version and writes the
 * `killswitch.changed` event in one transaction; `changesSince` is what each pod polls.
 */
export interface KillSwitchLog {
  toggle(t: KillSwitchToggle): Promise<{ change: KillSwitchChange; created: boolean }>;
  changesSince(version: number): Promise<KillSwitchChange[]>;
}

/** How a toggle is stored: the shared log, or (tests, a single process) a version source and a publish port. */
export type KillSwitchWiring = { log: KillSwitchLog } | { versions: KillSwitchVersionSource; publish: Publish };

export interface KillSwitchCommand {
  scope: "GLOBAL" | "TENANT";
  tenantId?: string;
  active: boolean;
}

export interface KillSwitchResult {
  scope: "GLOBAL" | "TENANT";
  tenantId: string | null;
  active: boolean;
  version: number;
  changedAt: string;
  byUser: string;
}

export interface KillSwitchOutcome {
  body: KillSwitchResult;
  /** The published event, or undefined when the switch was already in the requested state. */
  event?: AgentEvent<KillSwitchChangedData>;
}

/** Validates the POST body of `/api/v1/agents/kill-switch`. */
export function parseKillSwitchCommand(raw: unknown): KillSwitchCommand {
  const body = asObject(raw, ["scope", "tenantId", "active"]);
  const active = body.active;
  if (typeof active !== "boolean") {
    throw new ApiError(400, "AGT-0400", "active must be a boolean");
  }
  const tenantId = body.tenantId;
  if (body.scope === "GLOBAL") {
    if (tenantId !== undefined && tenantId !== null) {
      throw new ApiError(400, "AGT-0400", "GLOBAL scope takes no tenantId");
    }
    return { scope: "GLOBAL", active };
  }
  if (body.scope !== "TENANT") {
    throw new ApiError(400, "AGT-0400", "scope must be GLOBAL or TENANT");
  }
  if (typeof tenantId !== "string" || !isUuid(tenantId)) {
    throw new ApiError(400, "AGT-0400", "TENANT scope needs a tenantId");
  }
  return { scope: "TENANT", tenantId: tenantId.toLowerCase(), active };
}

function resultOf(change: KillSwitchChange): KillSwitchResult {
  return {
    scope: change.scope,
    tenantId: change.tenantId,
    active: change.active,
    version: change.version,
    changedAt: change.changedAt,
    byUser: change.byUser,
  };
}

/**
 * Toggles a kill switch (AC-47.6, FR-47.12). A tenant admin may only toggle
 * their own tenant; the global switch needs platform.global.admin.
 *
 * Idempotent by scope (9.47.5): asking for the state the switch is already in
 * returns its current toggle and publishes nothing. Otherwise the change is
 * published first and applied to this process's registry only once publishing
 * succeeded, so a failed publish never leaves this worker in a state the other
 * workers did not hear about.
 */
export async function handleKillSwitchCommand(
  input: {
    principal: Principal;
    body: unknown;
    registry: KillSwitchRegistry;
    now: () => Date;
  } & KillSwitchWiring,
): Promise<KillSwitchOutcome> {
  const cmd = parseKillSwitchCommand(input.body);
  if (cmd.scope === "GLOBAL") {
    requireDuty(input.principal, GLOBAL_KILL_DUTY, "the global kill switch");
  } else {
    requireDuty(input.principal, TENANT_KILL_DUTY, "the tenant kill switch");
    if (cmd.tenantId !== input.principal.tenantId) {
      throw new ApiError(403, "AGT-0403", "a tenant administrator may only toggle their own tenant");
    }
  }
  if ("log" in input) {
    // The stored state decides idempotence, so two pods taking the same toggle store it once.
    const { change, created } = await input.log.toggle({
      actorTenantId: input.principal.tenantId,
      scope: cmd.scope,
      ...(cmd.tenantId !== undefined ? { tenantId: cmd.tenantId } : {}),
      active: cmd.active,
      byUser: input.principal.userId,
      changedAt: input.now().toISOString(),
    });
    // This pod applies at once; the others apply it on their next poll.
    input.registry.apply(change);
    return created ? { body: resultOf(change), event: killSwitchChangedEvent(change, input.principal.tenantId) } : { body: resultOf(change) };
  }
  const current = input.registry.lastChange(cmd.tenantId);
  if (current !== undefined && current.active === cmd.active) {
    return { body: resultOf(current) };
  }
  const change = buildKillSwitchChange({
    scope: cmd.scope,
    tenantId: cmd.tenantId,
    active: cmd.active,
    byUser: input.principal.userId,
    version: input.versions.next(),
    changedAt: input.now().toISOString(),
  });
  const event = killSwitchChangedEvent(change, input.principal.tenantId);
  await input.publish(event);
  input.registry.apply(change);
  return { body: resultOf(change), event };
}

export interface KillSwitchState {
  active: boolean;
  version: number | null;
  changedAt: string | null;
}

export interface KillSwitchStatus {
  tenantId: string;
  /** True when agent runs for this tenant are halted for any reason. */
  halted: boolean;
  /** True when this worker has not heard from the kill-switch stream within 10 s and fails closed. */
  stale: boolean;
  tenant: KillSwitchState & { byUser: string | null };
  /** The operator who toggled the global switch belongs to another tenant, so no user id is shown. */
  global: KillSwitchState;
}

function stateOf(change: KillSwitchChange | undefined): KillSwitchState {
  return { active: change?.active ?? false, version: change?.version ?? null, changedAt: change?.changedAt ?? null };
}

/**
 * `GET /api/v1/agents/kill-switch`: the caller's tenant switch and the global
 * switch as this worker sees them, for the agent administration screen
 * (9.47.6). Readable by agents.admin or platform.global.admin.
 */
export function readKillSwitchStatus(principal: Principal, registry: KillSwitchRegistry): KillSwitchStatus {
  if (!principal.duties.has(TENANT_KILL_DUTY) && !principal.duties.has(GLOBAL_KILL_DUTY)) {
    requireDuty(principal, TENANT_KILL_DUTY, "the kill switch status");
  }
  const tenant = registry.lastChange(principal.tenantId);
  return {
    tenantId: principal.tenantId,
    halted: registry.isHalted(principal.tenantId),
    stale: registry.isStale(),
    tenant: { ...stateOf(tenant), byUser: tenant?.byUser ?? null },
    global: stateOf(registry.lastChange()),
  };
}
