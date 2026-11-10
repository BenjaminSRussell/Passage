const DAY_MS = 86_400_000;
const dateFormat = new Intl.DateTimeFormat("en", { dateStyle: "medium", timeZone: "UTC" });

export function formatDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : dateFormat.format(date);
}

export function formatAge(value: string, now: Date = new Date()): string {
  const then = new Date(value).getTime();
  if (Number.isNaN(then)) return "";
  const days = Math.floor((now.getTime() - then) / DAY_MS);
  if (days < 1) return "Today";
  return days === 1 ? "1 day" : `${days} days`;
}

export function isPastDate(value: string, now: Date = new Date()): boolean {
  const due = new Date(value).getTime();
  return !Number.isNaN(due) && due + DAY_MS <= now.getTime();
}
