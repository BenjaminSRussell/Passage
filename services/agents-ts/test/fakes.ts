import { addUsage, assertCanStartRun, budgetStatus, type BudgetState, type TokenBudget } from "../src/domain/budget.js";
import type { AgentVersion } from "../src/domain/registry.js";
import { chunkIds, type AiAccessAudit, type AiAccessRow } from "../src/runtime/aiAccessAudit.js";
import type { Money } from "../src/domain/money.js";
import type {
  RiskAssessment,
  ApprovalDecision,
  ApprovalPort,
  ApprovalRequest,
  ModelProvider,
  ModelResponse,
  BudgetPort,
  RunDeps,
  RunRequest,
  ToolDescriptor,
  ToolPort,
  ToolResult,
} from "../src/runtime/engine.js";
import type { AiRouter } from "../src/runtime/aiRouter.js";
import type { AiPolicyGuard } from "../src/runtime/aiPolicyGuard.js";
import { createEgressGuard } from "../src/runtime/egressGuard.js";
import type { OutputGuard } from "../src/runtime/outputGuard.js";

export const allowAllOutput: OutputGuard = { check: async (input) => ({ ok: true, dataClass: "P0", text: input.text, linksRemoved: 0 }), neutralise: (text) => text };

export const TENANT = "00000000-0000-7000-8000-0000000000aa";
export const INVOKER = "00000000-0000-7000-8000-000000000001";
export const APPROVER = "00000000-0000-7000-8000-000000000002";
export const REAL_IBAN = "DE89370400440532013000";

export const usd = (amount: string): Money => ({ amount, currency: "USD" });

export const apAgent: AgentVersion = {
  agentId: "ap_coding",
  version: 3,
  modelId: "claude-test-model",
  systemPrompt: "Propose account and tax code for the vendor invoice.",
  allowedTools: ["depot.ap.invoice.post", "depot.ap.invoice.propose_coding"],
  requiredDuties: ["APInvoicePost"],
};

export const postDescriptor: ToolDescriptor = { requiredDuty: "APInvoicePost", approvalMode: "approve_before_commit" };
/** The AP coding proposal tool: suggest mode, so AUTO may commit it (FR-47.5). */
export const codingDescriptor: ToolDescriptor = { requiredDuty: "APInvoicePost", approvalMode: "suggest" };

export class ScriptedProvider implements ModelProvider {
  readonly seen: Array<{ system: string; user: string; allowedTools: readonly string[]; maxOutputTokens?: number; answerOnly?: boolean }> = [];
  private readonly responses: ModelResponse[];

  constructor(responses: ModelResponse[]) {
    this.responses = [...responses];
  }

  async complete(req: { system: string; user: string; allowedTools: readonly string[]; maxOutputTokens?: number }): Promise<ModelResponse> {
    this.seen.push(req);
    const next = this.responses.shift();
    if (!next) throw new Error("no scripted model response left");
    return next;
  }
}

export const allowAllPolicy: AiPolicyGuard = {
  check: async () => ({ allowed: true, policyVersion: 1, reason: "allowed" }),
  invalidate: () => undefined,
};

export const testGuard = (deps: Omit<Parameters<typeof createEgressGuard>[0], "policy"> = {}) => createEgressGuard({ policy: allowAllPolicy, ...deps });

export const openRouter = (provider: ModelProvider): AiRouter => ({
  plan: async ({ refs }) => ({ routeId: "test", provider, keep: [...refs], excluded: [], restricted: [], classes: refs.map(() => "NONE" as const) }),
});

export const allowAiAccess = (): AiAccessAudit & { rows: AiAccessRow[] } => {
  const rows: AiAccessRow[] = [];
  return {
    rows,
    record: async (_caller, batch) => {
      rows.push(...batch.flatMap(chunkIds));
    },
  };
};

export class FakeTools implements ToolPort {
  readonly calls: Array<{ name: string; args: Record<string, unknown> }> = [];
  private readonly descriptors: Record<string, ToolDescriptor>;
  private readonly result: ToolResult;

