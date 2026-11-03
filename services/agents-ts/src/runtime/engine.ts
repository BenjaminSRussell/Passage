import {
  buildActionRecord,
  type ActionRecord,
  type Decision,
} from "../domain/audit.js";
import { selfApprovalBlocked, canCallTool } from "../domain/authz.js";
import {
  assertRunWithinCap,
  estimateTokens,
  type BudgetState,
} from "../domain/budget.js";
import type { Money } from "../domain/money.js";
import {
  decideDisposition,
  validatePolicy,
  type ModePolicy,
} from "../domain/policy.js";
import type { AgentVersion } from "../domain/registry.js";
import { assertNoPii, Pseudonymizer } from "../domain/redaction.js";
import { isTerminal, transition, type RunStatus } from "../domain/run.js";
import { AgentError } from "../errors.js";

export interface ModelUsage {
  tokensIn: number;
  tokensOut: number;
}

export interface Proposal {
  tool: string;
  /** Uses pseudonyms for any PII the model saw; restored before the commit call. */
  args: Record<string, unknown>;
  confidence: number;
  amount: Money;
}

export interface ModelResponse {
  text: string;
  proposal?: Proposal;
  /** Absent when the provider reports no usage; the run then estimates (BR-47.4). */
  usage?: ModelUsage;
}

/** Provider layer (FR-47.13). Only pseudonymized text reaches this port. */
export interface ModelProvider {
  complete(req: {
    system: string;
    user: string;
    allowedTools: readonly string[];
  }): Promise<ModelResponse>;
}

export interface ToolDescriptor {
  requiredDuty: string;
  approvalMode: "none" | "suggest" | "approve_before_commit";
}

export type ToolResult =
  | { ok: true; output: unknown; ref?: string }
  | { ok: false; code: string; message: string };

/** The MCP gateway as seen by the runtime: each call carries the invoking user's token. */
export interface ToolPort {
  describe(name: string): ToolDescriptor | undefined;
  call(name: string, args: Record<string, unknown>): Promise<ToolResult>;
}

export interface RiskAssessment {
  vendorHighRisk: boolean;
  sodConflict: boolean;
  /**
   * The amount of the source document (invoice, payment), read by the system.
   * When present it replaces the model's figure for the AUTO amount limit and
   * the self-approval limit, so text in a document cannot talk a proposal
   * under a limit (9.47.12 prompt-injection risk).
   */
  amount?: Money;
}

/** System-side facts about a proposal. Never derived from model output. */
export interface RiskPort {
  assess(tool: string, args: Record<string, unknown>): RiskAssessment;
}

export interface ApprovalRequest {
  runId: string;
  seq: number;
  tool: string;
  args: Record<string, unknown>;
  confidence: number;
  amount: Money;
  /** The model's explanation with real values restored; shown in the inbox only (FR-47.8). */
  rationale: string;
}

export interface ApprovalDecision {
  decision: "APPROVED" | "REJECTED";
  approverUserId: string | null;
  /** "EXPIRED" when BR-47.3 timed the request out. */
  reason?: string;
}

/** Workflow inbox (FR-47.8). Resolves when a human decides or the request expires. */
export interface ApprovalPort {
  request(req: ApprovalRequest): Promise<ApprovalDecision>;
}

export interface KillSwitchPort {
  isHalted(tenantId: string): boolean;
}

export const RUN_TIMEOUT_MS = 24 * 60 * 60 * 1000;

export interface BudgetRequest {
  tenantId: string;
  agentId: string;
  runId: string;
  month: string;
  tokensIn: number;
  tokensOut: number;
  /** A hold older than this (by the database clock) is swept and charged in full: its worker died before settling. */
  ttlMs: number;
}

export interface BudgetHold {
  holdId: string;
  tenantId: string;
  month: string;
}

/**
 * Persisted token budget (FR-47.11): reserve before the model call, settle with the actual usage
 * right after it, so a hold never outlives the call it covers. Settle returns the month's state.
 */
export interface BudgetPort {
  reserve(req: BudgetRequest): Promise<BudgetHold>;
  settle(hold: BudgetHold, actual: ModelUsage): Promise<BudgetState>;
}

/**
 * Persisted audit trail (AC-47.3). `startRun` opens the run as RUNNING before any
 * model call, so a reused run id is refused before a tool can commit; `finishRun`
 * writes the action rows and the final status, FAILED_AFTER_COMMIT when
 * `afterCommitError` is set.
 */
export interface AuditPort {
  startRun(req: RunRequest): Promise<void>;
  finishRun(req: RunRequest, result: RunResult, afterCommitError?: string): Promise<void>;
}

