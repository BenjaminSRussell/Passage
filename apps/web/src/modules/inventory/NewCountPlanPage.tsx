import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
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
  freezeStock: boolean;
  fourEyes: boolean;
  locationFrom: string;
  locationTo: string;
  abcClass: "" | "A" | "B" | "C";
  samplePct: string;
  minDaysSinceCount: string;
  includeVendorOwned?: boolean;
}

const fields = [
  "legalEntityId",
  "name",
  "warehouse",
  "currency",
  "unit",
  "tolerancePct",
  "toleranceValue",
  "locationFrom",
  "locationTo",
  "abcClass",
  "samplePct",
  "minDaysSinceCount",
] as const;
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
  const from = values.locationFrom.trim();
  const to = values.locationTo.trim();
  if (from.length > 60) errors.locationFrom = "Up to 60 characters.";
  if (to.length > 60) errors.locationTo = "Up to 60 characters.";
  if (from !== "" && to !== "" && from > to) errors.locationTo = "The range end must not be before its start.";
  const sample = values.samplePct.trim();
  if (sample !== "" && (!DECIMAL.test(sample) || Number(sample) < 0 || Number(sample) > 100)) errors.samplePct = "Enter a percentage from 0 to 100.";
  const age = values.minDaysSinceCount.trim();
  if (age !== "" && (!/^[0-9]{1,4}$/.test(age) || Number(age) < 1 || Number(age) > 3650)) errors.minDaysSinceCount = "Enter a whole number of days from 1 to 3650.";
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
    freezeStock: false,
    fourEyes: false,
    locationFrom: "",
    locationTo: "",
    abcClass: "",
    samplePct: "",
    minDaysSinceCount: "",
    includeVendorOwned: false,
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
    const age = values.minDaysSinceCount.trim();
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
          freezeStock: values.freezeStock,
          fourEyes: values.fourEyes,
          locationFrom: values.locationFrom.trim(),
          locationTo: values.locationTo.trim(),
          abcClass: values.abcClass,
          samplePct: values.samplePct.trim(),
          minDaysSinceCount: age === "" ? undefined : Number(age),
          includeVendorOwned: values.includeVendorOwned ? true : undefined,
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
  const set = (name: Exclude<keyof CountPlanValues, "blind" | "freezeStock" | "fourEyes">) => (event: { target: { value: string } }) =>
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
          <FormField id="plan-loc-from" label="Locations from" optional hint="Counts locations from this value on, in alphabetical order." error={errors.locationFrom}>
            {(control) => <Input {...control} value={values.locationFrom} maxLength={60} onChange={set("locationFrom")} invalid={Boolean(errors.locationFrom)} />}
          </FormField>
          <FormField id="plan-loc-to" label="Locations to" optional error={errors.locationTo}>
            {(control) => <Input {...control} value={values.locationTo} maxLength={60} onChange={set("locationTo")} invalid={Boolean(errors.locationTo)} />}
          </FormField>
          <FormField id="plan-abc" label="ABC class" optional hint="Only products of this class in the latest ABC run." error={errors.abcClass}>
            {(control) => (
              <Select {...control} value={values.abcClass} onChange={(event) => change({ ...values, abcClass: event.target.value as CountPlanValues["abcClass"] })}>
                <option value="">All classes</option>
                <option value="A">A</option>
                <option value="B">B</option>
                <option value="C">C</option>
              </Select>
            )}
          </FormField>
          <FormField id="plan-sample" label="Sample (%)" optional hint="Count only this share of the selected rows." error={errors.samplePct}>
            {(control) => <Input {...control} inputMode="decimal" value={values.samplePct} onChange={set("samplePct")} invalid={Boolean(errors.samplePct)} />}
          </FormField>
          <FormField id="plan-age" label="Not counted for (days)" optional hint="Count only rows whose last posted count is at least this many days old, or that were never counted." error={errors.minDaysSinceCount}>
            {(control) => <Input {...control} inputMode="numeric" value={values.minDaysSinceCount} maxLength={4} onChange={set("minDaysSinceCount")} invalid={Boolean(errors.minDaysSinceCount)} />}
          </FormField>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input type="checkbox" id="plan-freeze" checked={values.freezeStock} onChange={(event) => change({ ...values, freezeStock: event.target.checked })} />
              <label htmlFor="plan-freeze">Freeze stock while counting</label>
            </span>
            <span className={styles.muted}>Journals, transfers, blockings and receipts or issues that move a counted row are refused until the sheet is posted or cancelled. Without it, a movement after a line was counted sends that line back for recount.</span>
          </div>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input type="checkbox" id="plan-four-eyes" checked={values.fourEyes} onChange={(event) => change({ ...values, fourEyes: event.target.checked })} />
              <label htmlFor="plan-four-eyes">Separate approval</label>
            </span>
            <span className={styles.muted}>A supervisor who did not count must approve the sheet before it posts.</span>
          </div>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input type="checkbox" id="plan-vendor-owned" checked={values.includeVendorOwned} onChange={(event) => change({ ...values, includeVendorOwned: event.target.checked })} />
              <label htmlFor="plan-vendor-owned">Include vendor-owned stock</label>
            </span>
            <span className={styles.muted}>Consignment stock is counted too. A variance changes its quantity only: no value and no ledger entry.</span>
          </div>
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
