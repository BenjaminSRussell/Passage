import type { BadgeTone } from "../../shell/ui";
import type { ConflictState, Severity } from "./api/client";

export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** The first block of an id, enough to tell rows apart on screen. */
export function shortId(id: string): string {
  return id.split("-")[0] ?? id;
}

/** "ACCEPTED_RISK" → "Accepted risk". */
export function sentence(value: string): string {
  const words = value.replace(/[_-]+/g, " ").toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** Whole days from now until iso (negative once past). */
export function daysUntil(iso: string, now: Date = new Date()): number {
  return Math.ceil((new Date(iso).getTime() - now.getTime()) / 86_400_000);
}

/** Whole days since iso, at least 0. */
export function ageDays(iso: string, now: Date = new Date()): number {
  return Math.max(0, Math.floor((now.getTime() - new Date(iso).getTime()) / 86_400_000));
}

/** YYYY-MM-DD of now plus days, in UTC. */
export function dateIn(days: number, now: Date = new Date()): string {
  return new Date(now.getTime() + days * 86_400_000).toISOString().slice(0, 10);
}

export const severityTone: Record<Severity, BadgeTone> = { HIGH: "danger", MEDIUM: "warning", LOW: "neutral" };

export const stateTone: Record<ConflictState, BadgeTone> = { OPEN: "warning", MITIGATED: "info", RESOLVED: "success", ACCEPTED_RISK: "neutral" };
