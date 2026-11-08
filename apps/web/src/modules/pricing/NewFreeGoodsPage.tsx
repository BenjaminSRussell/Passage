import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import {
  createFreeGoodsRule,
  describeProblem,
  FREE_GOODS_CALC_RULES,
  FREE_GOODS_KINDS,
  newKey,
  type FreeGoodsCalcRule,
  type FreeGoodsCustomerKind,
  type FreeGoodsKind,
  type FreeGoodsProductKind,
  type FreeGoodsRuleInput,
  type Problem,
} from "./api/client";
import { CALC_LABELS, CUSTOMER_KIND_LABELS, KIND_LABELS, PRODUCT_KIND_LABELS } from "./freeGoodsFormat";
import { DAY, DECIMAL, UUID } from "./format";
import styles from "./pages.module.css";

type Errors = Record<string, string>;

export const emptyFreeGoods: FreeGoodsRuleInput = {
  legalEntityId: "",
  customerKind: "all",
  customerKey: "",
  productKind: "product",
  productId: "",
  unit: "EA",
  kind: "INCLUSIVE",
  everyQuantity: "",
  freeQuantity: "",
  minQuantity: "0",
  validFrom: "",
  validTo: "",
};

/** Client checks that mirror the service (FR-26.22); the service has the last word. */
export function validateFreeGoods(v: FreeGoodsRuleInput): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (v.customerKind === "all") {
    if (v.customerKey.trim() !== "") errors.customerKey = "Leave empty when the rule is for all customers.";
  } else if (v.customerKey.trim() === "" || v.customerKey.trim().length > 64) {
    errors.customerKey = "Enter the customer or group (up to 64 characters).";
  }
  if (v.productId.trim() === "" || v.productId.trim().length > 64) errors.productId = `Enter the ${v.productKind === "product_group" ? "product group" : "product"} (up to 64 characters).`;
  if (v.unit.trim() === "" || v.unit.trim().length > 16) errors.unit = "Enter the unit (up to 16 characters).";
  const every = Number(v.everyQuantity);
  const free = Number(v.freeQuantity);
  if (!DECIMAL.test(v.everyQuantity.trim()) || every <= 0) errors.everyQuantity = "Enter a quantity above 0.";
  if (!DECIMAL.test(v.freeQuantity.trim()) || free <= 0) errors.freeQuantity = "Enter a quantity above 0.";
  else if (v.kind === "INCLUSIVE" && !errors.everyQuantity && free >= every) errors.freeQuantity = "An inclusive rule must give fewer free units than the quantity ordered.";
  if (v.minQuantity.trim() !== "" && !DECIMAL.test(v.minQuantity.trim())) errors.minQuantity = "Enter the minimum as a number, or 0.";
  if (!DAY.test(v.validFrom)) errors.validFrom = "Enter the first day as YYYY-MM-DD.";
  if (!DAY.test(v.validTo)) errors.validTo = "Enter the last day as YYYY-MM-DD.";
  else if (DAY.test(v.validFrom) && v.validTo < v.validFrom) errors.validTo = "The last day cannot be before the first.";
  return errors;
}

export function toFreeGoodsInput(v: FreeGoodsRuleInput): FreeGoodsRuleInput {
  return {
    legalEntityId: v.legalEntityId.trim(),
    customerKind: v.customerKind,
    customerKey: v.customerKind === "all" ? "" : v.customerKey.trim(),
    productKind: v.productKind ?? "product",
    productId: v.productId.trim(),
    unit: v.unit.trim(),
    kind: v.kind,
    // No calcRule means the legal entity's free goods calculation rule (pricing parameters).
    calcRule: v.calcRule || undefined,
    everyQuantity: v.everyQuantity.trim(),
    freeQuantity: v.freeQuantity.trim(),
    minQuantity: v.minQuantity.trim() === "" ? "0" : v.minQuantity.trim(),
    validFrom: v.validFrom,
    validTo: v.validTo,
  };
}

/** The service reports a reversed range on "validity"; show it under the last day. */
function fieldErrors(fields: Record<string, string>): Errors {
  const { validity, ...rest } = fields;
  return validity ? { ...rest, validTo: validity } : rest;
}

/** Create a free goods rule (FR-26.22). It starts live. */
export function NewFreeGoodsPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<FreeGoodsRuleInput>(emptyFreeGoods);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<FreeGoodsRuleInput>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateFreeGoods(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createFreeGoodsRule(toFreeGoodsInput(values), key.current);
      navigate(`/pricing/free-goods/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(fieldErrors(problem.fields));
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  type TextField = "legalEntityId" | "customerKey" | "productId" | "unit" | "everyQuantity" | "freeQuantity" | "minQuantity" | "validFrom" | "validTo";
  function text(id: string, field: TextField, label: string, opts: { hint?: string; optional?: boolean; mono?: boolean; decimal?: boolean; type?: string } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} optional={opts.optional} error={errors[field]}>
        {(c) => (
          <Input
            {...c}
            type={opts.type}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<FreeGoodsRuleInput>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New free goods rule" description="Give free units for every quantity ordered of a product or product group and unit." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("fg-legal-entity", "legalEntityId", "Legal entity id", { mono: true })}
          <FormField id="fg-customer-kind" label="Customers">
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
          {values.customerKind === "all" ? null : text("fg-customer-key", "customerKey", values.customerKind === "customer" ? "Customer" : "Customer group", { mono: true })}
          <FormField id="fg-product-kind" label="Applies to">
            {(c) => (
              <Select {...c} value={values.productKind ?? "product"} onChange={(e) => update({ productKind: e.target.value as FreeGoodsProductKind })}>
                {(Object.keys(PRODUCT_KIND_LABELS) as FreeGoodsProductKind[]).map((k) => (
                  <option key={k} value={k}>
                    {PRODUCT_KIND_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("fg-product", "productId", values.productKind === "product_group" ? "Product group" : "Product", {
            mono: true,
            hint: values.productKind === "product_group" ? "Every product of this group earns the free goods; a rule on the product itself outranks it." : undefined,
          })}
          {text("fg-unit", "unit", "Unit", { mono: true, hint: "The rule applies to lines ordered in this unit." })}
          <FormField id="fg-kind" label="Variant" hint={values.kind === "INCLUSIVE" ? "1 free within every 10 ordered: the free units are part of the order." : "1 extra per 10 ordered: the free units are added to the order."}>
            {(c) => (
              <Select {...c} value={values.kind} onChange={(e) => update({ kind: e.target.value as FreeGoodsKind })}>
                {FREE_GOODS_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {KIND_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="fg-calc" label="Calculation" hint="Legal entity default takes the free goods calculation rule on the Pricing parameters page.">
            {(c) => (
              <Select {...c} value={values.calcRule ?? ""} onChange={(e) => update({ calcRule: (e.target.value || undefined) as FreeGoodsCalcRule | undefined })}>
                <option value="">Legal entity default</option>
                {FREE_GOODS_CALC_RULES.map((k) => (
                  <option key={k} value={k}>
                    {CALC_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("fg-every", "everyQuantity", "For every quantity ordered", { mono: true, decimal: true })}
          {text("fg-free", "freeQuantity", "Free quantity", { mono: true, decimal: true })}
          {text("fg-min", "minQuantity", "Minimum quantity", { mono: true, decimal: true, optional: true, hint: "No free goods below this ordered quantity." })}
          {text("fg-from", "validFrom", "Valid from", { type: "date", mono: true })}
          {text("fg-to", "validTo", "Valid to", { type: "date", mono: true, hint: "Inclusive." })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the rule" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save rule
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/free-goods">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
