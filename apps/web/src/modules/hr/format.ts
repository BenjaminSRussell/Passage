export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** Today's date as YYYY-MM-DD in the viewer's calendar. */
export function todayIso(now: Date = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}

/** The calendar day of a UTC range bound ("2026-03-16T00:00:00Z" -> "2026-03-16"). */
export function dayOf(iso?: string | null): string {
  return iso ? iso.slice(0, 10) : dash;
}

/** The last day inside a half-open range [from, to): the day before `to`. */
export function lastDay(validTo?: string | null): string {
  if (!validTo) return "Open-ended";
  const t = Date.parse(validTo);
  return Number.isNaN(t) ? validTo : new Date(t - 86_400_000).toISOString().slice(0, 10);
}

export const workerTypeLabel: Record<string, string> = { EMPLOYEE: "Employee", CONTRACTOR: "Contractor" };

export const employmentTypeLabel: Record<string, string> = {
  FULL_TIME: "Full time",
  PART_TIME: "Part time",
  TEMP: "Temporary",
  CONTRACTOR: "Contractor",
};

export const assignmentKindLabel: Record<string, string> = {
  PRIMARY: "Primary",
  CONCURRENT: "Concurrent",
  GLOBAL_HOST: "Global host",
};

/** A decimal FTE string such as "0.50" with at most two decimals, from min (in hundredths) to 2.00 (FR-42.3). Integer arithmetic only. */
export function validFte(raw: string, minHundredths: number): boolean {
  const m = /^(\d)(?:\.(\d{1,2}))?$/.exec(raw.trim());
  if (!m) return false;
  const hundredths = Number(m[1]) * 100 + Number((m[2] ?? "").padEnd(2, "0"));
  return hundredths >= minHundredths && hundredths <= 200;
}
