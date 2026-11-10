import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { BillingApiError, createFreeTextInvoice, describeProblem, newKey, type FreeTextInvoiceInput, type Problem } from "./api/client";
import { ACCOUNT, CURRENCY, DAY, DECIMAL, TAX_CODE, today, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

export interface LineValues {
  description: string;
  qty: string;
  unitPrice: string;
  discountPct: string;
  revenueAccount: string;
  taxCode: string;
}

export interface InstallmentValues {
  percent: string;
  offsetDays: string;
}

export interface FreeTextValues {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  currency: string;
  invoiceDate: string;
  paymentTermDays: string;
  chargeAmount: string;
  lines: LineValues[];
  /** Optional installment plan (FR-29.18); empty means one open item due after the payment terms. */
  installments: InstallmentValues[];
}

const MAX_LINES = 50;
const MAX_INSTALLMENTS = 24;
const emptyLine: LineValues = { description: "", qty: "1", unitPrice: "", discountPct: "", revenueAccount: "", taxCode: "" };

export function emptyFreeText(day = today()): FreeTextValues {
  return {
    legalEntityId: "",
    customerId: "",
    invoiceAccountId: "",
    currency: "EUR",
    invoiceDate: day,
    paymentTermDays: "30",
    chargeAmount: "",
    lines: [{ ...emptyLine }],
    installments: [],
  };
}

/** Client checks that mirror the service (FR-29.4); the service has the last word. */
export function validateFreeText(v: FreeTextValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!UUID.test(v.customerId.trim())) errors.customerId = "Enter the customer id (a UUID).";
  if (!UUID.test(v.invoiceAccountId.trim())) errors.invoiceAccountId = "Enter the invoice account id (a UUID).";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter a three-letter currency code such as EUR.";
  if (!DAY.test(v.invoiceDate)) errors.invoiceDate = "Enter the invoice date as YYYY-MM-DD.";
  const days = Number(v.paymentTermDays);
  if (!/^\d{1,3}$/.test(v.paymentTermDays.trim()) || days > 365) errors.paymentTermDays = "Enter 0 to 365 days.";
  if (v.chargeAmount.trim() !== "" && !DECIMAL.test(v.chargeAmount.trim())) errors.chargeAmount = "Enter the charge as a number such as 12.50, or leave it empty.";
  v.lines.forEach((l, i) => {
    const at = (f: string) => `lines[${i}].${f}`;
    if (l.description.trim() === "" || l.description.trim().length > 200) errors[at("description")] = "Enter a description of up to 200 characters.";
    if (!DECIMAL.test(l.qty.trim()) || Number(l.qty) <= 0) errors[at("qty")] = "Enter a quantity above zero.";
    if (!DECIMAL.test(l.unitPrice.trim())) errors[at("unitPrice")] = "Enter the unit price as a number such as 100.00.";
    if (l.discountPct.trim() !== "" && (!DECIMAL.test(l.discountPct.trim()) || Number(l.discountPct) > 100)) {
      errors[at("discountPct")] = "Enter a discount from 0 to 100, or leave it empty.";
    }
    if (!ACCOUNT.test(l.revenueAccount.trim())) errors[at("revenueAccount")] = "Enter the revenue account, for example 4900.";
    if (!TAX_CODE.test(l.taxCode.trim())) errors[at("taxCode")] = "Enter the tax code in capitals, for example DE19.";
  });
  let total = 0;
  v.installments.forEach((t, i) => {
    const at = (f: string) => `installmentTerms[${i}].${f}`;
    const pct = t.percent.trim();
    if (!DECIMAL.test(pct) || Number(pct) <= 0 || Number(pct) > 100) errors[at("percent")] = "Enter a share above 0 and at most 100.";
    else total += Math.round(Number(pct) * 1_000_000);
    const days = t.offsetDays.trim();
    if (!/^\d{1,3}$/.test(days) || Number(days) > 365) errors[at("offsetDays")] = "Enter 0 to 365 days after the invoice date.";
    else if (i > 0 && Number(days) < Number(v.installments[i - 1].offsetDays)) errors[at("offsetDays")] = "Must not fall before the previous installment.";
  });
  if (v.installments.length > 0 && !Object.keys(errors).some((k) => k.startsWith("installmentTerms[")) && total !== 100_000_000) {
    errors.installmentTerms = `The shares total ${total / 1_000_000}%; they must total 100%.`;
  }
  return errors;
}

export function toFreeTextInput(v: FreeTextValues): FreeTextInvoiceInput {
  const charge = v.chargeAmount.trim();
  return {
    legalEntityId: v.legalEntityId.trim(),
    customerId: v.customerId.trim(),
    invoiceAccountId: v.invoiceAccountId.trim(),
    currency: v.currency.trim(),
    invoiceDate: v.invoiceDate,
    paymentTermDays: Number(v.paymentTermDays),
    ...(charge ? { chargeAmount: charge } : {}),
    lines: v.lines.map((l) => ({
      description: l.description.trim(),
      qty: l.qty.trim(),
      unitPrice: l.unitPrice.trim(),
      ...(l.discountPct.trim() ? { discountPct: l.discountPct.trim() } : {}),
      revenueAccount: l.revenueAccount.trim(),
      taxCode: l.taxCode.trim(),
    })),
    ...(v.installments.length > 0
      ? { installmentTerms: v.installments.map((t) => ({ percent: t.percent.trim(), offsetDays: Number(t.offsetDays) })) }
      : {}),
  };
}

type HeaderField = Exclude<keyof FreeTextValues, "lines" | "installments">;

