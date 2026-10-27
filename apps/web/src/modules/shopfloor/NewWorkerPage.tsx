import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createWorker, describeProblem, newKey, type NewWorker, type Problem } from "./api/client";
import { UUID } from "./format";
import styles from "./pages.module.css";

interface Values {
  name: string;
  partyId: string;
  badgeId: string;
  userId: string;
  costRate: string;
  currency: string;
}

type Field = keyof Values;
type Errors = Partial<Record<Field, string>>;

const RATE = /^\d{1,14}(\.\d{1,6})?$/;
const CURRENCY = /^[A-Z]{3}$/;

/** Checks that mirror the service, and the request the form sends. */
export function validateWorker(values: Values): { errors: Errors; input?: NewWorker } {
  const errors: Errors = {};
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!UUID.test(values.partyId.trim())) errors.partyId = "Enter the employee's party id (a UUID).";
  if (values.badgeId.trim().length > 64) errors.badgeId = "Use at most 64 characters.";
  if (values.userId.trim().length > 200) errors.userId = "Use at most 200 characters.";
  if (!RATE.test(values.costRate.trim())) errors.costRate = "Enter an hourly rate such as 21.50.";
  if (!CURRENCY.test(values.currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      name,
      partyId: values.partyId.trim(),
      badgeId: values.badgeId.trim() || null,
      userId: values.userId.trim() || null,
      costRate: values.costRate.trim(),
      currency: values.currency,
    },
  };
}

/** Register a worker so they can clock in and have their pay calculated. */
export function NewWorkerPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ name: "", partyId: "", badgeId: "", userId: "", costRate: "", currency: "USD" });
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
      navigate(`/shopfloor/workers/${worker.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["name", "partyId", "badgeId", "userId", "costRate", "currency"] as const) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      if (problem.code === "SF-0105") fieldErrors.badgeId = "This badge is already assigned to another worker.";
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
          onChange={(event) => update({ [id]: id === "currency" ? event.target.value.toUpperCase() : event.target.value } as Partial<Values>)}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New worker" description="Pay lines use the cost rate in this currency; production costing uses it for labor hours." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("name", "Name", undefined, false, { maxLength: 120 })}
          {field("partyId", "Party id", "The employee's party record (wave 7).")}
          {field("badgeId", "Badge", "Scanned at the kiosk; unique in your tenant.", true, { maxLength: 64 })}
          {field("userId", "Sign-in user", "This user can't approve their own time.", true, { maxLength: 200 })}
          {field("costRate", "Cost rate per hour", "For example 21.50.", false, { inputMode: "decimal" })}
          {field("currency", "Currency", undefined, false, { maxLength: 3 })}
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
            <Link className={buttonClassName("ghost")} to="/shopfloor/workers">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
