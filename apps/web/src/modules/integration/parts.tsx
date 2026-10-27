import { Alert, Badge, DataTable, type DataTableColumn } from "../../shell/ui";
import type {
  AckDocument,
  AckStatus,
  AsnDocument,
  CanonicalDocument,
  CanonicalOrder,
  FunctionalAck,
  InvoiceDocument,
  Issue,
  ItemRefDoc,
  MessageStatus,
  OrderLine,
  Problem,
} from "./api/client";
import { ACK_LABELS, ACK_TYPES, AK_CODES, dash, formatEdiDate, MESSAGE_LABELS, PURPOSES } from "./format";
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

/** A failed request: the problem text and code, and the fields the service rejected. */
export function ProblemAlert({ problem, title }: { problem: Problem | null; title: string }) {
  const fields = problem ? Object.entries(problem.fields) : [];
  return (
    <Alert tone="danger" title={problem ? title : undefined} live="alert">
      {problem ? (
        <>
          {`${problem.message} (${problem.code})`}
          {fields.length > 0 ? (
            <ul className={styles.plainList} aria-label="Rejected fields">
              {fields.map(([field, message]) => (
                <li key={field}>
                  <span className={styles.mono}>{field}</span> {message}
                </li>
              ))}
            </ul>
          ) : null}
        </>
      ) : null}
    </Alert>
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

const refs = (items: ItemRefDoc[] = []) => (items.length ? items.map((r) => `${r.qualifier} ${r.value}`).join(", ") : dash);
const place = (loc?: { name: string; gln: string }) => (loc ? [loc.name, loc.gln ? `GLN ${loc.gln}` : ""].filter(Boolean).join(" · ") : dash);

interface AsnRow {
  key: string;
  poNumber: string;
  sscc: string;
  lineNo: string;
  quantity: string;
  itemRefs: ItemRefDoc[];
}

const asnColumns: DataTableColumn<AsnRow>[] = [
  { key: "poNumber", header: "Order", mono: true },
  { key: "sscc", header: "SSCC", mono: true },
  { key: "lineNo", header: "Line", mono: true },
  { key: "itemRefs", header: "Item references", mono: true, render: (row) => refs(row.itemRefs) },
  { key: "quantity", header: "Shipped", align: "end" },
];

/** An outbound 856: shipment header and the order, pack and item levels. */
export function AsnView({ asn, label }: { asn: AsnDocument; label: string }) {
  const rows: AsnRow[] = (asn.orders ?? []).flatMap((o) =>
    (o.packs ?? []).flatMap((p) =>
      (p.items ?? []).map((it, i) => ({
        key: `${p.sscc}-${it.lineNo}-${i}`,
        poNumber: o.poNumber,
        sscc: p.sscc,
        lineNo: it.lineNo,
        quantity: `${it.quantity} ${it.uom}`,
        itemRefs: it.itemRefs,
      })),
    ),
  );
  return (
    <div className={styles.stack}>
      <dl className={styles.details}>
        <Entry term="Shipment" value={asn.shipmentId} mono />
        <Entry term="Shipped" value={`${formatEdiDate(asn.shipDate)} ${asn.shipTime ?? ""}`.trim()} />
        <Entry term="Carrier (SCAC)" value={asn.scac} mono />
        <Entry term="Gross weight" value={asn.grossWeight ? `${asn.grossWeight} ${asn.weightUnit}` : undefined} />
        <Entry term="Bill of lading" value={asn.bol} mono />
        <Entry term="Ship from" value={place(asn.shipFrom)} />
        <Entry term="Ship to" value={place(asn.shipTo)} />
      </dl>
      <DataTable label={label} columns={asnColumns} rows={rows} getRowKey={(row) => row.key} empty={{ title: "No packs", description: "The notice carries no items." }} />
    </div>
  );
}

type InvoiceLineRow = InvoiceDocument["lines"][number];

const invoiceColumns: DataTableColumn<InvoiceLineRow>[] = [
  { key: "lineNo", header: "Line", mono: true },
  { key: "itemRefs", header: "Item references", mono: true, render: (row) => refs(row.itemRefs) },
  { key: "quantity", header: "Quantity", align: "end", render: (row) => `${row.quantity} ${row.uom}` },
  { key: "unitPrice", header: "Unit price", align: "end" },
];

/** An outbound 810: header, lines, and the total TDS01 carries. */
export function InvoiceView({ invoice, label }: { invoice: InvoiceDocument; label: string }) {
  return (
    <div className={styles.stack}>
      <dl className={styles.details}>
        <Entry term={invoice.creditNote ? "Credit note" : "Invoice"} value={invoice.number} mono />
        <Entry term="Invoice date" value={formatEdiDate(invoice.date)} />
        <Entry term="Purchase order" value={invoice.poNumber} mono />
        <Entry term="Bill to" value={place(invoice.billTo)} />
        <Entry term="Ship to" value={place(invoice.shipTo)} />
        <Entry term="Total" value={invoice.total} mono />
      </dl>
      <DataTable label={label} columns={invoiceColumns} rows={invoice.lines ?? []} getRowKey={(row, i) => `${row.lineNo}-${i}`} empty={{ title: "No lines" }} />
    </div>
  );
}

type AckLineRow = NonNullable<AckDocument["lines"]>[number];

const ackColumns: DataTableColumn<AckLineRow>[] = [
  { key: "lineNo", header: "Line", mono: true },
  { key: "status", header: "Status", mono: true },
  { key: "orderedQty", header: "Ordered", align: "end", render: (row) => `${row.orderedQty} ${row.uom}` },
  { key: "ackQty", header: "Acknowledged", align: "end", render: (row) => `${row.ackQty} ${row.uom}` },
];

/** An outbound 855 purchase order acknowledgment. */
export function AckDocumentView({ ack, label }: { ack: AckDocument; label: string }) {
  return (
    <div className={styles.stack}>
      <dl className={styles.details}>
        <Entry term="Purchase order" value={ack.poNumber} mono />
        <Entry term="Acknowledgment" value={ACK_TYPES[ack.ackType] ? `${ACK_TYPES[ack.ackType]} (${ack.ackType})` : ack.ackType} />
        <Entry term="Acknowledged on" value={formatEdiDate(ack.ackDate)} />
      </dl>
      {ack.lines && ack.lines.length > 0 ? (
        <DataTable label={label} columns={ackColumns} rows={ack.lines} getRowKey={(row, i) => `${row.lineNo}-${i}`} empty={{ title: "No lines" }} />
      ) : null}
    </div>
  );
}

/** A partner's 997: which of our groups and transactions it answers. */
export function FunctionalAckView({ ack, label }: { ack: FunctionalAck; label: string }) {
  return (
    <div className={styles.stack}>
      <dl className={styles.details}>
        <Entry term="Answers our group" value={`${ack.functionalId} ${ack.groupControl}`} mono />
        <Entry term="Group result (AK9)" value={AK_CODES[ack.code] ? `${AK_CODES[ack.code]} (${ack.code})` : ack.code} />
      </dl>
      {ack.sets.length > 0 ? (
        <ul className={styles.plainList} aria-label={label}>
          {ack.sets.map((set) => (
            <li key={`${set.setId}-${set.control}`} className={styles.issue}>
              <Badge tone={set.code === "R" ? "danger" : set.code === "E" ? "warning" : "success"}>{AK_CODES[set.code] ?? set.code}</Badge>
              <span className={styles.mono}>{`${set.setId} ST02 ${set.control}`}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className={styles.note}>The 997 answers the whole group without per-transaction detail.</p>
      )}
    </div>
  );
}

/** The canonical document of one stored message, shown by its transaction set. */
export function CanonicalView({ transactionSet, document, label }: { transactionSet: string; document: CanonicalDocument; label: string }) {
  switch (transactionSet) {
    case "850":
      return <OrderView order={document as CanonicalOrder} label={label} />;
    case "856":
      return <AsnView asn={document as AsnDocument} label={label} />;
    case "810":
      return <InvoiceView invoice={document as InvoiceDocument} label={label} />;
    case "855":
      return <AckDocumentView ack={document as AckDocument} label={label} />;
    case "997":
      return <FunctionalAckView ack={document as FunctionalAck} label={label} />;
    default:
      return <p className={styles.note}>{`No view for transaction set ${transactionSet}.`}</p>;
  }
}
