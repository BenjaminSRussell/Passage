import { useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, Input, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  createRegistrations,
  describeProblem,
  listJobs,
  newKey,
  type CurrentJob,
  type Job,
  type Problem,
  type RegistrationInput,
  type RegistrationResult,
} from "./api/client";
import { formatDateTime, formatHours } from "./format";
import { JobStatusBadge } from "./JobsPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** The kiosk's device id on registrations, so a supervisor can tell where a punch came from. */
export const KIOSK_DEVICE_ID = "web-kiosk";

type Notice = { tone: "success" | "warning"; title: string; lines: string[] } | null;

/** "MO-1 OP-10" when the dispatch list holds the job, else the job id. */
export function jobLabel(jobId: string, jobs: readonly Job[] | null): string {
  const job = jobs?.find((j) => j.id === jobId);
  return job ? `${job.orderRef} ${job.operationRef}` : jobId;
}

/**
 * The closed job-stops that end the worker's open work at `stoppedAt` (FR-25.3). A bundle's
 * stops share one interval, as the server's BR-25.1 split requires: from the latest start of
 * its jobs (the kiosk starts a bundle's jobs together, so they share it).
 */
export function stopItems(workerId: string, open: readonly CurrentJob[], stoppedAt: string, key: () => string = newKey): RegistrationInput[] {
  const bundleStart = new Map<string, string>();
  for (const j of open) {
    if (!j.bundleId) continue;
    const seen = bundleStart.get(j.bundleId);
    if (!seen || Date.parse(j.startedAt) > Date.parse(seen)) bundleStart.set(j.bundleId, j.startedAt);
  }
  return open.map((j) => ({
    id: key(),
    workerId,
    kind: "job-stop",
    jobId: j.jobId,
    bundleId: j.bundleId,
    startedAt: j.bundleId ? (bundleStart.get(j.bundleId) ?? j.startedAt) : j.startedAt,
    stoppedAt,
    deviceId: KIOSK_DEVICE_ID,
  }));
}

/** What the server did besides storing the starts: auto-stops (SF-0002) and finished-job flags (SF-0004). */
export function describeStartResults(results: readonly RegistrationResult[], jobs: readonly Job[] | null, started: readonly string[]): string[] {
  const lines: string[] = [];
  for (const res of results) {
    for (const stop of res.autoStopped ?? []) {
      const capped = stop.flags.includes("auto-stop-capped") ? " It had run for more than 24 hours, so a supervisor reviews it." : "";
      lines.push(`${jobLabel(stop.jobId, jobs)} was stopped automatically after ${formatHours(stop.hours)} (SF-0002).${capped}`);
    }
  }
  results.forEach((res, i) => {
    if (res.flags?.includes("job-finished-conflict")) {
      lines.push(`${jobLabel(started[i] ?? "", jobs)} was already finished on the server; your supervisor reviews this start (SF-0004).`);
    }
  });
  return lines;
}

export interface KioskJobsProps {
  workerId: string;
  /** The worker's home resource; null means the dispatch list holds every job, which the kiosk says. */
  homeResourceId?: string | null;
  openJobs: CurrentJob[];
  onOpenJobsChange(next: CurrentJob[]): void;
}

/**
 * Kiosk job actions (FR-25.3): the worker's open job or bundle with a stop button, and the
 * dispatch list to start one job, or several together as a bundle. Starting work stops the
 * open work on the server, which says so (SF-0002). A failed action keeps its client UUIDs,
 * so pressing the button again can never register it twice.
 */
