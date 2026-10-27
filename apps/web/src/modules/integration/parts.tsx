import { Badge, DataTable, type DataTableColumn } from "../../shell/ui";
import type { AckStatus, CanonicalOrder, Issue, MessageStatus, OrderLine } from "./api/client";
import { ACK_LABELS, dash, formatEdiDate, MESSAGE_LABELS, PURPOSES } from "./format";
import styles from "./pages.module.css";

export function AckPill({ status }: { status: AckStatus }) {
  const { label, tone } = ACK_LABELS[status] ?? { label: status, tone: "neutral" as const };
  return <Badge tone={tone}>{label}</Badge>;
}

export function MessagePill({ status }: { status: MessageStatus | "MAPPED" | "FAILED" }) {
  const { label, tone } = MESSAGE_LABELS[status] ?? { label: status, tone: "neutral" as const };
  return <Badge tone={tone}>{label}</Badge>;
}

/** Findings on one transaction; rejecting findings make the 997 AK5 code R. */
export function IssueList({ issues, label }: { issues: Issue[]; label: string }) {
  if (issues.length === 0) return null;
  return (
    <ul className={styles.plainList} aria-label={label}>
      {issues.map((issue, i) => (
        <li key={`${issue.code}-${i}`} className={styles.issue}>
          <Badge tone={issue.reject ? "danger" : "warning"}>{issue.reject ? "Rejects" : "Warning"}</Badge>
          <span className={styles.mono}>{issue.code}</span>
          <span>{issue.detail}</span>
        </li>
      ))}
    </ul>
  );
}

const lineColumns: DataTableColumn<OrderLine>[] = [
  { key: "lineNo", header: "Line", mono: true },
  { key: "itemRef", header: "Item reference", mono: true, render: (row) => (row.itemRef ? `${row.itemRef.qualifier} ${row.itemRef.value}` : dash) },
  { key: "description", header: "Description", render: (row) => row.description ?? dash },
  { key: "quantity", header: "Quantity", align: "end", render: (row) => `${row.quantity} ${row.uom}` },
  { key: "unitPrice", header: "Unit price", align: "end", render: (row) => (row.priceBasis ? `${row.unitPrice} (${row.priceBasis})` : row.unitPrice) },
];

function Entry({ term, value, mono }: { term: string; value?: string; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value || dash}</dd>
    </div>
  );
}

/** The canonical order an 850 maps to: header fields and the PO1 lines. */
export function OrderView({ order, label }: { order: CanonicalOrder; label: string }) {
  const party = (name?: string, gln?: string) => [name, gln ? `GLN ${gln}` : ""].filter(Boolean).join(" · ");
  return (
    <div className={styles.stack}>
      <dl className={styles.details}>
        <Entry term="Purchase order" value={order.poNumber} mono />
        <Entry term="Purpose" value={PURPOSES[order.purpose] ? `${PURPOSES[order.purpose]} (${order.purpose})` : order.purpose} />
        <Entry term="Order type" value={order.orderType} mono />
        <Entry term="Order date" value={formatEdiDate(order.poDate)} />
        <Entry term="Currency" value={order.currency} mono />
        <Entry term="Requested delivery" value={formatEdiDate(order.requestedDelivery)} />
        <Entry term="Ship to" value={party(order.shipToName, order.shipToGln)} />
        <Entry term="Bill to" value={party(order.billToName, order.billToGln)} />
      </dl>
      <DataTable
        label={label}
        columns={lineColumns}
        rows={order.lines}
        getRowKey={(row, i) => `${row.lineNo}-${i}`}
        empty={{ title: "No lines mapped", description: "The 850 carried no valid PO1 lines." }}
      />
      {order.charges && order.charges.length > 0 ? (
        <ul className={styles.plainList} aria-label={`${label} charges`}>
          {order.charges.map((c, i) => (
            <li key={`${c.code}-${i}`} className={styles.issue}>
              <Badge tone="neutral">{c.indicator === "C" ? "Charge" : "Allowance"}</Badge>
              <span className={styles.mono}>{c.code}</span>
              <span>{c.amount}</span>
              {c.description ? <span>{c.description}</span> : null}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
