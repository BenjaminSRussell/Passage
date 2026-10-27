import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { createCampaign, describeProblem, newKey, type CampaignInput, type Problem } from "./api/client";
import { DAY, DECIMAL, UUID } from "./format";
import { CURRENCY } from "./fundFormat";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

export const emptyCampaign: CampaignInput = {
  legalEntityId: "",
  name: "",
  validFrom: "",
  validTo: "",
  targetCustomerGroup: "",
  budget: "",
  currency: "USD",
};

/** Client checks that mirror the service (FR-26.10); the service has the last word. */
export function validateCampaign(v: CampaignInput): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!DAY.test(v.validFrom)) errors.validFrom = "Enter the first day as YYYY-MM-DD.";
  if (!DAY.test(v.validTo)) errors.validTo = "Enter the last day as YYYY-MM-DD.";
  else if (DAY.test(v.validFrom) && v.validTo < v.validFrom) errors.validTo = "The last day cannot be before the first.";
  if (v.targetCustomerGroup.trim().length > 64) errors.targetCustomerGroup = "Use up to 64 characters.";
  if (!DECIMAL.test(v.budget.trim())) errors.budget = "Enter the budget as a number such as 5000.";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter a three-letter currency code such as USD.";
  return errors;
}

export function toCampaignInput(v: CampaignInput): CampaignInput {
  return {
    legalEntityId: v.legalEntityId.trim(),
    name: v.name.trim(),
    validFrom: v.validFrom,
    validTo: v.validTo,
    targetCustomerGroup: v.targetCustomerGroup.trim(),
    budget: v.budget.trim(),
    currency: v.currency.trim(),
  };
}

/** The service reports a reversed range on "validity"; show it under the last day. */
function fieldErrors(fields: Record<string, string>): Errors {
  const { validity, ...rest } = fields;
  return validity ? { ...rest, validTo: validity } : rest;
}

/** Create a campaign (FR-26.10). It starts live; trade agreements join it through their campaign field. */
export function NewCampaignPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<CampaignInput>(emptyCampaign);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CampaignInput>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateCampaign(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createCampaign(toCampaignInput(values), key.current);
      navigate(`/pricing/campaigns/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(fieldErrors(problem.fields));
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function text(id: string, field: keyof CampaignInput, label: string, opts: { hint?: string; optional?: boolean; mono?: boolean; decimal?: boolean; type?: string } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} optional={opts.optional} error={errors[field]}>
        {(c) => (
          <Input
            {...c}
            type={opts.type}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<CampaignInput>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New campaign" description="A campaign groups trade agreements under one validity, customer group, budget and kill switch." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("cp-legal-entity", "legalEntityId", "Legal entity id", { mono: true })}
          {text("cp-name", "name", "Campaign name")}
          {text("cp-from", "validFrom", "Valid from", { type: "date", mono: true })}
          {text("cp-to", "validTo", "Valid to", { type: "date", mono: true, hint: "Inclusive." })}
          {text("cp-group", "targetCustomerGroup", "Target customer group", {
            optional: true,
            mono: true,
            hint: "The campaign's agreements price only for customers in this group. Leave empty for every customer.",
          })}
          {text("cp-budget", "budget", "Budget", { mono: true, decimal: true, hint: "Planned spend. It is recorded, not enforced when pricing." })}
          {text("cp-currency", "currency", "Currency", { mono: true })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the campaign" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save campaign
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/campaigns">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
