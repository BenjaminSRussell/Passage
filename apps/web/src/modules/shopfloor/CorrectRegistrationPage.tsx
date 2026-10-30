import { useRef, useState, type FormEvent } from "react";
import { Link, useLocation, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Textarea } from "../../shell/ui";
import { correctRegistration, describeProblem, newKey, type Correction, type CorrectionInput, type Problem, type Registration } from "./api/client";
import { dash, formatDateTime, formatHours, UUID } from "./format";
import styles from "./pages.module.css";

export interface Values {
  /** datetime-local value in UTC, e.g. 2026-10-26T08:00. Empty keeps the stored time. */
  startedAt: string;
  stoppedAt: string;
  reason: string;
}

export type Errors = Partial<Record<keyof Values, string>>;

/** "2026-10-26T08:00" in UTC from an ISO instant, for a datetime-local field. */
export function toLocalInput(iso?: string | null): string {
  if (!iso) return "";
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? "" : date.toISOString().slice(0, 16);
}

const fromLocalInput = (value: string): string => new Date(`${value}:00Z`).toISOString();

/** Checks that mirror the service (FR-25.8), and the request the form sends. */
export function validateCorrection(values: Values, original?: Registration): { errors: Errors; input?: CorrectionInput } {
  const errors: Errors = {};
  const started = values.startedAt.trim();
  const stopped = values.stoppedAt.trim();
  const reason = values.reason.trim();
  if (started !== "" && Number.isNaN(new Date(`${started}:00Z`).getTime())) errors.startedAt = "Enter a valid date and time.";
  if (stopped !== "" && Number.isNaN(new Date(`${stopped}:00Z`).getTime())) errors.stoppedAt = "Enter a valid date and time.";
  if (original && (original.kind === "clock-in" || original.kind === "clock-out") && stopped !== "") {
    errors.stoppedAt = `A ${original.kind} is a point in time and has no stop.`;
  }
  if (started === "" && stopped === "") errors.startedAt = "Enter the corrected time.";
  if (!errors.startedAt && !errors.stoppedAt && started !== "" && stopped !== "") {
    const span = new Date(`${stopped}:00Z`).getTime() - new Date(`${started}:00Z`).getTime();
    if (span < 0) errors.stoppedAt = "The stop must not be before the start.";
    else if (span > 24 * 3_600_000) errors.stoppedAt = "A registration cannot exceed 24 hours.";
  }
  if (reason.length < 1 || reason.length > 500) errors.reason = "Say why the time changes, up to 500 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: CorrectionInput = { reason };
  if (started !== "") input.startedAt = fromLocalInput(started);
  if (stopped !== "") input.stoppedAt = fromLocalInput(stopped);
  return { errors, input };
}

const PROBLEM_FIELDS = ["startedAt", "stoppedAt", "reason"] as const;

/**
 * Correct the times of one registration with a reason (FR-25.8). Times are entered in UTC,
 * as the registrations list shows them. The registration may come from the worker page
 * (router state); without it the form still works from the id in the address.
 */
export function CorrectRegistrationPage() {
  const { id = "" } = useParams();
  const location = useLocation();
  const original = (location.state as { registration?: Registration } | null)?.registration;
  const [values, setValues] = useState<Values>({
    startedAt: original ? toLocalInput(original.startedAt) : "",
    stoppedAt: original ? toLocalInput(original.stoppedAt) : "",
    reason: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState<Correction | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const backTo = original ? `/shopfloor/workers/${original.workerId}` : "/shopfloor/corrections";

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (!UUID.test(id)) {
      setFailure({ code: "SF-0126", message: "This address does not name a registration.", fields: {} });
      return;
    }
    const { errors: found, input } = validateCorrection(values, original);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    try {
      setSaved(await correctRegistration(id, input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of PROBLEM_FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (saved) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Registration corrected" />
        <Alert tone="success" title="Correction saved" live="status">
          {`Start ${formatDateTime(saved.oldStartedAt)} is now ${formatDateTime(saved.newStartedAt)}${saved.newStoppedAt ? `, stop ${formatDateTime(saved.newStoppedAt)}` : ""}. Recalculate the worker's pay; an approved day is revised with reversal lines.`}
        </Alert>
        <div className={styles.formActions}>
          <Link className={buttonClassName("primary")} to={`/shopfloor/workers/${saved.workerId}/revise`}>
            Revise pay
          </Link>
          <Link className={buttonClassName("secondary")} to="/shopfloor/corrections">
            All corrections
          </Link>
        </div>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Correct registration"
        description="Change the start or stop of a registration. The old times, the new times and your reason stay in the audit trail."
      />
      {original ? (
        <Card title="Stored registration">
          <dl className={styles.details}>
            <div className={styles.entry}>
              <dt className={styles.term}>Kind</dt>
              <dd className={styles.value}>{original.kind}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Started</dt>
              <dd className={styles.value}>{formatDateTime(original.startedAt)}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Stopped</dt>
              <dd className={styles.value}>{formatDateTime(original.stoppedAt)}</dd>
            </div>
            <div className={styles.entry}>
              <dt className={styles.term}>Hours</dt>
              <dd className={styles.value}>{original.hours ? formatHours(original.hours) : dash}</dd>
            </div>
          </dl>
        </Card>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="correct-startedAt" label="Started (UTC)" hint="Leave as it is to keep the stored start." error={errors.startedAt}>
            {(control) => (
              <Input {...control} type="datetime-local" value={values.startedAt} invalid={Boolean(errors.startedAt)} onChange={(event) => update({ startedAt: event.target.value })} />
            )}
          </FormField>
          {original && (original.kind === "clock-in" || original.kind === "clock-out") ? null : (
            <FormField id="correct-stoppedAt" label="Stopped (UTC)" hint="Fill in to close an open registration or move its stop." optional error={errors.stoppedAt}>
              {(control) => (
                <Input {...control} type="datetime-local" value={values.stoppedAt} invalid={Boolean(errors.stoppedAt)} onChange={(event) => update({ stoppedAt: event.target.value })} />
              )}
            </FormField>
          )}
          <FormField id="correct-reason" label="Reason" hint="Kept for audit, e.g. forgot to clock in; confirmed with the shift lead." error={errors.reason}>
            {(control) => (
              <Textarea {...control} rows={3} maxLength={500} value={values.reason} invalid={Boolean(errors.reason)} onChange={(event) => update({ reason: event.target.value })} />
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not correct the registration" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save correction
            </Button>
            <Link className={buttonClassName("ghost")} to={backTo}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
