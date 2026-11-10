import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, FormField, Input, Select } from "../../shell/ui";
import {
  createEmployment,
  describeProblem,
  newKey,
  type AssignmentKind,
  type Employment,
  type EmploymentType,
  type NewEmployment,
  type Problem,
} from "./api/client";
import { DATE, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

interface Values {
  legalEntityId: string;
  employmentType: EmploymentType;
  assignmentKind: AssignmentKind;
  homeLegalEntityId: string;
  hireDate: string;
  terminationDate: string;
  plannedEndDate: string;
}

type Field = keyof Values;
type Errors = Partial<Record<Field, string>>;
const FIELDS: Field[] = ["legalEntityId", "employmentType", "assignmentKind", "homeLegalEntityId", "hireDate", "terminationDate", "plannedEndDate"];

/** The service's employment rules (8.42.5, BR-42.14), and the request the form sends. */
export function validateEmployment(values: Values): { errors: Errors; input?: NewEmployment } {
  const errors: Errors = {};
  const legalEntityId = values.legalEntityId.trim();
  if (!UUID.test(legalEntityId)) errors.legalEntityId = "Choose a legal entity.";
  if (!DATE.test(values.hireDate)) errors.hireDate = "Enter the hire date.";
  if (values.terminationDate && !DATE.test(values.terminationDate)) errors.terminationDate = "Enter a date or leave it empty.";
  else if (values.terminationDate && DATE.test(values.hireDate) && values.terminationDate < values.hireDate)
    errors.terminationDate = "The termination date can't be before the hire date.";
  if (values.plannedEndDate && DATE.test(values.hireDate) && values.plannedEndDate < values.hireDate)
    errors.plannedEndDate = "The planned end can't be before the hire date.";
  if (values.assignmentKind === "GLOBAL_HOST") {
    const home = values.homeLegalEntityId.trim();
    if (!UUID.test(home) || home === legalEntityId) errors.homeLegalEntityId = "A global host needs a home legal entity other than the host.";
    if (!DATE.test(values.plannedEndDate)) errors.plannedEndDate = "A global host needs a planned end date.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewEmployment = { legalEntityId, employmentType: values.employmentType, assignmentKind: values.assignmentKind, hireDate: values.hireDate };
  if (values.terminationDate) input.terminationDate = values.terminationDate;
  if (values.plannedEndDate) input.plannedEndDate = values.plannedEndDate;
  if (values.assignmentKind === "GLOBAL_HOST") input.homeLegalEntityId = values.homeLegalEntityId.trim();
  return { errors, input };
}

/** Start an employment at a legal entity for one worker. */
export function EmploymentForm({ workerId, onCreated, onCancel }: { workerId: string; onCreated(e: Employment): void; onCancel(): void }) {
  const [values, setValues] = useState<Values>({
    legalEntityId: "",
    employmentType: "FULL_TIME",
    assignmentKind: "PRIMARY",
    homeLegalEntityId: "",
    hireDate: "",
    terminationDate: "",
    plannedEndDate: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateEmployment(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      onCreated(await createEmployment(workerId, input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "HCM-EMP-002") fieldErrors.terminationDate = problem.message;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const input = (id: Field, label: string, hint?: string, optional?: boolean, type = "text") => (
    <FormField id={`employment-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input {...control} type={type} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<Values>)} />
      )}
    </FormField>
  );

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Start employment">
      <LookupFormField id="employment-legalEntityId" label="Legal entity" entity="legalEntity" hint="The legal entity that employs the worker." value={values.legalEntityId} error={errors.legalEntityId} onChange={(legalEntityId) => update({ legalEntityId })} />
      <FormField id="employment-employmentType" label="Employment type">
        {(control) => (
          <Select {...control} value={values.employmentType} onChange={(event) => update({ employmentType: event.target.value as EmploymentType })}>
            <option value="FULL_TIME">Full time</option>
            <option value="PART_TIME">Part time</option>
            <option value="TEMP">Temporary</option>
            <option value="CONTRACTOR">Contractor</option>
          </Select>
        )}
      </FormField>
      <FormField id="employment-assignmentKind" label="Kind" hint="One primary employment per legal entity at a time.">
        {(control) => (
          <Select {...control} value={values.assignmentKind} onChange={(event) => update({ assignmentKind: event.target.value as AssignmentKind })}>
            <option value="PRIMARY">Primary</option>
            <option value="CONCURRENT">Concurrent</option>
            <option value="GLOBAL_HOST">Global host</option>
          </Select>
        )}
      </FormField>
      {values.assignmentKind === "GLOBAL_HOST" ? (
        <LookupFormField id="employment-homeLegalEntityId" label="Home legal entity" entity="legalEntity" prefill={false} hint="Where the worker is employed at home." value={values.homeLegalEntityId} error={errors.homeLegalEntityId} onChange={(homeLegalEntityId) => update({ homeLegalEntityId })} />
      ) : null}
      {input("hireDate", "Hire date", undefined, false, "date")}
      {input("terminationDate", "Termination date", "The last day employed.", true, "date")}
      {input("plannedEndDate", "Planned end date", undefined, values.assignmentKind !== "GLOBAL_HOST", "date")}
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="Could not start the employment" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          Start employment
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
