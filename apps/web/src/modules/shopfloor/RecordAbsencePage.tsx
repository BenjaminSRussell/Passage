import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import { createRegistrations, describeProblem, listAbsenceCodes, listWorkers, newKey, type Problem, type RegistrationInput } from "./api/client";
import { today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

interface Values {
  workerId: string;
  absenceCodeId: string;
  date: string;
  /** Local start time HH:MM; an absence must not overlap the worker's clocked time. */
  start: string;
  hours: string;
  note: string;
}

type Field = keyof Values;
type Errors = Partial<Record<Field, string>>;

/** Checks that mirror the service, and the registration the form sends (id is the client UUID). */
export function validateAbsence(values: Values, id: string): { errors: Errors; input?: RegistrationInput } {
  const errors: Errors = {};
  const hours = values.hours.trim();
  if (!values.workerId) errors.workerId = "Choose a worker.";
  if (!values.absenceCodeId) errors.absenceCodeId = "Choose an absence code.";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(values.date) || Number.isNaN(new Date(`${values.date}T00:00:00`).getTime())) errors.date = "Enter the day of the absence.";
  if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(values.start)) errors.start = "Enter the start time, such as 08:00.";
  const places = hours.split(".")[1]?.length ?? 0;
  if (!/^\d{1,2}(\.\d+)?$/.test(hours) || places > 2 || Number(hours) <= 0 || Number(hours) > 16) errors.hours = "Enter hours above 0 and up to 16, such as 8 or 4.5.";
  if (values.note.length > 500) errors.note = "Use at most 500 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  const start = new Date(`${values.date}T${values.start}:00`);
  const stop = new Date(start.getTime() + Math.round(Number(hours) * 3600) * 1000);
  return {
    errors,
    input: {
      id,
      workerId: values.workerId,
      kind: "absence",
      absenceCodeId: values.absenceCodeId,
      startedAt: start.toISOString(),
      stoppedAt: stop.toISOString(),
      note: values.note.trim() || null,
    },
  };
}

/** A supervisor records an absence for a worker (FR-25.2); pay:calculate then adds its pay line on the start day. */
export function RecordAbsencePage() {
  const navigate = useNavigate();
  const workers = useLoad((signal) => listWorkers({ active: true }, signal), "absence-workers");
  const codes = useLoad((signal) => listAbsenceCodes({ active: true }, signal), "absence-codes-active");
  const [values, setValues] = useState<Values>({ workerId: "", absenceCodeId: "", date: today(), start: "08:00", hours: "8", note: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One client id per form state: a retried submit is a duplicate on the server, not a second absence.
  const id = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    id.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAbsence(values, id.current);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createRegistrations([input]);
      navigate("/shopfloor/absence-codes");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      const map: Record<string, Field> = { "items[0].workerId": "workerId", "items[0].absenceCodeId": "absenceCodeId", "items[0].startedAt": "start", "items[0].stoppedAt": "hours", "items[0].note": "note" };
      for (const [name, message] of Object.entries(problem.fields)) if (map[name]) fieldErrors[map[name]] = message;
      if (problem.code === "SF-0145") fieldErrors.absenceCodeId = "This absence code no longer exists.";
      if (problem.code === "SF-0146") fieldErrors.absenceCodeId = "This absence code is inactive.";
      if (problem.code === "SF-0101") fieldErrors.workerId = "This worker was not found or is inactive.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const loadError = workers.error ?? codes.error;
  const loading = workers.loading || codes.loading;
  return (
    <div className={styles.stack}>
      <PageHeader title="Record absence" description="The absence is stored on the day entered and becomes an absence-paid or absence-unpaid pay line the next time the worker's pay is calculated." />
      {loadError ? (
        <Alert tone="danger" title="Could not load workers and absence codes" live="alert" action={<Button size="sm" onClick={() => { workers.reload(); codes.reload(); }}>Try again</Button>}>
          <ProblemMessage problem={loadError} />
        </Alert>
      ) : loading ? (
        <Skeleton height="tile" />
      ) : (codes.data ?? []).length === 0 ? (
        <Alert tone="info" title="No active absence codes">
          Create an absence code first.{" "}
          <Link to="/shopfloor/absence-codes/new">New absence code</Link>
        </Alert>
      ) : (
        <Card>
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="absence-worker" label="Worker" error={errors.workerId}>
              {(control) => (
                <Select {...control} value={values.workerId} onChange={(event) => update({ workerId: event.target.value })} invalid={Boolean(errors.workerId)}>
                  <option value="">Choose a worker</option>
                  {(workers.data ?? []).map((w) => (
                    <option key={w.id} value={w.id}>
                      {w.name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="absence-reason" label="Absence code" error={errors.absenceCodeId}>
              {(control) => (
                <Select {...control} value={values.absenceCodeId} onChange={(event) => update({ absenceCodeId: event.target.value })} invalid={Boolean(errors.absenceCodeId)}>
                  <option value="">Choose a code</option>
                  {(codes.data ?? []).map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.code} - {c.name} ({c.paid ? "paid" : "unpaid"})
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="absence-date" label="Day" error={errors.date}>
              {(control) => <Input {...control} type="date" value={values.date} onChange={(event) => update({ date: event.target.value })} invalid={Boolean(errors.date)} />}
            </FormField>
            <FormField id="absence-start" label="Starts at" hint="The absence is paid on the day it starts. It must not overlap clocked time, or the day is flagged for review." error={errors.start}>
              {(control) => <Input {...control} type="time" value={values.start} onChange={(event) => update({ start: event.target.value })} invalid={Boolean(errors.start)} />}
            </FormField>
            <FormField id="absence-hours" label="Hours" hint="Up to 16; with the day's worked hours the total cannot pass 24." error={errors.hours}>
              {(control) => <Input {...control} inputMode="decimal" value={values.hours} onChange={(event) => update({ hours: event.target.value })} invalid={Boolean(errors.hours)} />}
            </FormField>
            <FormField id="absence-note" label="Note" error={errors.note}>
              {(control) => <Input {...control} maxLength={500} value={values.note} onChange={(event) => update({ note: event.target.value })} invalid={Boolean(errors.note)} />}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not record the absence" live="alert">
                  <ProblemMessage problem={failure} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Record absence
              </Button>
              <Link className={buttonClassName("ghost")} to="/shopfloor/absence-codes">
                Cancel
              </Link>
            </div>
          </form>
        </Card>
      )}
    </div>
  );
}
