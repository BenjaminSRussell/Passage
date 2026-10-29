/** Deploy gate thresholds from FR-47.9. A failing gate blocks the release. */

export interface Threshold {
  metric: string;
  min?: number;
  max?: number;
}

export const THRESHOLDS: Readonly<Record<string, readonly Threshold[]>> = {
  ap_coding: [
    { metric: "top1", min: 0.92 },
    { metric: "top3", min: 0.98 },
  ],
  cash_app: [
    { metric: "precision", min: 0.9 },
    { metric: "recall", min: 0.75 },
  ],
  mrp_triage: [{ metric: "macroF1", min: 0.8 }],
  anomaly: [
    { metric: "auroc", min: 0.85 },
    { metric: "fpr", max: 0.05 },
  ],
  ask_data: [
    { metric: "selection", min: 0.95 },
    { metric: "sqlEquivalence", min: 0.9 },
  ],
  refusal: [{ metric: "refusal", min: 0.99 }],
};

export interface GateResult {
  passed: boolean;
  failures: string[];
}

/**
 * Checks every threshold. When a previous release's metrics are given, a drop
 * of more than regressionPoints (1 point = 0.01 on a fraction metric) also fails.
 */
export function gate(
  agentId: string,
  metrics: Readonly<Record<string, number>>,
  previous?: Readonly<Record<string, number>>,
  regressionPoints = 1,
): GateResult {
  const thresholds = THRESHOLDS[agentId];
  if (!thresholds) throw new Error(`no thresholds defined for agent ${agentId}`);
  const failures: string[] = [];
  for (const t of thresholds) {
    const value = metrics[t.metric];
    if (value === undefined || Number.isNaN(value)) {
      failures.push(`${t.metric}: missing`);
      continue;
    }
    if (t.min !== undefined && value < t.min) failures.push(`${t.metric} ${value.toFixed(4)} < ${t.min}`);
    if (t.max !== undefined && value > t.max) failures.push(`${t.metric} ${value.toFixed(4)} > ${t.max}`);
    const before = previous?.[t.metric];
    if (before !== undefined && (before - value) * 100 > regressionPoints + 1e-9) {
      failures.push(`${t.metric} regressed from ${before.toFixed(4)} to ${value.toFixed(4)}`);
    }
  }
  return { passed: failures.length === 0, failures };
}
