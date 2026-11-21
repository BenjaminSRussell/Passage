import { describe, expect, it } from "vitest";
import { MAX_PASTE_ROWS, parsePaste, type PasteField } from "./PasteDialog";

const fields: PasteField[] = [
  { key: "item", header: "Item" },
  {
    key: "qty",
    header: "Quantity",
    parse: (cell) => Number(cell),
    validate: (value) =>
      typeof value === "number" && value < 0
        ? "Quantity cannot be negative"
        : null,
  },
];

describe("parsePaste", () => {
  it("splits tab-separated rows, including CRLF line ends and a trailing blank line", () => {
    const { rows } = parsePaste("A-1\t2\r\nA-2\t3\r\n\r\n", fields);
    expect(rows.map((r) => r.values)).toEqual([
      { item: "A-1", qty: 2 },
      { item: "A-2", qty: 3 },
    ]);
    expect(rows.every((r) => r.errors.length === 0)).toBe(true);
  });

  it("drops blank lines between rows instead of sending them on as empty rows", () => {
    const { rows } = parsePaste("A-1\t2\n\n  \nA-2\t3\n", fields);
    expect(rows.map((r) => r.values)).toEqual([
      { item: "A-1", qty: 2 },
      { item: "A-2", qty: 3 },
    ]);
  });

  it("flags a number that is not finite", () => {
    const { rows } = parsePaste("A-1\tInfinity\nA-2\t1e999", fields);
    expect(rows.map((r) => r.errors)).toEqual([["Quantity is not a number"], ["Quantity is not a number"]]);
  });

  it("skips a first row that repeats the headings, ignoring case", () => {
    expect(parsePaste("item\tquantity\nA-1\t2", fields).rows).toHaveLength(1);
  });

  it("returns no rows for empty or whitespace text", () => {
    expect(parsePaste("", fields).rows).toEqual([]);
    expect(parsePaste("  \n \n", fields).rows).toEqual([]);
  });

  it("flags a number that is not one and a value the field rejects", () => {
    const { rows } = parsePaste("A-1\tabc\nA-2\t-4", fields);
    expect(rows[0].errors).toEqual(["Quantity is not a number"]);
    expect(rows[1].errors).toEqual(["Quantity cannot be negative"]);
  });

  it("flags a ragged row: a missing cell is validated as empty and extra cells are an error", () => {
    const { rows } = parsePaste("A-1\nA-2\t1\tstray", fields);
    expect(rows[0].errors).toEqual(["Quantity is not a number"]);
    expect(parsePaste("A-1\t \n", fields).rows[0].errors).toEqual([
      "Quantity is not a number",
    ]);
    expect(rows[1].errors).toEqual(["1 more cell than columns"]);
  });

  it("refuses more rows than the cap and keeps exactly the cap", () => {
    const line = (n: number) => `A-${n}\t1`;
    const lines = (count: number) =>
      Array.from({ length: count }, (_, i) => line(i)).join("\n");
    expect(parsePaste(lines(MAX_PASTE_ROWS + 1), fields)).toEqual({
      rows: [],
      tooMany: true,
    });
    expect(parsePaste(lines(MAX_PASTE_ROWS), fields)).toMatchObject({
      tooMany: false,
    });
  });
});
