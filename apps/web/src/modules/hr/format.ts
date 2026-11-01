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

export const planTypeLabel: Record<string, string> = {
  MEDICAL: "Medical",
  DENTAL: "Dental",
  VISION: "Vision",
  LIFE: "Life",
  RETIREMENT_401K: "Retirement (401k)",
  HSA: "Health savings (HSA)",
  FSA: "Flexible spending (FSA)",
  OTHER: "Other",
};

export const rateBasisLabel: Record<string, string> = { PER_PAY_PERIOD: "per pay period", MONTHLY: "a month" };

export const lifeEventLabel: Record<string, string> = {
  MARRIAGE: "Marriage",
  BIRTH: "Birth",
  ADOPTION: "Adoption",
  DIVORCE: "Divorce",
  LOSS_OF_COVERAGE: "Loss of other coverage",
  OTHER: "Other",
};

export const endReasonLabel: Record<string, string> = {
  TERMINATION: "Termination",
  LIFE_EVENT: "Life event",
  PLAN_END: "Plan end",
};

export const enrollmentStatusLabel: Record<string, { label: string; tone: "neutral" | "success" | "warning" | "danger" | "info" }> = {
  PENDING: { label: "Starts later", tone: "info" },
  ACTIVE: { label: "Active", tone: "success" },
  ENDED: { label: "Ended", tone: "neutral" },
};

/** ISO 4217 minor units that differ from 2 (as platform money.MinorUnits). */
const minorUnits: Record<string, number> = {
  BHD: 3, IQD: 3, JOD: 3, KWD: 3, LYD: 3, OMR: 3, TND: 3,
  BIF: 0, CLP: 0, DJF: 0, GNF: 0, ISK: 0, JPY: 0, KMF: 0, KRW: 0, PYG: 0, RWF: 0, UGX: 0, UYI: 0, VND: 0, VUV: 0, XAF: 0, XOF: 0, XPF: 0,
  CLF: 4, UYW: 4,
};

/** Decimal places a rate in this currency may have. */
export function currencyPlaces(currency: string): number {
  return minorUnits[currency] ?? 2;
}

/** A non-negative money string with at most the currency's places ("12.50"; "1500" for JPY). No floats. */
export function validRate(raw: string, currency: string): boolean {
  const places = currencyPlaces(currency);
  const pattern = places === 0 ? /^\d{1,14}$/ : new RegExp(`^\\d{1,14}(?:\\.\\d{1,${places}})?$`);
  return pattern.test(raw.trim());
}

/** "12.5" in USD shown as "12.50 USD"; the string is padded, never parsed into a float. */
export function formatRate(rate: string, currency: string): string {
  const places = currencyPlaces(currency);
  const [whole, frac = ""] = rate.split(".");
  return places === 0 ? `${whole} ${currency}` : `${whole}.${frac.padEnd(places, "0")} ${currency}`;
}

export const registrationStatusLabel: Record<string, { label: string; tone: "neutral" | "success" | "warning" | "danger" | "info" }> = {
  REQUESTED: { label: "Awaiting approval", tone: "warning" },
  CONFIRMED: { label: "Confirmed", tone: "info" },
  WAITLISTED: { label: "Waitlisted", tone: "warning" },
  ATTENDED: { label: "Attended", tone: "success" },
  NO_SHOW: { label: "No-show", tone: "danger" },
  CANCELED: { label: "Cancelled", tone: "neutral" },
};

/** An RFC 3339 instant from the service shown as "2026-06-01 09:00 UTC" (sessions are scheduled in UTC). */
export function formatUtc(iso?: string | null): string {
  if (!iso) return dash;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const s = new Date(t).toISOString();
  return `${s.slice(0, 10)} ${s.slice(11, 16)} UTC`;
}

/** A datetime-local value ("2026-06-01T09:00") read as UTC, as the service's RFC 3339 ("2026-06-01T09:00:00Z"); null when malformed. */
export function utcFromLocalInput(value: string): string | null {
  const m = /^(\d{4}-\d{2}-\d{2})T(\d{2}):(\d{2})$/.exec(value.trim());
  if (!m) return null;
  const iso = `${m[1]}T${m[2]}:${m[3]}:00Z`;
  return Number.isNaN(Date.parse(iso)) ? null : iso;
}

/** "Expires 24 months after the session" or "Does not expire". */
export function validityLabel(months: number): string {
  if (months <= 0) return "Does not expire";
  return `${months} month${months === 1 ? "" : "s"} after the session ends`;
}

/** Orders two decimal strings ("-1.5", "0.80") without floats: negative when a < b, 0 when equal. */
export function compareDecimal(a: string, b: string): number {
  const places = Math.max(a.split(".")[1]?.length ?? 0, b.split(".")[1]?.length ?? 0);
  const units = (value: string) => {
    const negative = value.trim().startsWith("-");
    const [whole, frac = ""] = value.trim().replace(/^-/, "").split(".");
    const magnitude = BigInt(whole) * 10n ** BigInt(places) + BigInt(frac.padEnd(places, "0") || "0");
    return negative ? -magnitude : magnitude;
  };
  const diff = units(a) - units(b);
  return diff < 0n ? -1 : diff > 0n ? 1 : 0;
}

/** A signed decimal string with at most `places` decimals ("-2.5"), checked without floats. */
export function validSigned(raw: string, places: number): boolean {
  return new RegExp(`^-?\\d{1,8}(?:\\.\\d{1,${places}})?$`).test(raw.trim());
}
