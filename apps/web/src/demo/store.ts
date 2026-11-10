// Small in-memory tables and date helpers for the demo handlers.

/** A table keyed by id. Rows are cloned in and out so pages can't mutate the store by accident. */
export function table<T extends { id: string }>(seed: T[]) {
  const rows = new Map<string, T>(seed.map((r) => [r.id, structuredClone(r)]));
  return {
    all(): T[] {
      return [...rows.values()].map((r) => structuredClone(r));
    },
    get(id: string): T | undefined {
      const r = rows.get(id);
      return r === undefined ? undefined : structuredClone(r);
    },
    insert(row: T): T {
      rows.set(row.id, structuredClone(row));
      return structuredClone(row);
    },
    update(id: string, patch: Partial<T> | ((row: T) => T)): T | undefined {
      const current = rows.get(id);
      if (current === undefined) return undefined;
      const next = typeof patch === "function" ? patch(structuredClone(current)) : { ...current, ...patch };
      rows.set(id, structuredClone(next));
      return structuredClone(next);
    },
    remove(id: string): boolean {
      return rows.delete(id);
    },
  };
}

export const newId = (): string => crypto.randomUUID();

/**
 * Deterministic UUIDs for seed data, so cross-domain references line up: uid(kind, n).
 * kind groups an entity type (see company.ts), n numbers rows within it.
 */
export function uid(kind: number, n: number): string {
  // Kind and number lead, so the 8-character short ids some tables show differ row to row.
  return `${kind.toString(16).padStart(3, "0")}${n.toString(16).padStart(5, "0")}-0000-4000-8000-${n.toString(16).padStart(12, "0")}`;
}

const DAY = 86_400_000;

/** A YYYY-MM-DD date offset from today, so the demo always looks current. */
export function day(offset: number): string {
  return new Date(Date.now() + offset * DAY).toISOString().slice(0, 10);
}

/** An RFC 3339 timestamp offset from now by days (fractions allowed). */
export function at(offsetDays: number): string {
  return new Date(Date.now() + offsetDays * DAY).toISOString();
}

/** A decimal string with fixed places, as the services send amounts and quantities. */
export function dec(n: number, places = 2): string {
  return n.toFixed(places);
}

/** Filters rows by every query parameter that names a field (?status=OPEN&customerId=...), ignoring paging keys. */
export function filterBy<T extends object>(rows: T[], query: URLSearchParams, ignore: string[] = ["cursor", "limit", "offset", "page", "pageSize", "sort", "order"]): T[] {
  let out = rows;
  for (const [key, value] of query.entries()) {
    if (value === "" || ignore.includes(key)) continue;
    out = out.filter((r) => {
      const v = (r as Record<string, unknown>)[key];
      return v === undefined || String(v) === value;
    });
  }
  return out;
}
