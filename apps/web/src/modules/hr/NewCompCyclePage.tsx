import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Button, Card, PageHeader } from "../../shell/ui";
import type { CycleKind, GuidelineRow, NewCompCycle } from "./api/recruitment";
import { DATE, UUID, compareDecimal, validRate, validSigned } from "./format";
import { createCompCycle } from "./api/recruitment";
import styles from "./pages.module.css";
import { CheckField, FormActions, SelectField, TextField, labelled, useSubmit } from "./parts";

const RATIO = /^\d{1,8}(\.\d{1,4})?$/;
const MAX_ROWS = 50;
const MAX_BUDGETS = 500;

export interface CycleValues {
  name: string;
  kind: string;
  effectiveDate: string;
  currency: string;
  allowOverBudget: boolean;
  matrix: GuidelineRow[];
  budgets: Array<{ departmentId: string; budgetAmount: string }>;
}

type Errors = Record<string, string>;
const blankRow = (): GuidelineRow => ({ rating: 3, compaMin: "0.80", compaMax: "1.20", pctMin: "0", pctMax: "5" });
const MATRIX_FIELDS = [
  ["rating", "Rating"],
  ["compaMin", "Compa-ratio from"],
  ["compaMax", "Compa-ratio below"],
  ["pctMin", "Raise from (%)"],
  ["pctMax", "Raise to (%)"],
] as const;

/** The service's cycle rules (FR-42.22), and the request the form sends. Decimals stay strings. */
export function validateCycle(v: CycleValues): { errors: Errors; input?: NewCompCycle } {
  const errors: Errors = {};
  const name = v.name.trim();
  if (name === "" || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!DATE.test(v.effectiveDate)) errors.effectiveDate = "Choose the effective date.";
  const currency = v.currency.trim().toUpperCase();
  if (!/^[A-Z]{3}$/.test(currency)) errors.currency = "Use a three-letter currency code, for example USD.";

  v.matrix.forEach((row, i) => {
    const rating = Number(row.rating);
    const bad = (message: string) => {
      errors.guidelineMatrix ??= `Row ${i + 1}: ${message}`;
      errors[`matrix[${i}]`] = `Row ${i + 1}: ${message}`;
    };
    if (!Number.isInteger(rating) || rating < 1 || rating > 5) bad("the rating is 1 to 5.");
    else if (!RATIO.test(row.compaMin.trim())) bad("the compa-ratio start is not a number.");
    else if (!RATIO.test(row.compaMax.trim())) bad("the compa-ratio end is not a number.");
    else if (compareDecimal(row.compaMax, row.compaMin) <= 0) bad("the compa-ratio end must exceed its start.");
    else if (!validSigned(row.pctMin, 3) || !validSigned(row.pctMax, 3)) bad("the raise range must be numbers with up to three decimals.");
    else if (compareDecimal(row.pctMax, row.pctMin) < 0 || compareDecimal(row.pctMin, "-100") < 0 || compareDecimal(row.pctMax, "100") > 0) bad("the raise range runs from -100 to 100, low to high.");
    else if (v.matrix.slice(0, i).some((o) => Number(o.rating) === rating && compareDecimal(row.compaMin, o.compaMax) < 0 && compareDecimal(o.compaMin, row.compaMax) < 0)) bad("it overlaps an earlier row for the same rating.");
  });
  if (v.matrix.length === 0) errors.guidelineMatrix = "Add at least one guideline row.";

  const seen = new Set<string>();
  if (v.budgets.length === 0) errors.budgets = "Add at least one department budget.";
  v.budgets.forEach((b, i) => {
    const department = b.departmentId.trim().toLowerCase();
    const bad = (message: string) => {
      errors.budgets ??= `Budget ${i + 1}: ${message}`;
      errors[`budgets[${i}]`] = `Budget ${i + 1}: ${message}`;
    };
    if (!UUID.test(department)) bad("enter the department's id.");
    else if (seen.has(department)) bad("this department is already listed.");
    else if (/^[A-Z]{3}$/.test(currency) && !validRate(b.budgetAmount, currency)) bad("enter an amount within the currency's decimals.");
    seen.add(department);
  });
  if (Object.keys(errors).length > 0) return { errors };

  return {
    errors,
    input: {
      name,
      kind: v.kind as CycleKind,
      effectiveDate: v.effectiveDate,
      currency,
      allowOverBudget: v.allowOverBudget,
      guidelineMatrix: v.matrix.map((r) => ({ rating: Number(r.rating), compaMin: r.compaMin.trim(), compaMax: r.compaMax.trim(), pctMin: r.pctMin.trim(), pctMax: r.pctMax.trim() })),
      budgets: v.budgets.map((b) => ({ departmentId: b.departmentId.trim().toLowerCase(), budgetAmount: b.budgetAmount.trim() })),
    },
  };
}

