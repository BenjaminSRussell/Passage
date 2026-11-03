// The outline editor's draft: a flat list of rows with a depth, turned into the tree the service takes.
import type { OutlineKind, OutlineNodeInput, OutlineRow } from "./api/client";
import { decimalPattern, uuidPattern } from "./parts";

export const MAX_DEPTH = 5;
export const MAX_NODES = 200;

export interface OutlineDraftRow {
  key: string;
  kind: OutlineKind;
  /** 1 for a top-level node; a node nests under the nearest level above it with a smaller depth. */
  depth: number;
  description: string;
  productId: string;
  qty: string;
  unit: string;
  unitPrice: string;
}

let seq = 0;
export function newOutlineRow(patch: Partial<OutlineDraftRow> = {}): OutlineDraftRow {
  return { key: `o${++seq}`, kind: "ITEM", depth: 1, description: "", productId: "", qty: "", unit: "", unitPrice: "", ...patch };
}

/** A draft that shows stored rows, so replacing an outline starts from what is there. */
export function draftFromOutline(rows: OutlineRow[]): OutlineDraftRow[] {
  return rows.map((r) =>
    newOutlineRow({
      kind: r.kind,
      depth: r.depth,
      description: r.description ?? "",
      productId: r.productId ?? "",
      qty: r.kind === "ITEM" ? r.qty : "",
      unit: r.unit ?? "",
      unitPrice: r.kind === "ITEM" ? r.unitPrice : "",
    }),
  );
}

const SCALE = 6;
const hasScale = (v: string) => (v.split(".")[1] ?? "").length <= SCALE;

/**
 * Checks the rows' shape and builds the request tree. Quantities and prices stay decimal
 * strings; the amounts and sums are the service's (PRC-SRV-004), so no money arithmetic
 * happens here. Errors are keyed outline[i].field by draft row.
 */
export function toOutlineInput(rows: OutlineDraftRow[]): { outline?: OutlineNodeInput[]; errors: Record<string, string> } {
  const errors: Record<string, string> = {};
  if (rows.length > MAX_NODES) errors.outline = `An outline holds at most ${MAX_NODES} rows.`;
  rows.forEach((r, i) => {
    const at = (field: string) => `outline[${i}].${field}`;
    const prev = rows[i - 1];
    if (i === 0 && r.depth !== 1) errors[at("depth")] = "The first row is at the top level.";
    if (prev && r.depth > prev.depth + 1) errors[at("depth")] = "A row can nest one level below the row above it.";
    if (prev && prev.kind === "ITEM" && r.depth > prev.depth) errors[at("depth")] = "A service item has no rows below it.";
    if (r.kind === "LEVEL") {
      if (r.description.trim() === "") errors[at("description")] = "Enter the level's description.";
      const next = rows[i + 1];
      if (!next || next.depth <= r.depth) errors[at("kind")] = "A level needs at least one row below it.";
      return;
    }
    if (r.description.trim() === "" && r.productId.trim() === "") errors[at("description")] = "Enter a description or a product id.";
    if (r.productId.trim() !== "" && !uuidPattern.test(r.productId.trim())) errors[at("productId")] = "Enter a UUID or leave blank.";
    if (!decimalPattern.test(r.qty.trim()) || Number(r.qty) <= 0 || !hasScale(r.qty.trim())) errors[at("qty")] = "Enter a quantity above 0 with at most six decimals.";
    if (r.unit.trim() === "") errors[at("unit")] = "Enter the unit.";
    if (!decimalPattern.test(r.unitPrice.trim()) || !hasScale(r.unitPrice.trim())) errors[at("unitPrice")] = "Enter a price with at most six decimals.";
  });
  if (Object.keys(errors).length > 0) return { errors };

  const root: OutlineNodeInput[] = [];
  const open: { depth: number; children: OutlineNodeInput[] }[] = [{ depth: 0, children: root }];
  for (const r of rows) {
    while (open[open.length - 1].depth >= r.depth) open.pop();
    const parent = open[open.length - 1];
    if (r.kind === "LEVEL") {
      const node: OutlineNodeInput = { kind: "LEVEL", description: r.description.trim(), children: [] };
      parent.children.push(node);
      open.push({ depth: r.depth, children: node.children as OutlineNodeInput[] });
    } else {
      parent.children.push({
        kind: "ITEM",
        description: r.description.trim() || undefined,
        productId: r.productId.trim() || undefined,
        qty: r.qty.trim(),
        unit: r.unit.trim(),
        unitPrice: r.unitPrice.trim(),
      });
    }
  }
  return { errors, outline: root };
}
