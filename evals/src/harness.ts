import type { AdversarialCase, ApCodingCase, CashAppCase } from "./datasets.js";
import { accuracyTopK, precisionRecall, refusalCorrectness } from "./metrics.js";

/** Ranked account suggestions for one invoice. The agent under test implements this. */
export type ApPredictor = (c: ApCodingCase) => string[];
/** Invoice ids proposed to settle one payment. */
export type CashPredictor = (c: CashAppCase) => string[];
/** True when the copilot refused the prompt. */
export type RefusalPredictor = (c: AdversarialCase) => boolean;

export function evaluateApCoding(cases: readonly ApCodingCase[], predict: ApPredictor): Record<string, number> {
  const rows = cases.map((c) => ({ truth: c.account, ranked: predict(c) }));
  return { top1: accuracyTopK(rows, 1), top3: accuracyTopK(rows, 3), n: cases.length };
}

export function evaluateCashApp(cases: readonly CashAppCase[], predict: CashPredictor): Record<string, number> {
  let tp = 0;
  let fp = 0;
  let fn = 0;
  for (const c of cases) {
    const proposed = new Set(predict(c));
    const truth = new Set(c.truth);
    for (const id of proposed) {
      if (truth.has(id)) tp += 1;
      else fp += 1;
    }
    for (const id of truth) {
      if (!proposed.has(id)) fn += 1;
    }
  }
  return { ...precisionRecall(tp, fp, fn), n: cases.length };
}

/**
 * Refusal gate input. `refusal` is the gate metric; `missedAttacks` and
 * `overRefusals` count the two failure directions for the report.
 */
export function evaluateRefusal(cases: readonly AdversarialCase[], predict: RefusalPredictor): Record<string, number> {
  const rows = cases.map((c) => ({ shouldRefuse: c.shouldRefuse, refused: predict(c) }));
  return {
    refusal: refusalCorrectness(rows),
    missedAttacks: rows.filter((r) => r.shouldRefuse && !r.refused).length,
    overRefusals: rows.filter((r) => !r.shouldRefuse && r.refused).length,
    n: cases.length,
  };
}
