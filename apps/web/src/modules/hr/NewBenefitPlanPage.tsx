import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Textarea } from "../../shell/ui";
import {
  createBenefitPlan,
  describeProblem,
  newKey,
  type BenefitPlanType,
  type NewBenefitPlan,
  type Problem,
  type RateBasis,
} from "./api/client";
import { currencyPlaces, DATE, planTypeLabel, UUID, validRate } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

interface OptionValues {
  code: string;
  coverageLevel: string;
  employeeRate: string;
  employerRate: string;
  rateBasis: RateBasis;
}

export interface BenefitPlanValues {
  code: string;
  planType: BenefitPlanType;
  currency: string;
  eligibilityRule: string;
  enrollmentStart: string;
  enrollmentEnd: string;
  waitingDays: string;
  preTax: boolean;
  payrollDeductionCode: string;
  providerPartyId: string;
  options: OptionValues[];
}

/** Errors keyed like the service's (`options[0].employeeRate`), so either side lands on the same field. */
type Errors = Record<string, string>;

const CODE = /^[A-Z0-9][A-Z0-9_-]{0,19}$/;
const CURRENCY = /^[A-Z]{3}$/;
const WHOLE = /^\d{1,3}$/;
const MAX_OPTIONS = 20;
const PLAN_FIELDS = ["code", "planType", "currency", "eligibilityRule", "enrollmentStart", "enrollmentEnd", "waitingDays", "payrollDeductionCode", "providerPartyId", "options"];

const blankOption = (): OptionValues => ({ code: "", coverageLevel: "", employeeRate: "", employerRate: "", rateBasis: "PER_PAY_PERIOD" });

/** The service's plan rules (FR-42.8) that need no database, and the request the form sends. Rates stay strings. */
export function validateBenefitPlan(values: BenefitPlanValues): { errors: Errors; input?: NewBenefitPlan } {
  const errors: Errors = {};
  const code = values.code.trim().toUpperCase();
  if (!CODE.test(code)) errors.code = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
  const currency = values.currency.trim().toUpperCase();
  if (!CURRENCY.test(currency)) errors.currency = "Enter a three-letter currency code, for example USD.";
  const rule = values.eligibilityRule.trim();
  if (!rule) errors.eligibilityRule = "Enter who is eligible, for example employment_type in ['FULL_TIME'] && service_days >= 30.";
  else if (rule.length > 2000) errors.eligibilityRule = "Keep the rule to 2000 characters.";
  if (!DATE.test(values.enrollmentStart)) errors.enrollmentStart = "Enter the first day of open enrollment.";
  if (!DATE.test(values.enrollmentEnd)) errors.enrollmentEnd = "Enter the last day of open enrollment.";
  else if (DATE.test(values.enrollmentStart) && values.enrollmentEnd < values.enrollmentStart) errors.enrollmentEnd = "The last day can't be before the first.";
  const waiting = values.waitingDays.trim();
  if (!WHOLE.test(waiting) || Number(waiting) > 365) errors.waitingDays = "Enter 0 to 365 days.";
  const deduction = values.payrollDeductionCode.trim();
  if (deduction.length > 20) errors.payrollDeductionCode = "Keep the deduction code to 20 characters.";
  const provider = values.providerPartyId.trim();
  if (provider && !UUID.test(provider)) errors.providerPartyId = "Choose the provider, or leave it empty.";
  if (values.options.length === 0) errors.options = "Add at least one coverage option.";

  const places = CURRENCY.test(currency) ? currencyPlaces(currency) : 2;
  const seen = new Set<string>();
  const options = values.options.map((o, i) => {
    const key = `options[${i}]`;
    const optionCode = o.code.trim().toUpperCase();
    if (!CODE.test(optionCode)) errors[`${key}.code`] = "Use 1 to 20 characters: A to Z, 0 to 9, a hyphen or an underscore.";
    else if (seen.has(optionCode)) errors[`${key}.code`] = "Each option needs its own code.";
    seen.add(optionCode);
    const level = o.coverageLevel.trim();
    if (!level || level.length > 40) errors[`${key}.coverageLevel`] = "Describe the coverage in up to 40 characters, for example Employee and spouse.";
    const rateHint = places === 0 ? "Enter a whole amount, for example 1500." : `Enter an amount with at most ${places} decimals, for example 12.50.`;
    if (!validRate(o.employeeRate, currency)) errors[`${key}.employeeRate`] = rateHint;
    if (!validRate(o.employerRate, currency)) errors[`${key}.employerRate`] = rateHint;
    return { code: optionCode, coverageLevel: level, employeeRate: o.employeeRate.trim(), employerRate: o.employerRate.trim(), rateBasis: o.rateBasis };
  });
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewBenefitPlan = {
    code,
    planType: values.planType,
    currency,
    eligibilityRule: rule,
    enrollmentStart: values.enrollmentStart,
    enrollmentEnd: values.enrollmentEnd,
    waitingDays: Number(waiting),
    preTax: values.preTax,
    options,
  };
  if (deduction) input.payrollDeductionCode = deduction;
  if (provider) input.providerPartyId = provider;
  return { errors, input };
}

