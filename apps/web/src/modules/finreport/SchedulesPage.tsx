import { Link, useNavigate } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { listScheduleFirings, listSchedules, type Schedule, type ScheduleFiring } from "./api/client";
import { CURRENCY_TYPE_LABELS, periodLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/**
 * The last run of each schedule, from a list that is newest first: its newest
 * firing, and when the schedule fired for several ledgers at that time, a
 * failed one first so a failure on one ledger is not hidden by the others.
 */
export function lastFiringBySchedule(firings: readonly ScheduleFiring[]): Map<string, ScheduleFiring> {
  const out = new Map<string, ScheduleFiring>();
  for (const f of firings) {
    const seen = out.get(f.scheduleCode);
    if (!seen || (seen.fireTime === f.fireTime && seen.status !== "failed" && f.status === "failed")) out.set(f.scheduleCode, f);
  }
  return out;
}

function LastRun({ firing }: { firing: ScheduleFiring | undefined }) {
  if (!firing) return <span className={styles.muted}>Not yet</span>;
  const when = <time dateTime={firing.fireTime}>{firing.fireTime}</time>;
  if (firing.status === "completed") {
    return (
      <span>
        <Badge tone="success">Done</Badge> {when} for {periodLabel(firing.periodYear, firing.periodNo)}
      </span>
    );
  }
  return (
    <span>
      <Badge tone="danger">Failed</Badge> {when}
      {firing.errorCode ? <span className={styles.mono}> {firing.errorCode}</span> : null}
    </span>
  );
}

/** Distribution schedules (FR-15.8): statements that run on a timetable and go to their recipients. */
export function SchedulesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listSchedules(signal), "schedules");
  // The last run is a convenience: if it cannot be loaded the table still shows the schedules.
  const firings = useLoad((signal) => listScheduleFirings({ latest: true }, signal), "firings");
  const last = lastFiringBySchedule(firings.data ?? []);
  const columns: DataTableColumn<Schedule>[] = [
    { key: "code", header: "Code", mono: true },
    { key: "name", header: "Name" },
    { key: "definition", header: "Definition", mono: true },
    { key: "cron", header: "Runs at", mono: true, render: (s) => `${s.cron} (${s.timezone})` },
    { key: "ledgerIds", header: "Ledgers", mono: true, align: "end", render: (s) => String(s.ledgerIds.length) },
    { key: "currencyType", header: "Currency type", render: (s) => CURRENCY_TYPE_LABELS[s.currencyType] ?? s.currencyType },
    { key: "formats", header: "Formats", render: (s) => s.formats.map((f) => f.toUpperCase()).join(", ") },
    { key: "recipients", header: "Recipients", mono: true, align: "end", render: (s) => String(s.recipients.length) },
    {
      key: "nextRunAt",
      header: "Next run",
      mono: true,
      render: (s) => (s.enabled ? s.nextRunAt ? <time dateTime={s.nextRunAt}>{s.nextRunAt}</time> : "—" : <Badge tone="neutral">Paused</Badge>),
    },
    {
      key: "lastRun",
      header: "Last run",
      render: (s) => (firings.loading && !firings.data ? <span className={styles.muted}>Loading</span> : firings.error ? <span className={styles.muted}>Unavailable</span> : <LastRun firing={last.get(s.code)} />),
    },
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Distribution schedules"
        description="Statements that run on a timetable for a set of ledgers. Each run is stored in the report library and handed to the recipients."
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/finreport/library">
              Report library
            </Link>
            <Link className={buttonClassName("primary")} to="/finreport/schedules/new">
              New schedule
            </Link>
          </>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the schedules" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Distribution schedules"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(s) => s.code}
          loading={state.loading}
          empty={{ title: "No schedules yet", description: "Create a schedule to send a statement pack on a timetable, such as every night at 23:00." }}
          onRowOpen={(s) => navigate(`/finreport/schedules/${encodeURIComponent(s.code)}`)}
        />
      )}
      <p className={styles.note}>
        A schedule fires for the calendar month before its run time. The files are in the report library as soon as the run ends; sending them by email is done by the notification service
        and follows in a later release. A failed run says why in the last run column, and is not repeated until the next run time.
      </p>
    </div>
  );
}
