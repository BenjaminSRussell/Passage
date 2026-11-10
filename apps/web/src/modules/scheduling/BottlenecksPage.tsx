import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeProblem, listBottlenecks, refreshCapacityLoad, type BottleneckEpisode, type BottleneckStatus, type LoadRefresh, type Problem } from "./api/client";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const STATUS_OPTIONS: { value: BottleneckStatus; label: string }[] = [
  { value: "open", label: "Open" },
  { value: "closed", label: "Closed" },
  { value: "all", label: "All" },
];

/** Bottleneck episodes of the resources: a resource with weeks above the load threshold for consecutive weeks (AC-23.6, SCHED-0015). */
export function BottlenecksPage() {
  const [status, setStatus] = useState<BottleneckStatus>("open");
  const [busy, setBusy] = useState(false);
  const [rebuilt, setRebuilt] = useState<LoadRefresh | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const state = useLoad((signal) => listBottlenecks(status, signal), status);

  async function rebuild() {
    setBusy(true);
    setFailure(null);
    try {
      setRebuilt(await refreshCapacityLoad());
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<BottleneckEpisode>[] = [
    { key: "resourceCode", header: "Resource", mono: true },
    { key: "firstWeek", header: "From week of", mono: true },
    {
      key: "currentFlaggedWeeks",
      header: "Flagged weeks",
      align: "end",
      render: (row) => (row.closedAt ? `${row.flaggedWeeks} when detected` : String(row.currentFlaggedWeeks)),
    },
    {
      key: "currentPeakLoadPct",
      header: "Peak load %",
      align: "end",
      render: (row) => (row.closedAt ? row.peakLoadPct.toFixed(1) : row.currentPeakLoadPct.toFixed(1)),
    },
    { key: "rule", header: "Rule", render: (row) => `above ${row.thresholdPct} % for ${row.consecutiveWeeks} weeks` },
    { key: "detectedAt", header: "Detected", render: (row) => formatDateTime(row.detectedAt) },
    { key: "closedAt", header: "Status", render: (row) => (row.closedAt ? <Badge tone="neutral">{`Closed ${formatDateTime(row.closedAt)}`}</Badge> : <Badge tone="warning">Open</Badge>) },
    {
      key: "open",
      header: "Detail",
      render: (row) => (
        <Link to={`/scheduling/bottlenecks/${row.id}`} aria-label={`Bottleneck of ${row.resourceCode} from ${row.firstWeek}`}>
          Open
        </Link>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Bottlenecks"
        description="Resources whose weekly load stays above the threshold for consecutive weeks. An episode is reported once when it opens and closes when the resource has no such week ahead. The weekly load behind it is updated with every run, move and calendar change."
        actions={
          <>
            <Button variant="secondary" loading={busy} onClick={rebuild}>
              Rebuild weekly load
            </Button>
            <Link className={buttonClassName("secondary")} to="/scheduling/load">
              Capacity load
            </Link>
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="bottleneck-status" label="Show">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(STATUS_OPTIONS.find((o) => o.value === event.target.value)?.value ?? "open")}>
              {STATUS_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {failure ? (
        <Alert tone="danger" title="Could not rebuild the weekly load" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {rebuilt ? (
        <Alert tone="success" live="status" title="Weekly load rebuilt">
          {`${rebuilt.resources} resources, ${rebuilt.loadRows} loaded weeks, ${rebuilt.openEpisodes} open bottlenecks.`}
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load bottlenecks" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Bottlenecks"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{
            title: status === "closed" ? "No closed bottlenecks" : "No bottlenecks",
            description: status === "closed" ? dash : "No resource is loaded above the threshold for consecutive weeks.",
          }}
        />
      )}
    </div>
  );
}