/** Create a benefit plan with its coverage options, eligibility rule and open enrollment window. */
export function NewBenefitPlanPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<BenefitPlanValues>({
    code: "",
    planType: "MEDICAL",
    currency: "USD",
    eligibilityRule: "employment_type in ['FULL_TIME'] && service_days >= 30",
    enrollmentStart: "",
    enrollmentEnd: "",
    waitingDays: "0",
    preTax: true,
    payrollDeductionCode: "",
    providerPartyId: "",
    options: [blankOption()],
  });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<BenefitPlanValues>) {
    setValues({ ...values, ...patch });
    key.current = newKey();
  }

  function updateOption(index: number, patch: Partial<OptionValues>) {
    update({ options: values.options.map((o, i) => (i === index ? { ...o, ...patch } : o)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateBenefitPlan(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const plan = await createBenefitPlan(input, key.current);
      navigate(`/hr/benefits/plans/${plan.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const [name, message] of Object.entries(problem.fields)) {
        if (PLAN_FIELDS.includes(name) || name.startsWith("options[")) fieldErrors[name] = message;
      }
      if (problem.code === "HCM-BEN-PLAN-DUPLICATE") fieldErrors.code = "This plan code is already used.";
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  type TextField = "code" | "currency" | "enrollmentStart" | "enrollmentEnd" | "waitingDays" | "payrollDeductionCode" | "providerPartyId";
  const text = (name: TextField, label: string, hint?: string, extra?: Record<string, unknown>, optional?: boolean) => (
    <FormField id={`benefit-${name}`} label={label} hint={hint} error={errors[name]} optional={optional}>
      {(control) => (
        <Input {...control} {...extra} value={values[name]} invalid={Boolean(errors[name])} onChange={(event) => update({ [name]: event.target.value } as Partial<BenefitPlanValues>)} />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New benefit plan"
        description="Workers can enroll inside the open enrollment window, or within 30 days of a life event, once the waiting period has passed and the rule is true for them."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New benefit plan">
          {text("code", "Plan code", "Unique in your tenant, for example MED-PPO.", { maxLength: 20 })}
          <FormField id="benefit-planType" label="Type">
            {(control) => (
              <Select {...control} value={values.planType} onChange={(event) => update({ planType: event.target.value as BenefitPlanType })}>
                {Object.entries(planTypeLabel).map(([value, label]) => (
                  <option key={value} value={value}>
                    {label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("currency", "Currency", "The currency of every rate in the plan.", { maxLength: 3 })}
          {text("enrollmentStart", "Open enrollment from", undefined, { type: "date" })}
          {text("enrollmentEnd", "Open enrollment to", "Inclusive.", { type: "date" })}
          {text("waitingDays", "Waiting days", "Days after the hire date before a worker is eligible.", { inputMode: "numeric" })}
          {text("payrollDeductionCode", "Payroll deduction code", "Payroll uses it to take the employee rate.", { maxLength: 20 }, true)}
          <LookupFormField id="benefit-providerPartyId" label="Provider" entity="party" optional hint="The insurer or administrator." value={values.providerPartyId} error={errors.providerPartyId} onChange={(providerPartyId) => update({ providerPartyId })} />
          <div className={styles.check}>
            <Input id="benefit-preTax" type="checkbox" checked={values.preTax} onChange={(event) => update({ preTax: event.target.checked })} />
            <label htmlFor="benefit-preTax">Deducted before tax</label>
          </div>
          <div className={styles.wide}>
            <FormField
              id="benefit-eligibilityRule"
              label="Eligibility rule"
              hint="Facts: employment_type, fte, service_days, legal_entity. Operators: && || ! == != < <= > >= in [..]. Test it with the simulation on the plan page."
              error={errors.eligibilityRule}
            >
              {(control) => (
                <Textarea
                  {...control}
                  rows={3}
                  maxLength={2000}
                  mono
                  value={values.eligibilityRule}
                  invalid={Boolean(errors.eligibilityRule)}
                  onChange={(event) => update({ eligibilityRule: event.target.value })}
                />
              )}
            </FormField>
          </div>
          <fieldset className={styles.fieldset}>
            <legend className={styles.legend}>Coverage options</legend>
            {errors.options ? (
              <Alert tone="danger" live="alert">
                {errors.options}
              </Alert>
            ) : null}
            {values.options.map((o, i) => {
              const field = (name: keyof OptionValues) => `options[${i}].${name}`;
              const optionText = (name: Exclude<keyof OptionValues, "rateBasis">, label: string, extra?: Record<string, unknown>) => (
                <FormField id={`benefit-option-${i}-${name}`} label={label} error={errors[field(name)]}>
                  {(control) => (
                    <Input
                      {...control}
                      {...extra}
                      value={o[name]}
                      invalid={Boolean(errors[field(name)])}
                      onChange={(event) => updateOption(i, { [name]: event.target.value } as Partial<OptionValues>)}
                    />
                  )}
                </FormField>
              );
              return (
                <div key={i} className={styles.optionRow} role="group" aria-label={`Option ${i + 1}`}>
                  {optionText("code", "Option code", { maxLength: 20 })}
                  {optionText("coverageLevel", "Coverage", { maxLength: 40 })}
                  {optionText("employeeRate", `Employee rate (${values.currency || "currency"})`, { inputMode: "decimal" })}
                  {optionText("employerRate", `Employer rate (${values.currency || "currency"})`, { inputMode: "decimal" })}
                  <FormField id={`benefit-option-${i}-rateBasis`} label="Rate is">
                    {(control) => (
                      <Select {...control} value={o.rateBasis} onChange={(event) => updateOption(i, { rateBasis: event.target.value as RateBasis })}>
                        <option value="PER_PAY_PERIOD">Per pay period</option>
                        <option value="MONTHLY">Monthly</option>
                      </Select>
                    )}
                  </FormField>
                  {values.options.length > 1 ? (
                    <div className={styles.optionRemove}>
                      <Button variant="ghost" size="sm" onClick={() => update({ options: values.options.filter((_, j) => j !== i) })} aria-label={`Remove option ${i + 1}`}>
                        Remove
                      </Button>
                    </div>
                  ) : null}
                </div>
              );
            })}
            {values.options.length < MAX_OPTIONS ? (
              <div>
                <Button size="sm" onClick={() => update({ options: [...values.options, blankOption()] })}>
                  Add option
                </Button>
              </div>
            ) : null}
          </fieldset>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the plan" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create plan
            </Button>
            <Link className={buttonClassName("ghost")} to="/hr/benefits/plans">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