/** Free-text invoice (FR-29.4): account-driven lines with a tax code each, saved as a proposal. */
export function NewFreeTextInvoicePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<FreeTextValues>(() => emptyFreeText());
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<FreeTextValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  function setLine(i: number, patch: Partial<LineValues>) {
    update({ lines: values.lines.map((l, j) => (j === i ? { ...l, ...patch } : l)) });
  }

  function setInstallment(i: number, patch: Partial<InstallmentValues>) {
    update({ installments: values.installments.map((t, j) => (j === i ? { ...t, ...patch } : t)) });
  }

  function installment(i: number, field: keyof InstallmentValues, label: string, hint: string) {
    const name = `installmentTerms[${i}].${field}`;
    return (
      <FormField id={`ft-inst-${i}-${field}`} label={label} hint={hint} error={errors[name]}>
        {(c) => (
          <Input
            {...c}
            className={styles.mono}
            inputMode="decimal"
            value={values.installments[i][field]}
            onChange={(e) => setInstallment(i, { [field]: e.target.value })}
            invalid={Boolean(errors[name])}
          />
        )}
      </FormField>
    );
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateFreeText(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const created = await createFreeTextInvoice(toFreeTextInput(values), key.current);
      navigate(`/billing/invoices/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      // A refused request is final; only a network or server failure may be retried with the same key.
      if (error instanceof BillingApiError && error.status < 500) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  function header(id: string, field: HeaderField, label: string, opts: { hint?: string; optional?: boolean; mono?: boolean; decimal?: boolean; type?: string } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} optional={opts.optional} error={errors[field]}>
        {(c) => (
          <Input
            {...c}
            type={opts.type}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<FreeTextValues>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  function line(i: number, field: keyof LineValues, label: string, opts: { hint?: string; optional?: boolean; mono?: boolean; decimal?: boolean } = {}) {
    const name = `lines[${i}].${field}`;
    return (
      <FormField id={`ft-line-${i}-${field}`} label={label} hint={opts.hint} optional={opts.optional} error={errors[name]}>
        {(c) => (
          <Input
            {...c}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values.lines[i][field]}
            onChange={(e) => setLine(i, { [field]: e.target.value })}
            invalid={Boolean(errors[name])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New free-text invoice"
        description="Bill services or other charges that do not come from a shipment. The invoice is saved as a proposal with tax quoted per line; you post it from the next page."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          {header("ft-legal-entity", "legalEntityId", "Legal entity id", { mono: true })}
          {header("ft-customer", "customerId", "Customer id", { mono: true })}
          {header("ft-account", "invoiceAccountId", "Invoice account id", { mono: true, hint: "The account that receives the invoice; often the customer itself." })}
          {header("ft-currency", "currency", "Currency", { mono: true })}
          {header("ft-date", "invoiceDate", "Invoice date", { type: "date", mono: true })}
          {header("ft-terms", "paymentTermDays", "Payment terms (days)", { mono: true, hint: "Due date = invoice date + these days." })}
          {header("ft-charge", "chargeAmount", "Header charge", { optional: true, mono: true, decimal: true, hint: "Freight or handling on the whole invoice." })}
          {values.lines.map((_, i) => (
            <fieldset key={i} className={styles.lineSet}>
              <legend className={styles.groupsLegend}>{`Line ${i + 1}`}</legend>
              {line(i, "description", "Description")}
              {line(i, "qty", "Quantity", { mono: true, decimal: true })}
              {line(i, "unitPrice", "Unit price", { mono: true, decimal: true })}
              {line(i, "discountPct", "Discount %", { optional: true, mono: true, decimal: true })}
              {line(i, "revenueAccount", "Revenue account", { mono: true })}
              {line(i, "taxCode", "Tax code", { mono: true })}
              {values.lines.length > 1 ? (
                <Button variant="ghost" size="sm" onClick={() => update({ lines: values.lines.filter((_, j) => j !== i) })}>
                  {`Remove line ${i + 1}`}
                </Button>
              ) : null}
            </fieldset>
          ))}
          <div className={styles.formActions}>
            {values.lines.length < MAX_LINES ? (
              <Button variant="secondary" onClick={() => update({ lines: [...values.lines, { ...emptyLine }] })}>
                Add line
              </Button>
            ) : null}
            <span className={styles.note}>Net, tax and gross are calculated by the service with per-line rounding.</span>
          </div>
          {values.installments.map((_, i) => (
            <fieldset key={i} className={styles.lineSet}>
              <legend className={styles.groupsLegend}>{`Installment ${i + 1}`}</legend>
              {installment(i, "percent", "Share %", "Of the gross.")}
              {installment(i, "offsetDays", "Due after (days)", "Days after the invoice date.")}
              <Button variant="ghost" size="sm" onClick={() => update({ installments: values.installments.filter((_, j) => j !== i) })}>
                {`Remove installment ${i + 1}`}
              </Button>
            </fieldset>
          ))}
          <div className={styles.formActions}>
            {values.installments.length < MAX_INSTALLMENTS ? (
              <Button variant="secondary" onClick={() => update({ installments: [...values.installments, { percent: "", offsetDays: values.installments.length === 0 ? "0" : "" }] })}>
                Add installment
              </Button>
            ) : null}
            <span className={styles.note}>
              Optional. Split the gross into installments, each its own open item with its own due date; the shares must total 100% and the last one takes the rounding.
            </span>
          </div>
          {errors.installmentTerms ? (
            <div className={styles.formActions}>
              <Alert tone="danger" live="alert">
                {errors.installmentTerms}
              </Alert>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "BIL-0001" ? "The tax engine is unavailable; nothing was saved" : "Could not save the invoice"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save proposal
            </Button>
            <Link className={buttonClassName("ghost")} to="/billing">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
