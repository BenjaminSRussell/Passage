import { useState } from "react";
import { Alert, Badge, Button, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { Link } from "react-router-dom";
import { fetchCapacityLoad, fetchDailyLoad, listResources, type LoadItem, type LoadPeriod } from "./api/client";
import { dash, hours, shortId, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const WEEK_OPTIONS = [2, 4, 8, 13] as const;
const DAY_OPTIONS = [7, 14, 31] as const;

interface LoadView {
  rows: LoadItem[];
  codes: Record<string, string>;
}

/** Capacity load by resource and week or day with the weekly bottleneck flag and a drill-down to the booked jobs (FR-23.9, PRD 4.23.14). */
export function CapacityLoadPage() {
  const [from, setFrom] = useState(today());
  const [period, setPeriod] = useState<LoadPeriod>("week");
  const [count, setCount] = useState<number>(4);
  const options: readonly number[] = period === "week" ? WEEK_OPTIONS : DAY_OPTIONS;
  const state = useLoad<LoadView>(async (signal) => {
    const [rows, resources] = await Promise.all([
      period === "week" ? fetchCapacityLoad(from, count, [], signal) : fetchDailyLoad(from, count, [], signal),
      listResources(signal),
    ]);
    return { rows, codes: Object.fromEntries(resources.map((r) => [r.id, r.code])) };
  }, `${from}|${period}|${count}`);

  function changePeriod(next: LoadPeriod) {
    setPeriod(next);
    setCount(next === "week" ? 4 : 14);
  }

  const codes = state.data?.codes ?? {};
  const columns: DataTableColumn<LoadItem>[] = [
    { key: "resourceId", header: "Resource", mono: true, render: (row) => codes[row.resourceId] ?? shortId(row.resourceId) },
    { key: "periodStart", header: period === "week" ? "Week of" : "Day", mono: true, render: (row) => start(row) },
    { key: "openMinutes", header: "Open h", align: "end", render: (row) => hours(row.openMinutes) },
    { key: "bookedMinutes", header: "Booked h", align: "end", render: (row) => hours(row.bookedMinutes) },
    { key: "loadPct", header: "Load %", align: "end", render: (row) => (row.openMinutes > 0 ? row.loadPct.toFixed(1) : dash) },
    ...(period === "week"
      ? [{ key: "bottleneck", header: "Bottleneck", render: (row: LoadItem) => (row.bottleneck ? <Badge tone="warning">Bottleneck</Badge> : "No") }]
      : []),
    {
      key: "jobs",
      header: "Detail",
      render: (row) =>
        row.start && row.end ? (
          <Link
            to={`/scheduling/load/jobs?${new URLSearchParams({ resourceId: row.resourceId, from: row.start, to: row.end })}`}
            aria-label={`Jobs of ${codes[row.resourceId] ?? shortId(row.resourceId)} from ${start(row)}`}
          >
            Jobs
          </Link>
        ) : (
          dash
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Capacity load"
        description="Booked against open hours per resource and week or day. A resource is a bottleneck when its weekly load stays above the threshold for consecutive weeks. Weeks start Monday 00:00 UTC; days are local days of each resource's calendar time zone and are not flagged."
      />
      <div className={styles.toolbar}>
        <FormField id="load-from" label="From">
          {(control) => <Input {...control} type="date" value={from} onChange={(event) => event.target.value && setFrom(event.target.value)} />}
        </FormField>
        <FormField id="load-period" label="Period">
          {(control) => (
            <Select {...control} value={period} onChange={(event) => changePeriod(event.target.value === "day" ? "day" : "week")}>
              <option value="week">Weeks</option>
              <option value="day">Days</option>
            </Select>
          )}
        </FormField>
        <FormField id="load-weeks" label={period === "week" ? "Weeks" : "Days"}>
          {(control) => (
            <Select {...control} value={String(count)} onChange={(event) => setCount(Number(event.target.value))}>
              {options.map((n) => (
                <option key={n} value={n}>
                  {`${n} ${period === "week" ? "weeks" : "days"}`}
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
          getRowKey={(row) => `${row.resourceId}-${start(row)}`}
          loading={state.loading}
          empty={{ title: "No capacity to show", description: "Add resources with working calendars to see their load." }}
        />
      )}
    </div>
  );
}

/** The first day of a load bucket; older answers carry only weekStart. */
function start(row: LoadItem): string {
  return row.periodStart ?? row.weekStart ?? "";
}
