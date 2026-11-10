import { lookupProblem, type ProblemCatalogEntry } from "./catalog";

/**
 * The parts of a failure that the shell shows: a stable code, the server's
 * RFC 9457 title and detail, and a request id for support.
 */
export interface NormalizedProblem {
  code?: string;
  title?: string;
  detail?: string;
  requestId?: string;
  status?: number;
}

type Bag = Record<string, unknown>;

const isBag = (value: unknown): value is Bag => typeof value === "object" && value !== null;

function text(...values: unknown[]): string | undefined {
  for (const value of values) {
    if (typeof value === "string" && value.trim() !== "") return value;
  }
  return undefined;
}

/**
 * Reads any failure the modules hold: an `Error` subclass with `code`, a
 * module `{ message, code }` pair, a raw problem body, an error that wraps
 * one in `problem`, a plain string, or nothing.
 */
export function normalizeProblem(input: unknown): NormalizedProblem {
  if (input === null || input === undefined) return {};
  if (typeof input === "string") return input.trim() === "" ? {} : { detail: input };
  if (!isBag(input)) return {};
  const inner = isBag(input.problem) ? input.problem : {};
  const status = typeof input.status === "number" ? input.status : typeof inner.status === "number" ? inner.status : undefined;
  return {
    code: text(input.code, inner.code),
    title: text(input.title, inner.title),
    detail: text(input.detail, inner.detail, input.message),
    requestId: text(input.requestId, input.traceId, inner.requestId, inner.traceId),
    status,
  };
}

export interface ProblemView {
  problem: NormalizedProblem;
  entry?: ProblemCatalogEntry;
  /** The sentence(s) to show: the catalog message and action, or the server's own words. */
  summary: string;
}

const GENERIC = "Something went wrong.";

/** Works out what to show for a failure. */
export function describeProblemForUser(input: unknown, fallback?: string): ProblemView {
  const problem = normalizeProblem(input);
  const entry = lookupProblem(problem.code);
  const summary = entry ? `${entry.message} ${entry.action}` : (problem.detail ?? problem.title ?? fallback ?? GENERIC);
  return { problem, entry, summary };
}

/**
 * Plain text for a failure, for places that need a string (a hint, a
 * confirmation line). Prefer `ProblemMessage`, which also keeps the code
 * and request id available for support.
 */
export function problemSummary(input: unknown, fallback?: string): string {
  return describeProblemForUser(input, fallback).summary;
}
