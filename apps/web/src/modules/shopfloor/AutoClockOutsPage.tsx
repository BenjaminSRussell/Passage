import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import {
  AUTO_CLOCK_OUT_HOURS,
  describeProblem,
  listAutoClockOuts,
  runAutoClockOut,
  type AutoClockOutReview,
  type AutoClockOutRun,
  type Problem,
  type Registration,
} from "./api/client";
import { addDays, browserZone, dash, daysBetween, formatDateTime, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const MAX_DAYS = 31;

/** Checks that mirror the service: whole hours, 4 to 24 (SF-0130). */
export function validateAfterHours(raw: string): string | null {
  const n = Number(raw);
  if (raw.trim() === "" || !Number.isInteger(n) || n < AUTO_CLOCK_OUT_HOURS.min || n > AUTO_CLOCK_OUT_HOURS.max) {
    return `Enter whole hours from ${AUTO_CLOCK_OUT_HOURS.min} to ${AUTO_CLOCK_OUT_HOURS.max}.`;
  }
  return null;
}

/** The stored clock-out as the correction form shows it, so the form starts at its time. */
export function asRegistration(row: AutoClockOutReview): Registration {
  return {
    id: row.registrationId,
    workerId: row.workerId,
    kind: "clock-out",
    jobId: null,
    bundleId: null,
    qtyGood: "0",
    qtyScrap: "0",
    scrapReasonId: null,
    startedAt: row.clockedOutAt,
    stoppedAt: null,
    hours: null,
    serverReceivedAt: row.closedAt,
    deviceId: null,
    offline: false,
    note: null,
    clockOffsetSeconds: null,
    flags: ["auto-clock-out"],
  };
}

const columns: DataTableColumn<AutoClockOutReview>[] = [
  { key: "workerName", header: "Worker", render: (row) => <Link to={`/shopfloor/workers/${row.workerId}`}>{row.workerName}</Link> },
  { key: "clockedInAt", header: "Clocked in", mono: true, render: (row) => (row.clockedInAt ? formatDateTime(row.clockedInAt) : dash) },
  { key: "clockedOutAt", header: "Clocked out", mono: true, render: (row) => formatDateTime(row.clockedOutAt) },
  { key: "closedAt", header: "Closed by the server", mono: true, render: (row) => formatDateTime(row.closedAt) },
  {
    key: "reviewed",
    header: "Review",
    render: (row) => (row.reviewed ? <Badge tone="success">Reviewed</Badge> : <Badge tone="warning">Needs review</Badge>),
  },
  {
    key: "correct",
    header: "",
    render: (row) => (
      <Link
        to={`/shopfloor/registrations/${row.registrationId}/correct`}
        state={{ registration: asRegistration(row) }}
        aria-label={`${row.reviewed ? "Correct" : "Confirm or correct"} the clock-out of ${row.workerName}`}
      >
        {row.reviewed ? "Correct" : "Confirm or correct"}
      </Link>
    ),
  },
];

/**
 * Clock-ins the auto clock-out job closed (D2h, BR-25.3), for a supervisor to confirm or
 * correct, and a button that runs the sweep now. The platform scheduler runs it hourly.
 */
export function AutoClockOutsPage() {
  const [from, setFrom] = useState(addDays(today(), -6));
  const [to, setTo] = useState(today());
  const [hours, setHours] = useState(String(AUTO_CLOCK_OUT_HOURS.default));
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [run, setRun] = useState<AutoClockOutRun | null>(null);
  const span = daysBetween(from, to);
  const rangeError =
    span === null ? "Enter both dates." : span < 1 ? "To must not be before From." : span > MAX_DAYS ? `Show at most ${MAX_DAYS} days.` : null;
  const hoursError = validateAfterHours(hours);
  const state = useLoad(
    (signal) => (rangeError ? Promise.resolve([] as AutoClockOutReview[]) : listAutoClockOuts({ from, to, timeZone: browserZone() }, signal)),
    `${from}|${to}|${rangeError}`,
  );

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setRun(null);
    if (hoursError) return;
    setBusy(true);
    try {
      setRun(await runAutoClockOut(Number(hours)));
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Auto clock-outs"
        description="A clock-in left open too long is closed by the server. Its day cannot be approved until you confirm the clock-out (keep its time) or correct it, then recalculate the day."
      />
      <Card title="Close forgotten clock-ins now">
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField
            id="auto-after"
            label="Close clock-ins open longer than (hours)"
            hint="The clock-out is written at the clock-in plus this many hours."
            error={hoursError ?? undefined}
          >
            {(control) => (
              <Input
                {...control}
                type="number"
                min={AUTO_CLOCK_OUT_HOURS.min}
                max={AUTO_CLOCK_OUT_HOURS.max}
                value={hours}
                invalid={Boolean(hoursError)}
                onChange={(event) => setHours(event.target.value)}
              />
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not run the sweep" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          {run ? (
            <div className={styles.formActions}>
              <Alert tone={run.items.length > 0 ? "success" : "info"} title="Sweep finished" live="status">
                {run.items.length === 0
                  ? `No clock-in has been open longer than ${run.afterHours} hours.`
                  : `Closed ${run.items.length} clock-in${run.items.length === 1 ? "" : "s"} open longer than ${run.afterHours} hours.`}
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Close forgotten clock-ins
            </Button>
          </div>
        </form>
      </Card>
      <div className={styles.toolbar}>
        <FormField id="auto-from" label="From">
          {(control) => <Input {...control} type="date" value={from} onChange={(event) => setFrom(event.target.value)} />}
        </FormField>
        <FormField id="auto-to" label="To" error={rangeError ?? undefined}>
          {(control) => <Input {...control} type="date" value={to} onChange={(event) => setTo(event.target.value)} invalid={Boolean(rangeError)} />}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the auto clock-outs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <Card title="Closed by the server">
          <DataTable
            label="Auto clock-outs"
            columns={columns}
            rows={state.data ?? []}
            getRowKey={(row) => row.registrationId}
            loading={state.loading}
            empty={{ title: "No automatic clock-outs in this range", description: "Clock-ins closed by the sweep appear here for review." }}
          />
        </Card>
      )}
    </div>
  );
}
