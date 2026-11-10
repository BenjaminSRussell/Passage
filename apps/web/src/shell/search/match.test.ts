import { describe, expect, it } from "vitest";
import sharedCases from "../../../../../services/search/internal/store/testdata/matching.json?raw";
import { filterBySearch, isNumberQuery, matchesSearch, rankBySearch, wordSimilarity, type SearchText } from "./match";

interface SharedCase {
  query: string;
  number: string;
  title: string;
  match: boolean;
  why: string;
}

const cases = JSON.parse(sharedCases) as SharedCase[];

describe("matchesSearch", () => {
  // The search service runs the same file against Postgres (TestSearchMatchesTheSharedCases).
  it.each(cases)("$query against $number $title: $why", ({ query, number, title, match }) => {
    expect(matchesSearch(query, { number, name: title })).toBe(match);
  });

  it("has shared cases for every rule", () => {
    expect(cases.length).toBeGreaterThan(20);
    expect(cases.some((c) => c.match)).toBe(true);
    expect(cases.some((c) => !c.match)).toBe(true);
  });

  it("matches every row for an empty query and a row without a name by its number", () => {
    expect(matchesSearch("  ", { number: "PO-1" })).toBe(true);
    expect(matchesSearch("po-1", { number: "PO-1" })).toBe(true);
    expect(matchesSearch("purchase", { number: "PO-1" })).toBe(false);
  });
});

describe("isNumberQuery", () => {
  it.each([
    ["po", true],
    ["po-1042", true],
    ["inv-2026", true],
    ["2026", true],
    ["7-eleven", false],
    ["inv 2026", false],
    ["contoso", false],
    ["abc", false],
  ])("%s is a number query: %s", (text, expected) => {
    expect(isNumberQuery(text)).toBe(expected);
  });
});

describe("wordSimilarity", () => {
  // Values from pg_trgm's word_similarity in PostgreSQL 16.
  it.each([
    ["contoso", "p-7 contoso ltd", 1],
    ["contsoo", "p-7 contoso ltd", 0.5],
    ["kontozo", "p-7 contoso ltd", 0.25],
    ["kowalsky", "w-0112 dana kowalski", 7 / 9],
    ["contoso", "v-0003 contosso valves", 0.75],
    ["zebra", "p-7 contoso ltd", 0],
    ["", "p-7 contoso ltd", 0],
    ["contoso", "", 0],
  ])("%s in %s is %s", (query, text, expected) => {
    expect(wordSimilarity(query, text)).toBeCloseTo(expected, 6);
  });
});

describe("searching rows", () => {
  const rows: (SearchText & { id: string })[] = [
    { id: "1", number: "V-0007", name: "Contoso Pumps" },
    { id: "2", number: "CON-1", name: "Northwind" },
    { id: "3", number: "V-0003", name: "Contosso Valves" },
    { id: "4", number: "CONTOSO", name: "Fabrikam" },
    { id: "5", number: "V-0001", name: "Adventure Works" },
  ];
  const read = (row: SearchText) => row;

  it("filters in the list's own order", () => {
    expect(filterBySearch(rows, "contoso", read).map((r) => r.id)).toEqual(["1", "3", "4"]);
    expect(filterBySearch(rows, "v-000", read).map((r) => r.id)).toEqual(["1", "3", "5"]);
    expect(filterBySearch(rows, "", read)).toBe(rows);
  });

  it("ranks the exact number first, then the closest name, then by number", () => {
    expect(rankBySearch(rows, "contoso", read).map((r) => r.id)).toEqual(["4", "1", "3"]);
    expect(rankBySearch(rows, "v-000", read).map((r) => r.id)).toEqual(["5", "3", "1"]);
  });
});
