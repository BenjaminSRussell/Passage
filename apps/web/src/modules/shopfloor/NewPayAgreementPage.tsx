import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createPayAgreement, describeProblem, newKey, type NewPayAgreement, type Problem } from "./api/client";
import { WEEKDAYS } from "./format";
import styles from "./pages.module.css";

interface Values {
  code: string;
  name: string;
  dailyThreshold: string;
  weeklyThreshold: string;
  overtimeMultiplier: string;
  sundayMultiplier: string;
  weekStart: string;
}

type Field = keyof Values;
type Errors = Partial<Record<Field, string>>;

const FIELDS: Field[] = ["code", "name", "dailyThreshold", "weeklyThreshold", "overtimeMultiplier", "sundayMultiplier", "weekStart"];

/** A decimal of at most `scale` places within [lo, hi]. */
function decimalWithin(raw: string, lo: number, hi: number, scale: number): boolean {
  const value = raw.trim();
  if (!/^\d{1,3}(\.\d+)?$/.test(value)) return false;
  const places = value.split(".")[1]?.length ?? 0;
  const n = Number(value);
  return places <= scale && n >= lo && n <= hi;
}

/** Checks that mirror the service, and the request the form sends. */
export function validateAgreement(values: Values): { errors: Errors; input?: NewPayAgreement } {
  const errors: Errors = {};
  const code = values.code.trim();
  const name = values.name.trim();
  if (code.length < 1 || code.length > 40) errors.code = "Enter a code of up to 40 characters.";
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!decimalWithin(values.dailyThreshold, 0, 24, 2)) errors.dailyThreshold = "Enter hours from 0 to 24, such as 8.";
  if (!decimalWithin(values.weeklyThreshold, 0, 168, 2)) errors.weeklyThreshold = "Enter hours from 0 to 168, such as 40.";
  if (!decimalWithin(values.overtimeMultiplier, 1, 5, 4)) errors.overtimeMultiplier = "Enter a multiplier from 1 to 5, such as 1.5.";
  if (!decimalWithin(values.sundayMultiplier, 1, 5, 4)) errors.sundayMultiplier = "Enter a multiplier from 1 to 5, such as 2.";
  const weekStart = Number(values.weekStart);
  if (!Number.isInteger(weekStart) || weekStart < 0 || weekStart > 6) errors.weekStart = "Choose a day.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      code,
      name,
      dailyThreshold: values.dailyThreshold.trim(),
      weeklyThreshold: values.weeklyThreshold.trim(),
      overtimeMultiplier: values.overtimeMultiplier.trim(),
      sundayMultiplier: values.sundayMultiplier.trim(),
      weekStart,
    },
  };
}

/** Create a pay agreement (FR-25.7). */
export function NewPayAgreementPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({
    code: "",
    name: "",
    dailyThreshold: "8",
    weeklyThreshold: "40",
    overtimeMultiplier: "1.5",
    sundayMultiplier: "2",
    weekStart: "1",
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
    const { errors: found, input } = validateAgreement(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createPayAgreement(input, key.current);
      navigate("/shopfloor/pay-agreements");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "SF-0142") fieldErrors.code = "This code is already used.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const field = (id: Exclude<Field, "weekStart">, label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`agreement-${id}`} label={label} hint={hint} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} onChange={(event) => update({ [id]: event.target.value } as Partial<Values>)} invalid={Boolean(errors[id])} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New pay agreement" description="Overtime applies to base hours over the thresholds; hours on Sunday are paid at the Sunday multiplier and do not count toward the weekly threshold." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {field("code", "Code", "Unique, for example US-HOURLY.", { maxLength: 40 })}
          {field("name", "Name", undefined, { maxLength: 120 })}
          {field("dailyThreshold", "Daily overtime after (hours)", "0 to 24.", { inputMode: "decimal" })}
          {field("weeklyThreshold", "Weekly overtime after (hours)", "0 to 168; base hours only.", { inputMode: "decimal" })}
          {field("overtimeMultiplier", "Overtime multiplier", "1 to 5.", { inputMode: "decimal" })}
          {field("sundayMultiplier", "Sunday multiplier", "1 to 5.", { inputMode: "decimal" })}
          <FormField id="agreement-weekStart" label="Pay week starts on" error={errors.weekStart}>
            {(control) => (
              <Select {...control} value={values.weekStart} onChange={(event) => update({ weekStart: event.target.value })} invalid={Boolean(errors.weekStart)}>
                {WEEKDAYS.map((day, index) => (
                  <option key={day} value={String(index)}>
                    {day}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the pay agreement" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create pay agreement
            </Button>
            <Link className={buttonClassName("ghost")} to="/shopfloor/pay-agreements">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
