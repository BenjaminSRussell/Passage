import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  approvePay,
  calculatePay,
  describeProblem,
  listWorkerDays,
  listWorkers,
  MAX_PAY_RANGE_DAYS,
  type DayStatus,
  type Problem,
  type Worker,
  type WorkerDay,
} from "./api/client";
import { addDays, browserZone, daysBetween, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { dayColumns } from "./dayColumns";

/** One pay:approve call: a worker's contiguous ready days, with no flagged day inside. */
export interface ApprovalRun {
  workerId: string;
  workerName: string;
  from: string;
  to: string;
  days: number;
}

/** A calculated day without open flags can be approved (SF-0006 refuses flagged days). */
export function isReady(day: WorkerDay): boolean {
  return day.status === "calculated" && day.flags.length === 0;
}

/**
 * Groups the ready days of each worker into the fewest approval ranges. A
 * flagged day splits a worker's range, because the server refuses a range that
 * holds one; approved days inside a range are returned unchanged.
 */
export function approvalRuns(days: WorkerDay[]): ApprovalRun[] {
  const byWorker = new Map<string, WorkerDay[]>();
  for (const d of days) byWorker.set(d.workerId, [...(byWorker.get(d.workerId) ?? []), d]);
  const runs: ApprovalRun[] = [];
  for (const [workerId, list] of byWorker) {
    let run: ApprovalRun | null = null;
    for (const d of [...list].sort((a, b) => a.workDate.localeCompare(b.workDate))) {
      if (isReady(d)) {
        if (run) {
          run.to = d.workDate;
          run.days += 1;
        } else {
          run = { workerId, workerName: d.workerName, from: d.workDate, to: d.workDate, days: 1 };
          runs.push(run);
        }
      } else if (d.status === "calculated") {
        run = null;
      }
    }
  }
  return runs;
}

const STATUS_OPTIONS: { value: DayStatus | ""; label: string }[] = [
  { value: "calculated", label: "Waiting for approval" },
  { value: "approved", label: "Approved" },
  { value: "", label: "All" },
];

interface Outcome {
  approved: number;
  failures: { run: ApprovalRun; problem: Problem }[];
}

/** Supervisor time approval: calculate a worker's pay, review worker-days and approve them (FR-25.8, FR-25.14). */
export function TimeApprovalPage() {
  const [from, setFrom] = useState(addDays(today(), -6));
  const [to, setTo] = useState(today());
  const [status, setStatus] = useState<DayStatus | "">("calculated");
  const [workerId, setWorkerId] = useState("");
  const [workerError, setWorkerError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [failures, setFailures] = useState<{ title: string; problem: Problem }[]>([]);

  const span = daysBetween(from, to);
  const rangeError =
    span === null ? "Enter both dates." : span < 1 ? "To must not be before From." : span > MAX_PAY_RANGE_DAYS ? `Use at most ${MAX_PAY_RANGE_DAYS} days.` : null;

  const days = useLoad<WorkerDay[]>((signal) => (rangeError ? Promise.resolve([]) : listWorkerDays({ from, to, status }, signal)), `${from}|${to}|${status}`);
  const workers = useLoad<Worker[]>((signal) => listWorkers({ active: true }, signal), "workers");

  const rows = days.data ?? [];
  const runs = approvalRuns(rows);
  const ready = runs.reduce((n, r) => n + r.days, 0);

  function start() {
    setBusy(true);
    setNotice(null);
    setFailures([]);
  }

  async function approve(list: ApprovalRun[]) {
    start();
    const outcome: Outcome = { approved: 0, failures: [] };
    try {
      // One call per worker range, in turn, so one refused worker does not stop the rest.
      for (const run of list) {
        try {
          const res = await approvePay({ workerId: run.workerId, from: run.from, to: run.to });
          outcome.approved += res.days.filter((d) => d.newlyApproved).length;
        } catch (error) {
          outcome.failures.push({ run, problem: describeProblem(error) });
        }
      }
    } finally {
      setBusy(false);
    }
    if (outcome.approved > 0) setNotice(`Approved ${outcome.approved} ${outcome.approved === 1 ? "day" : "days"}.`);
    setFailures(outcome.failures.map(({ run, problem }) => ({ title: `Could not approve ${run.workerName}, ${run.from} to ${run.to}`, problem })));
    days.reload();
  }

  async function calculate() {
    if (rangeError) return;
    const worker = workers.data?.find((w) => w.id === workerId);
    if (!worker) {
      setWorkerError("Choose the worker whose pay to calculate.");
      return;
    }
    start();
    try {
      const res = await calculatePay({ workerId, from, to, timeZone: browserZone() });
      setNotice(`Calculated ${res.days.length} ${res.days.length === 1 ? "day" : "days"} for ${worker.name}.`);
      days.reload();
    } catch (error) {
      setFailures([{ title: `Could not calculate pay for ${worker.name}`, problem: describeProblem(error) }]);
    } finally {
      setBusy(false);
    }
  }

  const columns: DataTableColumn<WorkerDay>[] = [
    {
      key: "workerName",
      header: "Worker",
      render: (row) => <Link to={`/shopfloor/workers/${row.workerId}`}>{row.workerName}</Link>,
    },
    ...dayColumns,
    {
      key: "approve",
      header: "Action",
      render: (row) =>
        isReady(row) ? (
          <Button
            size="sm"
            disabled={busy}
            aria-label={`Approve ${row.workerName} ${row.workDate}`}
            onClick={() => approve([{ workerId: row.workerId, workerName: row.workerName, from: row.workDate, to: row.workDate, days: 1 }])}
          >
            Approve
          </Button>
        ) : null,
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Time approval"
        description="Review calculated worker-days and their pay lines, then approve them for payroll. Days with open flags must be corrected first, and nobody approves their own time."
        actions={
          <Button variant="primary" loading={busy} disabled={ready === 0} onClick={() => approve(runs)}>
            {ready > 0 ? `Approve ${ready} ready ${ready === 1 ? "day" : "days"}` : "Approve ready days"}
          </Button>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="approval-from" label="From">
          {(control) => <Input {...control} type="date" value={from} onChange={(event) => setFrom(event.target.value)} />}
        </FormField>
        <FormField id="approval-to" label="To" error={rangeError ?? undefined}>
          {(control) => <Input {...control} type="date" value={to} onChange={(event) => setTo(event.target.value)} invalid={Boolean(rangeError)} />}
        </FormField>
        <FormField id="approval-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value as DayStatus | "")}>
              {STATUS_OPTIONS.map((o) => (
                <option key={o.label} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      <Card title="Calculate pay">
        <div className={styles.toolbar}>
          <FormField
            id="approval-worker"
            label="Worker"
            hint={workers.error ? `Workers could not be loaded: ${workers.error.message} (${workers.error.code})` : undefined}
            error={workerError ?? undefined}
          >
            {(control) => (
              <Select
                {...control}
                value={workerId}
                invalid={Boolean(workerError)}
                disabled={!workers.data}
                onChange={(event) => {
                  setWorkerId(event.target.value);
                  setWorkerError(null);
                }}
              >
                <option value="">{workers.loading ? "Loading workers…" : "Choose a worker"}</option>
                {(workers.data ?? []).map((w) => (
                  <option key={w.id} value={w.id}>
                    {w.badgeId ? `${w.name} (${w.badgeId})` : w.name}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <Button loading={busy} disabled={Boolean(rangeError)} onClick={calculate}>
            Calculate
          </Button>
        </div>
        <p className={styles.note}>{`Turns the worker's clock registrations from ${from} to ${to} into pay lines in ${browserZone()}. Approved days are never recalculated.`}</p>
      </Card>
      {notice ? (
        <Alert tone="success" title="Done" live="status">
          {notice}
        </Alert>
      ) : null}
      {failures.map((f) => (
        <Alert key={f.title} tone="danger" title={f.title} live="alert">
          {f.problem.message} ({f.problem.code})
        </Alert>
      ))}
      {days.error ? (
        <Alert tone="danger" title="Could not load worker-days" live="alert" action={<Button size="sm" onClick={days.reload}>Try again</Button>}>
          {days.error.message} ({days.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Worker-days"
          columns={columns}
          rows={rows}
          getRowKey={(row) => `${row.workerId}|${row.workDate}`}
          loading={days.loading}
          empty={{
            title: status === "calculated" ? "Nothing waiting for approval" : "No worker-days in this range",
            description: "Calculate a worker's pay to create worker-days for review.",
          }}
        />
      )}
    </div>
  );
}
