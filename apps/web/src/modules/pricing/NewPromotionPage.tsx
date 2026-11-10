import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Textarea } from "../../shell/ui";
import { createPromotion, describeProblem, newKey, type Problem, type PromotionInput } from "./api/client";
import { DECIMAL, UUID } from "./format";
import { CURRENCY } from "./fundFormat";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

/** The baseline window (FR-26.20): the 13 weeks before the promotion. */
export const BASELINE_WEEKS = 13;

export interface PromotionDraft {
  legalEntityId: string;
  name: string;
  eventCategory: string;
  eventType: string;
  customerScope: string;
  fundId: string;
  campaignId: string;
  productId: string;
  currency: string;
  startDate: string;
  endDate: string;
  /** The 13 weekly unit counts, oldest first, separated by commas, spaces or new lines. */
  baseline: string;
  liftPct: string;
  plannedSpend: string;
}

type Errors = Record<string, string>;

export const emptyPromotion: PromotionDraft = {
  legalEntityId: "",
  name: "",
  eventCategory: "",
  eventType: "",
  customerScope: "",
  fundId: "",
  campaignId: "",
  productId: "",
  currency: "USD",
  startDate: "",
  endDate: "",
  baseline: "",
  liftPct: "0",
  plannedSpend: "0",
};

const DAY = /^\d{4}-\d{2}-\d{2}$/;
const SIGNED_DECIMAL = /^-?\d+(\.\d+)?$/;

/** Splits the pasted baseline into its weekly values. */
export function baselineWeeks(text: string): string[] {
  return text.split(/[\s,;]+/).filter((w) => w !== "");
}

/** Client checks that mirror the service (FR-26.20); the service has the last word. */
export function validatePromotion(v: PromotionDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (v.fundId.trim() !== "" && !UUID.test(v.fundId.trim())) errors.fundId = "Choose a fund, or clear it.";
  if (v.campaignId.trim() !== "" && !UUID.test(v.campaignId.trim())) errors.campaignId = "Choose a campaign, or clear it.";
  if (v.productId.trim() === "" || v.productId.trim().length > 64) errors.productId = "Enter the product id (up to 64 characters).";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!DAY.test(v.startDate)) errors.startDate = "Enter the first day of the promotion.";
  if (!DAY.test(v.endDate)) errors.endDate = "Enter the last day of the promotion.";
  else if (DAY.test(v.startDate) && v.endDate < v.startDate) errors.endDate = "The last day cannot be before the first.";
  const weeks = baselineWeeks(v.baseline);
  if (weeks.length !== BASELINE_WEEKS || weeks.some((w) => !DECIMAL.test(w))) {
    errors.baselineWeeklyUnits = `Enter ${BASELINE_WEEKS} weekly unit counts, oldest first (you entered ${weeks.length}).`;
  }
  if (!SIGNED_DECIMAL.test(v.liftPct.trim()) || Number(v.liftPct) < -100) errors.liftPct = "Enter a lift of -100 or more.";
  if (!DECIMAL.test(v.plannedSpend.trim())) errors.plannedSpend = "Enter the planned spend as a number.";
  for (const field of ["eventCategory", "eventType", "customerScope"] as const) {
    if (v[field].trim().length > 64) errors[field] = "Use up to 64 characters.";
  }
  return errors;
}

export function toPromotionInput(v: PromotionDraft): PromotionInput {
  const input: PromotionInput = {
    legalEntityId: v.legalEntityId.trim(),
    name: v.name.trim(),
    eventCategory: v.eventCategory.trim(),
    eventType: v.eventType.trim(),
    customerScope: v.customerScope.trim(),
    productId: v.productId.trim(),
    currency: v.currency.trim(),
    startDate: v.startDate,
    endDate: v.endDate,
    baselineWeeklyUnits: baselineWeeks(v.baseline),
    liftPct: v.liftPct.trim(),
    plannedSpend: v.plannedSpend.trim(),
  };
  if (v.fundId.trim() !== "") input.fundId = v.fundId.trim();
  if (v.campaignId.trim() !== "") input.campaignId = v.campaignId.trim();
  return input;
}

