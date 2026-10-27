import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, PageHeader, Skeleton } from "../../shell/ui";
import { getPayExport } from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** One payroll export batch with its CSV file. */
export function PayExportDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getPayExport(id, signal), id);
  const batch = state.data;
  const href = batch?.csv ? `data:text/csv;charset=utf-8,${encodeURIComponent(batch.csv)}` : undefined;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Export batch"
        description={<Link to="/shopfloor/pay-exports">All payroll exports</Link>}
        actions={
          href ? (
            <a className={buttonClassName("primary")} href={href} download={`payroll-${batch?.batchId}.csv`}>
              Download CSV
            </a>
          ) : null
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the export batch" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : !batch ? (
        <Card>
          <Skeleton />
        </Card>
      ) : (
        <>
          <Card>
            <dl className={styles.details}>
              <div className={styles.entry}>
                <dt className={styles.term}>Batch</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{batch.batchId}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Work dates</dt>
                <dd className={styles.value}>{`${batch.fromDate} to ${batch.toDate}`}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Pay lines</dt>
                <dd className={styles.value}>{batch.lineCount}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Exported</dt>
                <dd className={styles.value}>{`${formatDateTime(batch.createdAt)} by ${batch.createdBy}`}</dd>
              </div>
            </dl>
          </Card>
          <Card title="File">
            <pre className={styles.file} tabIndex={0} aria-label="Payroll CSV">
              {batch.csv}
            </pre>
          </Card>
        </>
      )}
    </div>
  );
}
