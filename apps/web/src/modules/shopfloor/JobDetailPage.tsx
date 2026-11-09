import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  DUTY_JOB_REOPEN,
  DUTY_VIEW,
  describeProblem,
  getJob,
  listJobReopenings,
  newKey,
  recordJobFeedback,
  reopenJob,
  type Job,
  type JobCheck,
  type JobFeedbackInput,
  type JobReopening,
  type Problem,
} from "./api/client";
import { dash, formatDateTime } from "./format";
import { JobStatusBadge, qty } from "./JobsPage";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const KIND_LABELS: Record<JobCheck["kind"], string> = { numeric: "Numeric", "pass-fail": "Pass or fail", text: "Text" };

/** "10 to 12", "at least 10", "at most 12" or a dash. */
export function checkLimits(c: JobCheck): string {
  if (c.min && c.max) return `${c.min} to ${c.max}`;
  if (c.min) return `at least ${c.min}`;
  if (c.max) return `at most ${c.max}`;
  return dash;
}

const QTY = /^\d{1,14}(\.\d{1,4})?$/;

/** A well-formed quantity above zero, decided on the digits (no floats). */
const positive = (v: string) => QTY.test(v) && /[1-9]/.test(v);

export interface FeedbackValues {
  good: string;
  scrap: string;
  scrapReason: string;
  checks: Record<string, string>;
}

/** Client-side checks that mirror the server's; returns errors keyed by field. */
export function validateFeedback(values: FeedbackValues, finished: boolean): Record<string, string> {
  const errors: Record<string, string> = {};
  for (const [name, value] of [["good", values.good], ["scrap", values.scrap]] as const) {
    if (value.trim() !== "" && !QTY.test(value.trim())) errors[name] = "Enter a quantity of zero or more with up to 4 decimal places.";
  }
  const scrap = values.scrap.trim();
  if (!errors.scrap && positive(scrap) && values.scrapReason.trim() === "") {
    errors.scrapReason = "Scrap needs a reason code.";
  }
  if (values.scrapReason.trim().length > 64) errors.scrapReason = "Use at most 64 characters.";
  for (const [code, value] of Object.entries(values.checks)) {
    if (value.trim().length > 64) errors[`check-${code}`] = "Use at most 64 characters.";
  }
  const reported =
    finished ||
    Object.values(values.checks).some((v) => v.trim() !== "") ||
    [values.good, values.scrap].some((v) => positive(v.trim()));
  if (Object.keys(errors).length === 0 && !reported) errors.good = "Report a quantity or a check value, or finish the job.";
  return errors;
}

export function toFeedbackInput(feedbackId: string, values: FeedbackValues, finished: boolean, overrideReason?: string): JobFeedbackInput {
  const input: JobFeedbackInput = { feedbackId, finished };
  if (values.good.trim() !== "") input.goodQuantity = values.good.trim();
  if (values.scrap.trim() !== "") input.scrapQuantity = values.scrap.trim();
  if (values.scrapReason.trim() !== "") input.scrapReasonId = values.scrapReason.trim();
  const checks = Object.entries(values.checks)
    .filter(([, value]) => value.trim() !== "")
    .map(([code, value]) => ({ code, value: value.trim() }));
  if (checks.length > 0) input.checks = checks;
  if (overrideReason !== undefined) input.override = { reason: overrideReason };
  return input;
}

const empty: FeedbackValues = { good: "", scrap: "", scrapReason: "", checks: {} };

/**
 * Kiosk job detail (FR-25.4, FR-25.5): the plan, reported quantities and quality checks
 * with their latest results, and the report form. A finish with a mandatory check that
 * has not passed is refused (SF-0005) and a supervisor can finish it with a reason.
 */
