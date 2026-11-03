import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createBrokerContract, describeProblem, newKey, type BrokerContractInput, type BrokerRateKind, type Problem } from "./api/client";
import { DEFAULT_CONTRACT_ACCOUNTS, RATE_KIND_LABELS } from "./contractFormat";
import { DAY, DECIMAL, UUID } from "./format";
import styles from "./pages.module.css";

export interface BrokerDraft {
  legalEntityId: string;
  name: string;
  brokerVendorId: string;
  customerScope: "all" | "customer" | "group";
  customerKey: string;
  productScope: "all" | "product" | "group";
  productKey: string;
  rateKind: BrokerRateKind;
  rate: string;
  currency: string;
  validFrom: string;
  validTo: string;
  payableAccount: string;
  expenseAccount: string;
}

type Errors = Record<string, string>;

export const emptyBroker: BrokerDraft = {
  legalEntityId: "",
  name: "",
  brokerVendorId: "",
  customerScope: "all",
  customerKey: "",
  productScope: "all",
  productKey: "",
  rateKind: "PERCENT_NET",
  rate: "",
  currency: "USD",
  validFrom: "",
  validTo: "",
  payableAccount: DEFAULT_CONTRACT_ACCOUNTS.BROKER.payableAccount,
  expenseAccount: DEFAULT_CONTRACT_ACCOUNTS.BROKER.expenseAccount,
};

const ACCOUNT = /^\d{4,8}$/;

