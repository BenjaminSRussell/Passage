import { buildActionRecord, type ActionRecord, type Decision } from "../domain/audit.js";
import { selfApprovalBlocked, canCallTool } from "../domain/authz.js";
import {
  addUsage,
  assertCanStartRun,
  assertRunWithinCap,
  budgetStatus,
  estimateTokens,
  type BudgetState,
  type TokenBudget,
} from "../domain/budget.js";
import type { Money } from "../domain/money.js";
import { decideDisposition, validatePolicy, type ModePolicy } from "../domain/policy.js";
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
  complete(req: { system: string; user: string; allowedTools: readonly string[] }): Promise<ModelResponse>;
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

export interface RunDeps {
  provider: ModelProvider;
  tools: ToolPort;
  approvals: ApprovalPort;
  risk: RiskPort;
  killSwitch: KillSwitchPort;
  now: () => string;
  nowMs: () => number;
  pseudonymTtlMs: number;
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
  budget: TokenBudget;
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
}

/**
 * Executes one agent run as the body of AgentRunWorkflow (FR-47.4). Start
 * rejections throw AgentError before any state exists. Anything that happens
 * after the run starts ends in a terminal status with an audit row per action.
 * This slice runs one model turn that yields one proposal; the multi-step loop
 * of read-tool calls before the proposal is a later slice (NOT-IMPLEMENTED.md).
 */
export async function runAgent(deps: RunDeps, req: RunRequest): Promise<RunResult> {
  if (deps.killSwitch.isHalted(req.tenantId)) {
    throw new AgentError("AGT-0201", "agent runs are halted by the kill switch");
  }
  assertCanStartRun(req.budget);
  if (!req.evalPassed) {
    throw new AgentError("AGT-0204", `agent ${req.agent.agentId} v${req.agent.version} has no passing evaluation`);
  }
  if (req.policy.mode === "OFF") throw new Error(`agent ${req.agent.agentId} is OFF for this legal entity`);
  const policyErrors = validatePolicy(req.policy);
  if (policyErrors.length > 0) throw new Error(`invalid agent policy: ${policyErrors.join("; ")}`);

  const pseudo = new Pseudonymizer(req.tenantId, deps.pseudonymTtlMs, deps.nowMs);
  const actions: ActionRecord[] = [];
  let status: RunStatus = "PLANNING";
  let code: string | undefined;
  let reason: string | undefined;
  let displayText = "";
  let tokensIn = 0;
  let tokensOut = 0;
  let estimated = false;
  let budget = req.budget;
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
    budgetState: budgetStatus(budget).state,
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
  ): void => {
    seq += 1;
    actions.push(
      buildActionRecord({
        runId: req.runId,
        seq,
        toolName: tool,
        inputRedacted,
        outputRedacted: pseudo.redact(JSON.stringify(output) ?? "null"),
        approverUserId,
        decision,
        decidedAt: deps.now(),
        committedEntityRef,
      }),
    );
  };

  try {
    const prompt = pseudo.redact(req.prompt);
    assertNoPii(prompt);
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
    // Tokens spent count against the budget even when the run then breaks its cap.
    budget = addUsage(budget, { tokensIn: usage.tokensIn, tokensOut: usage.tokensOut, estimated });
    assertRunWithinCap(tokensIn + tokensOut, req.runTokenCap);
    displayText = pseudo.restore(response.text);
    moveTo("ACTING");

    const p = response.proposal;
    if (!p) return finish("FAILED", undefined, "model returned no proposal");
    if (halted()) return finish("KILLED", undefined, "kill switch activated");

    const inputRedacted = pseudo.redact(JSON.stringify(p.args));
    const desc = deps.tools.describe(p.tool);
    if (!req.agent.allowedTools.includes(p.tool) || !desc) {
      record(p.tool, inputRedacted, { error: "not_allowed" }, "DENIED", null);
      return finish("FAILED", undefined, `tool ${p.tool} is not on the allow-list of ${req.agent.agentId}`);
    }
    if (!canCallTool(req.userDuties, desc.requiredDuty)) {
      record(p.tool, inputRedacted, { error: "AGT-0203" }, "DENIED", null);
      return finish("FAILED", "AGT-0203", `you do not hold duty ${desc.requiredDuty} required by ${p.tool}`);
    }
    if (desc.approvalMode === "none") {
      return finish("FAILED", undefined, `tool ${p.tool} is read-only and cannot be proposed`);
    }

    // Restore real values only now, for the approval view and the commit call.
    const args = JSON.parse(pseudo.restore(JSON.stringify(p.args))) as Record<string, unknown>;
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
      if (halted()) return finish("KILLED", undefined, "kill switch activated");
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
        record(p.tool, inputRedacted, { rejected: d.reason ?? "REJECTED" }, "REJECTED", approver);
        return finish("REJECTED", d.reason === "EXPIRED" ? "AGT-0208" : undefined, d.reason ?? "approver rejected");
      }
      if (selfApprovalBlocked({ approverUserId: approver, invokerUserId: req.invokerUserId, amount, selfApprovalLimit: req.selfApprovalLimit })) {
        record(p.tool, inputRedacted, { rejected: "AGT-0209" }, "REJECTED", approver);
        return finish("REJECTED", "AGT-0209", "you cannot approve an action from a run you started above the self-approval limit");
      }
      decision = "APPROVED";
    }

    if (halted()) return finish("KILLED", undefined, "kill switch activated before commit");
    const out = await deps.tools.call(p.tool, args);
    if (!out.ok) {
      record(p.tool, inputRedacted, { error: out.code }, decision, approver);
      return finish("FAILED", out.code, out.message);
    }
    record(p.tool, inputRedacted, out.output, decision, approver, out.ref ?? null);
    return finish("COMMITTED");
  } catch (err) {
    // Once the run has started it always ends in a terminal status: a provider,
    // inbox or tool port that throws fails the run instead of leaving it open.
    if (isTerminal(current())) throw err;
    if (err instanceof AgentError) return finish("FAILED", err.code, err.message);
    return finish("FAILED", undefined, err instanceof Error ? err.message : String(err));
  }
}
