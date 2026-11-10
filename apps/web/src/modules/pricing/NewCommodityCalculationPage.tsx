import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import {
  createCommodityCalculation,
  describeProblem,
  MARGIN_KINDS,
  newKey,
  type CommodityCalculationInput,
  type FreeGoodsCustomerKind,
  type MarginKind,
  type Problem,
} from "./api/client";
import { CUSTOMER_KIND_LABELS, MARGIN_LABELS } from "./commodityFormat";
import { DECIMAL, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

export const emptyCalculation: CommodityCalculationInput = {
  legalEntityId: "",
  name: "",
  basisId: "",
  factor: "",
  marginKind: "PERCENT",
  margin: "0",
  rounding: 6,
  maxIndexAgeDays: 7,
  customerKind: "all",
  customerKey: "",
  productId: "",
  unit: "",
  currency: "USD",
};

/** Client checks that mirror the service (FR-26.21); the service has the last word. */
export function validateCalculation(v: CommodityCalculationInput): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(v.basisId.trim())) errors.basisId = "Enter the cost basis type id (a UUID).";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Enter a name (up to 120 characters).";
  if (!DECIMAL.test(v.factor.trim()) || Number(v.factor) <= 0) errors.factor = "Enter a factor above 0.";
  if (v.margin.trim() !== "" && !DECIMAL.test(v.margin.trim())) errors.margin = "Enter the margin as a number, or 0.";
  else if (v.marginKind === "PERCENT" && Number(v.margin) > 1000) errors.margin = "A percentage is at most 1000.";
  if (!Number.isInteger(v.rounding) || v.rounding < 2 || v.rounding > 6) errors.rounding = "Enter 2 to 6 decimals.";
  if (!Number.isInteger(v.maxIndexAgeDays) || v.maxIndexAgeDays < 1 || v.maxIndexAgeDays > 365) errors.maxIndexAgeDays = "Enter 1 to 365 days.";
  if (v.customerKind === "all") {
    if (v.customerKey.trim() !== "") errors.customerKey = "Leave empty when the price is for all customers.";
  } else if (v.customerKey.trim() === "" || v.customerKey.trim().length > 64) {
    errors.customerKey = "Enter the customer or group (up to 64 characters).";
  }
  if (v.productId.trim() === "" || v.productId.trim().length > 64) errors.productId = "Enter the product (up to 64 characters).";
  if (v.unit.trim() === "" || v.unit.trim().length > 16) errors.unit = "Enter the unit (up to 16 characters).";
  if (!/^[A-Z]{3}$/.test(v.currency.trim())) errors.currency = "Enter a three-letter currency code in capitals.";
  return errors;
}

export function toCalculationInput(v: CommodityCalculationInput): CommodityCalculationInput {
  return {
    legalEntityId: v.legalEntityId.trim(),
    name: v.name.trim(),
    basisId: v.basisId.trim(),
    factor: v.factor.trim(),
    marginKind: v.marginKind,
    margin: v.margin.trim() === "" ? "0" : v.margin.trim(),
    rounding: v.rounding,
    maxIndexAgeDays: v.maxIndexAgeDays,
    customerKind: v.customerKind,
    customerKey: v.customerKind === "all" ? "" : v.customerKey.trim(),
    productId: v.productId.trim(),
    unit: v.unit.trim(),
    currency: v.currency.trim(),
  };
}

type TextField = "legalEntityId" | "basisId" | "name" | "factor" | "margin" | "customerKey" | "productId" | "unit" | "currency";

/** Create a commodity price calculation (FR-26.21). */
export function NewCommodityCalculationPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<CommodityCalculationInput>(emptyCalculation);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<CommodityCalculationInput>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateCalculation(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createCommodityCalculation(toCalculationInput(values), key.current);
      navigate(`/pricing/commodity/calculations/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function text(id: string, field: TextField, label: string, opts: { hint?: string; mono?: boolean; decimal?: boolean; optional?: boolean } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} optional={opts.optional} error={errors[field]}>
        {(c) => (
          <Input
            {...c}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<CommodityCalculationInput>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  function whole(id: string, field: "rounding" | "maxIndexAgeDays", label: string, hint: string) {
    return (
      <FormField id={id} label={label} hint={hint} error={errors[field]}>
        {(c) => (
          <Input
            {...c}
            inputMode="numeric"
            className={styles.mono}
            value={Number.isNaN(values[field]) ? "" : String(values[field])}
            onChange={(e) => update({ [field]: e.target.value === "" ? Number.NaN : Number(e.target.value) } as Partial<CommodityCalculationInput>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New price calculation" description="Price = index x factor, plus a margin. A run reprices the active prices on the key below." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("cc-legal-entity", "legalEntityId", "Legal entity id", { mono: true })}
          {text("cc-name", "name", "Name")}
          {text("cc-basis", "basisId", "Cost basis type id", { mono: true, hint: "Open a cost basis type to copy its id." })}
          {text("cc-factor", "factor", "Conversion factor", { mono: true, decimal: true, hint: "Turns the index per the cost basis unit into a price per product unit." })}
          <FormField id="cc-margin-kind" label="Margin">
            {(c) => (
              <Select {...c} value={values.marginKind} onChange={(e) => update({ marginKind: e.target.value as MarginKind })}>
                {MARGIN_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {MARGIN_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("cc-margin", "margin", values.marginKind === "PERCENT" ? "Margin percentage" : "Margin amount", { mono: true, decimal: true, optional: true })}
          {whole("cc-rounding", "rounding", "Decimals", "Index x factor and the price are rounded half to even to 2 to 6 decimals.")}
          {whole("cc-age", "maxIndexAgeDays", "Index is stale after (days)", "A run warns (PRC-0016) when the newest quote is older than this.")}
          <FormField id="cc-customer-kind" label="Customers">
            {(c) => (
              <Select {...c} value={values.customerKind} onChange={(e) => update({ customerKind: e.target.value as FreeGoodsCustomerKind })}>
                {(Object.keys(CUSTOMER_KIND_LABELS) as FreeGoodsCustomerKind[]).map((k) => (
                  <option key={k} value={k}>
                    {CUSTOMER_KIND_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.customerKind === "all" ? null : text("cc-customer-key", "customerKey", values.customerKind === "customer" ? "Customer" : "Customer group", { mono: true })}
          {text("cc-product", "productId", "Product", { mono: true })}
          {text("cc-unit", "unit", "Unit", { mono: true, hint: "Only prices in this unit, at quantity break 0 without variant or campaign, are repriced." })}
          {text("cc-currency", "currency", "Currency", { mono: true, hint: "The currency the cost basis is quoted in; prices are not converted." })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the calculation" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save calculation
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/commodity/calculations">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
