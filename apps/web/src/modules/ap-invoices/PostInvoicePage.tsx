import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Link, useLocation } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  FormField,
  Input,
  PageHeader,
  FormErrorSummary,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
} from "../../shell/ui";
import { newKey, postVendorInvoice, type Invoice, type InvoiceInput } from "./api/client";
import { fieldErrors } from "../../shell/api/http";
import { DECIMAL, UUID, type InvoiceNavState } from "./parts";
import styles from "../../shell/pageLayout.module.css";
import { ProblemMessage } from "../../shell/errors";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

const MAX_LINES = 500;

export interface InvoiceLineDraft {
  receiptId: string;
  receiptLineNo: string;
  qty: string;
  unitPrice: string;
  standardCost: boolean;
}

export interface InvoiceDraft {
  vendorId: string;
  legalEntityId: string;
  invoiceNumber: string;
  invoiceDate: string;
  postingDate: string;
  currency: string;
  ledgerId: string;
  lines: InvoiceLineDraft[];
}

type Errors = Record<string, string>;

export const emptyInvoiceLine: InvoiceLineDraft = { receiptId: "", receiptLineNo: "1", qty: "", unitPrice: "", standardCost: false };

export const emptyInvoice: InvoiceDraft = {
  vendorId: "",
  legalEntityId: "",
  invoiceNumber: "",
  invoiceDate: "",
  postingDate: "",
  currency: "USD",
  ledgerId: "",
  lines: [emptyInvoiceLine],
};

/** Client checks that mirror POST /invoices:post; the service has the last word. */
export function validateInvoice(v: InvoiceDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.vendorId)) errors.vendorId = "Choose a vendor.";
  if (!UUID.test(v.legalEntityId)) errors.legalEntityId = "Choose a legal entity.";
  if (!UUID.test(v.ledgerId)) errors.ledgerId = "Enter a UUID.";
  if (v.invoiceNumber.trim() === "" || v.invoiceNumber.trim().length > 64) errors.invoiceNumber = "Enter an invoice number of up to 64 characters.";
  for (const key of ["invoiceDate", "postingDate"] as const) {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(v[key])) errors[key] = "Enter a date.";
  }
  if (!/^[A-Z]{3}$/.test(v.currency)) errors.currency = "Use a 3-letter upper-case currency code.";
  if (v.lines.length < 1 || v.lines.length > MAX_LINES) errors.lines = `Enter 1 to ${MAX_LINES} lines.`;
  v.lines.forEach((l, i) => {
    const at = (field: string) => `lines[${i}].${field}`;
    if (!UUID.test(l.receiptId)) errors[at("receiptId")] = "Choose the receipt.";
    if (!/^\d+$/.test(l.receiptLineNo) || Number(l.receiptLineNo) < 1) errors[at("receiptLineNo")] = "Enter a line number from 1.";
    if (!DECIMAL.test(l.qty) || Number(l.qty) <= 0) errors[at("qty")] = "Enter a quantity above zero.";
    if (!DECIMAL.test(l.unitPrice)) errors[at("unitPrice")] = "Enter a price of zero or more.";
  });
  return errors;
}

export function toInvoiceInput(v: InvoiceDraft): InvoiceInput {
  return {
    vendorId: v.vendorId,
    legalEntityId: v.legalEntityId,
    invoiceNumber: v.invoiceNumber.trim(),
    invoiceDate: v.invoiceDate,
    postingDate: v.postingDate,
    currency: v.currency,
    ledgerId: v.ledgerId,
    lines: v.lines.map((l) => ({ receiptId: l.receiptId, receiptLineNo: Number(l.receiptLineNo), qty: l.qty.trim(), unitPrice: l.unitPrice.trim(), standardCost: l.standardCost })),
  };
}

function initialDraft(state: InvoiceNavState | null): InvoiceDraft {
  const items = state?.items ?? [];
  if (items.length === 0) return emptyInvoice;
  const header = state?.header ?? {};
  return {
    ...emptyInvoice,
    ...(header.legalEntityId ? { legalEntityId: header.legalEntityId } : {}),
    ...(header.invoiceNumber ? { invoiceNumber: header.invoiceNumber } : {}),
    ...(header.invoiceDate ? { invoiceDate: header.invoiceDate } : {}),
    vendorId: items[0].vendorId,
    currency: items[0].currency,
    lines: items.map((i) => ({ receiptId: i.receiptId, receiptLineNo: String(i.receiptLineNo), qty: i.qty, unitPrice: i.unitPrice, standardCost: false })),
  };
}

