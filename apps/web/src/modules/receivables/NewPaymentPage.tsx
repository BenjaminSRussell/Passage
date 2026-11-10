import { useRef, useState, type FormEvent } from "react";
import { useWorkContext } from "../../shell/lookup/workContextState";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import type { LookupEntityKey } from "../../shell/lookup/entities";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormErrorSummary, FormField, FormSection, Input, PageHeader, Select, summaryItems, useUnsavedChanges, type FormFieldTarget } from "../../shell/ui";
import { copy } from "../../shell/copy";
import { createPayment, describeProblem, newKey, PAYMENT_MODES, ReceivablesApiError, type PaymentInput, type PaymentMode, type Problem } from "./api/client";
import { CURRENCY, DAY, DECIMAL, MODE_LABELS, today, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

export interface PaymentDraft {
  legalEntityId: string;
  customerId: string;
  paymentMode: PaymentMode;
  sourceRef: string;
  amount: string;
  currency: string;
  receivedDate: string;
  valueDate: string;
  payerName: string;
  payerAccount: string;
  shortPayReason: string;
  remittance: { reference: string; amount: string }[];
}

const FIELD_TARGETS: Record<string, FormFieldTarget> = {
  legalEntityId: { id: "np-legal-entity", label: "Legal entity" },
  customerId: { id: "np-customer", label: "Customer" },
  amount: { id: "np-amount", label: "Amount" },
  currency: { id: "np-currency", label: "Currency" },
  receivedDate: { id: "np-received", label: "Received date" },
  valueDate: { id: "np-value", label: "Value date" },
  sourceRef: { id: "np-ref", label: "Check or bank reference" },
  payerName: { id: "np-payer", label: "Payer name" },
  payerAccount: { id: "np-account", label: "Payer bank account" },
  shortPayReason: { id: "np-short", label: "Short-pay reason" },
};

function remittanceTarget(key: string): FormFieldTarget | undefined {
  const match = /^remittance\[(\d+)\]$/.exec(key);
  return match ? { id: `np-rem-ref-${match[1]}`, label: `Reference ${Number(match[1]) + 1}` } : undefined;
}

const errorItems = (errors: Errors) => summaryItems(errors, FIELD_TARGETS, remittanceTarget);

export function emptyPayment(day: string = today()): PaymentDraft {
  return {
    legalEntityId: "",
    customerId: "",
    paymentMode: "CHECK",
    sourceRef: "",
    amount: "",
    currency: "USD",
    receivedDate: day,
    valueDate: day,
    payerName: "",
    payerAccount: "",
    shortPayReason: "",
    remittance: [{ reference: "", amount: "" }],
  };
}

/** Client checks that mirror the service validation; the service has the last word. */
export function validatePayment(v: PaymentDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (v.customerId.trim() !== "" && !UUID.test(v.customerId.trim())) errors.customerId = "Choose a customer or leave it empty.";
  if (!DECIMAL.test(v.amount.trim()) || /^0+(\.0+)?$/.test(v.amount.trim())) errors.amount = "Enter an amount above zero, such as 1250.00.";
  if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!DAY.test(v.receivedDate)) errors.receivedDate = "Enter the date the payment arrived.";
  if (!DAY.test(v.valueDate)) errors.valueDate = "Enter the bank value date.";
  if (v.sourceRef.trim().length > 120) errors.sourceRef = "Use up to 120 characters.";
  if (v.payerName.trim().length > 140) errors.payerName = "Use up to 140 characters.";
  const account = v.payerAccount.replace(/[^A-Za-z0-9]/g, "");
  if (v.payerAccount.trim() !== "" && (account.length < 8 || v.payerAccount.trim().length > 64)) {
    errors.payerAccount = "Enter the IBAN or bank account (8 or more letters and digits) or leave it empty.";
  }
  if (v.shortPayReason.trim().length > 120) errors.shortPayReason = "Use up to 120 characters.";
  v.remittance.forEach((line, i) => {
    const blank = line.reference.trim() === "" && line.amount.trim() === "";
    if (blank) return;
    if (line.reference.trim() === "" || line.reference.trim().length > 200) errors[`remittance[${i}]`] = "Enter the document reference (up to 200 characters).";
    else if (line.amount.trim() !== "" && !DECIMAL.test(line.amount.trim())) errors[`remittance[${i}]`] = "Enter the amount as a number such as 980.00.";
  });
  return errors;
}

/** The request body: trimmed, blank optional fields as null, blank remittance lines dropped. */
export function toPaymentInput(v: PaymentDraft): PaymentInput {
  const blankToNull = (s: string) => (s.trim() === "" ? null : s.trim());
  return {
    legalEntityId: v.legalEntityId.trim(),
    customerId: blankToNull(v.customerId),
    paymentMode: v.paymentMode,
    source: "MANUAL",
    sourceRef: blankToNull(v.sourceRef),
    amount: v.amount.trim(),
    currency: v.currency.trim(),
    receivedDate: v.receivedDate,
    valueDate: v.valueDate,
    payerName: blankToNull(v.payerName),
    payerAccount: blankToNull(v.payerAccount),
    shortPayReason: blankToNull(v.shortPayReason),
    remittance: v.remittance
      .filter((l) => l.reference.trim() !== "")
      .map((l) => ({ reference: l.reference.trim(), amount: l.amount.trim() === "" ? "0" : l.amount.trim() })),
  };
}

