import { useRef, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, PageHeader, Skeleton } from "../../shell/ui";
import { describeProblem, getJob, newKey, reprintJob, type Problem } from "./api/print";
import { dash, describeJobContent, describeJobError, formatDateTime, JobStatusBadge } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** One print job, its outcome, and "print again" (BR-32.8: a new job with reprintOf). */
export function PrintJobDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getJob(id, signal), id);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const key = useRef(newKey());

  async function reprint() {
    if (!state.data) return;
    setBusy(true);
    setFailure(null);
    try {
      const job = await reprintJob(state.data, key.current);
      key.current = newKey();
      navigate(`/mobile-flows/print-jobs/${job.id}`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const back = (
    <Link className={buttonClassName("ghost")} to="/mobile-flows/print-jobs">
      Back to print jobs
    </Link>
  );

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Print job" actions={back} />
        <Alert tone="danger" title="Could not load the print job" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }

  const job = state.data;
  if (!job) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Print job" actions={back} />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }

  const entries: [string, ReactNode, boolean?][] = [
    ["Status", <JobStatusBadge key="s" status={job.status} />],
    ["Content", describeJobContent(job), job.format !== "PDF"],
    ["Format", job.format === "PDF" ? "PDF document" : "ZPL label"],
    ["Queue", job.queueCode, true],
    ["Printer", job.printerCode || dash, true],
    ["Attempts", String(job.attempts)],
    ["Error", job.status === "FAILED" ? describeJobError(job.lastError, job.format) : dash],
    ["Created by", job.createdBy || dash],
    ["Created", formatDateTime(job.createdAt)],
    ["Completed", job.status === "QUEUED" ? dash : formatDateTime(job.updatedAt)],
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Print job"
        description={job.id}
        actions={
          <>
            <Button variant="primary" loading={busy} onClick={reprint}>
              Print again
            </Button>
            {back}
          </>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not print again" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {job.status === "FAILED" ? (
        <Alert tone="warning" title={job.format === "PDF" ? "The document did not print" : "The label did not print"}>
          {describeJobError(job.lastError, job.format)}
          {job.lastError === "PRN-0007" ? "" : " Check the printer, then print again."}
        </Alert>
      ) : null}
      <Card>
        <dl className={styles.details}>
          {entries.map(([term, value, mono]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
          {job.reprintOf ? (
            <div className={styles.entry}>
              <dt className={styles.term}>Reprint of</dt>
              <dd className={styles.value}>
                <Link to={`/mobile-flows/print-jobs/${job.reprintOf}`}>Original job</Link>
              </dd>
            </div>
          ) : null}
        </dl>
      </Card>
    </div>
  );
}
