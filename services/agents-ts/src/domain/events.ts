import type { RunResult } from "../runtime/engine.js";
import type { BudgetState, BudgetStatus } from "./budget.js";
import type { KillSwitchChange } from "./killswitch.js";
import type { RunStatus } from "./run.js";

/** Event types from PRD 9.47.5 that this slice builds payloads for. */
export const AGENT_EVENT_TYPES = {
  runCompleted: "dev.passage.agent.run.completed.v1",
  budgetThreshold: "dev.passage.agent.budget.threshold.v1",
  killSwitchChanged: "dev.passage.agent.killswitch.changed.v1",
} as const;

/** What the outbox receives: the type, the tenant the envelope is addressed to, and the data. */
export interface AgentEvent<T> {
  type: string;
  tenantId: string;
  data: T;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const MONTH = /^[0-9]{4}-(0[1-9]|1[0-2])$/;
const DECIMAL = /^[0-9]+(\.[0-9]{1,6})?$/;
const TERMINAL: readonly RunStatus[] = ["COMMITTED", "REJECTED", "FAILED", "KILLED"];
const SEVERITY: Record<BudgetState, number> = { OK: 0, SOFT: 1, HARD: 2 };

/** Payload field names follow PRD 9.47.5 and the other services' events (snake_case). */
export interface RunCompletedData {
  run_id: string;
  agent_id: string;
  version: number;
  status: RunStatus;
  code: string | null;
  tool_calls: Array<{ seq: number; tool: string; decision: string }>;
  tokens_in: number;
  tokens_out: number;
  estimated: boolean;
  cost_usd: string;
}

/**
 * `agent.run.completed` (run end). Carries tool names and decisions only: never
 * the redacted inputs, outputs or proposal text (PII stays out of events, PRD 9.47.4).
 */
export function runCompletedEvent(input: {
  tenantId: string;
  agentId: string;
  version: number;
  result: RunResult;
  costUsd: string;
}): AgentEvent<RunCompletedData> {
  if (!UUID.test(input.tenantId)) throw new Error("tenantId must be a tenant id");
  if (!Number.isSafeInteger(input.version) || input.version < 1) throw new Error("version must be a positive integer");
  if (!TERMINAL.includes(input.result.status)) throw new Error(`run ${input.result.runId} is not terminal (${input.result.status})`);
  if (!DECIMAL.test(input.costUsd)) throw new Error("costUsd must be a decimal string with at most 6 places");
  return {
    type: AGENT_EVENT_TYPES.runCompleted,
    tenantId: input.tenantId,
    data: {
      run_id: input.result.runId,
      agent_id: input.agentId,
      version: input.version,
      status: input.result.status,
      code: input.result.code ?? null,
      tool_calls: input.result.actions.map((a) => ({ seq: a.seq, tool: a.toolName, decision: a.decision })),
      tokens_in: input.result.tokensIn,
      tokens_out: input.result.tokensOut,
      estimated: input.result.estimated,
      cost_usd: input.costUsd,
    },
  };
}

export interface BudgetThresholdData {
  tenant_id: string;
  month: string;
  pct: number;
  state: "SOFT" | "HARD";
}

/**
 * The cost-guard alert (AC-47.4, PRD 9.47.5). Fires only when the budget moves
 * up into SOFT (80%) or HARD (100%); repeated readings in the same state do not
 * re-alert. Returns undefined when there is nothing to report.
 */
export function budgetThresholdEvent(input: {
  tenantId: string;
  month: string;
  previous: BudgetState;
  current: BudgetStatus;
}): AgentEvent<BudgetThresholdData> | undefined {
  if (!UUID.test(input.tenantId)) throw new Error("tenantId must be a tenant id");
  if (!MONTH.test(input.month)) throw new Error("month must be YYYY-MM");
  if (input.current.state === "OK") return undefined;
  if (SEVERITY[input.current.state] <= SEVERITY[input.previous]) return undefined;
  return {
    type: AGENT_EVENT_TYPES.budgetThreshold,
    tenantId: input.tenantId,
    data: { tenant_id: input.tenantId, month: input.month, pct: Math.floor(input.current.pct), state: input.current.state },
  };
}

export interface KillSwitchChangedData {
  scope: "GLOBAL" | "TENANT";
  tenant_id: string | null;
  active: boolean;
  by_user: string;
  version: number;
  changed_at: string;
}

/**
 * `agent.killswitch.changed`. A global toggle is addressed to the operator's
 * own tenant so every envelope carries a tenant id (consumers DLQ events
 * without one); `data.tenant_id` is null for GLOBAL.
 */
export function killSwitchChangedEvent(change: KillSwitchChange, envelopeTenantId: string): AgentEvent<KillSwitchChangedData> {
  if (!UUID.test(envelopeTenantId)) throw new Error("envelope tenant id must be a tenant id");
  return {
    type: AGENT_EVENT_TYPES.killSwitchChanged,
    tenantId: envelopeTenantId,
    data: {
      scope: change.scope,
      tenant_id: change.tenantId,
      active: change.active,
      by_user: change.byUser,
      version: change.version,
      changed_at: change.changedAt,
    },
  };
}
