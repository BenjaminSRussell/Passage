import { LINE_DIMENSIONS, type JournalType, type LineDimension, type LineInput } from "./api/client";
import { DECIMAL } from "./format";

/** The destination dimensions a TRANSFER row can name (batch and serial never change). */
const TO_DIMENSIONS = ["site", "warehouse", "location", "segment"] as const;
type ToDimension = (typeof TO_DIMENSIONS)[number];

/** One parsed row in the shape the add-line form validates. */
export interface CsvLineValues {
  productId: string;
  qty: string;
  enteredUnit: string;
  costPrice: string;
  dimensions: Record<LineDimension, string>;
  toDimensions: Record<ToDimension, string>;
}

export interface CsvRowError {
  /** Spreadsheet row number: the header is row 1. */
  row: number;
  messages: string[];
}

export interface CsvImport {
  lines: LineInput[];
  errors: CsvRowError[];
  /** Set when the header cannot be used; no row is read then. */
  headerError?: string;
}

/** Header names (lower case, no spaces or underscores) and the field each one fills. */
const FIELDS: Record<string, string> = {
  product: "productId",
  productid: "productId",
  qty: "qty",
  quantity: "qty",
  unit: "enteredUnit",
  enteredunit: "enteredUnit",
  uom: "enteredUnit",
  cost: "costPrice",
  costprice: "costPrice",
  price: "costPrice",
  site: "site",
  warehouse: "warehouse",
  location: "location",
  batch: "batch",
  serial: "serial",
  status: "inventory_status",
  inventorystatus: "inventory_status",
  owner: "owner",
  segment: "segment",
  tosite: "toSite",
  towarehouse: "toWarehouse",
  tolocation: "toLocation",
  tosegment: "toSegment",
};

/** Largest import in one go. The rows go to lines:bulk as one request, which the service applies in one transaction (all or none); the journal itself holds up to journal_max_lines (10,000 by default). */
export const MAX_IMPORT_ROWS = 1000;

/**
 * Splits delimited text into rows of cells. Handles quoted cells (a doubled
 * quote is a quote, a quoted cell may hold the delimiter and line breaks).
 */
export function splitDelimited(text: string, delimiter: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = "";
  let quoted = false;
  const src = text.replace(/^﻿/, "");
  for (let i = 0; i < src.length; i++) {
    const ch = src[i];
    if (quoted) {
      if (ch === '"' && src[i + 1] === '"') {
        cell += '"';
        i++;
      } else if (ch === '"') {
        quoted = false;
      } else {
        cell += ch;
      }
    } else if (ch === '"' && cell === "") {
      quoted = true;
    } else if (ch === delimiter) {
      row.push(cell);
      cell = "";
    } else if (ch === "\n" || ch === "\r") {
      if (ch === "\r" && src[i + 1] === "\n") i++;
      row.push(cell);
      cell = "";
      rows.push(row);
      row = [];
    } else {
      cell += ch;
    }
  }
  if (cell !== "" || row.length > 0) {
    row.push(cell);
    rows.push(row);
  }
  return rows.filter((r) => r.some((c) => c.trim() !== ""));
}

/** A spreadsheet paste is tab separated; a file saved as CSV uses commas or, in many locales, semicolons. */
function delimiterOf(text: string): string {
  const first = text.replace(/^﻿/, "").split(/\r?\n/, 1)[0] ?? "";
  if (first.includes("\t")) return "\t";
  return (first.match(/;/g)?.length ?? 0) > (first.match(/,/g)?.length ?? 0) ? ";" : ",";
}

/**
 * Reads journal lines from pasted or uploaded text. The first row names the
 * columns: product, qty and site are required; unit (default EA), cost,
 * warehouse, location, batch, serial, status, owner and segment are optional,
 * and a TRANSFER also names toSite (required) and optionally toWarehouse,
 * toLocation and toSegment. validate applies the same rules as the add-line
 * form; its messages are reported per row and nothing is returned for a file
 * with any error, so a journal never gets half an import.
 */
