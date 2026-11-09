import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createRoyaltyAgreement, describeProblem, newKey, type Problem, type RebatePeriod, type RoyaltyAgreementInput, type RoyaltyBasis } from "./api/client";
import { DEFAULT_CONTRACT_ACCOUNTS, ROYALTY_BASIS_LABELS } from "./contractFormat";
import { DAY, DECIMAL, UUID } from "./format";
import styles from "./pages.module.css";
import { PERIOD_LABELS } from "./rebateFormat";
import { ProblemMessage } from "../../shell/errors";

export interface RoyaltyTierDraft {
  from: string;
  value: string;
}

export interface RoyaltyDraft {
  legalEntityId: string;
  name: string;
  licensorVendorId: string;
  productScope: "product" | "group";
  productKey: string;
  basis: RoyaltyBasis;
  period: RebatePeriod;
  minimumRoyalty: string;
  currency: string;
  validFrom: string;
  validTo: string;
  payableAccount: string;
  expenseAccount: string;
  tiers: RoyaltyTierDraft[];
}

type Errors = Record<string, string>;

export const emptyRoyalty: RoyaltyDraft = {
  legalEntityId: "",
  name: "",
  licensorVendorId: "",
  productScope: "group",
  productKey: "",
  basis: "NET_SALES",
  period: "QUARTER",
  minimumRoyalty: "0",
  currency: "USD",
  validFrom: "",
  validTo: "",
  payableAccount: DEFAULT_CONTRACT_ACCOUNTS.ROYALTY.payableAccount,
  expenseAccount: DEFAULT_CONTRACT_ACCOUNTS.ROYALTY.expenseAccount,
  tiers: [{ from: "0", value: "" }],
};

const ACCOUNT = /^\d{4,8}$/;

/** Client checks that mirror the service (FR-26.17, BR-26.8); the service has the last word. */
export function validateRoyalty(v: RoyaltyDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!UUID.test(v.licensorVendorId.trim())) errors.licensorVendorId = "Enter the licensor's vendor id (a UUID).";
  if (v.productKey.trim() === "" || v.productKey.trim().length > 64) errors.productKey = "Enter the product or group (up to 64 characters).";
  if (!DECIMAL.test(v.minimumRoyalty.trim())) errors.minimumRoyalty = "Enter the minimum as a number, or 0.";
  if (!/^[A-Z]{3}$/.test(v.currency)) errors.currency = "Use a three-letter ISO 4217 code, such as USD.";
  if (!DAY.test(v.validFrom)) errors.validFrom = "Enter the first valid day.";
  if (!DAY.test(v.validTo)) errors.validTo = "Enter the last valid day.";
  else if (DAY.test(v.validFrom) && v.validTo < v.validFrom) errors.validTo = "The last day must be on or after the first day.";
  if (!ACCOUNT.test(v.payableAccount)) errors.payableAccount = "Enter a 4 to 8 digit account.";
  if (!ACCOUNT.test(v.expenseAccount)) errors.expenseAccount = "Enter a 4 to 8 digit account.";
  v.tiers.forEach((t, i) => {
    if (!DECIMAL.test(t.from)) errors[`tiers[${i}].from`] = "Enter a threshold of 0 or more.";
    else if (i > 0 && DECIMAL.test(v.tiers[i - 1].from) && Number(t.from) <= Number(v.tiers[i - 1].from)) errors[`tiers[${i}].from`] = "Thresholds must increase from tier to tier.";
    const rate = Number(t.value);
    if (!DECIMAL.test(t.value) || rate <= 0 || (v.basis === "NET_SALES" && rate > 100)) {
      errors[`tiers[${i}].value`] = v.basis === "NET_SALES" ? "Enter a rate above 0 and up to 100." : "Enter an amount per unit above 0.";
    }
  });
  return errors;
}

export function toRoyaltyInput(v: RoyaltyDraft): RoyaltyAgreementInput {
  const product = v.productKey.trim();
  return {
    legalEntityId: v.legalEntityId.trim(),
    name: v.name.trim(),
    licensorVendorId: v.licensorVendorId.trim(),
    ...(v.productScope === "product" ? { productId: product } : { productGroupId: product }),
    basis: v.basis,
    period: v.period,
    tiers: v.tiers.map((t) => (v.basis === "NET_SALES" ? { from: t.from, ratePct: t.value } : { from: t.from, amountPerUnit: t.value })),
    minimumRoyalty: v.minimumRoyalty.trim(),
    currency: v.currency,
    validFrom: v.validFrom,
    validTo: v.validTo,
    payableAccount: v.payableAccount,
    expenseAccount: v.expenseAccount,
  };
}

