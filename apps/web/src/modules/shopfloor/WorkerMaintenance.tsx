import { ProblemMessage } from "../../shell/errors";
import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, Textarea } from "../../shell/ui";
import { deactivateWorker, describeProblem, newKey, reactivateWorker, updateWorker, type Problem, type Worker } from "./api/client";
import { dash, formatDateTime } from "./format";
import { validateWorker } from "./NewWorkerPage";
import styles from "./pages.module.css";

type Field = "name" | "badgeId" | "userId" | "costRate" | "currency";
type Errors = Partial<Record<Field, string>>;

const REASON_MAX = 500;

function explain(problem: Problem): string | undefined {
  switch (problem.code) {
    case "SF-0143":
      return "The worker was changed by someone else. Reload the page and try again.";
    case "SF-0106":
      return "You can't edit your own worker record. Ask another supervisor.";
    case "SF-0169":
      return "The currency can't change because pay of this worker is already approved.";
    case "SF-0166":
      return "The worker is clocked in or has an open job. Close the open time (a clock-out and a job stop) first.";
    case "SF-0167":
      return "The worker is already inactive. Reload the page.";
    case "SF-0168":
      return "The worker is already active. Reload the page.";
    default:
      return undefined;
  }
}

/**
 * Edits a worker's own fields and deactivates or reactivates the worker (D2a'). A deactivated
 * worker can no longer sign in at a kiosk or register time, but the pay of the days already worked
 * is still calculated, approved and exported. The service enforces shopfloor.setup.maintain.
 */
export function WorkerMaintenance({ worker, onSaved }: { worker: Worker; onSaved(): void }) {
  const [values, setValues] = useState({
    name: worker.name,
    badgeId: worker.badgeId ?? "",
    userId: worker.userId ?? "",
    costRate: worker.costRate,
    currency: worker.currency,
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | undefined>();
  const [stateFailure, setStateFailure] = useState<Problem | null>(null);
  const [stateNotice, setStateNotice] = useState<string | null>(null);
  const [stateBusy, setStateBusy] = useState(false);
  const stateKey = useRef(newKey());

  function update(patch: Partial<typeof values>) {
    setValues({ ...values, ...patch });
    setSaved(false);
    key.current = newKey();
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setSaved(false);
    setFailure(null);
    const { errors: found, input } = validateWorker({ ...values, partyId: worker.partyId });
    const { partyId: _party, ...rest } = found;
    setErrors(rest);
    if (!input) return;
    const { partyId: _unused, ...edit } = input;
    setBusy(true);
    try {
      await updateWorker(worker.id, worker.version, edit, key.current);
      setSaved(true);
      key.current = newKey();
      onSaved();
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["name", "badgeId", "userId", "costRate", "currency"] as const) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (problem.code === "SF-0105") fieldErrors.badgeId = "This badge is already assigned to another worker.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      // A stale version or a conflict is a different request next time.
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  async function changeState(action: () => Promise<Worker>, notice: string) {
    setStateFailure(null);
    setStateNotice(null);
    setStateBusy(true);
    try {
      await action();
      setStateNotice(notice);
      setReason("");
      stateKey.current = newKey();
      onSaved();
    } catch (error) {
      setStateFailure(describeProblem(error));
      stateKey.current = newKey();
    } finally {
      setStateBusy(false);
    }
  }

  function deactivate(event: FormEvent) {
    event.preventDefault();
    const trimmed = reason.trim();
    if (trimmed.length < 1 || trimmed.length > REASON_MAX) {
      setReasonError(`Enter a reason of up to ${REASON_MAX} characters.`);
      return;
    }
    setReasonError(undefined);
    void changeState(() => deactivateWorker(worker.id, worker.version, trimmed, stateKey.current), "Worker deactivated");
  }

  const field = (id: Field, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`worker-edit-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input
          {...control}
          {...extra}
          value={values[id]}
          onChange={(event) => update({ [id]: id === "currency" ? event.target.value.toUpperCase() : event.target.value } as Partial<typeof values>)}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <>
      <Card title="Edit worker">
        <form className={styles.form} onSubmit={save} noValidate>
          {field("name", "Name", undefined, false, { maxLength: 120 })}
          {field("badgeId", "Badge", "Scanned at the kiosk; unique in your tenant. Clear it to remove the badge.", true, { maxLength: 64 })}
          {field("userId", "Sign-in user", "This user can't approve their own time.", true, { maxLength: 200 })}
          {field("costRate", "Cost rate per hour", "The base rate, for days before the first dated rate in the cost rate history (all days when there is none). Approved days keep their pay; days already calculated keep their lines until recalculated.", false, { inputMode: "decimal" })}
          {field("currency", "Currency", undefined, false, { maxLength: 3 })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the worker" live="alert">
                <ProblemMessage problem={failure} message={explain(failure)} />
              </Alert>
            </div>
          ) : null}
          {saved ? (
            <div className={styles.formActions}>
              <Alert tone="success" title="Worker saved" live="status">
                The changes apply to the next pay calculation.
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save worker
            </Button>
          </div>
        </form>
      </Card>
      <Card title={worker.active ? "Deactivate worker" : "Deactivated worker"}>
        {worker.active ? (
          <form className={styles.form} onSubmit={deactivate} noValidate>
            <p className={styles.note}>
              A deactivated worker can't sign in at a kiosk or register time. The pay of the days already worked is still calculated, approved and exported. A worker who is clocked in can't be deactivated.
            </p>
            <FormField id="worker-deactivate-reason" label="Reason" hint="Kept on the worker, for example left the company on 31 October." error={reasonError}>
              {(control) => (
                <Textarea {...control} rows={2} maxLength={REASON_MAX} value={reason} invalid={Boolean(reasonError)} onChange={(event) => setReason(event.target.value)} />
              )}
            </FormField>
            {stateFailure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not deactivate the worker" live="alert">
                  <ProblemMessage problem={stateFailure} message={explain(stateFailure)} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="danger" loading={stateBusy}>
                Deactivate worker
              </Button>
            </div>
          </form>
        ) : (
          <div className={styles.form}>
            <dl className={styles.details}>
              <div className={styles.entry}>
                <dt className={styles.term}>Deactivated</dt>
                <dd className={styles.value}>{worker.deactivatedAt ? formatDateTime(worker.deactivatedAt) : dash}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>By</dt>
                <dd className={styles.value}>{worker.deactivatedBy ?? dash}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Reason</dt>
                <dd className={styles.value}>{worker.deactivationReason ?? dash}</dd>
              </div>
            </dl>
            {stateFailure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not reactivate the worker" live="alert">
                  <ProblemMessage problem={stateFailure} message={explain(stateFailure)} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button variant="primary" loading={stateBusy} onClick={() => void changeState(() => reactivateWorker(worker.id, worker.version, stateKey.current), "Worker reactivated")}>
                Reactivate worker
              </Button>
            </div>
          </div>
        )}
        {stateNotice ? (
          <Alert tone="success" title={stateNotice} live="status">
            {stateNotice === "Worker deactivated" ? "The badge no longer signs in." : "The badge signs in again."}
          </Alert>
        ) : null}
      </Card>
    </>
  );
}