  constructor(descriptors: Record<string, ToolDescriptor>, result: ToolResult = { ok: true, output: { ref: "posting-1" }, ref: "posting-1" }) {
    this.descriptors = descriptors;
    this.result = result;
  }

  describe(name: string): ToolDescriptor | undefined {
    return this.descriptors[name];
  }

  async call(name: string, args: Record<string, unknown>): Promise<ToolResult> {
    this.calls.push({ name, args });
    return this.result;
  }
}

export class FakeApprovals implements ApprovalPort {
  readonly requests: ApprovalRequest[] = [];
  private readonly decision: ApprovalDecision;

  constructor(decision: ApprovalDecision) {
    this.decision = decision;
  }

  async request(req: ApprovalRequest): Promise<ApprovalDecision> {
    this.requests.push(req);
    return this.decision;
  }
}

/** In-memory ports for tests that do not exercise persistence: nothing is stored or limited. */
export const testAudit: RunDeps["audit"] = { startRun: async () => undefined, finishRun: async () => undefined, runStatus: async () => "RUNNING" };
export const testBudgets: RunDeps["budgets"] = {
  reserve: async (r) => ({ holdId: "test-hold", tenantId: r.tenantId, month: r.month }),
  settle: async () => "OK",
};

/** An in-memory month budget: a HARD budget refuses a hold, settle charges at once and reports the state. */
export function memoryBudgets(initial: TokenBudget): BudgetPort & { current(): TokenBudget } {
  let budget = initial;
  return {
    current: () => budget,
    reserve: async (r) => {
      assertCanStartRun(budget);
      return { holdId: "memory-hold", tenantId: r.tenantId, month: r.month };
    },
    settle: async (_hold, u): Promise<BudgetState> => {
      budget = addUsage(budget, { ...u, estimated: false });
      return budgetStatus(budget).state;
    },
  };
}

export function makeDeps(parts: {
  provider: ModelProvider;
  tools: ToolPort;
  approvals: ApprovalPort;
  kill?: { on: boolean };
  risk?: RiskAssessment;
  outputGuard?: OutputGuard;
}): RunDeps {
  const { outputGuard } = parts;
  return {
    ...(outputGuard !== undefined ? { outputGuard } : {}),
    provider: parts.provider,
    tools: parts.tools,
    approvals: parts.approvals,
    risk: { assess: () => parts.risk ?? { vendorHighRisk: false, sodConflict: false } },
    killSwitch: { isHalted: () => parts.kill?.on ?? false },
    now: () => "2026-10-21T00:00:00.000Z",
    nowMs: () => Date.UTC(2026, 9, 21),
    pseudonymTtlMs: 86_400_000,
    audit: testAudit,
    budgets: testBudgets,
  };
}

export function makeRequest(over: Partial<RunRequest> = {}): RunRequest {
  return {
    runId: "run-1",
    tenantId: TENANT,
    invokerUserId: INVOKER,
    userDuties: new Set(["APInvoicePost"]),
    agent: apAgent,
    evalPassed: true,
    policy: { mode: "SUGGEST", autoThreshold: 0.95, autoAmountLimit: usd("5000") },
    selfApprovalLimit: usd("1000"),
    runTokenCap: 400_000,
    prompt: `Code invoice INV-7 from Acme. Remit to IBAN ${REAL_IBAN}, total 1200 USD.`,
    ...over,
  };
}

/** A proposal the model would make: by default the IBAN is pseudonymized in its args. */
export function proposal(
  confidence: number,
  amount = "1200",
  tool = "depot.ap.invoice.post",
  args: Record<string, unknown> = { invoiceId: "INV-7", remitTo: "[IBAN-1]" },
): ModelResponse {
  return {
    text: JSON.stringify(args).includes("[IBAN-1]") ? "Proposed posting for INV-7 to the vendor's account [IBAN-1]." : "Proposed posting for INV-7.",
    proposal: {
      tool,
      args,
      confidence,
      amount: usd(amount),
    },
    usage: { tokensIn: 1200, tokensOut: 180 },
  };
}
