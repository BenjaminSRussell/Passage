import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getBottleneck, type BottleneckWeek } from "./api/client";
import { formatDateTime, hours } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** One bottleneck episode with the weekly load of its resource from the first flagged week on (AC-23.6). */
export function BottleneckDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getBottleneck(id, signal), id);
  const episode = state.data;

  if (state.error) {
    const missing = state.error.code === "SCHED-0147";
    return (
      <div className={styles.stack}>
        <PageHeader title="Bottleneck" actions={<Link className={buttonClassName("secondary")} to="/scheduling/bottlenecks">All bottlenecks</Link>} />
        {missing ? (
          <EmptyState title="Bottleneck not found" description="No bottleneck episode has this id." />
        ) : (
          <Alert tone="danger" title="Could not load the bottleneck" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        )}
      </div>
    );
  }
  if (!episode) {
    return (
      <div className={styles.loading} aria-busy="true">
        <span className="visually-hidden">Loading the bottleneck…</span>
        <Skeleton height="control" />
        <Skeleton height="control" />
      </div>
    );
  }

  const facts: [string, string][] = [
    ["Resource", episode.resourceCode],
    ["Rule", `above ${episode.thresholdPct} % for ${episode.consecutiveWeeks} weeks in a row`],
    ["First flagged week", episode.firstWeek],
    ["Detected", formatDateTime(episode.detectedAt)],
    ["Flagged weeks when detected", String(episode.flaggedWeeks)],
    ["Peak load when detected", `${episode.peakLoadPct.toFixed(1)} %`],
  ];
  if (episode.closedAt) facts.push(["Closed", formatDateTime(episode.closedAt)]);
  else facts.push(["Flagged weeks now", String(episode.currentFlaggedWeeks)], ["Peak load now", `${episode.currentPeakLoadPct.toFixed(1)} %`]);

  const columns: DataTableColumn<BottleneckWeek>[] = [
    { key: "weekStart", header: "Week of", mono: true },
    { key: "openMinutes", header: "Open h", align: "end", render: (row) => hours(row.openMinutes) },
    { key: "bookedMinutes", header: "Booked h", align: "end", render: (row) => hours(row.bookedMinutes) },
    { key: "loadPct", header: "Load %", align: "end", render: (row) => (row.openMinutes > 0 ? row.loadPct.toFixed(1) : "No open hours") },
    { key: "bottleneck", header: "Bottleneck", render: (row) => (row.bottleneck ? <Badge tone="warning">Bottleneck</Badge> : "No") },
    {
      key: "jobs",
      header: "Detail",
      render: (row) => {
        const end = new Date(Date.parse(`${row.weekStart}T00:00:00Z`) + 7 * 86_400_000).toISOString();
        return (
          <Link
            to={`/scheduling/load/jobs?${new URLSearchParams({ resourceId: episode.resourceId, from: `${row.weekStart}T00:00:00Z`, to: end.replace(".000Z", "Z") })}`}
            aria-label={`Jobs of ${episode.resourceCode} from ${row.weekStart}`}
          >
            Jobs
          </Link>
        );
      },
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Bottleneck on ${episode.resourceCode}`}
        meta={episode.closedAt ? <Badge tone="neutral">Closed</Badge> : <Badge tone="warning">Open</Badge>}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={`/scheduling/resources/${episode.resourceId}`}>
              Open the resource
            </Link>
            <Link className={buttonClassName("secondary")} to="/scheduling/bottlenecks">
              All bottlenecks
            </Link>
          </>
        }
      />
      <Card>
        <dl className={styles.details}>
          {facts.map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <DataTable
        label="Weekly load"
        columns={columns}
        rows={episode.weeks}
        getRowKey={(row) => row.weekStart}
        empty={{ title: "No booked weeks", description: "The resource has no booking from the first flagged week on." }}
      />
    </div>
  );
}