export interface RunDeps {
  provider: ModelProvider;
  tools: ToolPort;
  approvals: ApprovalPort;
  risk: RiskPort;
  killSwitch: KillSwitchPort;
  now: () => string;
  nowMs: () => number;
  pseudonymTtlMs: number;
  audit: AuditPort;
  budgets: BudgetPort;
}

export interface RunRequest {
  runId: string;
  tenantId: string;
  invokerUserId: string;
  userDuties: ReadonlySet<string>;
  agent: AgentVersion;
  evalPassed: boolean;
  policy: ModePolicy;
  selfApprovalLimit: Money;
  runTokenCap: number;
  prompt: string;
}

export interface RunResult {
  runId: string;
  status: RunStatus;
  code?: string;
  reason?: string;
  actions: ActionRecord[];
  tokensIn: number;
  tokensOut: number;
  estimated: boolean;
  budgetState: BudgetState;
  /** Real values, for the UI only. Never stored or sent back to a provider. */
  displayText: string;
  /** Set when the run committed but the audit write then failed. */
  postCommitError?: string;
}

/**
 * Executes one agent run as the body of AgentRunWorkflow (FR-47.4). Start
 * rejections throw AgentError before any state exists. Anything that happens
 * after the run starts ends in a terminal status with an audit row per action.
 * This slice runs one model turn that yields one proposal; the multi-step loop
 * of read-tool calls before the proposal is a later slice (NOT-IMPLEMENTED.md).
 */
const UNAUDITABLE_INPUT = '{"withheld":"AGT-0210"}';

export async function runAgent(
  deps: RunDeps,
  req: RunRequest,
): Promise<RunResult> {
  const cap = req.runTokenCap;
  if (!Number.isSafeInteger(cap) || cap <= 0) {
    throw new Error(`runTokenCap must be a positive safe integer, got ${cap}`);
  }
  const hold = await deps.budgets.reserve({
    tenantId: req.tenantId,
    agentId: req.agent.agentId,
    runId: req.runId,
    month: deps.now().slice(0, 7),
    tokensIn: cap,
    tokensOut: cap,
    ttlMs: RUN_TIMEOUT_MS,
  });
  const release = (): Promise<BudgetState> =>
    deps.budgets.settle(hold, { tokensIn: 0, tokensOut: 0 }).catch(() => "OK");
  try {
    await deps.audit.startRun(req);
  } catch (err) {
    await release();
    throw err;
  }
  let charged: BudgetState | undefined;
  const charge = async (usage: ModelUsage): Promise<void> => {
    charged = await deps.budgets.settle(hold, usage);
  };
  let result: RunResult;
  try {
    result = await executeRun(deps, req, charge);
  } catch (err) {
    const failed: RunResult = {
      runId: req.runId,
      status: "FAILED",
      code: err instanceof AgentError ? err.code : undefined,
      reason: err instanceof Error ? err.message : String(err),
      actions: [],
      tokensIn: 0,
      tokensOut: 0,
      estimated: false,
      budgetState: "OK",
      displayText: "",
    };
    await deps.audit.finishRun(req, failed).catch(() => undefined);
    if (charged === undefined) await release();
    throw err;
  }
  const budgetState = charged ?? (await release());
  result = { ...result, budgetState };
  try {
    await deps.audit.finishRun(req, result);
    return result;
  } catch (err) {
    if (result.status !== "COMMITTED") throw err;
    const message = err instanceof Error ? err.message : String(err);
    logPostCommitFailure(req, "audit write failed after commit", message);
    await deps.audit.finishRun(req, result, message).catch((retry: unknown) => {
      logPostCommitFailure(req, "run left RUNNING after commit", String(retry));
    });
    return { ...result, postCommitError: message };
  }
}

function logPostCommitFailure(req: RunRequest, message: string, error: string): void {
  process.stderr.write(
    `${JSON.stringify({ level: "error", message, service: "agents-ts", tenantId: req.tenantId, runId: req.runId, error })}\n`,
  );
}

