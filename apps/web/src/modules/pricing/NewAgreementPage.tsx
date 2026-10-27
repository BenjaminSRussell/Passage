import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import {
  CONDITION_TYPES,
  CUSTOMER_KINDS,
  PRODUCT_KINDS,
  createConditionRecord,
  describeProblem,
  getConditionRecord,
  newKey,
  type ConditionRecord,
  type ConditionRecordInput,
  type Problem,
} from "./api/client";
import { amountLabel, DAY, DECIMAL, isOrderLevel, TYPE_LABELS, UUID } from "./format";
import styles from "./pages.module.css";

type Values = ConditionRecordInput;
type Field = keyof Values | "validity";
type Errors = Partial<Record<Field, string>>;

const CUSTOMER_LABELS = { customer: "One customer", customer_group: "Customer group", all: "All customers" } as const;
const PRODUCT_LABELS = { product: "One product", product_group: "Product group", variant: "Product variant", all: "All products" } as const;

export const emptyAgreement: Values = {
  legalEntityId: "",
  type: "PRICE",
  customerKind: "customer",
  customerKey: "",
  productKind: "product",
  productKey: "",
  variant: "",
  currency: "USD",
  unit: "EA",
  fromQuantity: "0",
  amount: "",
  discPct1: "",
  discPct2: "",
  validFrom: "",
  validTo: "",
  searchNext: false,
  exclusive: false,
};

/** Client checks that mirror the service (FR-26.2, BR-26.10); the service has the last word. */
export function validateAgreement(v: Values): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (v.customerKind !== "all" && v.customerKey.trim() === "") errors.customerKey = "Enter the customer or customer group.";
  if (isOrderLevel(v.type) && v.productKind !== "all") errors.productKind = `A ${TYPE_LABELS[v.type].toLowerCase()} applies to the whole order: choose All products.`;
  else if (v.type === "MULTILINE_DISC" && v.productKind !== "product_group" && v.productKind !== "all")
    errors.productKind = "A multiline discount sums the lines of a product group: choose Product group or All products.";
  else if (v.productKind !== "all" && v.productKey.trim() === "") errors.productKey = "Enter the product or product group.";
  if (v.productKind === "variant" && v.variant.trim() === "") errors.variant = "Enter the variant.";
  if (!/^[A-Z]{3}$/.test(v.currency)) errors.currency = "Use a three-letter ISO 4217 code, such as USD.";
  if (!DECIMAL.test(v.fromQuantity || "0")) errors.fromQuantity = "Enter a quantity of 0 or more.";
  if (v.amount !== "" && !DECIMAL.test(v.amount)) errors.amount = "Enter an amount of 0 or more; negative prices are not allowed.";
  if (v.type === "PRICE" && v.amount === "") errors.amount = "Enter the price.";
  for (const name of ["discPct1", "discPct2"] as const) {
    if (v[name] !== "" && (!DECIMAL.test(v[name]) || Number(v[name]) > 100)) errors[name] = "Enter a percentage from 0 to 100.";
  }
  if (v.type !== "PRICE" && v.amount === "" && v.discPct1 === "" && v.discPct2 === "") errors.amount = "Enter an amount, a percentage or both.";
  if (v.type === "SURCHARGE" && v.discPct2 !== "" && Number(v.discPct2) !== 0) errors.discPct2 = "A surcharge takes one percentage; leave percentage 2 empty.";
  if (!DAY.test(v.validFrom)) errors.validFrom = "Enter the first valid day.";
  if (!DAY.test(v.validTo)) errors.validTo = "Enter the last valid day.";
  else if (DAY.test(v.validFrom) && v.validTo < v.validFrom) errors.validTo = "The last day must be on or after the first day.";
  return errors;
}

/** The editable fields of a stored record, used to start a copy. */
export function inputOf(r: ConditionRecord): Values {
  const pct = (p: string) => (Number(p) === 0 ? "" : p);
  return {
    legalEntityId: r.legalEntityId,
    type: r.type,
    customerKind: r.customerKind,
    customerKey: r.customerKey,
    productKind: r.productKind,
    productKey: r.productKey,
    variant: r.variant,
    currency: r.currency,
    unit: r.unit,
    fromQuantity: r.fromQuantity,
    amount: r.type !== "PRICE" && Number(r.amount) === 0 ? "" : r.amount,
    discPct1: pct(r.discPct1),
    discPct2: pct(r.discPct2),
    validFrom: r.validFrom,
    validTo: r.validTo,
    searchNext: r.searchNext,
    exclusive: r.exclusive,
  };
}

