import { Badge } from "../../shell/ui";
import type { SettlementExportRecord, SettlementExportStatus } from "./api/client";

/** Hands a CSV text to the browser as a file download. */
export function saveCsv(csv: string, filename: string) {
  const url = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

export const EXPORT_STATUS_LABELS: Record<SettlementExportStatus, string> = { ISSUED: "Issued", VOIDED: "Voided" };

/** Issued is in effect and blocks its period; voided is withdrawn and the period is free again. */
export function ExportStatusBadge({ status }: { status: SettlementExportStatus }) {
  return <Badge tone={status === "ISSUED" ? "success" : "danger"}>{EXPORT_STATUS_LABELS[status]}</Badge>;
}

/** The totals of an export in words: one amount per currency. */
export function describeTotals(record: Pick<SettlementExportRecord, "totals">): string {
  return record.totals.length === 0 ? "0" : record.totals.map((t) => `${t.amount} ${t.currency}`).join(", ");
}
