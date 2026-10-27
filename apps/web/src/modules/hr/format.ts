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

export const absenceUnitLabel: Record<string, string> = { DAYS: "days", HOURS: "hours" };

export const frequencyLabel: Record<string, string> = {
  MONTHLY: "Monthly",
  BIWEEKLY: "Every two weeks",
  ANNUAL: "Once a plan year",
  PER_HOUR_WORKED: "Per hour worked",
};

export const requestStatusLabel: Record<string, { label: string; tone: "neutral" | "success" | "warning" | "danger" | "info" }> = {
  SUBMITTED: { label: "Submitted", tone: "info" },
  APPROVED: { label: "Approved", tone: "success" },
  REJECTED: { label: "Rejected", tone: "danger" },
  CANCELED: { label: "Cancelled", tone: "neutral" },
};

/** A non-negative decimal string with at most `places` decimals ("1.25"), checked without floats. */
export function validDecimal(raw: string, places: number, allowZero = true): boolean {
  const m = new RegExp(`^(\\d{1,8})(?:\\.(\\d{1,${places}}))?$`).exec(raw.trim());
  if (!m) return false;
  return allowZero || /[1-9]/.test(m[1] + (m[2] ?? ""));
}

/** MM-DD of a day every year has (the service refuses 02-29). */
export function validPlanYearStart(raw: string): boolean {
  const m = /^(\d{2})-(\d{2})$/.exec(raw);
  if (!m) return false;
  const month = Number(m[1]);
  const day = Number(m[2]);
  const days = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  return month >= 1 && month <= 12 && day >= 1 && day <= days[month - 1];
}

const iso = (d: Date) => d.toISOString().slice(0, 10);

/** The last whole calendar month before `today` (YYYY-MM-DD), the usual MONTHLY run. */
export function lastWholeMonth(today: string): { start: string; end: string } {
  const [y, m] = today.split("-").map(Number);
  const start = new Date(Date.UTC(y, m - 2, 1));
  const end = new Date(Date.UTC(y, m - 1, 0));
  return { start: iso(start), end: iso(end) };
}

/** The first day of the plan year (MM-DD start) that contains `day` (YYYY-MM-DD). */
export function planYearOf(planYearStart: string, day: string): string {
  const year = Number(day.slice(0, 4));
  const candidate = `${year}-${planYearStart}`;
  return candidate <= day ? candidate : `${year - 1}-${planYearStart}`;
}

/** The YYYY-MM-DD date `days` days later (earlier when negative), in UTC. */
export function shiftDays(day: string, days: number): string {
  const t = Date.parse(`${day}T00:00:00Z`);
  return iso(new Date(t + days * 86_400_000));
}

/** Adds two-decimal strings from the service ("1.25" + "0.50") in whole hundredths, so no float rounding shows. */
export function addFixed2(a: string, b: string): string {
  const cents = (v: string) => {
    const m = /^(-?)(\d+)(?:\.(\d{1,2}))?$/.exec(v.trim());
    if (!m) return 0;
    const n = Number(m[2]) * 100 + Number((m[3] ?? "").padEnd(2, "0"));
    return m[1] ? -n : n;
  };
  const total = cents(a) + cents(b);
  const abs = Math.abs(total);
  return `${total < 0 ? "-" : ""}${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, "0")}`;
}
