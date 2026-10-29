/** Token usage for one run, as billed by the provider or as recorded on agent_run. */
export interface TokenUsage {
  tokensIn: number;
  tokensOut: number;
}

export interface RunUsageLine {
  runId: string;
  billed: TokenUsage;
  recorded: TokenUsage;
  /** BR-47.4: true when the recorded counts were estimated (provider reported no usage). */
  estimated: boolean;
}

export type VarianceKind = "within_tolerance" | "estimated_variance" | "variance";

export interface RunReconciliation {
  runId: string;
  kind: VarianceKind;
  /** Relative variance on the larger of the two totals, 0 when both are zero. */
  variance: number;
}

export interface ReconciliationReport {
  checked: number;
  withinTolerance: number;
  estimatedVariance: number;
  variance: number;
  /** Fraction of all checked runs whose variance is not explained by estimation. */
  unestimatedVarianceRate: number;
  lines: RunReconciliation[];
}

/** Tolerance from 9.47.9 and 9.47.16: billed and recorded tokens agree within 1%. */
export const RECONCILE_TOLERANCE = 0.01;

function relativeVariance(billed: number, recorded: number): number {
  const base = Math.max(Math.abs(billed), Math.abs(recorded));
  if (base === 0) return 0;
  return Math.abs(recorded - billed) / base;
}

/** Variance of a run as the larger of the input and output relative differences. */
export function runVariance(billed: TokenUsage, recorded: TokenUsage): number {
  return Math.max(relativeVariance(billed.tokensIn, recorded.tokensIn), relativeVariance(billed.tokensOut, recorded.tokensOut));
}

/**
 * Compares provider-billed tokens with recorded tokens per run. A run outside
 * tolerance is `estimated_variance` when its counts were estimated (expected,
 * flagged for review) and `variance` otherwise (the job alert condition).
 */
export function reconcileRuns(lines: RunUsageLine[], tolerance: number = RECONCILE_TOLERANCE): ReconciliationReport {
  const out: RunReconciliation[] = lines.map((line) => {
    const variance = runVariance(line.billed, line.recorded);
    let kind: VarianceKind = "within_tolerance";
    if (variance > tolerance) kind = line.estimated ? "estimated_variance" : "variance";
    return { runId: line.runId, kind, variance };
  });
  const count = (k: VarianceKind) => out.filter((l) => l.kind === k).length;
  const variance = count("variance");
  return {
    checked: out.length,
    withinTolerance: count("within_tolerance"),
    estimatedVariance: count("estimated_variance"),
    variance,
    unestimatedVarianceRate: out.length === 0 ? 0 : variance / out.length,
    lines: out,
  };
}
