import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, FormField, Input, Select } from "../../shell/ui";
import {
  createAssignment,
  describeProblem,
  listEmployments,
  listWorkers,
  newKey,
  type NewAssignment,
  type Position,
  type PositionAssignment,
  type Problem,
} from "./api/client";
import { DATE, dayOf, validFte } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

interface Values {
  workerId: string;
  employmentId: string;
  fteShare: string;
  isPrimary: boolean;
  validFrom: string;
  validTo: string;
}

type Field = Exclude<keyof Values, "isPrimary">;
type Errors = Partial<Record<Field, string>>;
const FIELDS: Field[] = ["workerId", "employmentId", "fteShare", "validFrom", "validTo"];

/** The service's assignment rules (FR-42.3), and the request the form sends. */
export function validateAssignment(values: Values): { errors: Errors; input?: NewAssignment } {
  const errors: Errors = {};
  if (!values.workerId) errors.workerId = "Choose a worker.";
  if (!values.employmentId) errors.employmentId = "Choose the employment that holds this position.";
  if (!validFte(values.fteShare, 1)) errors.fteShare = "Enter a share from 0.01 to 2.00, for example 1.00.";
  if (!DATE.test(values.validFrom)) errors.validFrom = "Enter the first day on the position.";
  if (values.validTo && (!DATE.test(values.validTo) || values.validTo <= values.validFrom)) errors.validTo = "The end must be after the start.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewAssignment = {
    workerId: values.workerId,
    employmentId: values.employmentId,
    fteShare: values.fteShare.trim(),
    isPrimary: values.isPrimary,
    validFrom: values.validFrom,
  };
  if (values.validTo) input.validTo = values.validTo;
  return { errors, input };
}

/** Place a worker on a position through one of their employments in the position's legal entity. */
export function AssignmentForm({ position, onCreated, onCancel }: { position: Position; onCreated(a: PositionAssignment): void; onCancel(): void }) {
  const [values, setValues] = useState<Values>({ workerId: "", employmentId: "", fteShare: position.fte, isPrimary: true, validFrom: dayOf(position.validFrom), validTo: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const workers = useLoad((signal) => listWorkers({}, signal), "assign-workers");
  const employments = useLoad(
    async (signal) => (values.workerId ? (await listEmployments(values.workerId, signal)).filter((e) => e.legalEntityId === position.legalEntityId && e.status !== "TERMINATED") : []),
    `assign-employments|${values.workerId}`,
  );

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAssignment(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      onCreated(await createAssignment(position.id, input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "HCM-POS-001") fieldErrors.fteShare = problem.message;
      if (problem.code === "HCM-ASG-002") fieldErrors.employmentId = problem.message;
      if (problem.code === "HCM-ASG-003") fieldErrors.validFrom = problem.message;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const employmentHint = !values.workerId
    ? "Choose a worker first."
    : employments.loading
      ? "Loading employments…"
      : employments.error
        ? `Could not load employments (${employments.error.code}).`
        : employments.data?.length === 0
          ? "This worker has no current employment in the position's legal entity."
          : undefined;

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Assign worker">
      <FormField id="assignment-workerId" label="Worker" error={errors.workerId} hint={workers.error ? `Could not load workers (${workers.error.code}).` : undefined}>
        {(control) => (
          <Select {...control} value={values.workerId} invalid={Boolean(errors.workerId)} onChange={(event) => update({ workerId: event.target.value, employmentId: "" })}>
            <option value="">Choose a worker</option>
            {(workers.data?.items ?? []).map((w) => (
              <option key={w.id} value={w.id}>
                {w.personnelNumber} {w.legalName}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="assignment-employmentId" label="Employment" hint={employmentHint} error={errors.employmentId}>
        {(control) => (
          <Select {...control} value={values.employmentId} invalid={Boolean(errors.employmentId)} onChange={(event) => update({ employmentId: event.target.value })}>
            <option value="">Choose an employment</option>
            {(employments.data ?? []).map((e) => (
              <option key={e.id} value={e.id}>
                {e.assignmentKind} from {e.hireDate}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="assignment-fteShare" label="FTE share" hint={`The position's FTE is ${position.fte}.`} error={errors.fteShare}>
        {(control) => <Input {...control} inputMode="decimal" value={values.fteShare} invalid={Boolean(errors.fteShare)} onChange={(event) => update({ fteShare: event.target.value })} />}
      </FormField>
      <FormField id="assignment-validFrom" label="Starts" error={errors.validFrom}>
        {(control) => <Input {...control} type="date" value={values.validFrom} invalid={Boolean(errors.validFrom)} onChange={(event) => update({ validFrom: event.target.value })} />}
      </FormField>
      <FormField id="assignment-validTo" label="Ends before" optional hint="The first day off the position." error={errors.validTo}>
        {(control) => <Input {...control} type="date" value={values.validTo} invalid={Boolean(errors.validTo)} onChange={(event) => update({ validTo: event.target.value })} />}
      </FormField>
      <div className={styles.check}>
        <Input id="assignment-isPrimary" type="checkbox" checked={values.isPrimary} onChange={(event) => update({ isPrimary: event.target.checked })} />
        <label htmlFor="assignment-isPrimary">Primary position</label>
      </div>
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="Could not assign the worker" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Assign worker
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
