import type { ConditionRecord, ConditionType, NetPriceRow } from "./api/client";

export const TYPE_LABELS: Record<ConditionType, string> = {
  PRICE: "Price",
  LINE_DISC: "Line discount",
  MULTILINE_DISC: "Multiline discount",
  TOTAL_DISC: "Total discount",
  SURCHARGE: "Surcharge",
};

/** Label of the amount field for a condition type: a price, a surcharge, or an amount off. */
export function amountLabel(type: ConditionType): string {
  if (type === "PRICE") return "Price";
  if (type === "SURCHARGE") return "Surcharge amount";
  return "Amount off";
}

/** Order-level conditions apply to the whole order, so they take All products. */
export function isOrderLevel(type: ConditionType): boolean {
  return type === "TOTAL_DISC" || type === "SURCHARGE";
}

/** Who a record applies to, in words. */
export function describeCustomer(r: Pick<ConditionRecord, "customerKind" | "customerKey">): string {
  if (r.customerKind === "all") return "All customers";
  if (r.customerKind === "customer_group") return `Group ${r.customerKey}`;
  return r.customerKey;
}

/** What a record applies to, in words. */
export function describeProduct(r: Pick<ConditionRecord, "productKind" | "productKey" | "variant">): string {
  if (r.productKind === "all") return "All products";
  if (r.productKind === "product_group") return `Group ${r.productKey}`;
  if (r.productKind === "variant") return `${r.productKey} / ${r.variant}`;
  return r.productKey;
}

/** "10% + 5%" for the two sequential percentages; empty when neither is set. */
export function describePercent(r: Pick<ConditionRecord, "discPct1" | "discPct2">): string {
  return [r.discPct1, r.discPct2]
    .filter((p) => p !== "" && Number(p) !== 0)
    .map((p) => `${p}%`)
    .join(" + ");
}

/** Today's calendar day in the browser's zone, as YYYY-MM-DD. */
export function today(now: Date = new Date()): string {
  const local = new Date(now.getTime() - now.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 10);
}

/** Adds whole days to a YYYY-MM-DD day. */
export function addDays(day: string, days: number): string {
  const d = new Date(`${day}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
}

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
export const DAY = /^\d{4}-\d{2}-\d{2}$/;
export const DECIMAL = /^\d+(\.\d+)?$/;
export const SIGNED_DECIMAL = /^-?\d+(\.\d+)?$/;

const SCALE = 6;

/** Exact sum of decimal strings (up to 6 places, as the service sends them); no floating point. */
export function sumDecimals(values: string[]): string {
  let total = 0n;
  for (const raw of values) {
    const negative = raw.startsWith("-");
    const [whole, fraction = ""] = raw.replace(/^[-+]/, "").split(".");
    const scaled = BigInt((whole || "0") + fraction.padEnd(SCALE, "0").slice(0, SCALE));
    total += negative ? -scaled : scaled;
  }
  const negative = total < 0n;
  const digits = (negative ? -total : total).toString().padStart(SCALE + 1, "0");
  const fraction = digits.slice(-SCALE).replace(/0+$/, "");
  return `${negative ? "-" : ""}${digits.slice(0, -SCALE)}${fraction ? `.${fraction}` : ""}`;
}

/** One row of the net price table: a quantity break of a product row. */
export interface NetPriceLine {
  key: string;
  product: string;
  unit: string;
  quantity: string;
  unitPrice: string;
  netUnitPrice: string;
  lineNet: string;
  priceRecordId: string;
  errorCode: string;
}

/** Flattens the rows of a net price response to one line per product and quantity break. */
export function netPriceLines(rows: NetPriceRow[]): NetPriceLine[] {
  return rows.flatMap((row) =>
    row.breaks.map((b) => ({
      key: `${row.productKind}|${row.productKey}|${row.variant}|${row.unit}|${b.quantity}`,
      product: describeProduct(row),
      unit: row.unit,
      quantity: b.quantity,
      unitPrice: b.unitPrice,
      netUnitPrice: b.netUnitPrice,
      lineNet: b.lineNet,
      priceRecordId: b.priceRecordId ?? "",
      errorCode: b.errorCode ?? "",
    })),
  );
}

/** The quantities at which each record set or changed the net price, by record id (the winning-price marker); statistical steps change nothing and are left out. */
export function winningQuantities(rows: NetPriceRow[]): Map<string, string[]> {
  const out = new Map<string, string[]>();
  for (const row of rows) {
    for (const b of row.breaks) {
      for (const a of b.applied) {
        if (a.statistical) continue;
        out.set(a.recordId, [...(out.get(a.recordId) ?? []), b.quantity]);
      }
    }
  }
  return out;
}

function csvCell(value: string): string {
  // A cell that starts with =, +, -, @, a tab or a carriage return can run as a formula in a
  // spreadsheet (OWASP CSV injection), so it is prefixed with ' and shown as text; a plain negative
  // number is left as it is.
  const safe = /^[=+\-@\t\r]/.test(value) && !/^-\d+(\.\d+)?$/.test(value) ? `'${value}` : value;
  return /[",\n\r]/.test(safe) ? `"${safe.replace(/"/g, '""')}"` : safe;
}

/** The net price table as CSV (RFC 4180, CRLF), amounts as the service sent them. */
export function netPricesCsv(lines: NetPriceLine[], currency: string): string {
  const head = ["Product", "Unit", "From quantity", `List price ${currency}`, `Net unit price ${currency}`, `Line net ${currency}`, "Winning price record", "Error"];
  const body = lines.map((l) => [l.product, l.unit, l.quantity, l.unitPrice, l.netUnitPrice, l.lineNet, l.priceRecordId, l.errorCode]);
  return [head, ...body].map((r) => r.map(csvCell).join(",")).join("\r\n") + "\r\n";
}