async function executeRun(
  deps: RunDeps,
  req: RunRequest,
  charge: (usage: ModelUsage) => Promise<void>,
): Promise<RunResult> {
  if (deps.killSwitch.isHalted(req.tenantId)) {
    throw new AgentError(
      "AGT-0201",
      "agent runs are halted by the kill switch",
    );
  }
  if (!req.evalPassed) {
    throw new AgentError(
      "AGT-0204",
      `agent ${req.agent.agentId} v${req.agent.version} has no passing evaluation`,
    );
  }
  if (req.policy.mode === "OFF")
    throw new Error(`agent ${req.agent.agentId} is OFF for this legal entity`);
  const policyErrors = validatePolicy(req.policy);
  if (policyErrors.length > 0)
    throw new Error(`invalid agent policy: ${policyErrors.join("; ")}`);

  const pseudo = new Pseudonymizer(
    req.tenantId,
    deps.pseudonymTtlMs,
    deps.nowMs,
  );
  const actions: ActionRecord[] = [];
  let status: RunStatus = "PLANNING";
  let code: string | undefined;
  let reason: string | undefined;
  let displayText = "";
  let tokensIn = 0;
  let tokensOut = 0;
  let estimated = false;
  let seq = 0;

  const current = (): RunStatus => status;
  const halted = (): boolean => deps.killSwitch.isHalted(req.tenantId);
  const moveTo = (to: RunStatus): void => {
    status = transition(status, to);
  };
  const snapshot = (): RunResult => ({
    runId: req.runId,
    status,
    code,
    reason,
    actions,
    tokensIn,
    tokensOut,
    estimated,
    budgetState: "OK",
    displayText,
  });
  const finish = (to: RunStatus, c?: string, why?: string): RunResult => {
    moveTo(to);
    code = c;
    reason = why;
    return snapshot();
  };
  const record = (
    tool: string,
    inputRedacted: string,
    output: unknown,
    decision: Decision,
    approverUserId: string | null,
    committedEntityRef: string | null = null,
    why?: string,
  ): void => {
    // seq advances only when the row is built, so a refused row leaves no gap.
    actions.push(
      buildActionRecord({
        runId: req.runId,
        seq: seq + 1,
        toolName: tool,
        inputRedacted,
        outputRedacted: pseudo.redact(JSON.stringify(output) ?? "null"),
        approverUserId,
        decision,
        decidedAt: deps.now(),
        committedEntityRef,
        ...(why !== undefined ? { reason: why } : {}),
      }),
    );
    seq += 1;
  };
  // AC-47.3: once the model has proposed an action, every way out of the run
  // leaves exactly one action row for it. `pending` holds what that row must
  // say if the run ends before an explicit record (kill switch, a port that
  // throws): DENIED with no approver until a decision exists, then the
  // decision and its approver, so a human approval is never lost.
  let pending:
    | {
        tool: string;
        inputRedacted: string;
        decision: Decision;
        approver: string | null;
      }
    | undefined;
  // Set once the tool call has committed, so a row written after that names the commit.
  let committed: { ref: string | null } | undefined;
  const closePending = (output: unknown, why: string): void => {
    if (pending === undefined || actions.length > 0) return;
    try {
      record(
        pending.tool,
        pending.inputRedacted,
        output,
        pending.decision,
        pending.approver,
        committed?.ref ?? null,
        why,
      );
    } catch (err) {
      if (!(err instanceof AgentError && err.code === "AGT-0210")) throw err;
    }
  };
  const killed = (why: string): RunResult => {
    closePending({ killed: why }, "KILLED");
    return finish("KILLED", undefined, why);
  };

  try {
    const prompt = pseudo.redact(req.prompt);
    assertNoPii(prompt);
    // AC-47.6: once halted, no provider call is made at all, not only no tool call.
    if (halted()) return killed("kill switch activated");
    const response = await deps.provider.complete({
      system: req.agent.systemPrompt,
      user: prompt,
      allowedTools: req.agent.allowedTools,
    });

    estimated = response.usage === undefined;
    const usage = response.usage ?? {
      tokensIn: estimateTokens(prompt) + estimateTokens(req.agent.systemPrompt),
      tokensOut: estimateTokens(response.text),
    };
    tokensIn += usage.tokensIn;
    tokensOut += usage.tokensOut;
    // Tokens spent are charged at once, so they count even when the run then breaks its cap or waits for approval.
    await charge(usage);
    assertRunWithinCap(tokensIn + tokensOut, req.runTokenCap);
    displayText = pseudo.restore(response.text);
    moveTo("ACTING");

    const p = response.proposal;
    if (!p) return finish("FAILED", undefined, "model returned no proposal");
    const inputRedacted = pseudo.redact(JSON.stringify(p.args));
    pending = {
      tool: p.tool,
      inputRedacted,
      decision: "DENIED",
      approver: null,
    };
    if (halted()) return killed("kill switch activated");

    const desc = deps.tools.describe(p.tool);
    if (!req.agent.allowedTools.includes(p.tool) || !desc) {
      record(p.tool, inputRedacted, { error: "not_allowed" }, "DENIED", null);
      return finish(
        "FAILED",
        undefined,
        `tool ${p.tool} is not on the allow-list of ${req.agent.agentId}`,
      );
    }
    if (!canCallTool(req.userDuties, desc.requiredDuty)) {
      record(p.tool, inputRedacted, { error: "AGT-0203" }, "DENIED", null);
      return finish(
        "FAILED",
        "AGT-0203",
        `you do not hold duty ${desc.requiredDuty} required by ${p.tool}`,
      );
    }
    if (desc.approvalMode === "none") {
      record(p.tool, inputRedacted, { error: "read_only" }, "DENIED", null);
      return finish(
        "FAILED",
        undefined,
        `tool ${p.tool} is read-only and cannot be proposed`,
      );
    }

    // Restore real values only now, for the approval view and the commit call.
    const args = JSON.parse(pseudo.restore(JSON.stringify(p.args))) as Record<
      string,
      unknown
    >;
    const risk = deps.risk.assess(p.tool, args);
    const amount = risk.amount ?? p.amount;
    // approve_before_commit always goes to a human: v1 has no autonomous posting
    // (9.47.1 out of scope, FR-47.8). AUTO applies only to suggest-mode tools.
    const disposition =
      desc.approvalMode === "approve_before_commit"
        ? "PROPOSE"
        : decideDisposition(req.policy, {
            confidence: p.confidence,
            amount,
            vendorHighRisk: risk.vendorHighRisk,
            sodConflict: risk.sodConflict,
          });

    let decision: Decision = "AUTO";
    let approver: string | null = null;
    if (disposition === "PROPOSE") {
      if (halted()) return killed("kill switch activated");
      moveTo("AWAITING_APPROVAL");
      const d = await deps.approvals.request({
        runId: req.runId,
        seq: seq + 1,
        tool: p.tool,
        args,
        confidence: p.confidence,
        amount,
        rationale: displayText,
      });
      approver = d.approverUserId;
      if (d.decision === "REJECTED") {
        record(
          p.tool,
          inputRedacted,
          { rejected: d.reason ?? "REJECTED" },
          "REJECTED",
          approver,
        );
        return finish(
          "REJECTED",
          d.reason === "EXPIRED" ? "AGT-0208" : undefined,
          d.reason ?? "approver rejected",
        );
      }
      if (
        selfApprovalBlocked({
          approverUserId: approver,
          invokerUserId: req.invokerUserId,
          amount,
          selfApprovalLimit: req.selfApprovalLimit,
        })
      ) {
        record(
          p.tool,
          inputRedacted,
          { rejected: "AGT-0209" },
          "REJECTED",
          approver,
        );
        return finish(
          "REJECTED",
          "AGT-0209",
          "you cannot approve an action from a run you started above the self-approval limit",
        );
      }
      decision = "APPROVED";
    }
    pending = { ...pending, decision, approver };

    if (halted()) return killed("kill switch activated before commit");
    try {
      assertNoPii(inputRedacted);
    } catch (err) {
      if (!(err instanceof AgentError && err.code === "AGT-0210")) throw err;
      record(
        p.tool,
        UNAUDITABLE_INPUT,
        { error: "AGT-0210" },
        "DENIED",
        approver,
      );
      return finish("FAILED", "AGT-0210", err.message);
    }
    const out = await deps.tools.call(p.tool, args);
    if (!out.ok) {
      record(p.tool, inputRedacted, { error: out.code }, decision, approver);
      return finish("FAILED", out.code, out.message);
    }
    committed = { ref: out.ref ?? null };
    record(
      p.tool,
      inputRedacted,
      out.output,
      decision,
      approver,
      committed.ref,
    );
    return finish("COMMITTED");
  } catch (err) {
    // Once the run has started it always ends in a terminal status: a provider,
    // inbox or tool port that throws fails the run instead of leaving it open.
    if (isTerminal(current())) throw err;
    // The tool call committed but its row was not written: the run is COMMITTED,
    // and the fallback row carries the reference instead of the full output.
    if (committed !== undefined) {
      closePending(
        { committed: committed.ref },
        "committed; tool output not recorded",
      );
      return finish(
        "COMMITTED",
        undefined,
        "committed; tool output not recorded",
      );
    }
    // A fixed output: the error message is not scanned for PII and stays off the row.
    closePending(
      { error: "port_error" },
      "run failed before the action was recorded",
    );
    if (err instanceof AgentError)
      return finish("FAILED", err.code, err.message);
    return finish(
      "FAILED",
      undefined,
      err instanceof Error ? err.message : String(err),
    );
  }
}
