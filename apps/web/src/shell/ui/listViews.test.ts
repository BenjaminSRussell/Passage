import { describe, expect, it } from "vitest";
import type { PreferenceStorage } from "../preferenceStorage";
import { cleanFilters, compareValues, loadViewStore, reconcileView, saveViewStore, sortRows, type SavedView } from "./listViews";

function memory(initial: Record<string, string> = {}): PreferenceStorage & { data: Record<string, string> } {
  const data = { ...initial };
  return {
    data,
    read: (k) => data[k] ?? null,
    write: (k, v) => {
      data[k] = v;
      return true;
    },
    remove: (k) => {
      delete data[k];
    },
  };
}

const view: SavedView = { id: "v1", name: "Mine", query: { filters: { status: "OPEN", gone: "x" }, sort: { key: "old", direction: "ascending" } }, visible: ["a", "old"], known: ["a", "old"] };

describe("sorting", () => {
  it("orders decimals numerically and text naturally, with empty values last in both directions", () => {
    const rows = [{ v: "10.50" }, { v: "9" }, { v: "" }, { v: "-2" }];
    expect(sortRows(rows, { key: "v", direction: "ascending" }, (r) => r.v).map((r) => r.v)).toEqual(["-2", "9", "10.50", ""]);
    expect(sortRows(rows, { key: "v", direction: "descending" }, (r) => r.v).map((r) => r.v)).toEqual(["10.50", "9", "-2", ""]);
    expect(compareValues("INV-9", "INV-10")).toBeLessThan(0);
  });

  it("sorts amounts within each currency, never across them", () => {
    const rows = [{ v: "5", c: "USD" }, { v: "900", c: "EUR" }, { v: "1", c: "USD" }, { v: "20", c: "EUR" }];
    const sorted = sortRows(rows, { key: "v", direction: "descending" }, (r) => r.v, (r) => r.c);
    expect(sorted.map((r) => `${r.c} ${r.v}`)).toEqual(["EUR 900", "EUR 20", "USD 5", "USD 1"]);
  });

  it("keeps the original order of equal rows", () => {
    const rows = [{ k: "a", n: 1 }, { k: "a", n: 2 }, { k: "a", n: 3 }];
    expect(sortRows(rows, { key: "k", direction: "descending" }, (r) => r.k).map((r) => r.n)).toEqual([1, 2, 3]);
  });
});

describe("view store", () => {
  it("round-trips through a versioned record", () => {
    const storage = memory();
    expect(saveViewStore(storage, "k", { views: [view], defaultId: "v1" })).toBe(true);
    expect(JSON.parse(storage.data.k).version).toBe(1);
    expect(loadViewStore(storage, "k")).toEqual({ views: [view], defaultId: "v1" });
  });

  it("ignores a record of another version, bad JSON, bad views and a default that no longer exists", () => {
    expect(loadViewStore(memory({ k: JSON.stringify({ version: 2, views: [view] }) }), "k").views).toEqual([]);
    expect(loadViewStore(memory({ k: "{nope" }), "k").views).toEqual([]);
    const mixed = JSON.stringify({ version: 1, views: [view, { id: 3 }, { ...view, id: "v2", query: { filters: {}, sort: { key: "a", direction: "sideways" } } }], defaultId: "missing" });
    expect(loadViewStore(memory({ k: mixed }), "k")).toEqual({ views: [view], defaultId: null });
  });
});

describe("reconcileView", () => {
  it("drops columns, sorts and filters that no longer exist and shows columns added since the view was saved", () => {
    const columns = [{ key: "a" }, { key: "b" }, { key: "c", defaultHidden: true }];
    const result = reconcileView(view, columns, ["status"]);
    expect(result.visible).toEqual(["a", "b"]);
    expect(result.query).toEqual({ filters: { status: "OPEN" }, sort: null });
  });

  it("drops a saved select choice whose option no longer exists", () => {
    const select = { kind: "select" as const, key: "status", label: "Status", options: [{ value: "OPEN", label: "Open" }] };
    expect(reconcileView(view, [{ key: "a" }], ["status"], [select]).query.filters).toEqual({ status: "OPEN" });
    const retired = { ...view, query: { ...view.query, filters: { status: "RETIRED" } } };
    expect(reconcileView(retired, [{ key: "a" }], ["status"], [select]).query.filters).toEqual({});
  });

  it("falls back to the default columns when none of the saved ones remain", () => {
    const result = reconcileView({ ...view, visible: ["old"], known: ["old", "a", "b"] }, [{ key: "a" }, { key: "b", defaultHidden: true }], []);
    expect(result.visible).toEqual(["a"]);
  });
});

describe("cleanFilters", () => {
  it("keeps trimmed non-empty values of known filters only", () => {
    expect(cleanFilters([{ kind: "text", key: "q", label: "Search" }, { kind: "dateRange", key: "d", label: "Date" }], { q: " abc ", dFrom: "2026-01-01", dTo: "", other: "x" })).toEqual({ q: "abc", dFrom: "2026-01-01" });
  });
});
