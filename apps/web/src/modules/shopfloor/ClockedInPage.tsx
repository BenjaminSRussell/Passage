import { useNavigate } from "react-router-dom";
import { Alert, Badge, Button, DataTable, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { listClockedIn, type ClockedWorker } from "./api/client";
import { dash, elapsed, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const columns: DataTableColumn<ClockedWorker>[] = [
  {
    key: "name",
    header: "Worker",
    render: (row) => (row.active ? row.name : <span className={styles.actions}>{row.name} <Badge tone="warning">Inactive</Badge></span>),
  },
  { key: "badgeId", header: "Badge", mono: true, render: (row) => row.badgeId ?? dash },
  { key: "clockedInAt", header: "Clocked in", render: (row) => formatDateTime(row.clockedInAt) },
  { key: "present", header: "Present for", align: "end", render: (row) => elapsed(row.clockedInAt) },
  {
    key: "jobs",
    header: "Current jobs",
    render: (row) =>
      row.jobs.length === 0 ? (
        <span className={styles.note}>No open job</span>
      ) : (
        <span className={styles.flags}>
          {row.jobs.map((job) => (
            <span key={job.jobId} className={styles.mono}>
              {job.jobId}
              {job.bundleId ? " (bundle)" : ""}
            </span>
          ))}
        </span>
      ),
  },
  {
    key: "jobSince",
    header: "On the job for",
    align: "end",
    render: (row) => (row.jobs.length === 0 ? dash : elapsed(row.jobs[0].startedAt)),
  },
];

/** Who is clocked in, with the jobs they are on (inquiry 4.25.14, FR-25.14 supervisor view). */
export function ClockedInPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listClockedIn(signal), "clocked-in");
  const rows = state.data ?? [];
  const onJob = rows.filter((r) => r.jobs.length > 0).length;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Who is clocked in"
        description="Workers whose last punch is a clock-in, longest present first, with the jobs they started since and have not stopped."
        actions={<Button onClick={state.reload} loading={state.loading}>Refresh</Button>}
      />
      {state.data ? (
        <StatGroup label="Attendance summary">
          <StatTile label="Clocked in" value={rows.length} />
          <StatTile label="On a job" value={onJob} />
          <StatTile label="Without a job" value={rows.length - onJob} tone="warning" />
        </StatGroup>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load who is clocked in" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Clocked-in workers"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.workerId}
          loading={state.loading}
          empty={{ title: "Nobody is clocked in", description: "Workers appear here when they clock in on a kiosk and leave when they clock out." }}
          onRowOpen={(row) => navigate(`/shopfloor/workers/${row.workerId}`)}
        />
      )}
    </div>
  );
}
