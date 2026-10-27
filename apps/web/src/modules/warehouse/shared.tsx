import { useState, type ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { Card, EmptyState, FormField, Input, type BadgeTone } from "../../shell/ui";
import type { Capacity, LineStatus, Location, LocationStatus, WorkStatus } from "./api/client";
import styles from "./pages.module.css";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DECIMAL = /^\d+(\.\d{1,6})?$/;
export const INTEGER = /^\d+$/;

const STORAGE_KEY = "passage.warehouse.id";

function stored(): string {
  try {
    return localStorage.getItem(STORAGE_KEY) ?? "";
  } catch {
    return "";
  }
}

/**
 * The warehouse a page works in: the `warehouse` URL parameter, or the last
 * valid one this browser used. Setting it updates the URL so links keep it.
 */
export function useWarehouse(): [string, (id: string) => void] {
  const [params, setParams] = useSearchParams();
  const [remembered] = useState(stored);
  const value = params.get("warehouse") ?? remembered;
  const set = (id: string) => {
    const next = new URLSearchParams(params);
    next.set("warehouse", id);
    setParams(next, { replace: true });
    if (UUID.test(id.trim())) {
      try {
        localStorage.setItem(STORAGE_KEY, id.trim());
      } catch {
        // Remembering the warehouse is a convenience only.
      }
    }
  };
  return [value, set];
}

export function WarehouseField({ value, onChange, id = "wh-warehouse", error }: { value: string; onChange(id: string): void; id?: string; error?: string }) {
  const invalid = error ?? (value.trim() !== "" && !UUID.test(value.trim()) ? "Enter the warehouse id, a UUID." : undefined);
  return (
    <FormField id={id} label="Warehouse id" hint="The warehouse's UUID, as listed under Organization › Warehouses." error={invalid}>
      {(c) => <Input {...c} className={styles.mono} value={value} onChange={(e) => onChange(e.target.value)} invalid={Boolean(invalid)} spellCheck={false} />}
    </FormField>
  );
}

export function ChooseWarehouse() {
  return (
    <Card>
      <EmptyState icon="box" title="Choose a warehouse" description="Enter the warehouse id above to see its records." headingLevel={3} compact />
    </Card>
  );
}

export function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** The first block of a UUID, enough to tell records apart on screen. */
export function shortId(id: string | undefined): string {
  return id ? id.slice(0, 8) : "—";
}

export function sentence(code: string): string {
  const words = code.toLowerCase().replace(/_/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export const WORK_STATUS_TONES: Record<WorkStatus, BadgeTone> = {
  OPEN: "neutral",
  ASSIGNED: "info",
  IN_PROGRESS: "info",
  COMPLETED: "success",
  PARTIALLY_COMPLETED: "warning",
  CANCELLED: "neutral",
  BLOCKED_NO_LOCATION: "danger",
};

export const LINE_STATUS_TONES: Record<LineStatus, BadgeTone> = { OPEN: "neutral", COMPLETED: "success", SHORT: "warning", CANCELLED: "neutral" };

export const LOCATION_STATUS_TONES: Record<LocationStatus, BadgeTone> = {
  ACTIVE: "success",
  BLOCKED_IN: "warning",
  BLOCKED_OUT: "warning",
  BLOCKED: "danger",
  INACTIVE: "neutral",
};

export function capacityText(c: Capacity): string {
  const parts = [
    c.maxWeight !== undefined ? `≤ ${c.maxWeight} kg` : null,
    c.maxVolume !== undefined ? `≤ ${c.maxVolume} m³` : null,
    c.maxLps !== undefined ? `≤ ${c.maxLps} LP` : null,
    c.maxItems !== undefined ? `≤ ${c.maxItems} items` : null,
  ].filter(Boolean);
  return `${parts.length > 0 ? parts.join(" · ") : "Unlimited"}${c.allowMixedItems ? " · mixed" : ""}`;
}

export function usedText(l: Location): string {
  return `${l.used.weight} kg · ${l.used.volume} m³ · ${l.used.lps} LP`;
}

export function stockText(l: Location): string {
  const entries = Object.entries(l.stock ?? {});
  return entries.length === 0 ? "Empty" : entries.map(([item, qty]) => `${item} ${qty}`).join(", ");
}

export function when(iso: string | undefined): ReactNode {
  return iso ? <time dateTime={iso}>{iso.slice(0, 16).replace("T", " ")}</time> : "—";
}
