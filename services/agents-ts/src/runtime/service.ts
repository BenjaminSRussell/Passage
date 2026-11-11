import { randomUUID } from "node:crypto";
import { ApiError, requireDuty, type Principal } from "../api/principal.js";
import { assertCanStartRun, type TokenBudget } from "../domain/budget.js";
import type { ActionRecord } from "../domain/audit.js";
import type { Money } from "../domain/money.js";
import type { AgentVersion } from "../domain/registry.js";
import { AGENT_INVOKE_DUTY } from "../agents/catalog.js";
import { AgentError } from "../errors.js";
import type { StoredRun } from "../store/pg.js";
import type { PendingApproval } from "../store/approvals.js";
import { createPersistedApprovalPort, type ApprovalInbox } from "./approvals.js";
import {
  runAgent,
  type AuditPort,
  type BudgetPort,
  type KillSwitchPort,
  type ModelProvider,
  type RunDeps,
  type RunResult,
} from "./engine.js";
import type { McpConnector } from "./mcp.js";

/** What the run service reads from the database beyond the engine's audit and budget ports. */
export interface RunStore extends AuditPort, BudgetPort {
  getRun(tenantId: string, runId: string): Promise<StoredRun | undefined>;
  loadActions(tenantId: string, runId: string): Promise<ActionRecord[]>;
  budgetSnapshot(tenantId: string, month: string): Promise<TokenBudget>;
}

export interface RunServiceConfig {
  /** Per-run token cap (FR-47.11), input and output. */
  runTokenCap: number;
  /** Above this an approver who started the run may not approve it (9.47.12, wave-48 SoD). */
  selfApprovalLimit: Money;
  /** `agent.approval_expiry_h`; absent means the BR-47.3 default of 72 hours. */
  approvalExpiryHours?: number;
  approvalPollMs?: number;
  pseudonymTtlMs: number;
}

export interface RunServiceDeps {
  agents: readonly AgentVersion[];
  /** True when the version has a passing evaluation on its current dataset (FR-47.10). */
  evalPassed: (agentId: string, version: number) => boolean;
  /** The provider for a model id; each agent version pins its model (FR-47.10). */
  providerFor: (modelId: string) => ModelProvider;
  mcp: McpConnector;
  store: RunStore;
  inbox: ApprovalInbox & { pendingForRun(tenantId: string, runId: string): Promise<PendingApproval[]> };
  killSwitch: KillSwitchPort;
  config: RunServiceConfig;
  now?: () => Date;
  sleep?: (ms: number) => Promise<void>;
  /** Runs the background work; tests pass a function that collects the promise. */
  spawn?: (work: Promise<unknown>) => void;
  newRunId?: () => string;
}

export interface StartedRun {
  runId: string;
  agentId: string;
  agentVersion: number;
  status: "RUNNING";
}

export interface RunView {
  run: StoredRun;
  pendingApprovals: PendingApproval[];
}

/**
 * Starts and reads agent runs for the HTTP surface (PRD 9.47.5). A start is refused before any state
 * exists when the agent is unknown, the caller lacks `agents.invoke` or the agent's own duties
 * (AGT-0203), the kill switch is on (AGT-0201), the version has no passing evaluation (AGT-0204) or
 * the month's budget is exhausted (AGT-0202). Then the run executes in the background with the
 * caller's token, and the answer is 202 with the run id; the run view shows its progress.
 */