/** Client checks that mirror the service (FR-26.16); the service has the last word. */
export function validateBroker(v: BrokerDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (v.name.trim() === "" || v.name.trim().length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (!UUID.test(v.brokerVendorId.trim())) errors.brokerVendorId = "Enter the broker's vendor id (a UUID).";
  if (v.customerScope !== "all" && !UUID.test(v.customerKey.trim())) errors.customerKey = v.customerScope === "customer" ? "Enter the customer id (a UUID)." : "Enter the customer group id (a UUID).";
  if (v.productScope !== "all" && (v.productKey.trim() === "" || v.productKey.trim().length > 64)) errors.productKey = "Enter the product or group (up to 64 characters).";
  const rate = Number(v.rate);
  if (!DECIMAL.test(v.rate.trim()) || rate <= 0 || (v.rateKind === "PERCENT_NET" && rate > 100)) {
    errors.rate = v.rateKind === "PERCENT_NET" ? "Enter a percentage above 0 and up to 100." : "Enter an amount per unit above 0.";
  }
  if (!/^[A-Z]{3}$/.test(v.currency)) errors.currency = "Use a three-letter ISO 4217 code, such as USD.";
  if (!DAY.test(v.validFrom)) errors.validFrom = "Enter the first valid day.";
  if (!DAY.test(v.validTo)) errors.validTo = "Enter the last valid day.";
  else if (DAY.test(v.validFrom) && v.validTo < v.validFrom) errors.validTo = "The last day must be on or after the first day.";
  if (!ACCOUNT.test(v.payableAccount)) errors.payableAccount = "Enter a 4 to 8 digit account.";
  if (!ACCOUNT.test(v.expenseAccount)) errors.expenseAccount = "Enter a 4 to 8 digit account.";
  return errors;
}

export function toBrokerInput(v: BrokerDraft): BrokerContractInput {
  const customer = v.customerKey.trim();
  const product = v.productKey.trim();
  return {
    legalEntityId: v.legalEntityId.trim(),
    name: v.name.trim(),
    brokerVendorId: v.brokerVendorId.trim(),
    ...(v.customerScope === "customer" ? { customerId: customer } : v.customerScope === "group" ? { customerGroupId: customer } : {}),
    ...(v.productScope === "product" ? { productId: product } : v.productScope === "group" ? { productGroupId: product } : {}),
    rateKind: v.rateKind,
    rate: v.rate.trim(),
    currency: v.currency,
    validFrom: v.validFrom,
    validTo: v.validTo,
    payableAccount: v.payableAccount,
    expenseAccount: v.expenseAccount,
  };
}

/** Create a broker contract (FR-26.16). It is active from creation; invoices inside its validity accrue. */
export function NewBrokerPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<BrokerDraft>(emptyBroker);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<BrokerDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateBroker(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createBrokerContract(toBrokerInput(values), key.current);
      navigate(`/pricing/brokers/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fields: Errors = {};
      for (const [name, message] of Object.entries(problem.fields)) {
        if (name === "customer") fields.customerKey = message;
        else if (name === "product") fields.productKey = message;
        else if (name === "validity") fields.validTo = message;
        else if (name === "accounts") fields.payableAccount = message;
        else fields[name] = message;
      }
      if (Object.keys(fields).length > 0) setErrors(fields);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (id: string, name: keyof BrokerDraft & string, label: string, extra: { hint?: string; type?: string; mono?: boolean; decimal?: boolean } = {}) => (
    <FormField id={id} label={label} hint={extra.hint} error={errors[name]}>
      {(c) => (
        <Input
          {...c}
          type={extra.type ?? "text"}
          inputMode={extra.decimal ? "decimal" : undefined}
          className={extra.mono ? styles.mono : undefined}
          value={String(values[name])}
          onChange={(e) => update({ [name]: e.target.value } as Partial<BrokerDraft>)}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New broker contract" description="The contract is active from creation: posted invoice lines in its scope and validity accrue broker commission." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {text("bk-legal-entity", "legalEntityId", "Legal entity id", { mono: true })}
          {text("bk-name", "name", "Contract name")}
          {text("bk-vendor", "brokerVendorId", "Broker vendor id", { mono: true })}
          <FormField id="bk-customer-scope" label="Customers">
            {(c) => (
              <Select {...c} value={values.customerScope} onChange={(e) => update({ customerScope: e.target.value as BrokerDraft["customerScope"], customerKey: "" })}>
                <option value="all">All customers</option>
                <option value="customer">One customer</option>
                <option value="group">A customer group</option>
              </Select>
            )}
          </FormField>
          {values.customerScope === "all" ? null : text("bk-customer", "customerKey", values.customerScope === "customer" ? "Customer id" : "Customer group id", { mono: true })}
          <FormField id="bk-product-scope" label="Products">
            {(c) => (
              <Select {...c} value={values.productScope} onChange={(e) => update({ productScope: e.target.value as BrokerDraft["productScope"], productKey: "" })}>
                <option value="all">All products</option>
                <option value="product">One product</option>
                <option value="group">A product group</option>
              </Select>
            )}
          </FormField>
          {values.productScope === "all" ? null : text("bk-product", "productKey", values.productScope === "product" ? "Product" : "Product group", { mono: true })}
          <FormField id="bk-rate-kind" label="Rate type">
            {(c) => (
              <Select {...c} value={values.rateKind} onChange={(e) => update({ rateKind: e.target.value as BrokerRateKind })}>
                {(Object.keys(RATE_KIND_LABELS) as BrokerRateKind[]).map((k) => (
                  <option key={k} value={k}>
                    {RATE_KIND_LABELS[k]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("bk-rate", "rate", values.rateKind === "PERCENT_NET" ? "Rate %" : `Amount per unit (${values.currency || "currency"})`, { mono: true, decimal: true })}
          {text("bk-currency", "currency", "Currency", { mono: true, hint: "Only invoices in this currency accrue." })}
          {text("bk-valid-from", "validFrom", "Valid from", { type: "date" })}
          {text("bk-valid-to", "validTo", "Valid to", { type: "date", hint: "Inclusive." })}
          {text("bk-expense", "expenseAccount", "Commission expense account", { mono: true, hint: "Debited on accrual." })}
          {text("bk-payable", "payableAccount", "Broker payable account", { mono: true, hint: "Credited on accrual." })}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the contract" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save contract
            </Button>
            <Link className={buttonClassName("ghost")} to="/pricing/brokers">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
