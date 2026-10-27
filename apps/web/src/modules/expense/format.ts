// Shared input checks and helpers for the expense pages. The service has the last
// word; these only catch what a user can fix before sending.

export const DAY = /^\d{4}-\d{2}-\d{2}$/;
export const DECIMAL = /^\d+(\.\d+)?$/;
export const CURRENCY = /^[A-Z]{3}$/;

/** Most days one per diem computation takes (the service limit). */
export const MAX_TRIP_DAYS = 60;

/** Today's date as YYYY-MM-DD in UTC. */
export function today(): string {
  return new Date().toISOString().slice(0, 10);
}

/** Every calendar day from `from` to `to` inclusive, or [] when the range is invalid or longer than `max`. */
export function daysBetween(from: string, to: string, max = MAX_TRIP_DAYS): string[] {
  if (!DAY.test(from) || !DAY.test(to) || to < from) return [];
  const out: string[] = [];
  const d = new Date(`${from}T00:00:00Z`);
  const end = new Date(`${to}T00:00:00Z`);
  if (Number.isNaN(d.getTime()) || Number.isNaN(end.getTime())) return [];
  while (d <= end) {
    if (out.length === max) return [];
    out.push(d.toISOString().slice(0, 10));
    d.setUTCDate(d.getUTCDate() + 1);
  }
  return out;
}

/** Splits a list typed as commas, spaces or new lines into trimmed, non-empty entries. */
export function splitList(raw: string): string[] {
  return raw
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

/**
 * Splits a service problem into the field errors a form shows next to its inputs
 * and, for anything else, one page-level failure whose message names the fields.
 */
export function splitProblem(
  problem: { code: string; message: string; fields: Record<string, string> },
  shown: readonly string[],
): { fields: Record<string, string>; failure: { code: string; message: string } | null } {
  const fields: Record<string, string> = {};
  const other: string[] = [];
  for (const [key, msg] of Object.entries(problem.fields)) {
    if (shown.includes(key)) fields[key] = msg;
    else other.push(`${key} ${msg}`);
  }
  if (Object.keys(problem.fields).length === 0) return { fields, failure: { code: problem.code, message: problem.message } };
  return { fields, failure: other.length > 0 ? { code: problem.code, message: other.join("; ") } : null };
}
