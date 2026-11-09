import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createPosition, describeProblem, listPositions, newKey, type NewPosition, type Problem } from "./api/client";
import { DATE, todayIso, UUID, validFte } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

interface Values {
  positionNumber: string;
  jobCode: string;
  departmentId: string;
  legalEntityId: string;
  fte: string;
  allowOverfill: boolean;
  validFrom: string;
  validTo: string;
  reportsToPositionId: string;
}

type Field = Exclude<keyof Values, "allowOverfill">;
type Errors = Partial<Record<Field, string>>;
const FIELDS: Field[] = ["positionNumber", "jobCode", "departmentId", "legalEntityId", "fte", "validFrom", "validTo", "reportsToPositionId"];
const NUMBER = /^[A-Z0-9][A-Z0-9-]{0,19}$/;
const JOB = /^[A-Z0-9][A-Z0-9_-]{0,19}$/;

/** The service's position rules (FR-42.3), and the request the form sends. */
export function validatePosition(values: Values): { errors: Errors; input?: NewPosition } {
  const errors: Errors = {};
  const positionNumber = values.positionNumber.trim().toUpperCase();
  const jobCode = values.jobCode.trim().toUpperCase();
  if (!NUMBER.test(positionNumber)) errors.positionNumber = "Use 1 to 20 characters: A to Z, 0 to 9 or a hyphen.";
  if (!JOB.test(jobCode)) errors.jobCode = "Use 1 to 20 characters: A to Z, 0 to 9, _ or a hyphen.";
  if (!UUID.test(values.departmentId.trim())) errors.departmentId = "Enter the department's org unit id (a UUID).";
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!validFte(values.fte, 5)) errors.fte = "Enter an FTE from 0.05 to 2.00, for example 1.00 or 0.50.";
  if (!DATE.test(values.validFrom)) errors.validFrom = "Enter the date the position starts.";
  if (values.validTo && (!DATE.test(values.validTo) || values.validTo <= values.validFrom)) errors.validTo = "The end must be after the start.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewPosition = {
    positionNumber,
    jobCode,
    departmentId: values.departmentId.trim(),
    legalEntityId: values.legalEntityId.trim(),
    fte: values.fte.trim(),
    allowOverfill: values.allowOverfill,
    validFrom: values.validFrom,
  };
  if (values.validTo) input.validTo = values.validTo;
  if (values.reportsToPositionId) input.reportsToPositionId = values.reportsToPositionId;
  return { errors, input };
}

/** Create a position, optionally reporting to an existing one. */
export function NewPositionPage() {
  const navigate = useNavigate();
  const positions = useLoad((signal) => listPositions({}, signal), "positions");
  const [values, setValues] = useState<Values>({
    positionNumber: "",
    jobCode: "",
    departmentId: "",
    legalEntityId: "",
    fte: "1.00",
    allowOverfill: false,
    validFrom: todayIso(),
    validTo: "",
    reportsToPositionId: "",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePosition(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const position = await createPosition(input, key.current);
      navigate(`/hr/positions/${position.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "HCM-POS-DUPLICATE") fieldErrors.positionNumber = "This position number is already used.";
      if (problem.code === "HCM-POS-PARENT-INVALID") fieldErrors.reportsToPositionId = "That position doesn't exist on the start date.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (id: Field, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`position-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<Values>)} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New position" description="Assignments to a position can't exceed its FTE unless you allow overfill." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("positionNumber", "Position number", "Unique in your tenant, for example POS-100.", false, { maxLength: 20 })}
          {field("jobCode", "Job code", "For example ENG-2 or MGR.", false, { maxLength: 20 })}
          {field("departmentId", "Department id", "The organization unit of the department.")}
          {field("legalEntityId", "Legal entity id")}
          {field("fte", "FTE", "0.05 to 2.00. Two people sharing a job is 2.00.", false, { inputMode: "decimal" })}
          {field("validFrom", "Starts", undefined, false, { type: "date" })}
          {field("validTo", "Ends before", "The first day the position no longer exists.", true, { type: "date" })}
          <FormField
            id="position-reportsToPositionId"
            label="Reports to"
            optional
            hint={positions.error ? "Positions could not be loaded; you can still create a top position." : "The manager's position."}
            error={errors.reportsToPositionId}
          >
            {(control) => (
              <Select {...control} value={values.reportsToPositionId} onChange={(event) => update({ reportsToPositionId: event.target.value })}>
                <option value="">None (top of the hierarchy)</option>
                {(positions.data?.items ?? []).map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.positionNumber} ({p.jobCode})
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.check}>
            <Input
              id="position-allowOverfill"
              type="checkbox"
              checked={values.allowOverfill}
              onChange={(event) => update({ allowOverfill: event.target.checked })}
            />
            <label htmlFor="position-allowOverfill">Allow overfill</label>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the position" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create position
            </Button>
            <Link className={buttonClassName("ghost")} to="/hr/positions">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