export function NewCompCyclePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<CycleValues>({
    name: "",
    kind: "MERIT",
    effectiveDate: "",
    currency: "",
    allowOverBudget: false,
    matrix: [blankRow()],
    budgets: [{ departmentId: "", budgetAmount: "" }],
  });
  const form = useSubmit();
  const set = (patch: Partial<CycleValues>) => {
    setValues({ ...values, ...patch });
    form.touch();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validateCycle(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => createCompCycle(input, key), (cycle) => navigate(`/hr/compensation/cycles/${cycle.id}`));
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New compensation cycle" description="Managers propose raises inside the guideline matrix; each department has a budget." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <TextField id="cycle-name" label="Name" value={values.name} error={form.errors.name} onChange={(v) => set({ name: v })} />
          <SelectField id="cycle-kind" label="Kind" value={values.kind} error={form.errors.kind} onChange={(v) => set({ kind: v })} options={labelled(["MERIT", "BONUS", "BOTH"])} />
          <TextField id="cycle-effectiveDate" label="Effective date" type="date" value={values.effectiveDate} error={form.errors.effectiveDate} onChange={(v) => set({ effectiveDate: v })} />
          <TextField id="cycle-currency" label="Currency" maxLength={3} value={values.currency} error={form.errors.currency} onChange={(v) => set({ currency: v })} />
          <CheckField id="cycle-allowOverBudget" label="Allow proposals above a department's budget" checked={values.allowOverBudget} onChange={(allowOverBudget) => set({ allowOverBudget })} />
          <fieldset className={styles.fieldset}>
            <legend className={styles.legend}>Guideline matrix</legend>
            <p className={styles.note}>For a performance rating and a compa-ratio band, the raise a manager may propose.</p>
            {values.matrix.map((row, i) => (
              <div key={i} className={styles.optionRow} role="group" aria-label={`Guideline row ${i + 1}`}>
                {MATRIX_FIELDS.map(([field, label]) => (
                  <TextField
                    key={field}
                    id={`cycle-matrix-${i}-${field}`}
                    label={label}
                    value={String(row[field])}
                    error={field === "rating" ? form.errors[`matrix[${i}]`] : undefined}
                    onChange={(v) => set({ matrix: values.matrix.map((r, j) => (j === i ? { ...r, [field]: field === "rating" ? Number(v) || 0 : v } : r)) })}
                  />
                ))}
                {values.matrix.length > 1 ? (
                  <div className={styles.optionRemove}>
                    <Button variant="ghost" size="sm" aria-label={`Remove guideline row ${i + 1}`} onClick={() => set({ matrix: values.matrix.filter((_, j) => j !== i) })}>
                      Remove
                    </Button>
                  </div>
                ) : null}
              </div>
            ))}
            {form.errors.guidelineMatrix && !values.matrix.some((_, i) => form.errors[`matrix[${i}]`]) ? <p role="alert">{form.errors.guidelineMatrix}</p> : null}
            {values.matrix.length < MAX_ROWS ? (
              <div>
                <Button size="sm" onClick={() => set({ matrix: [...values.matrix, blankRow()] })}>
                  Add guideline row
                </Button>
              </div>
            ) : null}
          </fieldset>
          <fieldset className={styles.fieldset}>
            <legend className={styles.legend}>Department budgets</legend>
            {values.budgets.map((b, i) => (
              <div key={i} className={styles.optionRow} role="group" aria-label={`Budget ${i + 1}`}>
                <TextField
                  id={`cycle-budget-${i}-department`}
                  label="Department id"
                  error={form.errors[`budgets[${i}]`]}
                  value={b.departmentId}
                  onChange={(v) => set({ budgets: values.budgets.map((x, j) => (j === i ? { ...x, departmentId: v } : x)) })}
                />
                <TextField
                  id={`cycle-budget-${i}-amount`}
                  label="Budget"
                  value={b.budgetAmount}
                  onChange={(v) => set({ budgets: values.budgets.map((x, j) => (j === i ? { ...x, budgetAmount: v } : x)) })}
                />
                {values.budgets.length > 1 ? (
                  <div className={styles.optionRemove}>
                    <Button variant="ghost" size="sm" aria-label={`Remove budget ${i + 1}`} onClick={() => set({ budgets: values.budgets.filter((_, j) => j !== i) })}>
                      Remove
                    </Button>
                  </div>
                ) : null}
              </div>
            ))}
            {form.errors.budgets && !values.budgets.some((_, i) => form.errors[`budgets[${i}]`]) ? <p role="alert">{form.errors.budgets}</p> : null}
            {values.budgets.length < MAX_BUDGETS ? (
              <div>
                <Button size="sm" onClick={() => set({ budgets: [...values.budgets, { departmentId: "", budgetAmount: "" }] })}>
                  Add budget
                </Button>
              </div>
            ) : null}
          </fieldset>
          <FormActions errors={form.errors} busy={form.busy} submit="Create cycle" cancelTo="/hr/compensation/cycles" failure={form.failure} failureTitle="Could not create the cycle" />
        </form>
      </Card>
    </div>
  );
}