export function createRunService(deps: RunServiceDeps) {
  const now = deps.now ?? (() => new Date());
  const spawn =
    deps.spawn ??
    ((work: Promise<unknown>): void => {
      work.catch((err: unknown) => {
        process.stderr.write(`${JSON.stringify({ level: "error", message: "agent run failed", service: "agents-ts", error: String(err) })}\n`);
      });
    });
  const newRunId = deps.newRunId ?? randomUUID;
  const prod = new Map(deps.agents.map((a) => [a.agentId, a]));

  return {
    agentIds(): string[] {
      return [...prod.keys()];
    },

    async start(caller: Principal, token: string, agentId: string, prompt: string): Promise<StartedRun> {
      const agent = prod.get(agentId);
      if (agent === undefined) throw new ApiError(404, "AGT-0404", `no agent ${agentId}`);
      requireDuty(caller, AGENT_INVOKE_DUTY, `agent ${agentId}`);
      for (const duty of agent.requiredDuties) requireDuty(caller, duty, `agent ${agentId}`);
      if (deps.killSwitch.isHalted(caller.tenantId)) throw new AgentError("AGT-0201", "agent runs are halted by the kill switch");
      if (!deps.evalPassed(agent.agentId, agent.version)) {
        throw new AgentError("AGT-0204", `agent ${agent.agentId} v${agent.version} has no passing evaluation`);
      }
      assertCanStartRun(await deps.store.budgetSnapshot(caller.tenantId, now().toISOString().slice(0, 7)));

      const runId = newRunId();
      let session;
      try {
        session = await deps.mcp.connect(caller, token, runId);
      } catch {
        throw new ApiError(502, "AGT-0214", "the tool gateway is unavailable");
      }
      const nowIso = (): string => now().toISOString();
      const nowMs = (): number => now().getTime();
      const cfg = deps.config;
      const runDeps: RunDeps = {
        provider: deps.providerFor(agent.modelId),
        tools: session,
        approvals: createPersistedApprovalPort({
          inbox: deps.inbox,
          tenantId: caller.tenantId,
          invokerUserId: caller.userId,
          approvalDuty: (tool) => session.describe(tool)?.requiredDuty ?? AGENT_INVOKE_DUTY,
          selfApprovalLimit: cfg.selfApprovalLimit,
          ...(cfg.approvalExpiryHours !== undefined ? { expiryHours: cfg.approvalExpiryHours } : {}),
          nowMs,
          now: nowIso,
          ...(cfg.approvalPollMs !== undefined ? { pollMs: cfg.approvalPollMs } : {}),
          ...(deps.sleep !== undefined ? { sleep: deps.sleep } : {}),
        }),
        // System-side risk facts come from the owning domains once they expose them; until then
        // every proposal goes to a human (suggest mode), so the neutral default cannot auto-commit.
        risk: { assess: () => ({ vendorHighRisk: false, sodConflict: false }) },
        killSwitch: deps.killSwitch,
        now: nowIso,
        nowMs,
        pseudonymTtlMs: cfg.pseudonymTtlMs,
        audit: deps.store,
        budgets: deps.store,
      };
      spawn(
        runAgent(runDeps, {
          runId,
          tenantId: caller.tenantId,
          invokerUserId: caller.userId,
          userDuties: caller.duties,
          agent,
          evalPassed: true,
          // v1 has no autonomous posting (9.47.1): every agent runs in suggest mode.
          policy: { mode: "SUGGEST", autoThreshold: 1, autoAmountLimit: { amount: "0", currency: cfg.selfApprovalLimit.currency } },
          selfApprovalLimit: cfg.selfApprovalLimit,
          runTokenCap: cfg.runTokenCap,
          prompt,
        }),
      );
      return { runId, agentId: agent.agentId, agentVersion: agent.version, status: "RUNNING" };
    },

    async view(caller: Principal, runId: string): Promise<RunView> {
      const run = await deps.store.getRun(caller.tenantId, runId);
      if (run === undefined) throw new ApiError(404, "AGT-0404", "no such run");
      return { run, pendingApprovals: await deps.inbox.pendingForRun(caller.tenantId, runId) };
    },

    async actions(caller: Principal, runId: string): Promise<ActionRecord[]> {
      if ((await deps.store.getRun(caller.tenantId, runId)) === undefined) throw new ApiError(404, "AGT-0404", "no such run");
      return deps.store.loadActions(caller.tenantId, runId);
    },
  };
}

export type RunService = ReturnType<typeof createRunService>;
export type { RunResult };