/** Create a royalty agreement with tiers and a minimum royalty per period (FR-26.17). */
export function NewRoyaltyPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<RoyaltyDraft>(emptyRoyalty);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<RoyaltyDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }
  const setTier = (i: number, patch: Partial<RoyaltyTierDraft>) => update({ tiers: values.tiers.map((t, j) => (j === i ? { ...t, ...patch } : t)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateRoyalty(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createRoyaltyAgreement(toRoyaltyInput(values), key.current);
      navigate(`/pricing/royalties/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fields: Errors = {};
      for (const [name, message] of Object.entries(problem.fields)) {
        if (name === "product") fields.productKey = message;
        else if (name === "validity") fields.validTo = message;
        else if (name === "accounts") fields.payableAccount = message;
        else if (name === "tiers") fields["tiers[0].value"] = message;
        else fields[name] = message;
      }
      if (Object.keys(fields).length > 0) setErrors(fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: string, name: keyof RoyaltyDraft & string, label: string, extra: { hint?: string; type?: string; mono?: boolean; decimal?: boolean } = {}) => (
    <FormField id={id} label={label} hint={extra.hint} error={errors[name]}>
      {(c) => (
        <Input
          {...c}
          type={extra.type ?? "text"}
          inputMode={extra.decimal ? "decimal" : undefined}
          className={extra.mono ? styles.mono : undefined}
          value={String(values[name])}
          onChange={(e) => update({ [name]: e.target.value } as Partial<RoyaltyDraft>)}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  const valueLabel = values.basis === "NET_SALES" ? "Rate %" : `Amount per unit (${values.currency || "currency"})`;
  return (
    <div className={styles.stack}>
      <PageHeader title="New royalty agreement" description="The agreement is active from creation: posted invoice lines for the product accrue royalty period by period." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("ry-legal-entity", "legalEntityId", "Legal entity id", { mono: true })}
          {text("ry-name", "name", "Agreement name")}
          {text("ry-licensor", "licensorVendorId", "Licensor vendor id", { mono: true })}
          <FormField id="ry-product-scope" label="Covers">
            {(c) => (
              <Select {...c} value={values.productScope} onChange={(e) => update({ productScope: e.target.value as RoyaltyDraft["productScope"] })}>
                <option value="group">A product group</option>
                <option value="product">One product</option>
              </Select>
            )}
          </FormField>
          {text("ry-product", "productKey", values.productScope === "product" ? "Product" : "Product group", { mono: true })}
          <FormField id="ry-basis" label="Basis">
            {(c) => (
              <Select {...c} value={values.basis} onChange={(e) => update({ basis: e.target.value as RoyaltyBasis })}>
                {(Object.keys(ROYALTY_BASIS_LABELS) as RoyaltyBasis[]).map((b) => (
                  <option key={b} value={b}>
                    {ROYALTY_BASIS_LABELS[b]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="ry-period" label="Royalty period">
            {(c) => (
              <Select {...c} value={values.period} onChange={(e) => update({ period: e.target.value as RebatePeriod })}>
                {(Object.keys(PERIOD_LABELS) as RebatePeriod[]).map((p) => (
                  <option key={p} value={p}>
                    {PERIOD_LABELS[p]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("ry-minimum", "minimumRoyalty", "Minimum royalty per period", { mono: true, decimal: true, hint: "A period closed below this is topped up to it. 0 for none." })}
          {text("ry-currency", "currency", "Currency", { mono: true, hint: "Only invoices in this currency accrue." })}
          {text("ry-valid-from", "validFrom", "Valid from", { type: "date" })}
          {text("ry-valid-to", "validTo", "Valid to", { type: "date", hint: "Inclusive." })}
          {text("ry-expense", "expenseAccount", "Royalty expense account", { mono: true, hint: "Debited on accrual." })}
          {text("ry-payable", "payableAccount", "Royalty payable account", { mono: true, hint: "Credited on accrual." })}
          {values.tiers.map((t, i) => (
            <fieldset key={i} className={styles.lineSet}>
              <legend className={styles.groupsLegend}>{`Tier ${i + 1}`}</legend>
              <FormField
                id={`ry-tier-from-${i}`}
                label="From"
                hint={values.basis === "NET_SALES" ? "Cumulative net sales in the period." : "Cumulative quantity in the period."}
                error={errors[`tiers[${i}].from`]}
              >
                {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={t.from} onChange={(e) => setTier(i, { from: e.target.value })} invalid={Boolean(errors[`tiers[${i}].from`])} />}
              </FormField>
              <FormField id={`ry-tier-value-${i}`} label={valueLabel} error={errors[`tiers[${i}].value`]}>
                {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={t.value} onChange={(e) => setTier(i, { value: e.target.value })} invalid={Boolean(errors[`tiers[${i}].value`])} />}
              </FormField>
              {values.tiers.length > 1 ? (
                <Button variant="ghost" size="sm" onClick={() => update({ tiers: values.tiers.filter((_, j) => j !== i) })}>
                  {`Remove tier ${i + 1}`}
                </Button>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            <Button variant="secondary" onClick={() => update({ tiers: [...values.tiers, { from: "", value: "" }] })}>
              Add tier
            </Button>
            <span className={styles.note}>The tier reached by the period&apos;s cumulative total applies to the whole period.</span>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the agreement" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save agreement
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/royalties">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
