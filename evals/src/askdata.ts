import { accuracyTopK } from "./metrics.js";

/**
 * Ask-the-data scoring (FR-47.9): metric-selection accuracy and SQL
 * equivalence. Equivalence is decided on a canonical form of the semantic-layer
 * query, so member order, letter case and empty clauses do not matter, but
 * measures, dimensions, filters, time ranges and limits do.
 */

export interface CubeFilter {
  member: string;
  operator: string;
  values: string[];
}

export interface CubeTimeDimension {
  dimension: string;
  granularity?: string;
  dateRange?: string | [string, string];
}

export interface CubeQuery {
  measures: string[];
  dimensions?: string[];
  timeDimension?: CubeTimeDimension;
  filters?: CubeFilter[];
  limit?: number;
}

export interface AskDataCase {
  id: string;
  question: string;
  expectedMetric: string;
  expectedQuery: CubeQuery;
}

export interface AskDataPrediction {
  metric: string;
  query: CubeQuery;
}

const sortedUnique = (xs: readonly string[]): string[] => [...new Set(xs.map((x) => x.trim().toLowerCase()).filter((x) => x !== ""))].sort();

/** Operators whose values are positional ([from, to]); their order is part of the meaning. */
const ORDERED_OPERATORS = new Set(["indaterange", "notindaterange", "beforedate", "afterdate"]);

/** Canonical JSON of a query: sorted, lower-cased members, empty clauses dropped. */
export function canonicalQuery(q: CubeQuery): string {
  const filters = (q.filters ?? [])
    .map((f) => {
      const operator = f.operator.trim().toLowerCase();
      const values = f.values.map((v) => v.trim());
      return { member: f.member.trim().toLowerCase(), operator, values: ORDERED_OPERATORS.has(operator) ? values : values.sort() };
    })
    .sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)));
  const td = q.timeDimension;
  // A relative range ("Last month") is case-insensitive; an absolute [from, to] keeps its order.
  const range =
    td?.dateRange === undefined ? null : Array.isArray(td.dateRange) ? td.dateRange.map((d) => d.trim()) : td.dateRange.trim().toLowerCase();
  const canonical = {
    measures: sortedUnique(q.measures),
    dimensions: sortedUnique(q.dimensions ?? []),
    timeDimension: td
      ? { dimension: td.dimension.trim().toLowerCase(), granularity: (td.granularity ?? "").toLowerCase(), dateRange: range }
      : null,
    filters,
    limit: q.limit ?? null,
  };
  return JSON.stringify(canonical);
}

export function sqlEquivalent(a: CubeQuery, b: CubeQuery): boolean {
  return canonicalQuery(a) === canonicalQuery(b);
}

/** Metric-selection accuracy (top-1) and SQL equivalence over the case set. */
export function evaluateAskData(
  cases: readonly AskDataCase[],
  predict: (c: AskDataCase) => AskDataPrediction,
): Record<string, number> {
  if (cases.length === 0) return { selection: 0, sqlEquivalence: 0, n: 0 };
  const predictions = cases.map((c) => predict(c));
  const selectionRows = cases.map((c, i) => ({ truth: c.expectedMetric, ranked: [predictions[i]?.metric ?? ""] }));
  const equivalent = cases.filter((c, i) => {
    const p = predictions[i];
    return p !== undefined && sqlEquivalent(c.expectedQuery, p.query);
  }).length;
  return {
    selection: accuracyTopK(selectionRows, 1),
    sqlEquivalence: cases.length === 0 ? 0 : equivalent / cases.length,
    n: cases.length,
  };
}
