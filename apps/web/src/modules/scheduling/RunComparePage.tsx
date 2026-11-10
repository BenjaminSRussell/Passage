import { useEffect, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, EmptyState, FormField, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import { compareRuns, listRuns, type RunCompareChange, type RunCompareJob, type RunMeasures } from "./api/client";
import { dash, formatDateTime, formatMinutes, shortId } from "./format";
import styles from "./pages.module.css";
import { scopeLabels } from "./RunsPage";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import { OrderNumber } from "../mfg-shared/orders";

function signed(minutes: number): string {
  if (minutes === 0) return "0 min";
  return `${minutes > 0 ? "+" : "-"}${formatMinutes(Math.abs(minutes))}`;
}

const jobColumns: DataTableColumn<RunCompareJob>[] = [
  { key: "orderId", header: "Order", mono: true, render: (row) => <OrderNumber id={row.orderId} /> },
  { key: "operNumber", header: "Operation", align: "end" },
  { key: "resourceId", header: "Resource", mono: true, render: (row) => shortId(row.resourceId) },
  { key: "start", header: "Start", render: (row) => formatDateTime(row.start) },
  { key: "end", header: "End", render: (row) => formatDateTime(row.end) },
  { key: "lateMinutes", header: "Late", align: "end", render: (row) => (row.lateMinutes > 0 ? formatMinutes(row.lateMinutes) : dash) },
];

const changeColumns: DataTableColumn<RunCompareChange>[] = [
  { key: "orderId", header: "Order", mono: true, render: (row) => <OrderNumber id={row.orderId} /> },
  { key: "operNumber", header: "Operation", align: "end" },
  {
    key: "fromResourceId",
    header: "Resource",
    mono: true,
    render: (row) => (row.fromResourceId === row.toResourceId ? shortId(row.fromResourceId) : `${shortId(row.fromResourceId)} to ${shortId(row.toResourceId)}`),
  },
  { key: "fromStart", header: "Start in the base run", render: (row) => formatDateTime(row.fromStart) },
  { key: "toStart", header: "Start in the other run", render: (row) => formatDateTime(row.toStart) },
  { key: "startShiftMinutes", header: "Start shift", align: "end", render: (row) => signed(row.startShiftMinutes) },
  { key: "endShiftMinutes", header: "End shift", align: "end", render: (row) => signed(row.endShiftMinutes) },
  { key: "lateDeltaMinutes", header: "Lateness change", align: "end", render: (row) => signed(row.lateDeltaMinutes) },
];

interface MeasureRow {
  label: string;
  base: string;
  other: string;
}

function measureRows(base: RunMeasures, other: RunMeasures): MeasureRow[] {
  return [
    { label: "Jobs", base: String(base.jobs), other: String(other.jobs) },
    { label: "Orders", base: String(base.orders), other: String(other.orders) },
    { label: "Late jobs", base: String(base.lateJobs), other: String(other.lateJobs) },
    { label: "Total lateness", base: formatMinutes(base.totalLateMinutes), other: formatMinutes(other.totalLateMinutes) },
    { label: "Latest job late by", base: formatMinutes(base.maxLateMinutes), other: formatMinutes(other.maxLateMinutes) },
    { label: "Changeover time", base: formatMinutes(base.changeoverMinutes), other: formatMinutes(other.changeoverMinutes) },
    { label: "Makespan", base: formatMinutes(base.makespanMinutes), other: formatMinutes(other.makespanMinutes) },
    { label: "Booked time", base: formatMinutes(base.bookedMinutes), other: formatMinutes(other.bookedMinutes) },
    { label: "Utilization", base: `${base.utilizationPct.toFixed(1)} %`, other: `${other.utilizationPct.toFixed(1)} %` },
  ];
}

const measureColumns: DataTableColumn<MeasureRow>[] = [
  { key: "label", header: "Measure" },
  { key: "base", header: "Base run", align: "end" },
  { key: "other", header: "Other run", align: "end" },
];

/**
 * Compares the jobs two schedule runs placed (PRD 4.23.14 Run comparison): the
 * run in the path is the base, the one picked here (or in ?to=) the other.
 */
export function RunComparePage() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const to = params.get("to") ?? "";
  const [pick, setPick] = useState(to);
  useEffect(() => setPick(to), [to]);
  const runs = useLoad((signal) => listRuns(signal), "runs");
  const result = useLoad((signal) => (to ? compareRuns(id, to, signal) : Promise.resolve(null)), `${id}:${to}`);
  const choices = (runs.data ?? []).filter((r) => r.id !== id && r.snapshot);
  const cmp = result.data;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Compare schedule runs"
        description="Which jobs the other run added, dropped or moved against the base run, and how the plan measures differ."
        actions={
          <Link className={buttonClassName("secondary")} to={`/scheduling/runs/${id}`}>
            Back to the run
          </Link>
        }
      />
      <Card title="Runs">
        {runs.error ? (
          <Alert tone="danger" title="Could not load runs" live="alert" action={<Button size="sm" onClick={runs.reload}>Try again</Button>}>
            <ProblemMessage problem={runs.error} />
          </Alert>
        ) : !runs.data ? (
          <div aria-busy="true">
            <span className="visually-hidden">Loading runs…</span>
            <Skeleton height="control" />
          </div>
        ) : choices.length === 0 ? (
          <EmptyState
            compact
            headingLevel={3}
            title="No other run to compare with"
            description="Only runs that kept their jobs can be compared: schedule runs and rollbacks, not Gantt moves or runs from before snapshots."
          />
        ) : (
          <form
            className={styles.form}
            aria-label="Pick the run to compare with"
            onSubmit={(event) => {
              event.preventDefault();
              if (pick) setParams({ to: pick });
            }}
          >
            <FormField id="compare-to" label="Compare run" hint={`Base run ${shortId(id)}.`}>
              {(control) => (
                <Select {...control} value={pick} onChange={(e) => setPick(e.target.value)}>
                  <option value="">Choose a run…</option>
                  {choices.map((r) => (
                    <option key={r.id} value={r.id}>
                      {`${shortId(r.id)} - ${scopeLabels[r.scope] ?? r.scope}, ${formatDateTime(r.createdAt)}`}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" disabled={!pick}>
                Compare
              </Button>
              <Button variant="ghost" onClick={() => navigate(`/scheduling/runs/${id}`)}>
                Cancel
              </Button>
            </div>
          </form>
        )}
      </Card>
      {!to ? null : result.error ? (
        <Alert tone="danger" title="Could not compare the runs" live="alert" action={<Button size="sm" onClick={result.reload}>Try again</Button>}>
          <ProblemMessage problem={result.error} />
        </Alert>
      ) : !cmp || result.loading ? (
        <div aria-busy="true">
          <span className="visually-hidden">Comparing the runs…</span>
          <Skeleton height="tile" />
        </div>
      ) : (
        <>
          <Card title="Plan measures">
            <DataTable label="Plan measures of the two runs" columns={measureColumns} rows={measureRows(cmp.base.measures, cmp.other.measures)} getRowKey={(row) => row.label} />
          </Card>
          {cmp.base.run.scope !== cmp.other.run.scope ? (
            <Alert tone="warning" title="The runs have a different scope">
              {`The base run is ${(scopeLabels[cmp.base.run.scope] ?? cmp.base.run.scope).toLowerCase()} and the other ${(scopeLabels[cmp.other.run.scope] ?? cmp.other.run.scope).toLowerCase()}: a job only one of them selected shows as added or dropped.`}
            </Alert>
          ) : null}
          <Alert tone="info" live="status">
            {`${cmp.changedCount} moved, ${cmp.addedCount} only in the other run, ${cmp.removedCount} only in the base run, ${cmp.unchangedCount} unchanged.`}
            {cmp.truncated ? " Lists show the first 500 of each kind." : ""}
          </Alert>
          <Card title="Moved jobs">
            <DataTable
              label="Jobs placed differently"
              columns={changeColumns}
              rows={cmp.changed}
              getRowKey={(row) => row.jobId}
              empty={{ title: "No job moved", description: "Every job both runs placed is on the same resource at the same time." }}
            />
          </Card>
          <Card title="Only in the other run">
            <DataTable
              label="Jobs only the other run placed"
              columns={jobColumns}
              rows={cmp.added}
              getRowKey={(row) => row.jobId}
              empty={{ title: "Nothing added", description: "The other run placed no job the base run did not." }}
            />
          </Card>
          <Card title="Only in the base run">
            <DataTable
              label="Jobs only the base run placed"
              columns={jobColumns}
              rows={cmp.removed}
              getRowKey={(row) => row.jobId}
              empty={{ title: "Nothing dropped", description: "The base run placed no job the other run did not." }}
            />
          </Card>
        </>
      )}
    </div>
  );
}