const POST_INVOICE_FIELDS: Record<string, FormFieldTarget> = {
  vendorId: { id: "invoice-vendorId", label: "Vendor" },
  legalEntityId: { id: "invoice-legalEntityId", label: "Legal entity" },
  invoiceNumber: { id: "invoice-invoiceNumber", label: "Invoice number" },
  invoiceDate: { id: "invoice-invoiceDate", label: "Invoice date" },
  postingDate: { id: "invoice-postingDate", label: "Posting date" },
  currency: { id: "invoice-currency", label: "Currency" },
  ledgerId: { id: "invoice-ledgerId", label: "Ledger" },
  lines: { label: "Lines" },
};

const INVOICE_LINE_LABELS: Record<string, string> = { receiptId: "receipt", receiptLineNo: "receipt line", qty: "quantity", unitPrice: "unit price" };

/** A line's error is keyed `lines[i].field`. */
function invoiceLineTarget(key: string): FormFieldTarget | undefined {
  const match = /^lines\[(\d+)\]\.(\w+)$/.exec(key);
  if (!match) return undefined;
  const [, i, field] = match;
  const label = INVOICE_LINE_LABELS[field];
  return label ? { id: `inv-line-${i}-${field}`, label: `Line ${Number(i) + 1} ${label}` } : { label: `Line ${Number(i) + 1}` };
}

