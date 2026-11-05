import { AgentError } from "../errors.js";

export type BudgetState = "OK" | "SOFT" | "HARD";

export interface TokenBudget {
  limitIn: number;
  limitOut: number;
  usedIn: number;
  usedOut: number;
  /** Soft alert threshold in percent (ai.budget.soft_pct, default 80). */
  softPct: number;
}

export interface BudgetStatus {
  state: BudgetState;
  /** Highest consumed percentage across input and output. */
  pct: number;
}

/** A zero limit is a zero budget: any use is HARD. */
export function budgetStatus(b: TokenBudget): BudgetStatus {
  const ratio = (used: number, limit: number): number => {
    if (limit <= 0) return used > 0 ? 100 : 0;
    return (used / limit) * 100;
  };
  const pct = Math.max(ratio(b.usedIn, b.limitIn), ratio(b.usedOut, b.limitOut));
  if (pct >= 100) return { state: "HARD", pct };
  if (pct >= b.softPct) return { state: "SOFT", pct };
  return { state: "OK", pct };
}

export interface Usage {
  tokensIn: number;
  tokensOut: number;
  /** True when the provider reported no usage and tokens were estimated (BR-47.4). */
  estimated: boolean;
}

export function addUsage(b: TokenBudget, u: Usage): TokenBudget {
  if (u.tokensIn < 0 || u.tokensOut < 0) throw new Error("usage cannot be negative");
  return { ...b, usedIn: b.usedIn + u.tokensIn, usedOut: b.usedOut + u.tokensOut };
}

/**
 * BR-47.4: four characters per token when the provider reports no usage. It also sizes the output
 * allowance of a call (outputBudgetForCall). It undercounts CJK and emoji text several times over,
 * so it never bounds the input: assertPromptWithinCap does that by byte length.
 */
export function estimateTokens(text: string): number {
  return Math.ceil(text.length / 4);
}

/**
 * Refuses (AGT-0202) a call whose input could exceed the run's input hold. A token covers at
 * least one UTF-8 byte, so the byte length of everything sent (system text, prompt, tool schemas)
 * is an upper bound on input tokens whatever the script.
 */
export function assertPromptWithinCap(parts: readonly string[], cap: number): void {
  const bytes = parts.reduce((sum, part) => sum + Buffer.byteLength(part, "utf8"), 0);
  if (bytes > cap) {
    throw new AgentError("AGT-0202", `prompt is ${bytes} bytes, more than the ${cap} input tokens the run cap leaves`);
  }
}

/** FR-47.11 per-run cap. Throws AGT-0202 style rejection when a run would exceed it. */
export function assertRunWithinCap(runTokens: number, cap: number): void {
  if (runTokens > cap) {
    throw new AgentError("AGT-0202", `run used ${runTokens} tokens, cap is ${cap}`);
  }
}

/**
 * Output tokens a call may generate: what the run cap leaves after tokens already used and the
 * estimated prompt. Refuses (AGT-0202) when none is left, which also stops any further call of a
 * run that has already spent its cap.
 */
export function outputBudgetForCall(cap: number, usedTokens: number, promptTokens: number): number {
  const remaining = cap - usedTokens - promptTokens;
  if (remaining < 1) {
    throw new AgentError("AGT-0202", `run cap ${cap} leaves no room for a call: ${usedTokens} used, prompt estimated at ${promptTokens}`);
  }
  return remaining;
}

/** Run start check: a HARD budget rejects new runs (AC-47.7). */
export function assertCanStartRun(b: TokenBudget): void {
  const { state, pct } = budgetStatus(b);
  if (state === "HARD") {
    throw new AgentError("AGT-0202", `monthly token budget exhausted at ${pct.toFixed(1)}%`);
  }
}
