import type { KanbanState, Replenishment } from "./api/client";

export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** A positive quantity as the service accepts it: up to 12 integer digits and 6 decimals. */
export const QUANTITY = /^\d{1,12}(\.\d{1,6})?$/;

export function positiveQuantity(value: string): boolean {
  return QUANTITY.test(value) && !/^0+(\.0+)?$/.test(value);
}

/** The first block of an id, enough to tell rows apart on screen. */
export function shortId(id: string | null | undefined): string {
  if (!id) return dash;
  return id.split("-")[0] ?? id;
}

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** Today's date (UTC) as YYYY-MM-DD. */
export function today(): string {
  return new Date().toISOString().slice(0, 10);
}

export const STATE_LABELS: Record<KanbanState, string> = {
  "not-planned": "Not planned",
  planned: "Planned",
  prepared: "Prepared",
  "in-progress": "In progress",
  completed: "Completed",
  emptied: "Emptied",
};

/**
 * The forward step of a card (BR-24.6), or null when there is none. An
 * emptied card returns to Not planned only on a fixed rule (card reuse).
 */
export function nextState(state: KanbanState, replenishment: Replenishment): KanbanState | null {
  switch (state) {
    case "not-planned":
      return "planned";
    case "planned":
      return "prepared";
    case "prepared":
      return "in-progress";
    case "in-progress":
      return "completed";
    case "completed":
      return "emptied";
    case "emptied":
      return replenishment === "fixed" ? "not-planned" : null;
  }
}

/** A card is overdue when its due time has passed and it is not yet completed (FR-24.14). */
export function overdue(due: string | null, state: KanbanState, now: number = Date.now()): boolean {
  if (!due || state === "completed" || state === "emptied") return false;
  const t = Date.parse(due);
  return !Number.isNaN(t) && t < now;
}

export const REPLENISHMENT_LABELS: Record<Replenishment, string> = {
  fixed: "Fixed",
  scheduled: "Scheduled",
  event: "Event",
};
