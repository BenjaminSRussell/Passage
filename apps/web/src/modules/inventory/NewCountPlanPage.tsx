import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createCountPlan, describeProblem, type Problem } from "./api/client";
import { DECIMAL, UUID } from "./format";
import { fieldErrorsOf } from "./parts";
import styles from "./pages.module.css";

export interface CountPlanValues {
  legalEntityId: string;
  name: string;
  warehouse: string;
  currency: string;
  unit: string;
  tolerancePct: string;
  toleranceValue: string;
  blind: boolean;
}

const fields = ["legalEntityId", "name", "warehouse", "currency", "unit", "tolerancePct", "toleranceValue"] as const;
type Errors = Partial<Record<(typeof fields)[number], string>>;

/** Client checks that mirror the service. */
export function validateCountPlan(values: CountPlanValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (values.name.trim() === "" || values.name.trim().length > 120) errors.name = "Name the plan (up to 120 characters).";
  if (!/^[A-Z]{3}$/.test(values.currency.trim())) errors.currency = "Enter a three-letter currency code, such as USD.";
  if (!/^[A-Z0-9_-]{1,12}$/.test(values.unit.trim())) errors.unit = "Enter a unit code such as EA.";
  const pct = values.tolerancePct.trim();
  if (pct !== "" && (!DECIMAL.test(pct) || Number(pct) < 0 || Number(pct) > 100)) errors.tolerancePct = "Enter a percentage from 0 to 100.";
  const value = values.toleranceValue.trim();
  if (value !== "" && (!DECIMAL.test(value) || Number(value) < 0)) errors.toleranceValue = "Enter an amount of zero or more.";
  return errors;
}

/** New count plan with its approval tolerances and blind-count option. */
export function NewCountPlanPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<CountPlanValues>({
    legalEntityId: "",
    name: "",
    warehouse: "",
    currency: "",
    unit: "EA",
    tolerancePct: "5",
    toleranceValue: "100",
    blind: false,
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(crypto.randomUUID());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateCountPlan(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await createCountPlan(
        {
          legalEntityId: values.legalEntityId.trim(),
          name: values.name.trim(),
          warehouse: values.warehouse.trim(),
          currency: values.currency.trim(),
          unit: values.unit.trim(),
          tolerancePct: values.tolerancePct.trim(),
          toleranceValue: values.toleranceValue.trim(),
          blind: values.blind,
        },
        key.current,
      );
      navigate("/inventory/count-plans");
    } catch (error) {
      const problem = describeProblem(error);
      const mapped = fieldErrorsOf(problem, fields);
      if (Object.keys(mapped).length > 0) setErrors(mapped);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = crypto.randomUUID();
    } finally {
      setBusy(false);
    }
  }

  const change = (next: CountPlanValues) => {
    setValues(next);
    key.current = crypto.randomUUID();
  };
  const set = (name: Exclude<keyof CountPlanValues, "blind">) => (event: { target: { value: string } }) =>
    change({ ...values, [name]: name === "currency" || name === "unit" ? event.target.value.toUpperCase() : event.target.value });

  return (
    <div className={styles.stack}>
      <PageHeader title="New count plan" description="Variances above either tolerance need a supervisor's approval before they post." />
      <Card>
        <form onSubmit={submit} noValidate className={styles.form}>
          <FormField id="plan-legal-entity" label="Legal entity" error={errors.legalEntityId}>
            {(control) => <Input {...control} value={values.legalEntityId} onChange={set("legalEntityId")} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="plan-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={values.name} maxLength={120} onChange={set("name")} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="plan-warehouse" label="Warehouse" optional hint="Leave empty to count every warehouse." error={errors.warehouse}>
            {(control) => <Input {...control} value={values.warehouse} maxLength={60} onChange={set("warehouse")} />}
          </FormField>
          <FormField id="plan-currency" label="Currency" hint="The legal entity's accounting currency." error={errors.currency}>
            {(control) => <Input {...control} value={values.currency} maxLength={3} onChange={set("currency")} invalid={Boolean(errors.currency)} />}
          </FormField>
          <FormField id="plan-unit" label="Unit" error={errors.unit}>
            {(control) => <Input {...control} value={values.unit} maxLength={12} onChange={set("unit")} invalid={Boolean(errors.unit)} />}
          </FormField>
          <FormField id="plan-pct" label="Quantity tolerance (%)" error={errors.tolerancePct}>
            {(control) => <Input {...control} inputMode="decimal" value={values.tolerancePct} onChange={set("tolerancePct")} invalid={Boolean(errors.tolerancePct)} />}
          </FormField>
          <FormField id="plan-value" label="Value tolerance" error={errors.toleranceValue}>
            {(control) => (
              <Input {...control} inputMode="decimal" value={values.toleranceValue} onChange={set("toleranceValue")} invalid={Boolean(errors.toleranceValue)} />
            )}
          </FormField>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input type="checkbox" id="plan-blind" checked={values.blind} onChange={(event) => change({ ...values, blind: event.target.checked })} />
              <label htmlFor="plan-blind">Blind count</label>
            </span>
            <span className={styles.muted}>Counters do not see the book quantity while the sheet is open.</span>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the count plan" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create count plan
            </Button>
            <Link className={buttonClassName("ghost")} to="/inventory/count-plans">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
