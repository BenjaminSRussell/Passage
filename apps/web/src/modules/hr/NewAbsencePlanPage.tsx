import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import {
  createAbsencePlan,
  describeProblem,
  newKey,
  type AbsenceUnit,
  type AccrualDay,
  type AccrualFrequency,
  type NewAbsencePlan,
  type Problem,
  type Proration,
} from "./api/client";
import { validDecimal, validPlanYearStart } from "./format";
import styles from "./pages.module.css";

interface Values {
  code: string;
  unit: AbsenceUnit;
  accrualFrequency: AccrualFrequency;
  accrualRate: string;
  accrualDay: AccrualDay;
  proration: Proration;
  maxBalance: string;
  carryOverMax: string;
  carryOverExpiryMonths: string;
  planYearStart: string;
  minServiceDays: string;
  negativeBalanceAllowed: boolean;
}

type Field = Exclude<keyof Values, "negativeBalanceAllowed">;
type Errors = Partial<Record<Field, string>>;
const FIELDS: Field[] = [
  "code",
  "unit",
  "accrualFrequency",
  "accrualRate",
  "accrualDay",
  "proration",
  "maxBalance",
  "carryOverMax",
  "carryOverExpiryMonths",
  "planYearStart",
  "minServiceDays",
];
const CODE = /^[A-Z0-9][A-Z0-9-]{0,19}$/;
const WHOLE = /^\d{1,4}$/;

/** The service's plan rules (FR-42.6, 8.42.13), and the request the form sends. Decimals stay strings. */
export function validateAbsencePlan(values: Values): { errors: Errors; input?: NewAbsencePlan } {
  const errors: Errors = {};
  const code = values.code.trim().toUpperCase();
  if (!CODE.test(code)) errors.code = "Use 1 to 20 characters: A to Z, 0 to 9 or a hyphen.";
  if (!validDecimal(values.accrualRate, 4) || Number(values.accrualRate.split(".")[0]) >= 10000)
    errors.accrualRate = "Enter a rate per period below 10000 with at most 4 decimals, for example 1.25.";
  if (!validDecimal(values.maxBalance, 4)) errors.maxBalance = "Enter the highest balance with at most 4 decimals, for example 22.50.";
  if (!validDecimal(values.carryOverMax, 4)) errors.carryOverMax = "Enter how much may carry over, or 0.";
  const months = values.carryOverExpiryMonths.trim();
  if (!WHOLE.test(months) || Number(months) > 36) errors.carryOverExpiryMonths = "Enter 0 to 36 months. 0 keeps carry-over all year.";
  if (!validPlanYearStart(values.planYearStart.trim())) errors.planYearStart = "Enter MM-DD, for example 01-01. 02-29 isn't allowed.";
  const service = values.minServiceDays.trim();
  if (!WHOLE.test(service) || Number(service) > 3650) errors.minServiceDays = "Enter 0 to 3650 days.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      code,
      unit: values.unit,
      accrualFrequency: values.accrualFrequency,
      accrualRate: values.accrualRate.trim(),
      accrualDay: values.accrualDay,
      proration: values.proration,
      maxBalance: values.maxBalance.trim(),
      carryOverMax: values.carryOverMax.trim(),
      carryOverExpiryMonths: Number(months),
      planYearStart: values.planYearStart.trim(),
      negativeBalanceAllowed: values.negativeBalanceAllowed,
      minServiceDays: Number(service),
    },
  };
}

