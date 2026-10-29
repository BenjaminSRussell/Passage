import type { AskDataCase, AskDataPrediction, CubeQuery } from "./askdata.js";

/**
 * Deterministic ask-the-data baseline (FR-47.3, FR-47.9). It maps a question to
 * a semantic-layer metric and a Cube query by lexicon rules: metric words, entity
 * codes, a "by entity" grouping, and a single month with year. It is the floor
 * the copilot predictor must beat in CI; it is not the copilot. Questions it
 * cannot read fall back to an empty metric, which scores as a miss.
 */

/** Ordered: the first lexicon entry with a word in the question wins. */
const METRIC_LEXICON: ReadonlyArray<{ metric: string; words: RegExp }> = [
  { metric: "revenue", words: /\b(revenue|sales)\b/ },
  { metric: "cost", words: /\b(cost|costs|expense|expenses|spend)\b/ },
  { metric: "headcount", words: /\b(headcount|employees)\b/ },
  { metric: "dso_days", words: /\bdso\b/ },
];

/** Legal-entity codes the semantic layer knows. Matched as whole, upper-case tokens. */
export const ENTITY_CODES = ["USMF", "DEMF", "GBSI"] as const;

const MONTHS = [
  "january",
  "february",
  "march",
  "april",
  "may",
  "june",
  "july",
  "august",
  "september",
  "october",
  "november",
  "december",
];

export function askDataBaseline(c: AskDataCase): AskDataPrediction {
  const text = c.question;
  const lower = text.toLowerCase();
  const metric = METRIC_LEXICON.find((m) => m.words.test(lower))?.metric ?? "";

  const query: CubeQuery = { measures: [metric] };

  if (/\bby entity\b/.test(lower)) {
    query.dimensions = ["legal_entity.code"];
  }

  const codes = ENTITY_CODES.filter((code) => new RegExp(`\\b${code}\\b`).test(text));
  if (codes.length > 0) {
    query.filters = [{ member: "legal_entity.code", operator: "equals", values: [...codes] }];
  }

  const period = monthYear(lower);
  if (period) {
    const { year, month } = period;
    const last = new Date(Date.UTC(year, month, 0)).getUTCDate();
    const mm = String(month).padStart(2, "0");
    query.timeDimension = {
      dimension: "ledger.posting_date",
      granularity: "month",
      dateRange: [`${year}-${mm}-01`, `${year}-${mm}-${String(last).padStart(2, "0")}`],
    };
  }

  return { metric, query };
}

/** "<month name> <yyyy>" as a 1-based month, or undefined. */
function monthYear(lower: string): { year: number; month: number } | undefined {
  const m = /\b([a-z]+)\s+(\d{4})\b/.exec(lower);
  if (!m || m[1] === undefined || m[2] === undefined) return undefined;
  const idx = MONTHS.indexOf(m[1]);
  if (idx < 0) return undefined;
  return { year: Number(m[2]), month: idx + 1 };
}