export function parseLinesCsv(
  text: string,
  type: JournalType,
  validate: (values: CsvLineValues, type: JournalType) => Record<string, string | undefined>,
): CsvImport {
  const rows = splitDelimited(text, delimiterOf(text));
  if (rows.length === 0) return { lines: [], errors: [], headerError: "Paste or choose a file with a header row and at least one line." };
  const header = rows[0].map((h) => FIELDS[h.trim().toLowerCase().replace(/[\s_-]/g, "")]);
  const unknown = rows[0].filter((_, i) => header[i] === undefined).map((h) => h.trim() || "(empty)");
  if (unknown.length > 0) return { lines: [], errors: [], headerError: `Unknown column ${unknown.map((u) => `"${u}"`).join(", ")}.` };
  const seen = new Set<string>();
  for (const name of header) {
    if (seen.has(name)) return { lines: [], errors: [], headerError: `Column "${name}" appears twice.` };
    seen.add(name);
  }
  const missing = ["productId", "qty", "site"].filter((name) => !seen.has(name));
  if (missing.length > 0) return { lines: [], errors: [], headerError: `The header needs the columns ${missing.join(", ")}.` };
  if (type === "TRANSFER" && !seen.has("toSite")) return { lines: [], errors: [], headerError: "A transfer import needs the toSite column." };
  if (type !== "TRANSFER" && [...seen].some((name) => name.startsWith("to"))) {
    return { lines: [], errors: [], headerError: "Destination columns (toSite, toWarehouse, toLocation, toSegment) belong to a transfer journal." };
  }
  if (type !== "OWNERSHIP_CHANGE" && seen.has("owner")) return { lines: [], errors: [], headerError: "The owner column belongs to an ownership change journal." };
  if (rows.length - 1 > MAX_IMPORT_ROWS) return { lines: [], errors: [], headerError: `Import at most ${MAX_IMPORT_ROWS} lines at a time.` };

  const lines: LineInput[] = [];
  const errors: CsvRowError[] = [];
  rows.slice(1).forEach((cells, index) => {
    const row = index + 2;
    const values: CsvLineValues = {
      productId: "",
      qty: "",
      enteredUnit: "EA",
      costPrice: "",
      dimensions: Object.fromEntries(LINE_DIMENSIONS.map((d) => [d, ""])) as Record<LineDimension, string>,
      toDimensions: { site: "", warehouse: "", location: "", segment: "" },
    };
    header.forEach((field, i) => {
      const cell = (cells[i] ?? "").trim();
      if (field === "productId" || field === "qty" || field === "costPrice") values[field] = cell;
      else if (field === "enteredUnit") values.enteredUnit = cell === "" ? "EA" : cell.toUpperCase();
      else if (field.startsWith("to")) values.toDimensions[field.slice(2).toLowerCase() as ToDimension] = cell;
      else values.dimensions[field as LineDimension] = cell;
    });
    const messages: string[] = [];
    if (cells.length > header.length && cells.slice(header.length).some((c) => c.trim() !== "")) messages.push("Has more cells than the header has columns.");
    if (values.qty !== "" && !DECIMAL.test(values.qty)) messages.push("Quantity must be a number such as 12 or -3.5.");
    for (const message of Object.values(validate(values, type))) if (message && !messages.includes(message)) messages.push(message);
    if (messages.length > 0) {
      errors.push({ row, messages });
      return;
    }
    const transfer = type === "TRANSFER";
    lines.push({
      productId: values.productId,
      qty: values.qty,
      enteredUnit: values.enteredUnit,
      costPrice: values.costPrice,
      dimensions: values.dimensions,
      // A transfer keeps the source's batch, serial and status at the destination.
      ...(transfer
        ? { toDimensions: { ...values.toDimensions, batch: values.dimensions.batch, serial: values.dimensions.serial, inventory_status: values.dimensions.inventory_status } }
        : {}),
    });
  });
  if (errors.length > 0) return { lines: [], errors };
  if (lines.length === 0) return { lines: [], errors: [], headerError: "The file has a header but no lines." };
  return { lines, errors };
}
