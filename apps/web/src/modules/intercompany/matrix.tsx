import type { DataTableColumn } from "../../shell/ui";
import type { JournalLeg, MatrixRow } from "./api/client";
import { amount, dash } from "./format";
import type { RowField } from "./RowGrid";

export type MatrixKey = keyof MatrixRow;

/** Columns of the IC accounting matrix (BR-40.4). */
export const matrixFields: RowField<MatrixKey>[] = [
  { name: "entityA", label: "Entity A", mono: true },
  { name: "entityB", label: "Entity B", mono: true },
  { name: "dueFrom", label: "Due from B (in A)", mono: true },
  { name: "dueTo", label: "Due to A (in B)", mono: true },
];

export const emptyMatrixRow = (): Record<MatrixKey, string> => ({ entityA: "", entityB: "", dueFrom: "", dueTo: "" });

/** Requires every matrix cell and returns the trimmed rows; errors use matrix[i].field. */
export function checkMatrix(rows: Record<MatrixKey, string>[], errors: Record<string, string>): MatrixRow[] {
  return rows.map((row, i) => {
    for (const f of matrixFields) {
      if (row[f.name].trim() === "") errors[`matrix[${i}].${f.name}`] = `Enter ${f.label.toLowerCase()}.`;
    }
    return { entityA: row.entityA.trim(), entityB: row.entityB.trim(), dueFrom: row.dueFrom.trim(), dueTo: row.dueTo.trim() };
  });
}

/** Journal legs with debit and credit in their own columns. */
export const legColumns: DataTableColumn<JournalLeg>[] = [
  { key: "entity", header: "Entity", mono: true },
  { key: "account", header: "Account", mono: true },
  { key: "debit", header: "Debit", align: "end", render: (l) => (l.side === "debit" ? amount(l.amount) : dash) },
  { key: "credit", header: "Credit", align: "end", render: (l) => (l.side === "credit" ? amount(l.amount) : dash) },
  { key: "currency", header: "Currency", mono: true },
];
