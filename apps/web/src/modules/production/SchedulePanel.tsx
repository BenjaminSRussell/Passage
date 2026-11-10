import { useEffect, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, Select, type DataTableColumn } from "../../shell/ui";
import {
  SCHEDULING_METHODS,
  describeProblem,
  getOrderJobs,
  type CommandBody,
  type Problem,
  type ProductionJob,
  type ProductionOrder,
  type ScheduleDirection,
  type SchedulingMethod,
} from "./api/client";
import { dash, directionLabel, instant, jobTypeLabel, scheduleBasisLabel, schedulingMethodLabel } from "./labels";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface SchedulePanelProps {
  order: ProductionOrder;
  /** Runs :schedule with the chosen options; the panel shows the form only for an Estimated order. */
  onSchedule(body: CommandBody): void;
  busy?: boolean;
}

const columns: DataTableColumn<ProductionJob>[] = [
  { key: "operNumber", header: "Operation", align: "end" },
  { key: "jobType", header: "Job", render: (row) => jobTypeLabel[row.jobType] ?? row.jobType },
  { key: "resourceId", header: "Resource", mono: true },
  { key: "plannedStart", header: "Start", mono: true, render: (row) => instant(row.plannedStart) },
  { key: "plannedEnd", header: "End", mono: true, render: (row) => instant(row.plannedEnd) },
  { key: "plannedHours", header: "Hours", align: "end" },
  { key: "hoursReported", header: "Reported hours", align: "end" },
  { key: "status", header: "Status", render: (row) => <Badge tone="neutral">{row.status}</Badge> },
];

interface Draft {
  direction: ScheduleDirection;
  method: SchedulingMethod;
  at: string;
}

/** Checks the schedule options as the service does: a backward schedule needs a finish, from the order or the form. */
export function validateSchedule(draft: Draft, deliveryDate: string | undefined): string | undefined {
  if (draft.at !== "" && Number.isNaN(new Date(draft.at).getTime())) {
    return "Enter a valid date and time, or leave it empty.";
  }
  if (draft.direction === "backward" && draft.at === "" && !deliveryDate) {
    return "A backward schedule needs a finish time here or a delivery date on the order.";
  }
  return undefined;
}

/**
 * Schedule of a production order (BR-21.1, infinite capacity): the dates the schedule gave the order
 * and the dated jobs of its route, and for an Estimated order the form that schedules it.
 */
export function SchedulePanel({ order, onSchedule, busy = false }: SchedulePanelProps) {
  const [jobs, setJobs] = useState<{ items: ProductionJob[]; loading: boolean; error: Problem | null }>({ items: [], loading: true, error: null });
  const [draft, setDraft] = useState<Draft>({ direction: "forward", method: "operations", at: "" });
  const [error, setError] = useState<string | undefined>();

  // The jobs change with the order's version: a schedule, a reset or a cancel moves it.
  useEffect(() => {
    const controller = new AbortController();
    setJobs((j) => ({ ...j, loading: true, error: null }));
    getOrderJobs(order.id, controller.signal).then(
      (items) => setJobs({ items, loading: false, error: null }),
      (failure) => !controller.signal.aborted && setJobs({ items: [], loading: false, error: describeProblem(failure) }),
    );
    return () => controller.abort();
  }, [order.id, order.version]);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const problem = validateSchedule(draft, order.deliveryDate);
    setError(problem);
    if (problem) {
      return;
    }
    const body: CommandBody = { method: draft.method, direction: draft.direction };
    if (draft.at !== "") {
      body.at = new Date(draft.at).toISOString();
    }
    onSchedule(body);
  };

  const scheduled = Boolean(order.startDate && order.endDate);
  const late = scheduled && order.deliveryDate !== undefined && (order.endDate ?? "") > order.deliveryDate;
  const instantLabel = draft.direction === "forward" ? "Start at (your local time)" : "Finish by (your local time)";
  return (
    <Card title="Schedule">
      <div className={styles.stack}>
        {scheduled ? (
          <dl className={styles.details}>
            <div className={styles.entry}>
              <dt className={styles.term}>Start date</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{order.startDate}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>End date</dt>
              <dd className={`${styles.value} ${styles.mono}`}>{order.endDate}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Method</dt>
              <dd className={styles.value}>{order.schedulingMethod ? schedulingMethodLabel[order.schedulingMethod] : dash}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Dates from</dt>
              <dd className={styles.value}>
                {scheduleBasisLabel[order.scheduleBasis ?? "infinite"]}
                {order.scheduleBasis === "finite" && order.finiteRunId ? ` (run ${order.finiteRunId})` : ""}
              </dd>
            </div>
          </dl>
        ) : (
          <p className={styles.hint}>
            {order.status === "Estimated" ? "Not scheduled yet. Schedule the order to date its operations." : "This order has no schedule."}
          </p>
        )}
        {late ? (
          <Alert tone="warning" title="The schedule ends after the delivery date">
            {`The last job ends on ${order.endDate}; the order is due ${order.deliveryDate}.`}
          </Alert>
        ) : null}
        {order.scheduleBasis === "finite" && order.finiteCompressedOperations?.length ? (
          <Alert tone="warning" title="Some windows are shorter than the planned hours">
            {`Operation ${order.finiteCompressedOperations.join(", ")}: the finite window is shorter than the hours its jobs plan. The hours are kept; check the resource efficiency or the window.`}
          </Alert>
        ) : null}
        {order.status === "Estimated" ? (
          <form className={styles.commandForm} onSubmit={submit} noValidate aria-label="Schedule the order">
            <FormField id="schedule-direction" label="Direction">
              {(control) => (
                <Select {...control} value={draft.direction} onChange={(e) => setDraft({ ...draft, direction: e.target.value as ScheduleDirection })}>
                  {(Object.keys(directionLabel) as ScheduleDirection[]).map((d) => (
                    <option key={d} value={d}>
                      {directionLabel[d]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="schedule-method" label="Method">
              {(control) => (
                <Select {...control} value={draft.method} onChange={(e) => setDraft({ ...draft, method: e.target.value as SchedulingMethod })}>
                  {SCHEDULING_METHODS.map((m) => (
                    <option key={m} value={m}>
                      {schedulingMethodLabel[m]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="schedule-at" label={instantLabel} optional error={error}>
              {(control) => <Input {...control} type="datetime-local" value={draft.at} onChange={(e) => setDraft({ ...draft, at: e.target.value })} />}
            </FormField>
            <Button type="submit" variant="primary" loading={busy}>
              Schedule order
            </Button>
          </form>
        ) : null}
        {jobs.error ? (
          <Alert tone={jobs.error.status === 403 ? "info" : "danger"} title="The jobs could not be loaded" live="alert">
            {<ProblemMessage problem={jobs.error} />}
          </Alert>
        ) : (
          <DataTable
            label="Order jobs"
            columns={columns}
            rows={jobs.items}
            loading={jobs.loading}
            getRowKey={(row) => row.id}
            empty={{ title: "No jobs", description: "Scheduling dates each operation into jobs: queue, setup, process and transport." }}
          />
        )}
        <p className={`${styles.hint} ${styles.tableNote}`}>
          Until scheduling's windows are applied, jobs are dated with infinite capacity, one operation after the other, with no calendar.
        </p>
      </div>
    </Card>
  );
}
