import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createWorker, describeProblem, newKey, type NewWorker, type Problem, type WorkerType } from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";

interface Values {
  personnelNumber: string;
  workerType: WorkerType;
  partyPersonId: string;
  legalName: string;
  preferredName: string;
  userId: string;
  nationalId: string;
  bankAccount: string;
}

type Field = keyof Values;
type Errors = Partial<Record<Field, string>>;

const PERSONNEL_NUMBER = /^[A-Z0-9][A-Z0-9-]{0,19}$/;
const FIELDS: Field[] = ["personnelNumber", "workerType", "partyPersonId", "legalName", "preferredName", "userId", "nationalId", "bankAccount"];

/** The checks the service makes, and the request the form sends. */
export function validateWorker(values: Values): { errors: Errors; input?: NewWorker } {
  const errors: Errors = {};
  const personnelNumber = values.personnelNumber.trim().toUpperCase();
  if (!PERSONNEL_NUMBER.test(personnelNumber)) errors.personnelNumber = "Use 1 to 20 characters: A to Z, 0 to 9 or a hyphen.";
  if (!UUID.test(values.partyPersonId.trim())) errors.partyPersonId = "Enter the person's party id (a UUID).";
  const legalName = values.legalName.trim();
  if (legalName.length < 1 || legalName.length > 200) errors.legalName = "Enter a legal name of up to 200 characters.";
  if (values.userId.trim() && !UUID.test(values.userId.trim())) errors.userId = "Enter the sign-in user id (a UUID), or leave it empty.";
  const nationalId = values.nationalId.trim();
  if (nationalId.length < 1 || nationalId.length > 32) errors.nationalId = "Enter a national ID of up to 32 characters.";
  if (values.bankAccount.trim().length > 34) errors.bankAccount = "Use at most 34 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewWorker = { personnelNumber, workerType: values.workerType, partyPersonId: values.partyPersonId.trim(), legalName, nationalId };
  if (values.preferredName.trim()) input.preferredName = values.preferredName.trim();
  if (values.userId.trim()) input.userId = values.userId.trim();
  if (values.bankAccount.trim()) input.bankAccount = values.bankAccount.trim();
  return { errors, input };
}

/** Create a worker. The national ID and bank account are sealed in the PII vault. */
export function NewWorkerPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({
    personnelNumber: "",
    workerType: "EMPLOYEE",
    partyPersonId: "",
    legalName: "",
    preferredName: "",
    userId: "",
    nationalId: "",
    bankAccount: "",
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
    const { errors: found, input } = validateWorker(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const worker = await createWorker(input, key.current);
      navigate(`/hr/workers/${worker.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "HCM-WRK-DUPLICATE") fieldErrors.personnelNumber = "This personnel number is already used.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (id: Field, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`worker-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input
          {...control}
          {...extra}
          value={values[id]}
          onChange={(event) => update({ [id]: event.target.value } as Partial<Values>)}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New worker"
        description="A worker is one person (wave 7 party). Start an employment from the worker's page once it is created."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("personnelNumber", "Personnel number", "Unique in your tenant, for example WRK-1001.", false, { maxLength: 20 })}
          <FormField id="worker-workerType" label="Worker type">
            {(control) => (
              <Select {...control} value={values.workerType} onChange={(event) => update({ workerType: event.target.value as WorkerType })}>
                <option value="EMPLOYEE">Employee</option>
                <option value="CONTRACTOR">Contractor</option>
              </Select>
            )}
          </FormField>
          {field("partyPersonId", "Party id", "The person record this worker belongs to.")}
          {field("legalName", "Legal name", undefined, false, { maxLength: 200 })}
          {field("preferredName", "Preferred name", undefined, true, { maxLength: 200 })}
          {field("userId", "Sign-in user", "The user's id, so they can use self-service.", true)}
          {field("nationalId", "National ID", "Encrypted at rest. Shown masked, for example ***-**-6789.", false, { maxLength: 32, autoComplete: "off" })}
          {field("bankAccount", "Bank account", "Encrypted at rest. IBAN or account number.", true, { maxLength: 34, autoComplete: "off" })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the worker" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create worker
            </Button>
            <Link className={buttonClassName("ghost")} to="/hr/workers">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