/** Record a manual payment (FR-30.2): check, ACH, wire, card or cash, with the payer's remittance references. */
export function NewPaymentPage() {
  const navigate = useNavigate();
  const work = useWorkContext();
  const [values, setValues] = useState<PaymentDraft>(() => ({ ...emptyPayment(), legalEntityId: work.legalEntityId }));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const initial = useRef(JSON.stringify(values));
  const [attempt, setAttempt] = useState(0);
  const { prompt, release } = useUnsavedChanges(JSON.stringify(values) !== initial.current);

  function update(patch: Partial<PaymentDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  function updateLine(i: number, patch: Partial<PaymentDraft["remittance"][number]>) {
    update({ remittance: values.remittance.map((l, j) => (j === i ? { ...l, ...patch } : l)) });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validatePayment(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      setAttempt((n) => n + 1);
      return;
    }
    setBusy(true);
    try {
      const created = await createPayment(toPaymentInput(values), key.current);
      release();
      navigate(`/receivables/payments/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) {
        setErrors(problem.fields);
        setAttempt((n) => n + 1);
      } else setFailure(problem);
      // A refused request is final; only a network or server failure may be retried with the same key.
      if (!(error instanceof ReceivablesApiError) || error.status < 500) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  type TextField = "legalEntityId" | "customerId" | "sourceRef" | "amount" | "currency" | "payerName" | "payerAccount" | "shortPayReason";
  function text(id: string, field: TextField, label: string, opts: { entity?: LookupEntityKey; hint?: string; optional?: boolean; required?: boolean; mono?: boolean; decimal?: boolean } = {}) {
    return (
      <FormField id={id} label={label} hint={opts.hint} optional={opts.optional} required={opts.required} error={errors[field]}>
        {(c) =>
        opts.entity ? (
          <EntityLookup
            {...c}
            entity={opts.entity}
            scope={opts.entity === "customer" ? { legalEntityId: values.legalEntityId || undefined } : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<PaymentDraft>)}
            invalid={Boolean(errors[field])}
          />
        ) : (
          <Input
            {...c}
            className={opts.mono ? styles.mono : undefined}
            inputMode={opts.decimal ? "decimal" : undefined}
            value={values[field]}
            onChange={(e) => update({ [field]: e.target.value } as Partial<PaymentDraft>)}
            invalid={Boolean(errors[field])}
          />
        )}
      </FormField>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Record payment" description="The payment arrives as Received. Match and apply it to open items from its page." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormErrorSummary items={errorItems(errors)} attempt={attempt} />
          {text("np-legal-entity", "legalEntityId", "Legal entity", { entity: "legalEntity", mono: true, required: true })}
          {text("np-customer", "customerId", "Customer", { entity: "customer", optional: true, mono: true, hint: copy.fieldHelp.paymentCustomer })}
          <FormField id="np-mode" label="Payment mode" required>
            {(c) => (
              <Select {...c} value={values.paymentMode} onChange={(e) => update({ paymentMode: e.target.value as PaymentMode })}>
                {PAYMENT_MODES.map((m) => (
                  <option key={m} value={m}>
                    {MODE_LABELS[m]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("np-amount", "amount", "Amount", { mono: true, decimal: true, required: true })}
          {text("np-currency", "currency", "Currency", { mono: true, required: true })}
          <FormField id="np-received" label="Received date" required error={errors.receivedDate}>
            {(c) => <Input {...c} type="date" value={values.receivedDate} onChange={(e) => update({ receivedDate: e.target.value })} invalid={Boolean(errors.receivedDate)} />}
          </FormField>
          <FormField id="np-value" label="Value date" required hint={copy.fieldHelp.paymentValueDate} error={errors.valueDate}>
            {(c) => <Input {...c} type="date" value={values.valueDate} onChange={(e) => update({ valueDate: e.target.value })} invalid={Boolean(errors.valueDate)} />}
          </FormField>
          <FormSection collapse="more">
            {text("np-ref", "sourceRef", "Check or bank reference", { optional: true, mono: true })}
            {text("np-payer", "payerName", "Payer name", { optional: true, hint: copy.fieldHelp.paymentPayerName })}
            {text("np-account", "payerAccount", "Payer bank account", { optional: true, mono: true, hint: copy.fieldHelp.paymentPayerAccount })}
            {text("np-short", "shortPayReason", "Short-pay reason", { optional: true, hint: copy.fieldHelp.paymentShortReason })}
          </FormSection>
          <fieldset className={styles.lineSet}>
            <legend className={styles.groupsLegend}>Remittance: the documents the payer says this pays</legend>
            {values.remittance.map((line, i) => (
              <div key={i} className={styles.wide}>
                <div className={styles.form}>
                  <FormField id={`np-rem-ref-${i}`} label={`Reference ${i + 1}`} optional error={errors[`remittance[${i}]`]}>
                    {(c) => <Input {...c} className={styles.mono} value={line.reference} onChange={(e) => updateLine(i, { reference: e.target.value })} invalid={Boolean(errors[`remittance[${i}]`])} />}
                  </FormField>
                  <FormField id={`np-rem-amt-${i}`} label={`Amount ${i + 1}`} optional>
                    {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={line.amount} onChange={(e) => updateLine(i, { amount: e.target.value })} />}
                  </FormField>
                </div>
              </div>
            ))}
            <div className={styles.formActions}>
              <Button size="sm" onClick={() => update({ remittance: [...values.remittance, { reference: "", amount: "" }] })}>
                Add reference
              </Button>
              {values.remittance.length > 1 ? (
                <Button size="sm" variant="ghost" onClick={() => update({ remittance: values.remittance.slice(0, -1) })}>
                  Remove last reference
                </Button>
              ) : null}
            </div>
          </fieldset>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not record the payment" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record payment
            </Button>
            <Link className={buttonClassName("ghost")} to="/receivables/payments">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {prompt}
    </div>
  );
}