function toInput(v: Values): ConditionRecordInput {
  return {
    ...v,
    legalEntityId: v.legalEntityId.trim(),
    customerKey: v.customerKind === "all" ? "" : v.customerKey.trim(),
    productKey: v.productKind === "all" ? "" : v.productKey.trim(),
    variant: v.productKind === "variant" ? v.variant.trim() : "",
    unit: v.unit.trim(),
    fromQuantity: v.fromQuantity || "0",
    amount: v.amount || "0",
    discPct1: v.discPct1 || "0",
    discPct2: v.discPct2 || "0",
  };
}

/** Create a DRAFT trade agreement; ?copy=<id> starts from an existing record (grid "copy"). */
export function NewAgreementPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const copyFrom = params.get("copy");
  const [values, setValues] = useState<Values>(emptyAgreement);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  useEffect(() => {
    if (!copyFrom) return;
    const controller = new AbortController();
    getConditionRecord(copyFrom, controller.signal).then(
      (record) => {
        setValues(inputOf(record));
        key.current = newKey();
      },
      (error: unknown) => {
        if (!controller.signal.aborted) setFailure(describeProblem(error));
      },
    );
    return () => controller.abort();
  }, [copyFrom]);

  function update(patch: Partial<Values>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAgreement(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createConditionRecord(toInput(values), key.current);
      navigate(`/pricing/agreements/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const [name, message] of Object.entries(problem.fields)) {
        fieldErrors[name === "validity" ? "validTo" : (name as Field)] = message;
      }
      if (problem.code === "PRC-0003") fieldErrors.amount = problem.message;
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: string, name: keyof Values, label: string, extra: { hint?: string; optional?: boolean; type?: string; mono?: boolean } = {}) => (
    <FormField id={id} label={label} hint={extra.hint} optional={extra.optional} error={errors[name]}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          className={extra.mono ? styles.mono : undefined}
          value={String(values[name])}
          onChange={(event) => update({ [name]: event.target.value } as Partial<Values>)}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New trade agreement"
        description="The agreement is saved as a draft. Activating it checks that no active agreement for the same key, currency and unit overlaps its validity."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("ta-legal-entity", "legalEntityId", "Legal entity id", { mono: true })}
          <FormField id="ta-new-type" label="Type">
            {(control) => (
              <Select {...control} value={values.type} onChange={(e) => update({ type: e.target.value as Values["type"] })}>
                {CONDITION_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {TYPE_LABELS[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="ta-customer-kind" label="Applies to customers">
            {(control) => (
              <Select {...control} value={values.customerKind} onChange={(e) => update({ customerKind: e.target.value as Values["customerKind"] })}>
                {CUSTOMER_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {CUSTOMER_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.customerKind !== "all" ? text("ta-customer-key", "customerKey", values.customerKind === "customer" ? "Customer" : "Customer group") : null}
          <FormField id="ta-product-kind" label="Applies to products" error={errors.productKind}>
            {(control) => (
              <Select
                {...control}
                value={values.productKind}
                onChange={(e) => update({ productKind: e.target.value as Values["productKind"] })}
                invalid={Boolean(errors.productKind)}
              >
                {PRODUCT_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {PRODUCT_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.productKind !== "all" ? text("ta-product-key", "productKey", values.productKind === "product_group" ? "Product group" : "Product", { mono: true }) : null}
          {values.productKind === "variant" ? text("ta-variant", "variant", "Variant", { mono: true }) : null}
          {text("ta-currency", "currency", "Currency", { hint: "Documents in other currencies never use this agreement.", mono: true })}
          {text("ta-unit", "unit", "Unit", { optional: true, hint: "Leave empty to apply to any unit.", mono: true })}
          {text("ta-from-qty", "fromQuantity", "From quantity", { hint: "Quantity break: applies from this quantity up.", mono: true })}
          {text("ta-amount", "amount", amountLabel(values.type), { optional: values.type !== "PRICE", mono: true })}
          {text("ta-pct1", "discPct1", "Percentage 1", { optional: true, mono: true })}
          {text("ta-pct2", "discPct2", "Percentage 2", { optional: true, hint: "Applied after percentage 1.", mono: true })}
          {text("ta-valid-from", "validFrom", "Valid from", { type: "date" })}
          {text("ta-valid-to", "validTo", "Valid to", { type: "date", hint: "Inclusive." })}
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="ta-search-next" type="checkbox" checked={values.searchNext} onChange={(e) => update({ searchNext: e.target.checked })} />
              <label htmlFor="ta-search-next">Search next</label>
            </span>
            <span className={styles.note}>Keep looking at more general agreements and use the lowest net.</span>
          </div>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="ta-exclusive" type="checkbox" checked={values.exclusive} onChange={(e) => update({ exclusive: e.target.checked })} />
              <label htmlFor="ta-exclusive">Exclusive</label>
            </span>
            <span className={styles.note}>When this agreement wins, no later discount applies to the line.</span>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the agreement" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save draft
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/agreements">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
