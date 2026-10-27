import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createFund, describeProblem, newKey, type FundInput, type Problem } from "./api/client";
import { DECIMAL, UUID } from "./format";
import { CURRENCY } from "./fundFormat";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

export const emptyFund: FundInput = {
  legalEntityId: "",
  name: "",
  type: "TRADE",
  currency: "USD",
  customerScope: "",
  owner: "",
  budgetPerPeriod: "",
  allowOverspend: false,
};

/** Client checks that mirror the service (FR-26.19); the service has the last word. */
export function validateFund(v: FundInput): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (v.type.trim() === "" || v.type.trim().length > 32) errors.type = "Enter a fund type of up to 32 characters.";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter a three-letter currency code such as USD.";
  if (v.owner.trim() === "" || v.owner.trim().length > 64) errors.owner = "Enter the owner (up to 64 characters).";
  if (v.customerScope.trim().length > 64) errors.customerScope = "Use up to 64 characters.";
  if (!DECIMAL.test(v.budgetPerPeriod.trim())) errors.budgetPerPeriod = "Enter the budget per period as a number such as 25000.";
  return errors;
}

export function toFundInput(v: FundInput): FundInput {
  return {
    legalEntityId: v.legalEntityId.trim(),
    name: v.name.trim(),
    type: v.type.trim(),
    currency: v.currency.trim(),
    customerScope: v.customerScope.trim(),
    owner: v.owner.trim(),
    budgetPerPeriod: v.budgetPerPeriod.trim(),
    allowOverspend: v.allowOverspend,
  };
}

/** Create a trade promotion fund with its budget per period (FR-26.19). */
export function NewFundPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<FundInput>(emptyFund);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<FundInput>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateFund(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createFund(toFundInput(values), key.current);
      navigate(`/pricing/funds/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function text(id: string, field: Exclude<keyof FundInput, "allowOverspend">, label: string, opts: { hint?: string; optional?: boolean; mono?: boolean; decimal?: boolean } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} optional={opts.optional} error={errors[field]}>
        {(c) => (
          <Input
            {...c}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<FundInput>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New trade fund" description="The budget applies to every fiscal period on its own; usage is attributed per period." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("tf-legal-entity", "legalEntityId", "Legal entity id", { mono: true })}
          {text("tf-name", "name", "Fund name")}
          {text("tf-type", "type", "Fund type", { hint: "For example TRADE, COOP or MDF." })}
          {text("tf-currency", "currency", "Currency", { mono: true })}
          {text("tf-owner", "owner", "Owner", { hint: "The trade marketing manager responsible for the fund." })}
          {text("tf-scope", "customerScope", "Customer or customer group", { optional: true, hint: "Leave empty for all customers.", mono: true })}
          {text("tf-budget", "budgetPerPeriod", "Budget per period", { mono: true, decimal: true })}
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="tf-overspend" type="checkbox" checked={values.allowOverspend} onChange={(e) => update({ allowOverspend: e.target.checked })} />
              <label htmlFor="tf-overspend">Allow overspend</label>
            </span>
            <span className={styles.note}>Without it, usage that would take a period over budget is refused (PRC-0012).</span>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the fund" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save fund
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/funds">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
