const DAY_MS = 86_400_000;
import { copy } from "./copy";
import { formatLocale } from "./i18n/locale";

const dateFormat = new Intl.DateTimeFormat(formatLocale(), { dateStyle: "medium" });
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
  if (days < 1) return copy.format.today;
  return copy.format.days(days);
}

export function isPastDate(value: string, now: Date = new Date()): boolean {
  const due = parse(value);
  if (Number.isNaN(due.getTime())) return false;
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const startOfDue = DATE_ONLY.test(value) ? due : new Date(due.getFullYear(), due.getMonth(), due.getDate());
  return startOfDue.getTime() < startOfToday.getTime();
}

/**
 * The one set of number formatters. Pages use these instead of toFixed, toLocaleString or string concatenation, so
 * every number follows the person's language. A decimal string from a service is parsed here for display only;
 * amounts stay strings everywhere else.
 */
const asNumber = (value: number | string): number => (typeof value === "number" ? value : Number(value));

export function formatNumber(value: number | string, options: Intl.NumberFormatOptions = {}): string {
  const n = asNumber(value);
  return Number.isFinite(n) ? new Intl.NumberFormat(formatLocale(), options).format(n) : "";
}

export function formatCurrency(value: number | string, currency: string, options: Intl.NumberFormatOptions = {}): string {
  const n = asNumber(value);
  if (!Number.isFinite(n) || !/^[A-Z]{3}$/.test(currency)) return "";
  return new Intl.NumberFormat(formatLocale(), { style: "currency", currency, ...options }).format(n);
}

export function formatPercent(fraction: number, options: Intl.NumberFormatOptions = {}): string {
  return Number.isFinite(fraction) ? new Intl.NumberFormat(formatLocale(), { style: "percent", maximumFractionDigits: 1, ...options }).format(fraction) : "";
}

/** A quantity with its unit of measure, for example 12 kg. Units use their short symbol. */
export function formatQuantity(value: number | string, unit: string, options: Intl.NumberFormatOptions = {}): string {
  const n = formatNumber(value, options);
  return n === "" ? "" : `${n} ${unit}`;
}

export function formatDateTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : new Intl.DateTimeFormat(formatLocale(), { dateStyle: "medium", timeStyle: "short" }).format(date);
}
