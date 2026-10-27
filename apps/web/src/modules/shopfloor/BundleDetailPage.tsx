import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getWorker, listBundle, type Registration, type Worker } from "./api/client";
import { dash, fixed, formatDateTime, formatHours, formatOffset, sumDecimals, REGISTRATION_FLAG_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Bundle {
  rows: Registration[];
  /** The worker the bundle belongs to; null when the bundle is empty. */
  worker: Worker | null;
}

const columns: DataTableColumn<Registration>[] = [
  { key: "jobId", header: "Job", mono: true, render: (row) => row.jobId ?? dash },
  { key: "kind", header: "Kind" },
  { key: "startedAt", header: "Started", mono: true, render: (row) => formatDateTime(row.startedAt) },
  { key: "stoppedAt", header: "Stopped", mono: true, render: (row) => formatDateTime(row.stoppedAt) },
  { key: "qtyGood", header: "Good", align: "end", render: (row) => fixed(row.qtyGood, 2) },
  { key: "qtyScrap", header: "Scrap", align: "end", render: (row) => fixed(row.qtyScrap, 2) },
  { key: "hours", header: "Hours share", align: "end", render: (row) => formatHours(row.hours) },
  {
    key: "offline",
    header: "Source",
    render: (row) => (
      <span className={styles.flags}>
        {row.offline ? <Badge tone="info">Offline replay</Badge> : "Online"}
        {row.clockOffsetSeconds !== null ? <Badge tone="warning">{`Clock corrected ${formatOffset(row.clockOffsetSeconds)}`}</Badge> : null}
        {(row.flags ?? []).map((flag) => (
          <Badge key={flag} tone="warning">
            {REGISTRATION_FLAG_LABELS[flag] ?? flag}
          </Badge>
        ))}
      </span>
    ),
  },
];

/**
 * One job bundle (BR-25.1): the jobs a worker ran together, with the bundle's time
 * split across them by good quantity, so the bundle counts its hours once.
 */
export function BundleDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<Bundle>(async (signal) => {
    const rows = await listBundle(id, signal);
    const worker = rows.length > 0 ? await getWorker(rows[0].workerId, signal) : null;
    return { rows, worker };
  }, id);

  const header = (worker: Worker | null) => (
    <PageHeader
      title="Bundle"
      description="Jobs worked together. Their time is counted once and split by good quantity (equally when none is reported)."
      actions={
        worker ? (
          <Link className={buttonClassName("secondary")} to={`/shopfloor/workers/${worker.id}`}>
            {`Back to ${worker.name}`}
          </Link>
        ) : null
      }
    />
  );

  if (state.error) {
    return (
      <div className={styles.stack}>
        {header(null)}
        <Alert tone="danger" title="Could not load the bundle" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        {header(null)}
        <span className="visually-hidden">Loading the bundle…</span>
        <Card>
          <Skeleton />
        </Card>
      </div>
    );
  }
  const { rows, worker } = state.data;
  if (rows.length === 0) {
    return (
      <div className={styles.stack}>
        {header(null)}
        <Card>
          <EmptyState title="No registrations in this bundle" description="A bundle appears here once the kiosk posts its jobs." />
        </Card>
      </div>
    );
  }
  const closed = rows.filter((row) => row.hours !== null);
  const total = closed.length > 0 ? sumDecimals(closed.map((row) => row.hours ?? "0"), 2) : null;
  const interval = closed[0];

  return (
    <div className={styles.stack}>
      {header(worker)}
      <Card>
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Worker</dt>
            <dd className={styles.value}>{worker ? <Link to={`/shopfloor/workers/${worker.id}`}>{worker.name}</Link> : dash}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Worked</dt>
            <dd className={styles.value}>{interval ? `${formatDateTime(interval.startedAt)} to ${formatDateTime(interval.stoppedAt)}` : "Still running"}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Bundle hours</dt>
            <dd className={styles.value}>{formatHours(total)}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Jobs</dt>
            <dd className={styles.value}>{new Set(rows.map((row) => row.jobId)).size}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Bundle id</dt>
            <dd className={`${styles.value} ${styles.mono}`}>{id}</dd>
          </div>
        </dl>
      </Card>
      <Card title="Jobs in the bundle">
        <DataTable label="Bundle jobs" columns={columns} rows={rows} getRowKey={(row) => row.id} loading={state.loading} />
      </Card>
    </div>
  );
}
