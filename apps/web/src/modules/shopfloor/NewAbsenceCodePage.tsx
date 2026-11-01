import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createAbsenceCode, describeProblem, newKey, type NewAbsenceCode, type Problem } from "./api/client";
import styles from "./pages.module.css";

interface Values {
  code: string;
  name: string;
  /** "paid" or "unpaid". */
  pay: string;
  payMultiplier: string;
}

type Field = keyof Values;
type Errors = Partial<Record<Field, string>>;

/** Checks that mirror the service, and the request the form sends. */
export function validateAbsenceCode(values: Values): { errors: Errors; input?: NewAbsenceCode } {
  const errors: Errors = {};
  const code = values.code.trim();
  const name = values.name.trim();
  const paid = values.pay === "paid";
  const mult = values.payMultiplier.trim();
  if (code.length < 1 || code.length > 40) errors.code = "Enter a code of up to 40 characters.";
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (values.pay !== "paid" && values.pay !== "unpaid") errors.pay = "Choose paid or unpaid.";
  if (paid) {
    const places = mult.split(".")[1]?.length ?? 0;
    if (!/^\d{1,2}(\.\d+)?$/.test(mult) || places > 4 || Number(mult) <= 0 || Number(mult) > 5) errors.payMultiplier = "Enter a share above 0 and up to 5, such as 1 or 0.8.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: paid ? { code, name, paid, payMultiplier: mult } : { code, name, paid } };
}

/** Create an absence code (FR-25.2). */
export function NewAbsenceCodePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ code: "", name: "", pay: "paid", payMultiplier: "1" });
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
    const { errors: found, input } = validateAbsenceCode(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createAbsenceCode(input, key.current);
      navigate("/shopfloor/absence-codes");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of ["code", "name", "payMultiplier"] as const) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.fields.paid) fieldErrors.pay = problem.fields.paid;
      if (problem.code === "SF-0142") fieldErrors.code = "This code is already used.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New absence code" description="A paid absence is paid at its share of the worker's hourly rate. An unpaid absence keeps the hours on the worker's day with no amount." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="absence-code" label="Code" hint="Unique, for example SICK." error={errors.code}>
            {(control) => <Input {...control} maxLength={40} value={values.code} onChange={(event) => update({ code: event.target.value })} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="absence-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} maxLength={120} value={values.name} onChange={(event) => update({ name: event.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="absence-pay" label="Pay" error={errors.pay}>
            {(control) => (
              <Select {...control} value={values.pay} onChange={(event) => update({ pay: event.target.value })} invalid={Boolean(errors.pay)}>
                <option value="paid">Paid</option>
                <option value="unpaid">Unpaid</option>
              </Select>
            )}
          </FormField>
          {values.pay === "paid" ? (
            <FormField id="absence-multiplier" label="Share of the hourly rate" hint="Above 0 and up to 5; 1 pays the full rate." error={errors.payMultiplier}>
              {(control) => <Input {...control} inputMode="decimal" value={values.payMultiplier} onChange={(event) => update({ payMultiplier: event.target.value })} invalid={Boolean(errors.payMultiplier)} />}
            </FormField>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the absence code" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create absence code
            </Button>
            <Link className={buttonClassName("ghost")} to="/shopfloor/absence-codes">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
