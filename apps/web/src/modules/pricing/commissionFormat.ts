import type { CommissionBasis, CommissionRule, RepShare } from "./api/client";

export const COMMISSION_BASIS_LABELS: Record<CommissionBasis, string> = {
  REVENUE: "Revenue (line net)",
  MARGIN: "Margin (line net less cost)",
};

/** "R1 60% · R2 40%" */
export function describeReps(reps: RepShare[]): string {
  return reps.map((r) => `${r.repId} ${r.sharePct}%`).join(" · ");
}

/** A rule's level in the BR-26.12 search order, from most specific (1) to the default (4). */
export function ruleLevel(rule: Pick<CommissionRule, "customerGroup" | "itemGroup">): 1 | 2 | 3 | 4 {
  if (rule.customerGroup && rule.itemGroup) return 1;
  if (rule.customerGroup) return 2;
  if (rule.itemGroup) return 3;
  return 4;
}

export function groupLabel(key: string, all: string): string {
  return key === "" ? all : key;
}
