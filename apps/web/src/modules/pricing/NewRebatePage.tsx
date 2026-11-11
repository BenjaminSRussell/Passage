import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import {
  createRebateAgreement,
  describeProblem,
  newKey,
  type Problem,
  type RebateAgreementInput,
  type RebateBasis,
  type RebatePartyRole,
  type RebatePeriod,
} from "./api/client";
import { DAY, DECIMAL, UUID } from "./format";
import styles from "./pages.module.css";
import { BASIS_LABELS, PARTY_LABELS, PERIOD_LABELS, VENDOR_BASIS_LABELS } from "./rebateFormat";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

export interface TierDraft {
  from: string;
  value: string;
}

export interface RebateDraft {
  legalEntityId: string;
  name: string;
  /** CUSTOMER rebates a customer or group; VENDOR earns a rebate from a vendor (FR-26.15). */
  party: RebatePartyRole;
  scope: "customer" | "group";
  /** The customer, customer group or (vendor rebate) vendor id. */
  customer: string;
  /** Which invoice lines accrue: all, one product or one product group (FR-26.11). */
  productScope: "all" | "product" | "group";
  productKey: string;
  basis: RebateBasis;
  period: RebatePeriod;
  currency: string;
  validFrom: string;
  validTo: string;
  accrualAccount: string;
  expenseAccount: string;
  tiers: TierDraft[];
}

type Errors = Record<string, string>;

/** Default accounts per party role (§5.26.13): accrual 2150 / expense 4150, or receivable 2160 / income 5100. */
export const DEFAULT_ACCOUNTS: Record<RebatePartyRole, { accrualAccount: string; expenseAccount: string }> = {
  CUSTOMER: { accrualAccount: "2150", expenseAccount: "4150" },
  VENDOR: { accrualAccount: "2160", expenseAccount: "5100" },
};

export const emptyRebate: RebateDraft = {
  legalEntityId: "",
  name: "",
  party: "CUSTOMER",
  scope: "customer",
  customer: "",
  productScope: "all",
  productKey: "",
  basis: "NET_AMOUNT",
  period: "QUARTER",
  currency: "USD",
  validFrom: "",
  validTo: "",
  accrualAccount: "2150",
  expenseAccount: "4150",
  tiers: [{ from: "0", value: "" }],
};

const ACCOUNT = /^\d{4,8}$/;

/** Client checks that mirror the service (FR-26.11, BR-26.8); the service has the last word. */
export function validateRebate(v: RebateDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!UUID.test(v.customer.trim())) {
    errors.customer = v.party === "VENDOR" ? "Choose a vendor." : v.scope === "customer" ? "Choose a customer." : "Enter the customer group id (a UUID).";
  }
  if (v.productScope !== "all" && !UUID.test(v.productKey.trim())) {
    errors.productKey = v.productScope === "product" ? "Choose a product." : "Enter the product group id (a UUID).";
  }
  if (!/^[A-Z]{3}$/.test(v.currency)) errors.currency = "Use a three-letter ISO 4217 code, such as USD.";
  if (!DAY.test(v.validFrom)) errors.validFrom = "Enter the first valid day.";
  if (!DAY.test(v.validTo)) errors.validTo = "Enter the last valid day.";
  else if (DAY.test(v.validFrom) && v.validTo < v.validFrom) errors.validTo = "The last day must be on or after the first day.";
  if (!ACCOUNT.test(v.accrualAccount)) errors.accrualAccount = "Enter a 4 to 8 digit account.";
  if (!ACCOUNT.test(v.expenseAccount)) errors.expenseAccount = "Enter a 4 to 8 digit account.";
  v.tiers.forEach((t, i) => {
    if (!DECIMAL.test(t.from)) errors[`tiers[${i}].from`] = "Enter a threshold of 0 or more.";
    else if (i > 0 && DECIMAL.test(v.tiers[i - 1].from) && Number(t.from) <= Number(v.tiers[i - 1].from)) errors[`tiers[${i}].from`] = "Thresholds must increase from tier to tier.";
    const rate = Number(t.value);
    if (!DECIMAL.test(t.value) || rate <= 0 || (v.basis === "NET_AMOUNT" && rate > 100)) {
      errors[`tiers[${i}].value`] = v.basis === "NET_AMOUNT" ? "Enter a rate above 0 and up to 100." : "Enter an amount per unit above 0.";
    }
  });
  return errors;
}

