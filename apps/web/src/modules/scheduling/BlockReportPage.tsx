import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { fetchBlockReport, listResources, type BlockReport, type BlockUsage } from "./api/client";
import { dash, dayStart, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const MAX_DAYS = 62;
const DAY_MS = 86_400_000;
const empty: BlockReport = { items: [], truncated: false, blockMinutes: 0, matchedMinutes: 0, unusedMinutes: 0, utilizationPct: null };

function share(pct: number | null): string {
  return pct === null ? dash : `${pct}%`;
}

function hours(minutes: number): string {
  return `${(minutes / 60).toFixed(1)} h`;
}

/**
 * How much of each capacity block the jobs matching its condition use, per resource over a range of up to 62 days
 * (PRD 4.23.14 Block utilization). Low use means reserved capacity went unused.
 */
export function BlockReportPage() {
  const [from, setFrom] = useState(() => today());
  const [to, setTo] = useState(() => dayStart(today(), 13).slice(0, 10));
  const [resourceId, setResourceId] = useState("");
  const resources = useLoad((signal) => listResources(signal), "resources");
  // "To" is a day, inclusive: the range ends at the start of the next day.
  const days = from && to ? Math.round((Date.parse(`${to}T00:00:00Z`) - Date.parse(`${from}T00:00:00Z`)) / DAY_MS) + 1 : 0;
  const rangeProblem = !from || !to
    ? "Pick both the first and the last day."
    : days < 1
      ? "The last day is before the first day, so the range is empty."
      : days > MAX_DAYS
        ? `The range is ${days} days; the report covers at most ${MAX_DAYS}.`
        : "";
  const state = useLoad(
    (signal) =>
      rangeProblem ? Promise.resolve(empty) : fetchBlockReport({ from: dayStart(from), to: dayStart(to, 1), resourceId: resourceId || undefined }, signal),
    `${from}|${to}|${resourceId}|${rangeProblem}`,
  );

  const columns: DataTableColumn<BlockUsage>[] = [
    {
      key: "resource",
      header: "Resource",
      render: (row) => <Link to={`/scheduling/resources/${row.resourceId}`}>{`${row.resourceCode} ${row.resourceName}`}</Link>,
    },
    { key: "block", header: "Block", mono: true, render: (row) => <Link to={`/scheduling/capacity-blocks/${row.blockId}`}>{row.code}</Link> },
    { key: "priority", header: "Priority", align: "end", render: (row) => row.priority },
    { key: "windows", header: "Windows", align: "end", render: (row) => row.windows },
    { key: "held", header: "Held", align: "end", render: (row) => hours(row.blockMinutes) },
    { key: "matched", header: "Used", align: "end", render: (row) => hours(row.matchedMinutes) },
    { key: "unused", header: "Unused", align: "end", render: (row) => hours(row.unusedMinutes) },
    {
      key: "pct",
      header: "Utilization",
      align: "end",
      render: (row) => (row.utilizationPct === null ? dash : <Badge tone={row.utilizationPct < 50 ? "warning" : "success"}>{share(row.utilizationPct)}</Badge>),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Block utilization"
        description="How much of the capacity each block reserves is used by jobs that match its condition, per resource over the range. Held time is the open calendar time of the block's windows after higher-priority blocks; unused time is reserved capacity no matching job took."
        actions={
          <Link className={buttonClassName("secondary")} to="/scheduling/capacity-blocks">
            Capacity blocks
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="bu-from" label="From">
          {(control) => <Input {...control} type="date" value={from} onChange={(event) => setFrom(event.target.value)} />}
        </FormField>
        <FormField id="bu-to" label="To">
          {(control) => <Input {...control} type="date" value={to} onChange={(event) => setTo(event.target.value)} />}
        </FormField>
        <FormField id="bu-resource" label="Resource">
          {(control) => (
            <Select {...control} value={resourceId} onChange={(event) => setResourceId(event.target.value)}>
              <option value="">All resources with blocks</option>
              {(resources.data ?? []).map((r) => (
                <option key={r.id} value={r.id}>{`${r.code} ${r.name}`}</option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {rangeProblem && (
        <Alert tone="warning" title="Pick a valid range">
          {rangeProblem}
        </Alert>
      )}
      {rangeProblem ? null : state.error ? (
        <Alert tone="danger" title="Could not load block utilization" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <>
          {state.data && state.data.items.length > 0 && (
            <p aria-label="Totals">
              <Badge tone="info">{`Held ${hours(state.data.blockMinutes)}`}</Badge> <Badge tone="info">{`Used ${hours(state.data.matchedMinutes)}`}</Badge>{" "}
              <Badge tone="warning">{`Unused ${hours(state.data.unusedMinutes)}`}</Badge> <Badge tone="neutral">{`Utilization ${share(state.data.utilizationPct)}`}</Badge>
            </p>
          )}
          {state.data?.truncated && (
            <Alert tone="warning" title="Showing the first 200 resources">
              More resources have active blocks. Pick a resource to see the rest.
            </Alert>
          )}
          <DataTable
            label="Block utilization"
            columns={columns}
            rows={state.data?.items ?? []}
            getRowKey={(row) => `${row.resourceId}|${row.blockId}`}
            loading={state.loading}
            empty={{ title: "No capacity blocks in this range", description: "No active capacity block has a window in this range. Create a block to reserve capacity for matching jobs." }}
          />
        </>
      )}
    </div>
  );
}
