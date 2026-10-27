import type { Shift } from "./api/client";

export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"] as const;
/** Monday-first order for forms and calendars. */
export const WEEK_ORDER = [1, 2, 3, 4, 5, 6, 0] as const;

const DAY_MS = 86_400_000;

/** The first block of an id, enough to tell rows apart on screen. */
export function shortId(id: string): string {
  return id.split("-")[0] ?? id;
}

/** "2026-10-26 08:00 UTC". Scheduling stores and shows UTC. */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** 150 → "2 h 30 min"; 45 → "45 min"; 120 → "2 h". */
export function formatMinutes(minutes: number): string {
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  if (h === 0) return `${m} min`;
  return m === 0 ? `${h} h` : `${h} h ${m} min`;
}

/**
 * An exact decimal amount with its currency, padded to at least two decimals
 * and never rounded: "45.5", "EUR" → "45.50 EUR"; "30.125" stays "30.125".
 */
export function formatAmount(value: string, currency: string): string {
  const [whole, fraction = ""] = value.split(".");
  return `${whole}.${fraction.padEnd(2, "0")} ${currency}`;
}

export function hours(minutes: number): string {
  return (minutes / 60).toFixed(1);
}

/** Minutes after midnight → "08:00"; 1440 → "24:00". */
export function clock(minute: number): string {
  return `${String(Math.floor(minute / 60)).padStart(2, "0")}:${String(minute % 60).padStart(2, "0")}`;
}

/** "08:30" → 510, "24:00" → 1440; null when malformed. */
export function parseClock(value: string): number | null {
  const m = /^(\d{2}):(\d{2})$/.exec(value);
  if (!m) return null;
  const minutes = Number(m[1]) * 60 + Number(m[2]);
  return Number(m[2]) < 60 && minutes <= 1440 ? minutes : null;
}

/** "Mon–Fri 08:00–16:00" style summary of a weekly calendar. */
export function describeShifts(shifts: Shift[]): string {
  if (shifts.length === 0) return dash;
  return WEEK_ORDER.filter((d) => shifts.some((s) => s.weekday === d))
    .map((d) => {
      const spans = shifts.filter((s) => s.weekday === d).map((s) => `${clock(s.startMinute)}–${clock(s.endMinute)}`);
      return `${WEEKDAYS[d].slice(0, 3)} ${spans.join(", ")}`;
    })
    .join(" · ");
}

/** Today's UTC date as YYYY-MM-DD. */
export function today(now: Date = new Date()): string {
  return now.toISOString().slice(0, 10);
}

/** UTC midnight of a YYYY-MM-DD date plus days, as an ISO instant. */
export function dayStart(date: string, plusDays = 0): string {
  return new Date(Date.parse(`${date}T00:00:00Z`) + plusDays * DAY_MS).toISOString().replace(".000Z", "Z");
}

/** ISO instant → value for <input type="datetime-local"> in UTC ("2026-10-26T08:00"). */
export function toUtcInput(iso: string): string {
  return new Date(iso).toISOString().slice(0, 16);
}

/** <input type="datetime-local"> value read as UTC → ISO instant; null when empty or malformed. */
export function fromUtcInput(value: string): string | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return null;
  const ms = Date.parse(`${value}:00Z`);
  return Number.isNaN(ms) ? null : new Date(ms).toISOString().replace(".000Z", "Z");
}

/** "heuristic" → "Heuristic", "cp-sat" → "Cp sat". */
export function sentence(value: string): string {
  const words = value.replace(/[_-]+/g, " ").toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}
