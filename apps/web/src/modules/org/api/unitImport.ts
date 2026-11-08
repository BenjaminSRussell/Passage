import { referenceFetch } from "./reference";

// Bulk import of operating units: POST /v1/org/operating-units:import takes parsed rows, so the CSV is read here.

export const MAX_IMPORT_ROWS = 1000;

export interface UnitImportRow {
  type: string;
  code: string;
  name: string;
  legalEntity?: string;
}

export interface UnitImportRowReport {
  line: number;
  type: string;
  code: string;
  status: "created" | "unchanged" | "failed";
  id?: string;
  errors?: Record<string, string>;
}

export interface UnitImportReport {
  dryRun: boolean;
  applied: boolean;
  created: number;
  unchanged: number;
  failed: number;
  rows: UnitImportRowReport[];
}

export interface ParsedCsv {
  rows: UnitImportRow[];
  problems: string[];
}

/** Splits CSV text into records, honouring quoted fields (doubled quotes, commas and line breaks inside quotes). */
function records(text: string): string[][] {
  const out: string[][] = [];
  let field = "";
  let row: string[] = [];
  let quoted = false;
  const source = text.replace(/^﻿/, "");
  for (let i = 0; i < source.length; i++) {
    const ch = source[i];
    if (quoted) {
      if (ch === '"' && source[i + 1] === '"') {
        field += '"';
        i++;
      } else if (ch === '"') {
        quoted = false;
      } else {
        field += ch;
      }
    } else if (ch === '"') {
      quoted = true;
    } else if (ch === ",") {
      row.push(field);
      field = "";
    } else if (ch === "\n" || ch === "\r") {
      if (ch === "\r" && source[i + 1] === "\n") {
        i++;
      }
      row.push(field);
      field = "";
      out.push(row);
      row = [];
    } else {
      field += ch;
    }
  }
  if (field !== "" || row.length > 0) {
    row.push(field);
    out.push(row);
  }
  return out.filter((r) => r.some((cell) => cell.trim() !== ""));
}

const headerAliases: Record<string, keyof UnitImportRow> = {
  type: "type",
  code: "code",
  name: "name",
  legalentity: "legalEntity",
  defaultlegalentity: "legalEntity",
};

/** Reads a CSV with a header row naming type, code and name, and optionally legalEntity (a legal entity code). */
export function parseOperatingUnitCsv(text: string): ParsedCsv {
  const [header, ...body] = records(text);
  if (!header) {
    return { rows: [], problems: ["The file is empty."] };
  }
  const columns = header.map(
    (cell) =>
      headerAliases[
        cell
          .trim()
          .toLowerCase()
          .replace(/[\s_-]/g, "")
      ],
  );
  const problems = (["type", "code", "name"] as const)
    .filter((name) => !columns.includes(name))
    .map((name) => `The header has no ${name} column.`);
  if (problems.length > 0) {
    return { rows: [], problems };
  }
  const rows = body.map((cells) => {
    const row: UnitImportRow = { type: "", code: "", name: "" };
    columns.forEach((column, index) => {
      if (column) {
        row[column] = (cells[index] ?? "").trim();
      }
    });
    return row;
  });
  if (rows.length === 0) {
    problems.push("The file has a header but no rows.");
  }
  if (rows.length > MAX_IMPORT_ROWS) {
    problems.push(
      `The file has ${rows.length} rows; an import takes at most ${MAX_IMPORT_ROWS}.`,
    );
  }
  return { rows, problems };
}

export const importOperatingUnits = (rows: UnitImportRow[], dryRun: boolean) =>
  referenceFetch<UnitImportReport>(
    `/operating-units:import${dryRun ? "?dryRun=true" : ""}`,
    {
      method: "POST",
      body: JSON.stringify({ rows }),
    },
  );