/** Create an absence plan; the defaults are the PRD 8.42.13 ones. */
export function NewAbsencePlanPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({
    code: "",
    unit: "DAYS",
    accrualFrequency: "MONTHLY",
    accrualRate: "",
    accrualDay: "LAST_DAY",
    proration: "DAYS_IN_PERIOD",
    maxBalance: "",
    carryOverMax: "0",
    carryOverExpiryMonths: "3",
    planYearStart: "01-01",
    minServiceDays: "0",
    negativeBalanceAllowed: false,
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
    const { errors: found, input } = validateAbsencePlan(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const plan = await createAbsencePlan(input, key.current);
      navigate(`/hr/absence/plans/${plan.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const name of FIELDS) if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      if (problem.code === "HCM-ABS-PLAN-DUPLICATE") fieldErrors.code = "This plan code is already used.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: Field, label: string, hint?: string, extra?: Record<string, unknown>) => (
    <FormField id={`plan-${id}`} label={label} hint={hint} error={errors[id]}>
      {(control) => (
        <Input {...control} {...extra} value={values[id]} invalid={Boolean(errors[id])} onChange={(event) => update({ [id]: event.target.value } as Partial<Values>)} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New absence plan"
        description="Accrual is rate × days employed ÷ days in the period, rounded to 2 decimals, and never above the max balance."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("code", "Plan code", "Unique in your tenant, for example US-PTO-15.", { maxLength: 20 })}
          <FormField id="plan-unit" label="Unit">
            {(control) => (
              <Select {...control} value={values.unit} onChange={(event) => update({ unit: event.target.value as AbsenceUnit })}>
                <option value="DAYS">Days</option>
                <option value="HOURS">Hours</option>
              </Select>
            )}
          </FormField>
          <FormField id="plan-accrualFrequency" label="Accrues" hint="Hours-worked plans need time registrations, which aren't available yet.">
            {(control) => (
              <Select {...control} value={values.accrualFrequency} onChange={(event) => update({ accrualFrequency: event.target.value as AccrualFrequency })}>
                <option value="MONTHLY">Monthly</option>
                <option value="BIWEEKLY">Every two weeks</option>
                <option value="ANNUAL">Once a plan year</option>
                <option value="PER_HOUR_WORKED">Per hour worked</option>
              </Select>
            )}
          </FormField>
          {text("accrualRate", "Rate per period", "In the plan's unit.", { inputMode: "decimal" })}
          <FormField id="plan-accrualDay" label="Accrual day" hint="The day a period's accrual is due.">
            {(control) => (
              <Select {...control} value={values.accrualDay} onChange={(event) => update({ accrualDay: event.target.value as AccrualDay })}>
                <option value="LAST_DAY">Last day of the period</option>
                <option value="FIRST_DAY">First day of the period</option>
                <option value="PAYDATE">Pay date</option>
              </Select>
            )}
          </FormField>
          <FormField id="plan-proration" label="Proration">
            {(control) => (
              <Select {...control} value={values.proration} onChange={(event) => update({ proration: event.target.value as Proration })}>
                <option value="DAYS_IN_PERIOD">By days employed in the period</option>
                <option value="NONE">None: full rate for any part of the period</option>
              </Select>
            )}
          </FormField>
          {text("maxBalance", "Max balance", "Accrual above it is discarded.", { inputMode: "decimal" })}
          {text("carryOverMax", "Carry-over max", "The most that moves into the next plan year.", { inputMode: "decimal" })}
          {text("carryOverExpiryMonths", "Carry-over expires after (months)", "0 keeps carry-over for the whole year.", { inputMode: "numeric" })}
          {text("planYearStart", "Plan year starts (MM-DD)", undefined, { maxLength: 5 })}
          {text("minServiceDays", "Minimum service (days)", "Workers accrue once they have served this long.", { inputMode: "numeric" })}
          <div className={styles.check}>
            <Input
              id="plan-negativeBalanceAllowed"
              type="checkbox"
              checked={values.negativeBalanceAllowed}
              onChange={(event) => update({ negativeBalanceAllowed: event.target.checked })}
            />
            <label htmlFor="plan-negativeBalanceAllowed">Allow a negative balance, down to twice the rate</label>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the plan" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create plan
            </Button>
            <Link className={buttonClassName("ghost")} to="/hr/absence/plans">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