export function toAgreementInput(v: RebateDraft): RebateAgreementInput {
  const customer = v.customer.trim();
  return {
    legalEntityId: v.legalEntityId.trim(),
    name: v.name.trim(),
    partyRole: v.party,
    ...(v.party === "VENDOR" ? { vendorId: customer } : v.scope === "customer" ? { customerId: customer } : { customerGroupId: customer }),
    ...(v.productScope === "product" ? { productId: v.productKey.trim() } : v.productScope === "group" ? { productGroupId: v.productKey.trim() } : {}),
    basis: v.basis,
    period: v.period,
    tiers: v.tiers.map((t) => (v.basis === "NET_AMOUNT" ? { from: t.from, ratePct: t.value } : { from: t.from, amountPerUnit: t.value })),
    currency: v.currency,
    validFrom: v.validFrom,
    validTo: v.validTo,
    accrualAccount: v.accrualAccount,
    expenseAccount: v.expenseAccount,
  };
}

/**
 * The draft after switching the party role: accounts still on the old role's defaults move to
 * the new role's defaults; accounts the user typed are kept.
 */
export function withParty(v: RebateDraft, party: RebatePartyRole): RebateDraft {
  const from = DEFAULT_ACCOUNTS[v.party];
  const to = DEFAULT_ACCOUNTS[party];
  return {
    ...v,
    party,
    accrualAccount: v.accrualAccount === from.accrualAccount ? to.accrualAccount : v.accrualAccount,
    expenseAccount: v.expenseAccount === from.expenseAccount ? to.expenseAccount : v.expenseAccount,
  };
}

