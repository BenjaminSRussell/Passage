import { ON_HAND_GROUPS, type OnHandGroup, type OnHandRow } from "./api/client";

/** One CSV cell. Text that a spreadsheet would run as a formula is prefixed with an apostrophe. */
export function csvCell(value: string | undefined): string {
  let text = value ?? "";
  if (/^[=+@\t\r]/.test(text) || (/^-/.test(text) && !/^-\d+(\.\d+)?$/.test(text))) text = `'${text}`;
  return /[",\r\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

/**
 * The on-hand rows as CSV, one column per grouped dimension followed by the
 * quantities and the posted value. Availability columns are left out of an
 * as-of replay, as on screen.
 */
export function onHandCsv(rows: OnHandRow[], groupBy: readonly OnHandGroup[], asOf: boolean, labels: Record<OnHandGroup, string>): string {
  const groups = groupBy.length > 0 ? ON_HAND_GROUPS.filter((g) => groupBy.includes(g)) : (["product"] as OnHandGroup[]);
  const live = !asOf;
  const header = [
    ...groups.map((g) => labels[g]),
    "Physical",
    ...(live ? ["Available physical", "Available ordered", "Reserved", "Reserved on order", "Held"] : []),
    "Blocked",
    "Posted value",
    "Currency",
    "Negative stock",
  ];
  const lines = rows.map((row) =>
    [
      ...groups.map((g) => row.group[g] ?? ""),
      row.physicalInvent,
      ...(live ? [row.availPhysical, row.availOrdered, row.reservedPhysical, row.reservedOrdered, row.heldPhysical].map((v) => v ?? "") : []),
      row.blocked ?? "0",
      row.postedValue,
      row.currency,
      row.negative ? "yes" : "no",
    ]
      .map(csvCell)
      .join(","),
  );
  return [header.map(csvCell).join(","), ...lines].join("\r\n") + "\r\n";
}

/** Offers text as a file download; a no-op where the browser cannot create object URLs. */
export function downloadText(filename: string, text: string): boolean {
  if (typeof URL === "undefined" || typeof URL.createObjectURL !== "function") return false;
  const url = URL.createObjectURL(new Blob([text], { type: "text/csv;charset=utf-8" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
  return true;
}
