import type { LookupEntityKey } from "../lookup/entities";
import type { PreferenceStorage } from "../preferenceStorage";
import type { SortDirection } from "./DataTable";

export interface ListSort {
  key: string;
  direction: SortDirection;
}

export type FilterValues = Record<string, string>;

export interface ListQuery {
  filters: FilterValues;
  sort: ListSort | null;
}

export interface SelectOption {
  value: string;
  label: string;
}

export type ListFilterDef =
  | { kind: "text"; key: string; label: string }
  | { kind: "select"; key: string; label: string; options: SelectOption[]; anyLabel?: string }
  | { kind: "dateRange"; key: string; label: string }
  | { kind: "entity"; key: string; label: string; entity: LookupEntityKey };

export const rangeKeys = (key: string) => ({ from: `${key}From`, to: `${key}To` });

export function filterValueKeys(def: ListFilterDef): string[] {
  if (def.kind === "dateRange") {
    const { from, to } = rangeKeys(def.key);
    return [from, to];
  }
  return [def.key];
}

export interface ActiveFilter {
  def: ListFilterDef;
  values: FilterValues;
}

export function activeFilters(defs: ListFilterDef[], values: FilterValues): ActiveFilter[] {
  return defs
    .map((def) => ({ def, values: Object.fromEntries(filterValueKeys(def).map((k) => [k, values[k] ?? ""])) }))
    .filter((entry) => Object.values(entry.values).some((v) => v !== ""));
}

export function withoutFilter(values: FilterValues, def: ListFilterDef): FilterValues {
  const next = { ...values };
  filterValueKeys(def).forEach((k) => delete next[k]);
  return next;
}

export function cleanFilters(defs: ListFilterDef[], values: FilterValues): FilterValues {
  const out: FilterValues = {};
  defs.flatMap(filterValueKeys).forEach((k) => {
    const v = (values[k] ?? "").trim();
    if (v !== "") {
      out[k] = v;
    }
  });
  return out;
}

export function emptyQuery(): ListQuery {
  return { filters: {}, sort: null };
}

export function sameQuery(a: ListQuery, b: ListQuery): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

const DECIMAL = /^-?\d+(\.\d+)?$/;
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

export function compareValues(a: unknown, b: unknown): number {
  const left = a === null || a === undefined ? "" : String(a);
  const right = b === null || b === undefined ? "" : String(b);
  if (left === "" || right === "") {
    return left === right ? 0 : left === "" ? 1 : -1;
  }
  if (DECIMAL.test(left) && DECIMAL.test(right)) {
    return Number(left) - Number(right);
  }
  return collator.compare(left, right);
}

/** groupOf puts rows of one group together first (for example one currency), so amounts are only compared within a group. */
export function sortRows<T>(rows: T[], sort: ListSort | null, valueOf: (row: T, key: string) => unknown, groupOf?: (row: T) => unknown): T[] {
  if (!sort) {
    return rows;
  }
  const sign = sort.direction === "ascending" ? 1 : -1;
  return rows
    .map((row, index) => ({ row, index }))
    .sort((x, y) => {
      const a = valueOf(x.row, sort.key);
      const b = valueOf(y.row, sort.key);
      if (groupOf) {
        const byGroup = compareValues(groupOf(x.row), groupOf(y.row));
        if (byGroup !== 0) {
          return byGroup;
        }
      }
      const aEmpty = a === null || a === undefined || a === "";
      const bEmpty = b === null || b === undefined || b === "";
      if (aEmpty || bEmpty) {
        return (aEmpty ? 1 : 0) - (bEmpty ? 1 : 0) || x.index - y.index;
      }
      return sign * compareValues(a, b) || x.index - y.index;
    })
    .map((entry) => entry.row);
}

export interface SavedView {
  id: string;
  name: string;
  query: ListQuery;
  visible: string[];
  known: string[];
}

export interface ViewStore {
  views: SavedView[];
  defaultId: string | null;
}

export const VIEW_STORE_VERSION = 1;

export const viewStoreKey = (identity: string, listId: string) => `passage.list.v${VIEW_STORE_VERSION}.${identity}.${listId}`;

const isStrings = (value: unknown): value is string[] => Array.isArray(value) && value.every((v) => typeof v === "string");

function parseView(raw: unknown): SavedView | null {
  if (typeof raw !== "object" || raw === null) {
    return null;
  }
  const v = raw as Record<string, unknown>;
  const q = v.query as Record<string, unknown> | undefined;
  if (typeof v.id !== "string" || typeof v.name !== "string" || !isStrings(v.visible) || !isStrings(v.known) || !q) {
    return null;
  }
  const filters = q.filters;
  if (typeof filters !== "object" || filters === null || !Object.values(filters).every((x) => typeof x === "string")) {
    return null;
  }
  const sort = q.sort as Record<string, unknown> | null;
  const validSort =
    sort === null ||
    (typeof sort === "object" && typeof sort.key === "string" && (sort.direction === "ascending" || sort.direction === "descending"));
  if (!validSort) {
    return null;
  }
  return { id: v.id, name: v.name, visible: v.visible, known: v.known, query: { filters: filters as FilterValues, sort: sort as ListSort | null } };
}

export function loadViewStore(storage: PreferenceStorage, key: string): ViewStore {
  const empty: ViewStore = { views: [], defaultId: null };
  const text = storage.read(key);
  if (text === null) {
    return empty;
  }
  try {
    const parsed = JSON.parse(text) as { version?: unknown; views?: unknown; defaultId?: unknown };
    if (parsed.version !== VIEW_STORE_VERSION || !Array.isArray(parsed.views)) {
      return empty;
    }
    const views = parsed.views.map(parseView).filter((v): v is SavedView => v !== null);
    const defaultId = typeof parsed.defaultId === "string" && views.some((v) => v.id === parsed.defaultId) ? parsed.defaultId : null;
    return { views, defaultId };
  } catch {
    return empty;
  }
}

export function saveViewStore(storage: PreferenceStorage, key: string, store: ViewStore): boolean {
  return storage.write(key, JSON.stringify({ version: VIEW_STORE_VERSION, ...store }));
}

export interface ColumnShape {
  key: string;
  defaultHidden?: boolean;
}

export const defaultVisible = (columns: ColumnShape[]) => columns.filter((c) => !c.defaultHidden).map((c) => c.key);

/** defs lets a saved choice for a select filter be dropped once its option no longer exists. */
export function reconcileView(view: SavedView, columns: ColumnShape[], filterKeys: string[], defs: ListFilterDef[] = []): { query: ListQuery; visible: string[] } {
  const keys = new Set(columns.map((c) => c.key));
  const known = new Set(view.known);
  const kept = view.visible.filter((k) => keys.has(k));
  const added = columns.filter((c) => !known.has(c.key) && !c.defaultHidden).map((c) => c.key);
  const visible = [...kept, ...added];
  const allowed = new Set(filterKeys);
  const choices = new Map(defs.flatMap((d) => (d.kind === "select" ? [[d.key, new Set(d.options.map((o) => o.value))] as const] : [])));
  const filters = Object.fromEntries(Object.entries(view.query.filters).filter(([k, v]) => allowed.has(k) && (choices.get(k)?.has(v) ?? true)));
  const sort = view.query.sort && keys.has(view.query.sort.key) ? view.query.sort : null;
  return { query: { filters, sort }, visible: visible.length > 0 ? visible : defaultVisible(columns) };
}

export function inOrder(columns: ColumnShape[], visible: string[]): string[] {
  const shown = new Set(visible);
  return columns.filter((c) => shown.has(c.key)).map((c) => c.key);
}