export function JobDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getJob(id, signal), id);
  const [fresh, setFresh] = useState<Job | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const job = fresh && fresh.id === id ? fresh : state.data;
  const duties = usePrincipalDuties();
  // The dispatch list is an operator's page (shopfloor.time.register): the reopen form needs the
  // supervisor duty and the reopenings list shopfloor.view, so an operator sees neither (nor a 403).
  const canReopen = holdsDuty(duties, DUTY_JOB_REOPEN);
  const canSeeReopenings = holdsDuty(duties, DUTY_VIEW);
  const reopenings = useLoad(
    (signal) => (canSeeReopenings ? listJobReopenings(id, signal) : Promise.resolve([])),
    canSeeReopenings ? `${id}:reopenings` : "",
  );

  const resultFor = (code: string) => job?.results.find((r) => r.code === code);
  const checkColumns: DataTableColumn<JobCheck>[] = [
    { key: "name", header: "Check", render: (row) => `${row.name} (${row.code})` },
    { key: "kind", header: "Kind", render: (row) => KIND_LABELS[row.kind] ?? row.kind },
    { key: "limits", header: "Limits", mono: true, render: (row) => (row.kind === "numeric" ? checkLimits(row) : dash) },
    { key: "mandatory", header: "Mandatory", render: (row) => (row.mandatory ? "Yes" : "No") },
    { key: "value", header: "Latest value", mono: true, render: (row) => resultFor(row.code)?.value ?? dash },
    {
      key: "result",
      header: "Result",
      render: (row) => {
        const r = resultFor(row.code);
        if (!r) return row.mandatory ? <Badge tone="warning">Not recorded</Badge> : dash;
        return r.pass ? <Badge tone="success">Pass</Badge> : <Badge tone="danger">Fail</Badge>;
      },
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title={job ? `Job ${job.orderRef} ${job.operationRef}` : "Job"} description={<Link to="/shopfloor/jobs">Dispatch list</Link>} />
      {notice ? (
        <Alert tone="success" title="Report saved" live="status">
          {notice}
        </Alert>
      ) : null}
      {state.error && !job ? (
        <Alert tone="danger" title="Could not load the job" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !job ? (
        <Card>
          <Skeleton />
        </Card>
      ) : (
        <>
          <Card>
            <dl className={styles.details}>
              <div className={styles.entry}>
                <dt className={styles.term}>Status</dt>
                <dd className={styles.value}>
                  <JobStatusBadge status={job.status} />
                </dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Order</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{job.orderRef}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Operation</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{job.operationRef}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Resource</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{job.resourceId}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Start</dt>
                <dd className={styles.value}>{formatDateTime(job.sequenceStart)}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Planned</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{qty(job.plannedQuantity)}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Good</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{qty(job.goodQuantity)}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Scrap</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{qty(job.scrapQuantity)}</dd>
              </div>
              {job.finishedAt ? (
                <div className={styles.entry}>
                  <dt className={styles.term}>Finished</dt>
                  <dd className={styles.value}>{formatDateTime(job.finishedAt)}</dd>
                </div>
              ) : null}
            </dl>
          </Card>
          <Card title="Quality checks" padded={false}>
            <DataTable
              label="Quality checks"
              columns={checkColumns}
              rows={job.checks}
              getRowKey={(row) => row.code}
              empty={{ title: "No checks on this operation", description: "The job can be finished without recording a check." }}
            />
          </Card>
          {job.productionFinishedAt ? (
            <Alert tone="warning" title="Production finished this operation">
              Production finished it on {formatDateTime(job.productionFinishedAt)}. Starting work on this job (SF-0157) and reporting on it (SF-0161) are refused until the
              planner reopens the operation; a start replayed from an offline kiosk is kept and flagged for supervisor review. Work that was still open
              when production finished it was stopped at the finish.
            </Alert>
          ) : null}
          {job.status === "finished" && !canReopen ? (
            <Alert tone="info" title="This job is finished">
              It takes no more reports. A supervisor reopens a job before more is reported on it.
            </Alert>
          ) : job.status === "finished" ? (
            <ReopenForm
              key={job.version}
              job={job}
              onDone={(saved) => {
                setFresh(saved);
                setNotice("The job is reopened and is back on the dispatch list.");
                reopenings.reload();
              }}
            />
          ) : job.productionFinishedAt ? null : (
            <FeedbackForm
              key={job.version}
              job={job}
              onDone={(saved, finished) => {
                setFresh(saved);
                setNotice(finished ? "The job is finished and has left the dispatch list." : "Quantities and check values were added to the job.");
              }}
            />
          )}
          {canSeeReopenings ? (
            <Card title="Reopenings" padded={false}>
              {reopenings.error ? (
                <Alert tone="danger" title="Could not load the reopenings" live="alert" action={<Button size="sm" onClick={reopenings.reload}>Try again</Button>}>
                  <ProblemMessage problem={reopenings.error} />
                </Alert>
              ) : (
                <DataTable
                  label="Reopenings"
                  columns={reopeningColumns}
                  rows={reopenings.data ?? []}
                  getRowKey={(row) => row.id}
                  loading={reopenings.loading && !reopenings.data}
                  empty={{ title: "Never reopened", description: "A supervisor's reopening of this job after it was finished is listed here." }}
                />
              )}
            </Card>
          ) : null}
        </>
      )}
    </div>
  );
}

const reopeningColumns: DataTableColumn<JobReopening>[] = [
  { key: "at", header: "Reopened", render: (row) => formatDateTime(row.reopenedAt) },
  { key: "by", header: "By", render: (row) => row.reopenedBy },
  { key: "reason", header: "Reason", render: (row) => row.reason },
  { key: "finished", header: "Finish undone", render: (row) => formatDateTime(row.previousFinishedAt) },
  { key: "good", header: "Good then", mono: true, render: (row) => qty(row.goodQuantity) },
  { key: "scrap", header: "Scrap then", mono: true, render: (row) => qty(row.scrapQuantity) },
];

interface ReopenFormProps {
  job: Job;
  onDone(saved: Job): void;
}

/** Supervisor reopen of a finished job (FR-25.3): a reason, kept in the audit list below. */
function ReopenForm({ job, onDone }: ReopenFormProps) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per form: a resend after a lost response returns the stored job.
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const text = reason.trim();
    if (text.length < 1 || text.length > 500) {
      setError("Say why the job is reopened, up to 500 characters.");
      return;
    }
    setError(null);
    setFailure(null);
    setBusy(true);
    try {
      onDone(await reopenJob(job.id, job.version, text, key.current));
    } catch (failed) {
      const problem = describeProblem(failed);
      if (problem.fields.reason) setError(problem.fields.reason);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Reopen this job" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <div className={styles.formActions}>
          <Alert tone="info" title="This job is finished">
            It takes no more reports and is off the dispatch list. A supervisor can reopen it with a reason; its quantities and check results stay, and finishing it again passes
            the mandatory checks again.
          </Alert>
        </div>
        <FormField id="reopen-reason" label="Reason" hint="Kept in the reopening list, e.g. a late count." error={error ?? undefined}>
          {(control) => <Textarea {...control} value={reason} maxLength={500} rows={3} invalid={Boolean(error)} onChange={(e) => setReason(e.target.value)} />}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not reopen the job" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy} disabled={busy}>
            Reopen job
          </Button>
        </div>
      </form>
    </Card>
  );
}

