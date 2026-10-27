import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, DataTable, EmptyState, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { getOee, listMachines, type Machine, type OeeBucket, type OeeBucketSize } from "./api/client";
import { fixed, formatDateTime, percent, sumDecimals, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const BUCKETS: { value: OeeBucketSize; label: string }[] = [
  { value: "15m", label: "15 minutes" },
  { value: "60m", label: "1 hour" },
  { value: "5m", label: "5 minutes" },
];

/** The viewer's local day [00:00, next 00:00) as RFC 3339 instants. */
export function dayWindow(date: string): { from: string; to: string } | null {
  const start = new Date(`${date}T00:00:00`);
  if (Number.isNaN(start.getTime())) return null;
  const end = new Date(start);
  end.setDate(end.getDate() + 1);
  return { from: start.toISOString(), to: end.toISOString() };
}

/** OEE per resource and bucket from machine events (FR-25.10, BR-25.5). */
export function OeePage() {
  const [date, setDate] = useState(today());
  const [bucket, setBucket] = useState<OeeBucketSize>("15m");
  const [resourceId, setResourceId] = useState("");
  const machines = useLoad<Machine[]>((signal) => listMachines(signal), "machines");
  const range = dayWindow(date);
  const resourceIds = resourceId ? [resourceId] : (machines.data ?? []).slice(0, 50).map((m) => m.resourceId);
  const series = useLoad<OeeBucket[]>(
    (signal) => (range && resourceIds.length > 0 ? getOee({ resourceIds, from: range.from, to: range.to, bucket }, signal) : Promise.resolve([])),
    `${date}|${bucket}|${resourceIds.join(",")}`,
  );
  const machineOf = new Map((machines.data ?? []).map((m) => [m.resourceId, m.machineId]));
  const rows = series.data ?? [];

  const columns: DataTableColumn<OeeBucket>[] = [
    { key: "resourceId", header: "Machine", mono: true, render: (row) => machineOf.get(row.resourceId) ?? row.resourceId },
    { key: "bucketStart", header: "Bucket", render: (row) => formatDateTime(row.bucketStart) },
    { key: "plannedMinutes", header: "Planned", align: "end", render: (row) => `${fixed(row.plannedMinutes, 1)} min` },
    { key: "downMinutes", header: "Down", align: "end", render: (row) => `${fixed(row.downMinutes, 1)} min` },
    { key: "counts", header: "Good / total", align: "end", render: (row) => `${row.goodCount} / ${row.totalCount}` },
    { key: "availability", header: "Availability", align: "end", render: (row) => percent(row.availability) },
    {
      key: "performance",
      header: "Performance",
      align: "end",
      render: (row) => (row.overRate ? <Badge tone="warning">{`${percent(row.performance)} capped`}</Badge> : percent(row.performance)),
    },
    { key: "quality", header: "Quality", align: "end", render: (row) => percent(row.quality) },
    { key: "oee", header: "OEE", align: "end", render: (row) => <strong>{percent(row.oee)}</strong> },
  ];

  const noMachines = machines.data !== null && machines.data.length === 0;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="OEE"
        description="Availability × performance × quality per bucket, from machine states and counts. Running without a count for longer than the machine's idle threshold counts as idle downtime. Totals add minutes and counts; ratios are never averaged."
      />
      <div className={styles.toolbar}>
        <FormField id="oee-date" label="Day" error={range ? undefined : "Enter a date."}>
          {(control) => <Input {...control} type="date" value={date} invalid={!range} onChange={(event) => setDate(event.target.value)} />}
        </FormField>
        <FormField id="oee-machine" label="Machine">
          {(control) => (
            <Select {...control} value={resourceId} disabled={!machines.data} onChange={(event) => setResourceId(event.target.value)}>
              <option value="">{machines.loading ? "Loading machines…" : "All machines"}</option>
              {(machines.data ?? []).map((m) => (
                <option key={m.id} value={m.resourceId}>
                  {m.machineId}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="oee-bucket" label="Bucket">
          {(control) => (
            <Select {...control} value={bucket} onChange={(event) => setBucket(event.target.value as OeeBucketSize)}>
              {BUCKETS.map((b) => (
                <option key={b.value} value={b.value}>
                  {b.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button onClick={series.reload} loading={series.loading && resourceIds.length > 0}>
          Refresh
        </Button>
      </div>
      {machines.error ? (
        <Alert tone="danger" title="Could not load machines" live="alert" action={<Button size="sm" onClick={machines.reload}>Try again</Button>}>
          {machines.error.message} ({machines.error.code})
        </Alert>
      ) : null}
      {noMachines ? (
        <EmptyState
          title="No machines registered"
          description="OEE is computed from the events of registered machines."
          action={<Link to="/shopfloor/machines/new">Register a machine</Link>}
        />
      ) : series.error ? (
        <Alert tone="danger" title="Could not load OEE" live="alert" action={<Button size="sm" onClick={series.reload}>Try again</Button>}>
          {series.error.message} ({series.error.code})
        </Alert>
      ) : (
        <>
          {rows.length > 0 ? (
            <StatGroup label="Day totals">
              <StatTile label="Planned minutes" value={fixed(sumDecimals(rows.map((r) => r.plannedMinutes), 3), 1)} />
              <StatTile label="Down minutes" value={fixed(sumDecimals(rows.map((r) => r.downMinutes), 3), 1)} tone="warning" />
              <StatTile label="Good units" value={rows.reduce((n, r) => n + r.goodCount, 0)} />
              <StatTile label="Rejected units" value={rows.reduce((n, r) => n + r.totalCount - r.goodCount, 0)} tone="danger" />
            </StatGroup>
          ) : null}
          <DataTable
            label="OEE buckets"
            columns={columns}
            rows={rows}
            getRowKey={(row) => `${row.resourceId}|${row.bucketStart}`}
            loading={series.loading || machines.loading}
            empty={{ title: "No machine time in this day", description: "Buckets appear once a machine has reported its state; switched-off time is not scheduled." }}
          />
        </>
      )}
    </div>
  );
}
