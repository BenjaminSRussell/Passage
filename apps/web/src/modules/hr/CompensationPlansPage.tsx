import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { buttonClassName, Card, PageHeader, type DataTableColumn } from "../../shell/ui";
import { SKILL_CODE } from "./api/client";
import { createCompensationPlan, listCompensationPlans, type CompensationPlan, type NewCompensationPlan, type PayBasis } from "./api/personnel";
import styles from "./pages.module.css";
import { FormActions, humanize, ListPage, SelectField, TextField, useSubmit } from "./parts";

const columns: DataTableColumn<CompensationPlan>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "payBasis", header: "Pay basis", render: (row) => humanize(row.payBasis) },
  { key: "currency", header: "Currency", mono: true },
];

/** Compensation plans: the pay basis and currency a worker's fixed pay is set under. */
export function CompensationPlansPage() {
  return (
    <ListPage<CompensationPlan>
      title="Compensation plans"
      description="Pay is set against a plan, annual or hourly, in one currency."
      label="Compensation plans"
      noun="plans"
      load={(cursor, signal) => listCompensationPlans(cursor, signal)}
      loadKey="comp-plans"
      columns={columns}
      getRowKey={(row) => row.id}
      empty={{ title: "No compensation plans", description: "Add a plan before setting anyone's pay." }}
      actions={
        <Link className={buttonClassName("primary")} to="/hr/compensation/plans/new">
          New plan
        </Link>
      }
    />
  );
}

export interface PlanValues {
  code: string;
  name: string;
  payBasis: string;
  currency: string;
}

export function validatePlan(v: PlanValues): { errors: Record<string, string>; input?: NewCompensationPlan } {
  const errors: Record<string, string> = {};
  const code = v.code.trim().toUpperCase();
  if (!SKILL_CODE.test(code)) errors.code = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  const name = v.name.trim();
  if (name === "" || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const currency = v.currency.trim().toUpperCase();
  if (!/^[A-Z]{3}$/.test(currency)) errors.currency = "Use a three-letter currency code, for example USD.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { code, name, payBasis: v.payBasis as PayBasis, currency } };
}

export function NewCompensationPlanPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<PlanValues>({ code: "", name: "", payBasis: "ANNUAL", currency: "" });
  const form = useSubmit({ "HCM-CMP-PLAN-DUPLICATE": "code" });
  const set = (patch: Partial<PlanValues>) => {
    setValues({ ...values, ...patch });
    form.touch();
  };

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors, input } = validatePlan(values);
    form.setErrors(errors);
    form.setFailure(null);
    if (!input) return;
    await form.run((key) => createCompensationPlan(input, key), () => navigate("/hr/compensation/plans"));
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New compensation plan" />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <TextField id="plan-code" label="Code" value={values.code} error={form.errors.code} onChange={(v) => set({ code: v })} />
          <TextField id="plan-name" label="Name" value={values.name} error={form.errors.name} onChange={(v) => set({ name: v })} />
          <SelectField
            id="plan-payBasis"
            label="Pay basis"
            value={values.payBasis}
            error={form.errors.payBasis}
            onChange={(v) => set({ payBasis: v })}
            options={[
              ["ANNUAL", "Annual"],
              ["HOURLY", "Hourly"],
            ]}
          />
          <TextField id="plan-currency" label="Currency" hint="For example USD." maxLength={3} value={values.currency} error={form.errors.currency} onChange={(v) => set({ currency: v })} />
          <FormActions busy={form.busy} submit="Add plan" cancelTo="/hr/compensation/plans" failure={form.failure} failureTitle="Could not add the plan" />
        </form>
      </Card>
    </div>
  );
}
