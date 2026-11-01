import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getOrderImport, ORDER_IMPORT_SKIP_LABELS, type OrderImportSkipped } from "./api/client";
import { dash, formatDateTime, shortId } from "./format";
import { ImportStatusBadge } from "./OrderImportsPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const skippedColumns: DataTableColumn<OrderImportSkipped>[] = [
  { key: "operNumber", header: "Operation", align: "end" },
  { key: "code", header: "Reason", render: (row) => <Badge tone={row.code === "firmed" ? "info" : "warning"}>{ORDER_IMPORT_SKIP_LABELS[row.code] ?? row.code}</Badge> },
  { key: "detail", header: "Detail" },
];

/** One received production order: what happened to each of its operations (FR-23.10). */
export function OrderImportDetailPage() {
  const { orderId = "" } = useParams();
  const state = useLoad((signal) => getOrderImport(orderId, signal), orderId);
  const o = state.data;

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Production order" />
        <Alert tone="danger" title="Could not load the production order" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!o) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Production order" />
        <span className="visually-hidden">Loading the production order…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  const facts: [string, string][] = [
    ["Order", o.orderNumber || shortId(o.orderId)],
    ["Order id", o.orderId],
    ["Order version", String(o.orderVersion)],
    ["Event", o.reason === "redate" ? "Re-dated after a change" : "Scheduled"],
    ["Operations in the event", String(o.operationsTotal)],
    ["Created", String(o.created)],
    ["Updated (jobs dropped)", String(o.updated)],
    ["Unchanged", String(o.unchanged)],
    ["Removed from the route", String(o.removed)],
    ["Kept, held by a firmed job", String(o.keptFirmed)],
    ["Baseline start", o.orderStart ? formatDateTime(o.orderStart) : dash],
    ["Baseline end", o.orderEnd ? formatDateTime(o.orderEnd) : dash],
    ["Received", formatDateTime(o.receivedAt)],
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Production order ${o.orderNumber || shortId(o.orderId)}`}
        meta={<ImportStatusBadge status={o.status} />}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/scheduling/operations">
              Operations
            </Link>
            {o.replanRequired && (
              <Link className={buttonClassName("primary")} to="/scheduling/runs/new">
                Start run
              </Link>
            )}
          </>
        }
      />
      {o.replanRequired && (
        <Alert tone="warning" title="Schedule this order again">
          An operation changed after it was placed, so its jobs were removed and it is open again. Start a run to place it.
        </Alert>
      )}
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={term === "Order id" ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Operations not registered">
        {o.skipped.length === 0 ? (
          <EmptyState compact headingLevel={3} title="Nothing left out" description="Every operation of the order is registered for scheduling." />
        ) : (
          <DataTable label="Operations not registered" columns={skippedColumns} rows={o.skipped} getRowKey={(row) => `${row.operNumber}-${row.code}`} />
        )}
      </Card>
    </div>
  );
}
