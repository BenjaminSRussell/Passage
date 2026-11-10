const DAY_MS = 86_400_000;
const dateFormat = new Intl.DateTimeFormat("en", { dateStyle: "medium" });
const DATE_ONLY = /^(\d{4})-(\d{2})-(\d{2})$/;

function parse(value: string): Date {
  const match = DATE_ONLY.exec(value);
  return match ? new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3])) : new Date(value);
}

export function formatDate(value: string): string {
  const date = parse(value);
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
  const due = parse(value);
  if (Number.isNaN(due.getTime())) return false;
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const startOfDue = DATE_ONLY.test(value) ? due : new Date(due.getFullYear(), due.getMonth(), due.getDate());
  return startOfDue.getTime() < startOfToday.getTime();
}
