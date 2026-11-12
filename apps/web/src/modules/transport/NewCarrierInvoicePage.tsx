import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { describeProblem, registerCarrierInvoice, type InvoiceSource, type Line, type NewCarrierInvoice, type Problem } from "./api/client";
import { ChargeLinesEditor, cleanLines, validateLines } from "./ChargeLinesEditor";
import { CURRENCY, DATE, UUID } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

interface Values {
  carrierId: string;
  invoiceNo: string;
  invoiceDate: string;
  source: InvoiceSource;
  freightBillId: string;
  currency: string;
  lines: Line[];
}

/** Checks that mirror ValidateCarrierInvoice in the service, and the request the form sends. */
export function validateInvoice(v: Values): { errors: Record<string, string>; input?: NewCarrierInvoice } {
  const errors = validateLines(v.lines, "lines");
  if (!UUID.test(v.carrierId.trim())) errors.carrierId = "Choose the carrier.";
  const number = v.invoiceNo.trim();
  if (number.length < 1 || number.length > 40) errors.invoiceNo = "Enter the carrier's invoice number, up to 40 characters.";
  if (!DATE.test(v.invoiceDate)) errors.invoiceDate = "Enter the invoice date.";
  if (v.freightBillId.trim() !== "" && !UUID.test(v.freightBillId.trim())) errors.freightBillId = "Enter the freight bill id as a lower-case UUID, or leave it empty.";
  if (!CURRENCY.test(v.currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewCarrierInvoice = {
    carrierId: v.carrierId.trim(),
    invoiceNo: number,
    invoiceDate: v.invoiceDate,
    source: v.source,
    currency: v.currency,
    lines: cleanLines(v.lines),
  };
  if (v.freightBillId.trim() !== "") input.freightBillId = v.freightBillId.trim();
  return { errors, input };
}

const conflictText: Record<string, { field: keyof Values; text: string }> = {
  "TMS-0108": { field: "invoiceNo", text: "This invoice number is already registered for the carrier." },
  "TMS-0111": { field: "freightBillId", text: "This freight bill already has a carrier invoice." },
  "TMS-0100": { field: "freightBillId", text: "No freight bill with this id exists." },
};

/** Register a carrier invoice; with a freight bill it is matched at once (FR-34.7, BR-34.4). */
export function NewCarrierInvoicePage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [values, setValues] = useState<Values>({
    carrierId: params.get("carrierId") ?? "",
    invoiceNo: "",
    invoiceDate: "",
    source: "MANUAL",
    freightBillId: params.get("freightBillId") ?? "",
    currency: params.get("currency") ?? "USD",
    lines: [{ chargeCode: "BASE", amount: "" }],
  });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateInvoice(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const result = await registerCarrierInvoice(input);
      navigate(result.freightBill ? `/transport/freight-bills/${result.freightBill.id}` : "/transport/carrier-invoices");
    } catch (error) {
      const problem = describeProblem(error);
      const known = conflictText[problem.code];
      if (known) setErrors({ [known.field]: known.text });
      else if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const text = (id: Exclude<keyof Values, "lines" | "source">, label: string, hint?: string, optional?: boolean, extra?: Record<string, unknown>) => (
    <FormField id={`invoice-${id}`} label={label} hint={hint} optional={optional} error={errors[id]}>
      {(control) => (
        <Input
          {...control}
          {...extra}
          value={values[id]}
          onChange={(e) => setValues({ ...values, [id]: id === "currency" ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[id])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Register carrier invoice"
        description="Enter the invoice as the carrier sent it. With a freight bill it is matched charge by charge; without one it waits in the exception queue."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <LookupFormField id="invoice-carrierId" label="Carrier" entity="vendor" value={values.carrierId} error={errors.carrierId} onChange={(carrierId) => setValues({ ...values, carrierId })} />
          {text("invoiceNo", "Invoice number", undefined, false, { maxLength: 40, className: styles.mono })}
          {text("invoiceDate", "Invoice date", undefined, false, { type: "date" })}
          <FormField id="invoice-source" label="Source">
            {(control) => (
              <Select {...control} value={values.source} onChange={(e) => setValues({ ...values, source: e.target.value as InvoiceSource })}>
                <option value="MANUAL">Entered by hand</option>
                <option value="EDI210">EDI 210</option>
                <option value="API">Carrier API</option>
              </Select>
            )}
          </FormField>
          {text("freightBillId", "Freight bill id", "The bill to match against; leave empty if you do not know it.", true, { className: styles.mono })}
          {text("currency", "Currency", "Must be the freight bill's currency.", false, { maxLength: 3, className: styles.mono })}
          <ChargeLinesEditor field="lines" legend="Invoiced charges" lines={values.lines} errors={errors} onChange={(lines) => setValues({ ...values, lines })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not register the invoice" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Register invoice
            </Button>
            <Link className={buttonClassName("ghost")} to="/transport/carrier-invoices">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