/** What a refused plan tells the planner, by service code. */
function failureTitle(code: string): string {
  if (code === "PRC-0011") return "The fund is closed";
  if (code === "PRC-0069") return "The campaign is killed";
  if (code === "PRC-0041") return "The campaign does not exist";
  return "Could not save the promotion";
}

/** Plan a promotion (FR-26.20): merchandising event, product, window, 13-week baseline, lift and spend. */
export function NewPromotionPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  // A link from a campaign carries its id, legal entity and currency so the plan starts in it.
  const [values, setValues] = useState<PromotionDraft>(() => ({
    ...emptyPromotion,
    campaignId: params.get("campaign") ?? "",
    legalEntityId: params.get("legalEntity") ?? "",
    currency: params.get("currency") ?? emptyPromotion.currency,
  }));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<PromotionDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validatePromotion(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createPromotion(toPromotionInput(values), key.current);
      navigate(`/pricing/promotions/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function text(id: string, field: Exclude<keyof PromotionDraft, "baseline">, label: string, opts: { hint?: string; optional?: boolean; mono?: boolean; type?: string; decimal?: boolean } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} optional={opts.optional} error={errors[field]}>
        {(c) => (
          <Input
            {...c}
            type={opts.type}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<PromotionDraft>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New promotion" description="The baseline is the average of the 13 weeks before the promotion; the forecast applies the lift to every week of the window." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <LookupFormField id="pp-legal-entity" label="Legal entity" entity="legalEntity" value={values.legalEntityId} error={errors.legalEntityId} onChange={(legalEntityId) => update({ legalEntityId })} />
          {text("pp-name", "name", "Promotion name")}
          {text("pp-category", "eventCategory", "Event category", { optional: true, hint: "For example FEATURE or DISPLAY." })}
          {text("pp-type", "eventType", "Event type", { optional: true, hint: "For example TPR (temporary price reduction)." })}
          {text("pp-scope", "customerScope", "Customer or customer group", { optional: true, hint: "Leave empty for all customers.", mono: true })}
          <LookupFormField
            id="pp-fund"
            label="Fund"
            entity="fund"
            optional
            hint="An open fund in the same legal entity and currency."
            scope={{ legalEntityId: values.legalEntityId }}
            value={values.fundId}
            error={errors.fundId}
            onChange={(fundId) => update({ fundId })}
          />
          <LookupFormField
            id="pp-campaign"
            label="Campaign"
            entity="campaign"
            optional
            hint="A campaign that is not killed, in the same legal entity and currency, running over the whole window."
            scope={{ legalEntityId: values.legalEntityId }}
            value={values.campaignId}
            error={errors.campaignId}
            onChange={(campaignId) => update({ campaignId })}
          />
          {text("pp-product", "productId", "Product id", { mono: true })}
          {text("pp-currency", "currency", "Currency", { mono: true })}
          {text("pp-start", "startDate", "First day", { type: "date" })}
          {text("pp-end", "endDate", "Last day", { type: "date", hint: "Inclusive." })}
          {text("pp-lift", "liftPct", "Forecast lift %", { mono: true, decimal: true })}
          {text("pp-spend", "plannedSpend", "Planned spend", { mono: true, decimal: true })}
          <div className={styles.wide}>
            <FormField id="pp-baseline" label="Baseline weekly units" hint="13 numbers, oldest week first, separated by commas or new lines." error={errors.baselineWeeklyUnits}>
              {(c) => <Textarea {...c} rows={3} className={styles.mono} value={values.baseline} onChange={(e) => update({ baseline: e.target.value })} invalid={Boolean(errors.baselineWeeklyUnits)} />}
            </FormField>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failureTitle(failure.code)} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save promotion
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/promotions">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
