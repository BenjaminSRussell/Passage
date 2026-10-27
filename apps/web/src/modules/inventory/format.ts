import type { BadgeTone } from "../../shell/ui";
import type { JournalStatus, JournalType } from "./api/client";

export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DECIMAL = /^-?\d+(\.\d+)?$/;

export const typeLabels: Record<JournalType, string> = {
  MOVEMENT: "Movement",
  ADJUSTMENT: "Adjustment",
  OPENING_BALANCE: "Opening balance",
  COUNT: "Count",
};

const statusTones: Record<JournalStatus, BadgeTone> = {
  DRAFT: "neutral",
  SUBMITTED: "info",
  APPROVED: "info",
  POSTING: "info",
  POSTED: "success",
  REJECTED: "danger",
  CANCELLED: "neutral",
};

export function statusTone(status: JournalStatus): BadgeTone {
  return statusTones[status] ?? "neutral";
}

/** "DRAFT" → "Draft", "OPENING_BALANCE" → "Opening balance". */
export function sentence(value: string): string {
  const words = value.toLowerCase().replace(/_/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** The first block of an id, enough to tell journals apart on screen. */
export function shortId(id: string): string {
  return id.split("-")[0] ?? id;
}

/** "site S1 · warehouse W1" in canonical dimension order. */
export function describeDimensions(dims: Partial<Record<string, string>>, order: readonly string[]): string {
  const parts = order.filter((name) => dims[name]).map((name) => `${sentence(name)} ${dims[name]}`);
  return parts.length > 0 ? parts.join(" · ") : dash;
}

export function formatDateTime(iso?: string): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toISOString().slice(0, 16).replace("T", " ") + " UTC";
}

export function today(): string {
  return new Date().toISOString().slice(0, 10);
}

/** "-4" stays "-4"; "4" becomes "+4", so a variance reads as a change. */
export function signed(value: string): string {
  return value.startsWith("-") || Number(value) === 0 ? value : `+${value}`;
}
