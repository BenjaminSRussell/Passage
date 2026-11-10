import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { fetchDispatchList, listResources, type DispatchLine, type Resource } from "./api/client";
import { clock, dash, formatDateTime, formatInZone, formatMinutes, shortId, today, WEEKDAYS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

/** A shift to pick: its label and the local minute range it asks for (above 1440 it runs into the next day). */
export interface ShiftOption {
  label: string;
  startMinute: number;
  endMinute: number;
}

/**
 * The shifts of the resource that start on the weekday of a local date. The
 * calendar keeps a night shift as two spans, one to midnight and one from
 * midnight on the next weekday; they are joined into one window (22:00 to
 * 06:00 asks for 1320 to 1800). A next-day span that runs past the shift's own
 * start is round-the-clock capacity, not the end of this shift, and is left apart.
 */
export function shiftsOn(resource: Resource | undefined, date: string): ShiftOption[] {
  if (!resource || !DATE.test(date)) return [];
  const weekday = new Date(`${date}T00:00:00Z`).getUTCDay();
  const next = (weekday + 1) % 7;
  return resource.shifts
    .filter((s) => s.weekday === weekday)
    .sort((a, b) => a.startMinute - b.startMinute)
    .map((s) => {
      const rest =
        s.startMinute > 0 && s.endMinute === 1440
          ? resource.shifts.find((n) => n.weekday === next && n.startMinute === 0 && n.endMinute <= s.startMinute)
          : undefined;
      return rest
        ? { label: `${WEEKDAYS[weekday]} ${clock(s.startMinute)} to ${WEEKDAYS[next]} ${clock(rest.endMinute)}`, startMinute: s.startMinute, endMinute: 1440 + rest.endMinute }
        : { label: `${WEEKDAYS[weekday]} ${clock(s.startMinute)}-${clock(s.endMinute)}`, startMinute: s.startMinute, endMinute: s.endMinute };
    });
}

/** The jobs to run on one resource for a day or a shift, in start order, ready to print (PRD 4.23.15 Dispatch list). */
export function DispatchListPage() {
  const resources = useLoad((signal) => listResources(signal), "resources");
  const [resourceId, setResourceId] = useState("");
  const [date, setDate] = useState(() => today());
  const [shift, setShift] = useState("all");

  const list = resources.data ?? [];
  useEffect(() => {
    if (!resourceId && list.length > 0) setResourceId(list[0]!.id);
  }, [resourceId, list]);
  const resource = list.find((r) => r.id === resourceId);
  const shifts = shiftsOn(resource, date);
  const picked = shift === "all" ? undefined : shifts[Number(shift)];
  const span = picked ?? { startMinute: 0, endMinute: 1440 };
  const valid = Boolean(resourceId) && DATE.test(date);

  const state = useLoad(
    async (signal) => (valid ? fetchDispatchList(resourceId, date, span.startMinute, span.endMinute, signal) : null),
    valid ? `${resourceId}|${date}|${span.startMinute}|${span.endMinute}` : "idle",
  );

  const data = state.data;
  // Start and end print in the resource's own clock, the one the shift is read in.
  const zone = data?.resource.timeZone ?? "UTC";
  const columns: DataTableColumn<DispatchLine>[] = [
    { key: "sequence", header: "No.", align: "end" },
    { key: "orderId", header: "Order", mono: true, render: (row) => shortId(row.orderId) },
    { key: "operNumber", header: "Operation", align: "end", render: (row) => (row.segment > 1 ? `${row.operNumber} (part ${row.segment})` : String(row.operNumber)) },
    { key: "role", header: "Role", render: (row) => (row.role === "secondary" ? "Secondary" : "Primary") },
    { key: "start", header: `Start (${zone})`, render: (row) => formatInZone(row.start, zone) },
    { key: "end", header: `End (${zone})`, render: (row) => formatInZone(row.end, zone) },
    { key: "minutesInWindow", header: "In window", align: "end", render: (row) => formatMinutes(row.minutesInWindow) },
    { key: "setupGroup", header: "Setup group", render: (row) => row.setupGroup || dash },
    { key: "changeoverMinutes", header: "Changeover", align: "end", render: (row) => (row.changeoverMinutes > 0 ? formatMinutes(row.changeoverMinutes) : dash) },
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

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Dispatch list"
        description="The jobs booked on one resource for a day or a shift, in start order. The day, the shift and the start and end times follow the resource's own time zone."
        actions={
          <>
            <Button variant="secondary" onClick={() => globalThis.print()} disabled={!data}>
              Print
            </Button>
            <Link className={buttonClassName("secondary")} to="/scheduling">
              Open the Gantt
            </Link>
          </>
        }
      />
      {resources.error ? (
        <Alert tone="danger" title="Could not load the resources" live="alert" action={<Button size="sm" onClick={resources.reload}>Try again</Button>}>
          <ProblemMessage problem={resources.error} />
        </Alert>
      ) : (
        <div className={`${styles.toolbar} ${styles.noPrint}`}>
          <FormField id="dispatch-resource" label="Resource">
            {(control) => (
              <Select {...control} value={resourceId} onChange={(event) => { setResourceId(event.target.value); setShift("all"); }}>
                {list.map((r) => (
                  <option key={r.id} value={r.id}>{`${r.code} - ${r.name}`}</option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="dispatch-date" label="Date">
            {(control) => <Input {...control} type="date" value={date} onChange={(event) => { setDate(event.target.value); setShift("all"); }} />}
          </FormField>
          <FormField id="dispatch-shift" label="Shift">
            {(control) => (
              <Select {...control} value={shift} onChange={(event) => setShift(event.target.value)}>
                <option value="all">Whole day</option>
                {shifts.map((s, i) => (
                  <option key={`${s.startMinute}-${s.endMinute}`} value={String(i)}>{s.label}</option>
                ))}
              </Select>
            )}
          </FormField>
        </div>
      )}
      {!resources.loading && !resources.error && list.length === 0 && (
        <Alert tone="info" title="No resources yet">
          <Link to="/scheduling/resources/new">Create a resource</Link> and schedule operations on it to get a dispatch list.
        </Alert>
      )}
      {state.error ? (
        <Alert tone="danger" title="Could not load the dispatch list" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <>
          {data && (
            <p className={styles.note}>
              {`${data.resource.code} - ${data.resource.name}, ${data.date}: ${formatInZone(data.windowStart, zone)} to ${formatInZone(data.windowEnd, zone)} (${zone}; ${formatDateTime(data.windowStart)} to ${formatDateTime(data.windowEnd)}), ${data.lines.length} ${data.lines.length === 1 ? "line" : "lines"}, ${formatMinutes(data.totalMinutes)} booked.`}
            </p>
          )}
          {data?.truncated && (
            <Alert tone="warning" title="Showing the first 1,000 lines">
              Choose a shift or a single day to see the rest.
            </Alert>
          )}
          <DataTable
            label="Dispatch list"
            columns={columns}
            rows={data?.lines ?? []}
            getRowKey={(row) => `${row.jobId}-${row.role}-${row.segment}`}
            loading={state.loading || resources.loading || (list.length > 0 && !valid)}
            empty={{ title: "Nothing is booked", description: "No scheduled job holds this resource in the chosen day or shift." }}
          />
        </>
      )}
    </div>
  );
}