/** Create a customer (FR-26.11) or vendor (FR-26.15) rebate agreement with its tiers. */
export function NewRebatePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<RebateDraft>(emptyRebate);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<RebateDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }
  const setTier = (i: number, patch: Partial<TierDraft>) => update({ tiers: values.tiers.map((t, j) => (j === i ? { ...t, ...patch } : t)) });

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateRebate(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createRebateAgreement(toAgreementInput(values), key.current);
      navigate(`/pricing/rebates/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fields: Errors = {};
      for (const [name, message] of Object.entries(problem.fields)) {
        if (name === "customer" || name === "customerId" || name === "customerGroupId" || name === "vendorId") fields.customer = message;
        else if (name === "productId" || name === "productGroupId" || name === "productScope") fields.productKey = message;
        else if (name === "validity") fields.validTo = message;
        else if (name === "accounts") fields.accrualAccount = message;
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

  const text = (id: string, name: keyof RebateDraft & string, label: string, extra: { hint?: string; type?: string; mono?: boolean } = {}) => (
    <FormField id={id} label={label} hint={extra.hint} error={errors[name]}>
      {(c) => (
        <Input
          {...c}
          type={extra.type ?? "text"}
          className={extra.mono ? styles.mono : undefined}
          value={String(values[name])}
          onChange={(e) => update({ [name]: e.target.value } as Partial<RebateDraft>)}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  const valueLabel = values.basis === "NET_AMOUNT" ? "Rate %" : `Amount per unit (${values.currency || "currency"})`;
  const vendor = values.party === "VENDOR";
  const basisLabels = vendor ? VENDOR_BASIS_LABELS : BASIS_LABELS;
  return (
    <div className={styles.stack}>
      <PageHeader
        title="New rebate agreement"
        description="The agreement is active from its first valid day: posted invoices of the customer, or vendor invoices of the vendor, accrue against it period by period."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <LookupFormField id="rb-legal-entity" label="Legal entity" entity="legalEntity" value={values.legalEntityId} error={errors.legalEntityId} onChange={(legalEntityId) => update({ legalEntityId })} />
          {text("rb-name", "name", "Agreement name")}
          <FormField id="rb-party" label="Type" hint={vendor ? "Earned from a vendor on its invoices and settled by period claims." : "Paid to a customer on its posted invoices."}>
            {(c) => (
              <Select
                {...c}
                value={values.party}
                onChange={(e) => {
                  setValues((prev) => ({ ...withParty(prev, e.target.value as RebatePartyRole), customer: "" }));
                  key.current = newKey();
                }}
              >
                {(Object.keys(PARTY_LABELS) as RebatePartyRole[]).map((p) => (
                  <option key={p} value={p}>
                    {PARTY_LABELS[p]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {vendor ? null : (
            <FormField id="rb-scope" label="Rebates">
              {(c) => (
                <Select {...c} value={values.scope} onChange={(e) => update({ scope: e.target.value as RebateDraft["scope"], customer: "" })}>
                  <option value="customer">One customer</option>
                  <option value="group">A customer group</option>
                </Select>
              )}
            </FormField>
          )}
          {vendor || values.scope === "customer" ? (
            <LookupFormField
              key={vendor ? "vendor" : "customer"}
              id="rb-customer"
              label={vendor ? "Vendor" : "Customer"}
              entity={vendor ? "vendor" : "customer"}
              scope={{ legalEntityId: values.legalEntityId || undefined }}
              value={values.customer}
              error={errors.customer}
              onChange={(customer) => update({ customer })}
            />
          ) : (
            text("rb-customer", "customer", "Customer group id", { mono: true })
          )}
          <FormField id="rb-product-scope" label="Products" hint="Limit the agreement to the invoice lines of one product or one product group.">
            {(c) => (
              <Select {...c} value={values.productScope} onChange={(e) => update({ productScope: e.target.value as RebateDraft["productScope"], productKey: "" })}>
                <option value="all">All products</option>
                <option value="product">One product</option>
                <option value="group">A product group</option>
              </Select>
            )}
          </FormField>
          {values.productScope === "product" ? (
            <LookupFormField id="rb-product-key" label="Product" entity="product" value={values.productKey} error={errors.productKey} onChange={(productKey) => update({ productKey })} />
          ) : values.productScope === "group" ? (
            text("rb-product-key", "productKey", "Product group id", { mono: true })
          ) : null}
          <FormField id="rb-basis" label="Basis">
            {(c) => (
              <Select {...c} value={values.basis} onChange={(e) => update({ basis: e.target.value as RebateBasis })}>
                {(Object.keys(basisLabels) as RebateBasis[]).map((b) => (
                  <option key={b} value={b}>
                    {basisLabels[b]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="rb-period" label="Accrual period">
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
          {text("rb-currency", "currency", "Currency", { mono: true, hint: "Only invoices in this currency accrue." })}
          {text("rb-valid-from", "validFrom", "Valid from", { type: "date" })}
          {text("rb-valid-to", "validTo", "Valid to", { type: "date", hint: "Inclusive." })}
          {vendor ? (
            <>
              {text("rb-accrual", "accrualAccount", "Receivable account", { mono: true, hint: "Debited on accrual (vendor rebate receivable)." })}
              {text("rb-expense", "expenseAccount", "Income account", { mono: true, hint: "Credited on accrual (purchase rebate income)." })}
            </>
          ) : (
            <>
              {text("rb-expense", "expenseAccount", "Expense account", { mono: true, hint: "Debited on accrual." })}
              {text("rb-accrual", "accrualAccount", "Accrual account", { mono: true, hint: "Credited on accrual." })}
            </>
          )}
          {values.tiers.map((t, i) => (
            <fieldset key={i} className={styles.lineSet}>
              <legend className={styles.groupsLegend}>{`Tier ${i + 1}`}</legend>
              <FormField
                id={`rb-tier-from-${i}`}
                label="From"
                hint={values.basis === "NET_AMOUNT" ? "Cumulative net amount in the period." : "Cumulative quantity in the period."}
                error={errors[`tiers[${i}].from`]}
              >
                {(c) => <Input {...c} inputMode="decimal" className={styles.mono} value={t.from} onChange={(e) => setTier(i, { from: e.target.value })} invalid={Boolean(errors[`tiers[${i}].from`])} />}
              </FormField>
              <FormField id={`rb-tier-value-${i}`} label={valueLabel} error={errors[`tiers[${i}].value`]}>
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
            <span className={styles.note}>The tier reached by the period's cumulative total applies to the whole period.</span>
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
            <Link className={buttonClassName("ghost")} to="/pricing/rebates">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