interface FeedbackFormProps {
  job: Job;
  onDone(saved: Job, finished: boolean): void;
}

function FeedbackForm({ job, onDone }: FeedbackFormProps) {
  const [values, setValues] = useState<FeedbackValues>(empty);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<"report" | "finish" | "override" | null>(null);
  const [blocked, setBlocked] = useState<Problem | null>(null);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | null>(null);
  // One feedbackId per posting: a resend after a lost response is a replay on the
  // server, and an override resends the refused posting (which stored nothing).
  const feedbackId = useRef(newKey());

  function update(next: Partial<FeedbackValues>) {
    setValues((v) => ({ ...v, ...next }));
    feedbackId.current = newKey();
    setBlocked(null);
  }

  async function submit(finished: boolean, overrideReason?: string) {
    const found = validateFeedback(values, finished);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(overrideReason !== undefined ? "override" : finished ? "finish" : "report");
    try {
      const res = await recordJobFeedback(job.id, toFeedbackInput(feedbackId.current, values, finished, overrideReason));
      onDone(res.job, finished);
    } catch (failed) {
      const problem = describeProblem(failed);
      if (problem.code === "SF-0005") {
        setBlocked(problem);
      } else {
        const fields: Record<string, string> = {};
        if (problem.fields.goodQuantity) fields.good = problem.fields.goodQuantity;
        if (problem.fields.scrapQuantity) fields.scrap = problem.fields.scrapQuantity;
        if (problem.fields.scrapReasonId) fields.scrapReason = problem.fields.scrapReasonId;
        if (problem.fields["override.reason"]) setReasonError(problem.fields["override.reason"]);
        setErrors(fields);
        setFailure(problem);
      }
    } finally {
      setBusy(null);
    }
  }

  function report(event: FormEvent) {
    event.preventDefault();
    void submit(false);
  }

  function override(event: FormEvent) {
    event.preventDefault();
    const text = reason.trim();
    if (text.length < 1 || text.length > 500) {
      setReasonError("Say why the job may be finished, up to 500 characters.");
      return;
    }
    setReasonError(null);
    void submit(true, text);
  }

  return (
    <>
      <Card title="Report" headingLevel={3}>
        <form className={styles.form} onSubmit={report} noValidate>
          <FormField id="feedback-good" label="Good quantity" hint="Added to the good total." error={errors.good}>
            {(control) => <Input {...control} inputMode="decimal" value={values.good} invalid={Boolean(errors.good)} onChange={(e) => update({ good: e.target.value })} />}
          </FormField>
          <FormField id="feedback-scrap" label="Scrap quantity" hint="Added to the scrap total." error={errors.scrap}>
            {(control) => <Input {...control} inputMode="decimal" value={values.scrap} invalid={Boolean(errors.scrap)} onChange={(e) => update({ scrap: e.target.value })} />}
          </FormField>
          <FormField id="feedback-scrap-reason" label="Scrap reason" hint="Reason code, needed when scrap is reported." error={errors.scrapReason}>
            {(control) => (
              <Input {...control} maxLength={64} value={values.scrapReason} invalid={Boolean(errors.scrapReason)} onChange={(e) => update({ scrapReason: e.target.value })} />
            )}
          </FormField>
          {job.checks.length > 0 ? (
            <fieldset className={styles.valueGroup}>
              <legend className={styles.legend}>Check values</legend>
              {job.checks.map((check) => {
                const error = errors[`check-${check.code}`];
                const value = values.checks[check.code] ?? "";
                const set = (v: string) => update({ checks: { ...values.checks, [check.code]: v } });
                const hint = check.kind === "numeric" ? `Limits ${checkLimits(check)}${check.mandatory ? ", mandatory" : ""}` : check.mandatory ? "Mandatory" : "Optional";
                return (
                  <FormField key={check.code} id={`feedback-check-${check.code}`} label={check.name} hint={hint} error={error}>
                    {(control) =>
                      check.kind === "pass-fail" ? (
                        <Select {...control} value={value} onChange={(e) => set(e.target.value)}>
                          <option value="">Not recorded</option>
                          <option value="pass">Pass</option>
                          <option value="fail">Fail</option>
                        </Select>
                      ) : (
                        <Input
                          {...control}
                          inputMode={check.kind === "numeric" ? "decimal" : undefined}
                          maxLength={64}
                          value={value}
                          invalid={Boolean(error)}
                          onChange={(e) => set(e.target.value)}
                        />
                      )
                    }
                  </FormField>
                );
              })}
            </fieldset>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the report" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "report"} disabled={busy !== null}>
              Save report
            </Button>
            <Button loading={busy === "finish"} disabled={busy !== null} onClick={() => void submit(true)}>
              Report and finish
            </Button>
          </div>
        </form>
      </Card>
      {blocked ? (
        <Card title="Finish blocked by a mandatory check" headingLevel={3}>
          <form className={styles.form} onSubmit={override} noValidate>
            <div className={styles.formActions}>
              <Alert tone="warning" title="The job was not finished" live="alert">
                {blocked.message} Nothing from this report was saved. Correct the value, or a supervisor finishes the job with a reason.
              </Alert>
            </div>
            <FormField id="override-reason" label="Override reason" hint="Kept with the report, e.g. a deviation number." error={reasonError ?? undefined}>
              {(control) => (
                <Textarea {...control} value={reason} maxLength={500} rows={3} invalid={Boolean(reasonError)} onChange={(e) => setReason(e.target.value)} />
              )}
            </FormField>
            <div className={styles.formActions}>
              <Button type="submit" variant="danger" loading={busy === "override"} disabled={busy !== null}>
                Finish with override
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </>
  );
}
