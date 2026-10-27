// Step telemetry for the KPI layer (FR-32.11, §6.32.14 "Step timing by user and flow").
// Pure: records in, rollups out. The flow runner produces one StepTiming per evaluated step.
// Per-user breakdowns are personal data (§6.32.20), so they are produced only when the caller
// asks for them, and the duty check happens at the call site.

export interface StepTiming {
  flowCode: string;
  stepId: string;
  userId: string;
  deviceId: string;
  durationMs: number;
  failed: boolean; // the step ended in a validation or error outcome
  at: number; // epoch ms
}

export interface StepRollup {
  flowCode: string;
  stepId: string;
  userId?: string; // present only for per-user rollups
  count: number;
  errors: number;
  medianS: number;
  p95S: number;
}

/** Retention bounds in days (§6.32.13 telemetry_retention_days). */
export const TELEMETRY_RETENTION_DEFAULT_DAYS = 90;
export const TELEMETRY_RETENTION_MIN_DAYS = 30;
export const TELEMETRY_RETENTION_MAX_DAYS = 365;

const DAY_MS = 86_400_000;

/** Validates one timing before it is recorded. Returns null when valid, else the reason. */
export function validateTiming(t: StepTiming): string | null {
  if (!t.flowCode || !t.stepId) return "flow and step are required";
  if (!Number.isFinite(t.durationMs) || t.durationMs < 0) return "duration must be a finite, non-negative number";
  if (!Number.isFinite(t.at)) return "timestamp must be finite";
  return null;
}

/**
 * Nearest-rank percentile over sorted values, percent in 1..100. Integer arithmetic keeps
 * the rank exact (0.95 * 100 is not). Empty input returns 0.
 */
export function nearestRank(sorted: readonly number[], percent: number): number {
  if (sorted.length === 0) return 0;
  const rank = Math.ceil((percent * sorted.length) / 100);
  return sorted[Math.min(sorted.length, Math.max(1, rank)) - 1];
}

/**
 * Rolls timings up by flow and step, optionally by user too. Durations are reported in
 * seconds with millisecond precision, so the median and p95 are nearest-rank values from the
 * recorded samples (no interpolation).
 */
export function rollup(records: readonly StepTiming[], opts: { byUser?: boolean } = {}): StepRollup[] {
  const groups = new Map<string, { key: Omit<StepRollup, "count" | "errors" | "medianS" | "p95S">; durations: number[]; errors: number }>();
  for (const r of records) {
    const userId = opts.byUser ? r.userId : undefined;
    const id = `${r.flowCode}\u0000${r.stepId}\u0000${userId ?? ""}`;
    let g = groups.get(id);
    if (!g) {
      g = { key: { flowCode: r.flowCode, stepId: r.stepId, ...(userId !== undefined ? { userId } : {}) }, durations: [], errors: 0 };
      groups.set(id, g);
    }
    g.durations.push(r.durationMs);
    if (r.failed) g.errors++;
  }
  const out: StepRollup[] = [];
  for (const g of groups.values()) {
    const sorted = [...g.durations].sort((a, b) => a - b);
    out.push({
      ...g.key,
      count: sorted.length,
      errors: g.errors,
      medianS: round3(nearestRank(sorted, 50) / 1000),
      p95S: round3(nearestRank(sorted, 95) / 1000),
    });
  }
  return out.sort((a, b) => (a.flowCode + a.stepId + (a.userId ?? "")).localeCompare(b.flowCode + b.stepId + (b.userId ?? "")));
}

function round3(n: number): number {
  return Math.round(n * 1000) / 1000;
}

/** Keeps timings newer than the retention window. Days outside 30–365 are rejected. */
export function purgeTelemetry(
  records: readonly StepTiming[],
  now: number,
  retentionDays = TELEMETRY_RETENTION_DEFAULT_DAYS,
): { kept: StepTiming[]; removed: number } | { error: string } {
  if (!Number.isInteger(retentionDays) || retentionDays < TELEMETRY_RETENTION_MIN_DAYS || retentionDays > TELEMETRY_RETENTION_MAX_DAYS) {
    return { error: `retention must be a whole number of days from ${TELEMETRY_RETENTION_MIN_DAYS} to ${TELEMETRY_RETENTION_MAX_DAYS}` };
  }
  const cutoff = now - retentionDays * DAY_MS;
  const kept = records.filter((r) => r.at >= cutoff);
  return { kept, removed: records.length - kept.length };
}

/**
 * Appends a timing to a device-side log and drops records past the retention window
 * (FR-32.11, 90 days by default). An invalid timing or retention leaves the log unchanged and
 * returns the reason.
 */
export function appendTiming(
  log: readonly StepTiming[],
  t: StepTiming,
  now: number,
  retentionDays = TELEMETRY_RETENTION_DEFAULT_DAYS,
): { log: StepTiming[] } | { error: string } {
  const invalid = validateTiming(t);
  if (invalid) return { error: invalid };
  const purged = purgeTelemetry(log, now, retentionDays);
  if ("error" in purged) return purged;
  return { log: [...purged.kept, t] };
}