export function KioskJobs({ workerId, homeResourceId = null, openJobs, onOpenJobsChange }: KioskJobsProps) {
  const jobs = useLoad((signal) => listJobs({ workerId }, signal), `dispatch:${workerId}`);
  const [selected, setSelected] = useState<string[]>([]);
  const [selectError, setSelectError] = useState<string | null>(null);
  const [busy, setBusy] = useState<"start" | "bundle" | "stop" | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<Notice>(null);
  const pending = useRef<{ action: string; items: RegistrationInput[] } | null>(null);

  function toggle(id: string, on: boolean) {
    setSelected((s) => (on ? [...s, id] : s.filter((x) => x !== id)));
    setSelectError(null);
  }

  async function send(action: "start" | "bundle" | "stop", build: () => RegistrationInput[], done: (items: RegistrationInput[], res: RegistrationResult[]) => void) {
    const signature = `${action}:${action === "stop" ? openJobs.map((j) => j.jobId).join(",") : selected.join(",")}`;
    if (pending.current?.action !== signature) pending.current = { action: signature, items: build() };
    const items = pending.current.items;
    setBusy(action);
    setFailure(null);
    setNotice(null);
    try {
      const res = await createRegistrations(items);
      pending.current = null;
      done(items, res);
      jobs.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  function start(asBundle: boolean) {
    if (asBundle ? selected.length < 2 : selected.length !== 1) {
      setSelectError(asBundle ? "Select two or more jobs to start them as a bundle." : "Select one job to start, or use Start as bundle.");
      return;
    }
    const startedAt = new Date().toISOString();
    const bundleId = asBundle ? newKey() : null;
    void send(
      asBundle ? "bundle" : "start",
      () => selected.map((jobId) => ({ id: newKey(), workerId, kind: "job-start", jobId, bundleId, startedAt, deviceId: KIOSK_DEVICE_ID })),
      (items, res) => {
        const started = items.map((it) => it.jobId ?? "");
        onOpenJobsChange(items.map((it) => ({ jobId: it.jobId ?? "", bundleId: it.bundleId ?? null, startedAt: it.startedAt })));
        setSelected([]);
        const lines = describeStartResults(res, jobs.data, started);
        const names = started.map((id) => jobLabel(id, jobs.data)).join(", ");
        setNotice({
          tone: lines.length > 0 ? "warning" : "success",
          title: asBundle ? `Started a bundle of ${started.length} jobs` : "Job started",
          lines: [`Started ${names} at ${formatDateTime(items[0].startedAt)}.`, ...lines],
        });
      },
    );
  }

  function stop() {
    const stoppedAt = new Date().toISOString();
    void send(
      "stop",
      () => stopItems(workerId, openJobs, stoppedAt),
      (items) => {
        onOpenJobsChange([]);
        const names = items.map((it) => jobLabel(it.jobId ?? "", jobs.data)).join(", ");
        setNotice({ tone: "success", title: "Work stopped", lines: [`Stopped ${names} at ${formatDateTime(items[0].stoppedAt)}.`] });
      },
    );
  }

  const bundled = openJobs.some((j) => j.bundleId);
  const columns: DataTableColumn<Job>[] = [
    {
      key: "select",
      header: "Select",
      render: (row) => (
        <Input
          type="checkbox"
          id={`kiosk-job-${row.id}`}
          aria-label={`Select ${row.orderRef} ${row.operationRef}`}
          checked={selected.includes(row.id)}
          onChange={(e) => toggle(row.id, e.target.checked)}
        />
      ),
    },
    { key: "job", header: "Job", render: (row) => <Link to={`/shopfloor/jobs/${row.id}`}>{`${row.orderRef} ${row.operationRef}`}</Link> },
    { key: "start", header: "Planned start", render: (row) => formatDateTime(row.sequenceStart) },
    { key: "status", header: "Status", render: (row) => <JobStatusBadge status={row.status} /> },
  ];

  return (
    <>
      {notice ? (
        <Alert tone={notice.tone} title={notice.title} live="status">
          {notice.lines.map((line) => (
            <span key={line} className={styles.block}>
              {line}
            </span>
          ))}
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="The job action was not recorded" live="alert">
          {failure.message} ({failure.code}) Press the button again to retry the same action.
        </Alert>
      ) : null}
      <Card title={bundled ? "Current bundle" : "Current job"}>
        {openJobs.length === 0 ? (
          <p className={styles.value}>No job is running. Select one below to start it.</p>
        ) : (
          <div className={styles.stack}>
            <ul className={styles.plainList}>
              {openJobs.map((j) => (
                <li key={j.jobId}>
                  <Link to={`/shopfloor/jobs/${j.jobId}`}>{jobLabel(j.jobId, jobs.data)}</Link> since {formatDateTime(j.startedAt)}
                  {j.bundleId ? (
                    <>
                      {" "}
                      <Badge tone="info">Bundle</Badge>
                    </>
                  ) : null}
                </li>
              ))}
            </ul>
            <Button className={styles.kioskButton} variant="danger" loading={busy === "stop"} disabled={busy !== null} onClick={stop}>
              {bundled ? "Stop bundle" : "Stop job"}
            </Button>
          </div>
        )}
      </Card>
      <Card title="Start a job" padded={false}>
        {homeResourceId ? null : (
          <Alert tone="info" title="Showing every job">
            You have no home resource, so this list holds the jobs of every resource. Ask your supervisor to set your home resource to see only your own jobs.
          </Alert>
        )}
        {jobs.error ? (
          <Alert tone="danger" title="Could not load the dispatch list" live="alert" action={<Button size="sm" onClick={jobs.reload}>Try again</Button>}>
            {jobs.error.message} ({jobs.error.code})
          </Alert>
        ) : !jobs.data ? (
          <Skeleton />
        ) : (
          <DataTable
            label="Dispatch list"
            columns={columns}
            rows={jobs.data}
            getRowKey={(row) => row.id}
            empty={{ title: "No jobs to start", description: homeResourceId ? "There are no released or started jobs for your home resource right now." : "The schedule has no released or started jobs right now." }}
          />
        )}
      </Card>
      {jobs.data && jobs.data.length > 0 ? (
        <div className={styles.stack}>
          {selectError ? (
            <Alert tone="warning" live="alert">
              {selectError}
            </Alert>
          ) : null}
          <Button className={styles.kioskButton} variant="primary" loading={busy === "start"} disabled={busy !== null} onClick={() => start(false)}>
            Start job
          </Button>
          <Button className={styles.kioskButton} loading={busy === "bundle"} disabled={busy !== null} onClick={() => start(true)}>
            Start as bundle
          </Button>
        </div>
      ) : null}
    </>
  );
}
