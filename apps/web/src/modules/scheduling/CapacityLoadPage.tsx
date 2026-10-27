import { useState } from "react";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { fetchCapacityLoad, listResources, type LoadItem } from "./api/client";
import { dash, hours, shortId, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const WEEK_OPTIONS = [2, 4, 8, 13] as const;

interface LoadView {
  rows: LoadItem[];
  codes: Record<string, string>;
}

/** Capacity load by resource and week with the bottleneck flag (FR-23.9, PRD 4.23.14). */
export function CapacityLoadPage() {
  const [from, setFrom] = useState(today());
  const [weeks, setWeeks] = useState<number>(4);
  const state = useLoad<LoadView>(async (signal) => {
    const [rows, resources] = await Promise.all([fetchCapacityLoad(from, weeks, [], signal), listResources(signal)]);
    return { rows, codes: Object.fromEntries(resources.map((r) => [r.id, r.code])) };
  }, `${from}|${weeks}`);

  const codes = state.data?.codes ?? {};
  const columns: DataTableColumn<LoadItem>[] = [
    { key: "resourceId", header: "Resource", mono: true, render: (row) => codes[row.resourceId] ?? shortId(row.resourceId) },
    { key: "weekStart", header: "Week of", mono: true },
    { key: "openMinutes", header: "Open h", align: "end", render: (row) => hours(row.openMinutes) },
    { key: "bookedMinutes", header: "Booked h", align: "end", render: (row) => hours(row.bookedMinutes) },
    { key: "loadPct", header: "Load %", align: "end", render: (row) => (row.openMinutes > 0 ? row.loadPct.toFixed(1) : dash) },
    { key: "bottleneck", header: "Bottleneck", render: (row) => (row.bottleneck ? <Badge tone="warning">Bottleneck</Badge> : "No") },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Capacity load"
        description="Booked against open hours per resource and week. A resource is a bottleneck when its load stays above the threshold for consecutive weeks."
      />
      <div className={styles.toolbar}>
        <FormField id="load-from" label="From">
          {(control) => <Input {...control} type="date" value={from} onChange={(event) => event.target.value && setFrom(event.target.value)} />}
        </FormField>
        <FormField id="load-weeks" label="Weeks">
          {(control) => (
            <Select {...control} value={String(weeks)} onChange={(event) => setWeeks(Number(event.target.value))}>
              {WEEK_OPTIONS.map((w) => (
                <option key={w} value={w}>
                  {`${w} weeks`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load capacity" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Capacity load"
          columns={columns}
          rows={state.data?.rows ?? []}
          getRowKey={(row) => `${row.resourceId}-${row.weekStart}`}
          loading={state.loading}
          empty={{ title: "No capacity to show", description: "Add resources with working calendars to see their load." }}
        />
      )}
    </div>
  );
}
