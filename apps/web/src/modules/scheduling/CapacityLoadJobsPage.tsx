import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { fetchLoadJobs, getResource, type LoadJob, type LoadJobs, type Resource } from "./api/client";
import { dash, formatDateTime, formatMinutes, shortId, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const MAX_RANGE_MS = 93 * 86_400_000;
const INSTANT = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2})$/;

interface Detail {
  resource: Resource;
  jobs: LoadJobs;
}

/** The query this page accepts (a resource and the [from, to) instants of a load bucket), or null when it is not usable. */
export function readRange(params: URLSearchParams): { resourceId: string; from: string; to: string } | null {
  const resourceId = params.get("resourceId") ?? "";
  const from = params.get("from") ?? "";
  const to = params.get("to") ?? "";
  if (!UUID.test(resourceId) || !INSTANT.test(from) || !INSTANT.test(to)) return null;
  const span = Date.parse(to) - Date.parse(from);
  if (Number.isNaN(span) || span <= 0 || span > MAX_RANGE_MS) return null;
  return { resourceId, from, to };
}

/** The jobs behind one capacity load cell: every segment booked on the resource in the period (PRD 4.23.14 Capacity load detail). */
export function CapacityLoadJobsPage() {
  const [params] = useSearchParams();
  const range = readRange(params);
  const state = useLoad<Detail | null>(async (signal) => {
    if (!range) return null;
    const [resource, jobs] = await Promise.all([
      getResource(range.resourceId, signal),
      fetchLoadJobs(range.resourceId, range.from, range.to, signal),
    ]);
    return { resource, jobs };
  }, range ? `${range.resourceId}|${range.from}|${range.to}` : "invalid");

  const back = (
    <Link className={buttonClassName("secondary")} to="/scheduling/load">
      Back to capacity load
    </Link>
  );
  if (!range) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Capacity load detail" actions={back} />
        <Alert tone="danger" title="This link is incomplete" live="alert">
          Open the jobs of a resource from a row of the capacity load table.
        </Alert>
      </div>
    );
  }

  const columns: DataTableColumn<LoadJob>[] = [
    { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
    { key: "operNumber", header: "Operation", align: "end" },
    { key: "segment", header: "Segment", align: "end" },
    { key: "role", header: "Role", render: (row) => (row.role === "secondary" ? "Secondary" : "Primary") },
    { key: "start", header: "Start", render: (row) => formatDateTime(row.start) },
    { key: "end", header: "End", render: (row) => formatDateTime(row.end) },
    { key: "minutesInRange", header: "Minutes in period", align: "end", render: (row) => formatMinutes(row.minutesInRange) },
    {
      key: "status",
      header: "Status",
      render: (row) => (
        <>
          {row.firmed && <Badge tone="info">Firmed</Badge>} {!row.finite && <Badge tone="neutral">Infinite run</Badge>}
          {row.lateMinutes > 0 && <Badge tone="warning">{`Late ${formatMinutes(row.lateMinutes)}`}</Badge>}
          {!row.firmed && row.finite && row.lateMinutes === 0 ? dash : null}
        </>
      ),
    },
  ];
  const title = state.data ? `Jobs on ${state.data.resource.code}` : "Capacity load detail";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={title}
        description={`Segments booked from ${formatDateTime(range.from)} to ${formatDateTime(range.to)}${state.data ? ` (resource time zone ${state.data.resource.timeZone})` : ""}, earliest first.`}
        actions={back}
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the jobs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          {state.data?.jobs.truncated && (
            <Alert tone="warning" title="Showing the first 1,000 jobs">
              Choose a shorter period to see the rest.
            </Alert>
          )}
          <DataTable
            label="Jobs booked in the period"
            columns={columns}
            rows={state.data?.jobs.items ?? []}
            getRowKey={(row) => `${row.jobId}-${row.role}-${row.segment}`}
            loading={state.loading}
            empty={{ title: "Nothing is booked in this period", description: "No scheduled job holds this resource between these dates." }}
          />
        </>
      )}
    </div>
  );
}
