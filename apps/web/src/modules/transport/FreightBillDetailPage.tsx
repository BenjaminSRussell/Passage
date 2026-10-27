import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getFreightBill, type Line, type MatchLine } from "./api/client";
import { fixed, formatDateTime, matchTone, money, statusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const expectedColumns = (currency: string): DataTableColumn<Line>[] => [
  { key: "chargeCode", header: "Charge", mono: true },
  { key: "amount", header: "Expected", align: "end", render: (row) => money(row.amount, currency) },
];

/** The reconciliation grid: expected vs invoiced per charge, lines outside tolerance highlighted (PRD 6.34.6). */
const matchColumns: DataTableColumn<MatchLine>[] = [
  { key: "chargeCode", header: "Charge", mono: true },
  { key: "expected", header: "Expected", align: "end", render: (row) => fixed(row.expected, 2) },
  { key: "invoiced", header: "Invoiced", align: "end", render: (row) => fixed(row.invoiced, 2) },
  {
    key: "variance",
    header: "Variance",
    align: "end",
    render: (row) => <span className={row.within ? undefined : styles.outside}>{fixed(row.variance, 2)}</span>,
  },
  { key: "allowed", header: "Allowed", align: "end", render: (row) => fixed(row.allowed, 2) },
  {
    key: "within",
    header: "Tolerance",
    render: (row) => <Badge tone={row.within ? "success" : "warning"}>{row.within ? "Within" : "Outside"}</Badge>,
  },
];

/** One freight bill with its matched carrier invoice (BR-34.4). */
export function FreightBillDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getFreightBill(id, signal), `freight-bill:${id}`);
  const bill = state.data;

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Freight bill" />
        <Alert tone="danger" title="Could not load the freight bill" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link to="/transport/freight-bills">Back to freight bills</Link>
      </div>
    );
  }
  if (!bill) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Freight bill" />
        <Card>
          <p className={styles.note}>Loading the freight bill…</p>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }

  const entries: [string, string, boolean?][] = [
    ["Freight bill", bill.id, true],
    ["Load", bill.loadId, true],
    ["Carrier", bill.carrierId, true],
    ["Expected total", money(bill.expectedTotal, bill.currency)],
    ["Variance", money(bill.varianceTotal, bill.currency)],
    ["Created", formatDateTime(bill.createdAt)],
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Freight bill ${bill.id.slice(0, 8)}`}
        meta={<Badge tone={matchTone(bill.matchStatus)}>{statusLabel(bill.matchStatus)}</Badge>}
        actions={
          bill.carrierInvoiceId ? null : (
            <Link className={buttonClassName("primary")} to={`/transport/carrier-invoices/new?freightBillId=${bill.id}&carrierId=${bill.carrierId}&currency=${bill.currency}`}>
              Register carrier invoice
            </Link>
          )
        }
      />
      <Card>
        <dl className={styles.details}>
          {entries.map(([term, value, mono]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={`${styles.value} ${mono ? styles.mono : ""}`}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      {bill.match && bill.carrierInvoice ? (
        <Card title={`Matched to invoice ${bill.carrierInvoice.invoiceNo}`}>
          <div className={styles.stack}>
            <p className={styles.note}>
              {bill.match.status === "MATCHED"
                ? "Every charge is within tolerance."
                : "At least one charge is outside the tolerance of $5.00 or 2 % of the expected amount. Approval and dispute come in a later release; nothing is sent to payables until then."}
            </p>
            <DataTable label="Expected and invoiced charges" columns={matchColumns} rows={bill.match.lines} getRowKey={(row) => row.chargeCode} />
          </div>
        </Card>
      ) : (
        <Card title="Expected charges">
          <div className={styles.stack}>
            <DataTable label="Expected charges" columns={expectedColumns(bill.currency)} rows={bill.expectedLines} getRowKey={(row, i) => `${row.chargeCode}-${i}`} />
            <EmptyState compact headingLevel={3} title="No carrier invoice yet" description="Register the carrier's invoice to match it against these charges." />
          </div>
        </Card>
      )}
      <Link to="/transport/freight-bills">Back to freight bills</Link>
    </div>
  );
}
