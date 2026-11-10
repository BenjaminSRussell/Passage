import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { CODE, JOB_STATUSES, listJobs, type JobStatus, type PrintJob } from "./api/print";
import { dash, describeJobContent, formatDateTime, JobStatusBadge } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<PrintJob>[] = [
  { key: "createdAt", header: "Created", render: (row) => formatDateTime(row.createdAt) },
  { key: "templateCode", header: "Content", mono: true, render: (row) => describeJobContent(row) },
  { key: "queueCode", header: "Queue", mono: true },
  { key: "printerCode", header: "Printer", mono: true, render: (row) => row.printerCode || dash },
  { key: "status", header: "Status", render: (row) => <JobStatusBadge status={row.status} /> },
  { key: "attempts", header: "Attempts", align: "end", render: (row) => String(row.attempts) },
  { key: "lastError", header: "Error", mono: true, render: (row) => row.lastError || dash },
  { key: "reprintOf", header: "Reprint", render: (row) => (row.reprintOf ? "Yes" : dash) },
];

/** Print job log (§6.32.14): newest first, filtered by status or queue. */
export function PrintJobsPage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<JobStatus | "">("");
  const [queue, setQueue] = useState("");
  const queueFilter = queue.trim();
  const queueError = queueFilter !== "" && !CODE.test(queueFilter) ? "Use the queue code, such as DOCK." : undefined;
  const state = useLoad(
    (signal) => listJobs({ status, queueCode: queueError ? "" : queueFilter }, signal),
    `${status}|${queueError ? "" : queueFilter}`,
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Print jobs"
        description="Every label and document sent to a printer, with the device that printed it and the attempts it took. Failed jobs show MOB-0111 when no device answered, MOB-0112 when the queue has no printer of the right type, and PRN-0007 when the label does not fit any printer's media."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/mobile-flows/print-documents/new">
              Print document
            </Link>
            <Link className={buttonClassName("primary")} to="/mobile-flows/print-jobs/new">
              Print label
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="jobs-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as JobStatus | "")}>
              <option value="">All</option>
              {JOB_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {s.charAt(0) + s.slice(1).toLowerCase()}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="jobs-queue" label="Queue" optional error={queueError}>
          {(control) => <Input {...control} value={queue} onChange={(event) => setQueue(event.target.value)} invalid={Boolean(queueError)} />}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load print jobs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Print jobs"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          onRowOpen={(row) => navigate(`/mobile-flows/print-jobs/${row.id}`)}
          empty={{ title: "No print jobs", description: "Jobs appear here when a device or this page prints a label or a document." }}
        />
      )}
    </div>
  );
}