/** Post a vendor invoice against GR/IR items (POST /invoices:post). */
export function PostInvoicePage() {
  const location = useLocation();
  const queryClient = useQueryClient();
  const key = useRef(newKey());
  const [values, setValues] = useState<InvoiceDraft>(() => initialDraft(location.state as InvoiceNavState | null));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [posted, setPosted] = useState<Invoice | null>(null);
  const attempt = useSubmitAttempt(errors);
  // A posted invoice shows its result instead of the form, so leaving it then loses nothing.
  const { prompt } = useUnsavedChanges(useFormBaseline([values]) && posted === null);

  const update = (patch: Partial<InvoiceDraft>) => setValues((prev) => ({ ...prev, ...patch }));
  const updateLine = (index: number, patch: Partial<InvoiceLineDraft>) =>
    setValues((prev) => ({ ...prev, lines: prev.lines.map((l, i) => (i === index ? { ...l, ...patch } : l)) }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateInvoice(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      setPosted(await postVendorInvoice(toInvoiceInput(values), key.current));
      key.current = newKey();
      await queryClient.invalidateQueries({ queryKey: ["ap-invoices", "grir"] });
    } catch (error) {
      setErrors(fieldErrors(error));
      setFailure(error);
    } finally {
      setBusy(false);
    }
  }

  if (posted) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Post vendor invoice" description="The invoice cleared the GR/IR items it was posted against." />
        <Alert tone="success" title={`Invoice ${posted.invoiceNumber} posted`} live="status">
          Total {posted.total} {posted.currency} across {posted.lines.length} {posted.lines.length === 1 ? "line" : "lines"}, ledger entry {posted.ledgerEntryId}.
        </Alert>
        <div className={styles.filters}>
          <Link className={buttonClassName("primary")} to="/ap-invoices/grir">
            Back to GR/IR open items
          </Link>
          <Button
            onClick={() => {
              setPosted(null);
              setValues(emptyInvoice);
            }}
          >
            Post another invoice
          </Button>
        </div>
      </div>
    );
  }

  const text = (field: keyof Omit<InvoiceDraft, "lines">, label: string, extra: { hint?: string; type?: string; mono?: boolean } = {}) => (
    <FormField id={`invoice-${field}`} label={label} hint={extra.hint} error={errors[field]}>
      {(c) => <Input {...c} type={extra.type} className={extra.mono ? styles.mono : undefined} value={values[field]} onChange={(e) => update({ [field]: e.target.value })} invalid={Boolean(errors[field])} />}
    </FormField>
  );

  const lookup = (field: "vendorId" | "legalEntityId", label: string) => (
    <FormField id={`invoice-${field}`} label={label} error={errors[field]}>
      {(c) =>
        field === "vendorId" ? (
          <EntityLookup entity="vendor" scope={{ legalEntityId: values.legalEntityId || undefined }} {...c} value={values.vendorId} invalid={Boolean(errors.vendorId)} onChange={(e) => update({ vendorId: e.target.value })} />
        ) : (
          <EntityLookup entity="legalEntity" {...c} value={values.legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(e) => update({ legalEntityId: e.target.value })} />
        )
      }
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Post vendor invoice" description="Checks the AP period, clears GR/IR for the quantities invoiced and posts the vendor liability. A price difference goes to inventory, or to PPV for standard-cost lines." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Vendor invoice">
          <FormErrorSummary items={summaryItems(errors, POST_INVOICE_FIELDS, invoiceLineTarget)} attempt={attempt} />
          {lookup("legalEntityId", "Legal entity")}
          {lookup("vendorId", "Vendor")}
          {text("invoiceNumber", "Invoice number", { hint: "Unique per vendor, ignoring case." })}
          {text("invoiceDate", "Invoice date", { type: "date" })}
          {text("postingDate", "Posting date", { type: "date", hint: "Sets the fiscal period; it must be an open AP period." })}
          {text("currency", "Currency", { hint: "Must match the receipt currency." })}
          <LookupFormField id="invoice-ledgerId" label="Ledger" entity="ledger" scope={{ legalEntityId: values.legalEntityId || undefined }} value={values.ledgerId} error={errors.ledgerId} onChange={(ledgerId) => update({ ledgerId })} />
          {errors.lines ? (
            <div className={styles.formActions}>
              <Alert tone="danger" live="alert">
                {errors.lines}
              </Alert>
            </div>
          ) : null}
          {values.lines.map((line, i) => {
            const n = i + 1;
            const at = (field: string) => `lines[${i}].${field}`;
            const field = (name: "receiptId" | "receiptLineNo" | "qty" | "unitPrice", label: string, mono = false) => (
              <FormField id={`inv-line-${i}-${name}`} label={`Line ${n} ${label}`} error={errors[at(name)]}>
                {(c) => <Input {...c} className={mono ? styles.mono : undefined} value={line[name]} onChange={(e) => updateLine(i, { [name]: e.target.value })} invalid={Boolean(errors[at(name)])} />}
              </FormField>
            );
            return (
              <fieldset key={i} className={styles.group}>
                <legend className={styles.legend}>Line {n}</legend>
                <LookupFormField
                  id={`inv-line-${i}-receiptId`}
                  label={`Line ${n} receipt`}
                  entity="receipt"
                  scope={{ legalEntityId: values.legalEntityId || undefined }}
                  value={line.receiptId}
                  error={errors[at("receiptId")]}
                  onChange={(receiptId) => updateLine(i, { receiptId })}
                />
                {field("receiptLineNo", "receipt line")}
                {field("qty", "quantity")}
                {field("unitPrice", "unit price")}
                <span className={styles.check}>
                  <Input id={`inv-line-${i}-standardCost`} type="checkbox" checked={line.standardCost} onChange={(e) => updateLine(i, { standardCost: e.target.checked })} />
                  <label htmlFor={`inv-line-${i}-standardCost`}>{`Line ${n} standard cost`}</label>
                </span>
                {values.lines.length > 1 ? (
                  <div>
                    <Button onClick={() => update({ lines: values.lines.filter((_, j) => j !== i) })}>{`Remove line ${n}`}</Button>
                  </div>
                ) : null}
              </fieldset>
            );
          })}
          <div className={styles.formActions}>
            <Button onClick={() => update({ lines: [...values.lines, emptyInvoiceLine] })}>Add line</Button>
          </div>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not post the invoice" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Post invoice
            </Button>
            <Link className={buttonClassName("ghost")} to="/ap-invoices/grir">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {prompt}
    </div>
  );
}
