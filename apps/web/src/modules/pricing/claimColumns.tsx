import { Badge, type DataTableColumn } from "../../shell/ui";
import type { ClaimStatus, VendorClaim } from "./api/client";
import { CLAIM_STATUS_LABELS, SETTLEMENT_LABELS } from "./rebateFormat";

/** OPEN claims wait for approval; APPROVED claims go to w19 AP for settlement. */
export function ClaimStatusBadge({ status }: { status: ClaimStatus }) {
  return <Badge tone={status === "APPROVED" ? "success" : "neutral"}>{CLAIM_STATUS_LABELS[status]}</Badge>;
}

/** Columns of the vendor rebate claims grids (inquiry and agreement detail). */
export const claimColumns: DataTableColumn<VendorClaim>[] = [
  { key: "number", header: "Claim", mono: true },
  { key: "periodId", header: "Period", mono: true },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (c) => `${c.amount} ${c.currency}` },
  { key: "settlementMode", header: "Settles by", render: (c) => SETTLEMENT_LABELS[c.settlementMode] },
  { key: "status", header: "Status", render: (c) => <ClaimStatusBadge status={c.status} /> },
  { key: "createdAt", header: "Created", mono: true, render: (c) => c.createdAt.slice(0, 10) },
];
