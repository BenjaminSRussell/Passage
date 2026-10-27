import { useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, PageHeader, Textarea } from "../../shell/ui";
import { buildPeppolInvoice, describeProblem, type Problem, type UblBuildResult } from "./api/client";
import { IssueList, ProblemAlert } from "./parts";
import styles from "./pages.module.css";

/** A valid example: goods at 19 %, a book at 7 % and a 5.00 discount on the goods. */
export const PEPPOL_EXAMPLE = JSON.stringify(
  {
    creditNote: false,
    id: "INV-2026-000123",
    issueDate: "2026-10-27",
    dueDate: "2026-11-26",
    currency: "EUR",
    buyerReference: "PO-7788",
    supplier: { name: "Demo Manufacturer GmbH", endpointScheme: "0088", endpointId: "4006403333333", countryCode: "DE", vatId: "DE123456789" },
    customer: { name: "Demo Distributor SAS", endpointScheme: "0088", endpointId: "4000001234567", countryCode: "FR", vatId: "FR12345678901" },
    paymentMeansCode: "58",
    iban: "DE89370400440532013000",
    allowanceCharges: [{ charge: false, reason: "Discount", amount: "5.00", category: "S", percent: "19" }],
    lines: [
      { id: "1", name: "Widget", unitCode: "C62", quantity: "10", unitPrice: "10.00", category: "S", percent: "19" },
      { id: "2", name: "Operating manual", unitCode: "C62", quantity: "1", unitPrice: "20.00", category: "S", percent: "7" },
    ],
  },
  null,
  2,
);

/** Client check before the request: the invoice is a JSON object. */
export function parseInvoice(text: string): { invoice?: object; error?: string } {
  try {
    const parsed: unknown = JSON.parse(text);
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return { error: "The invoice must be a JSON object." };
    return { invoice: parsed };
  } catch (error) {
    return { error: `The invoice is not valid JSON: ${error instanceof Error ? error.message : "parse error"}.` };
  }
}

/**
 * Build a Peppol BIS Billing 3.0 invoice or credit note (FR-35.6, BR-35.4) and
 * see the UBL 2.1 XML, or the findings that block it. Schematron validation and
 * sending through the access point are not available yet.
 */
export function PeppolInvoicePage() {
  const [text, setText] = useState(PEPPOL_EXAMPLE);
  const [error, setError] = useState<string | undefined>();
  const [result, setResult] = useState<UblBuildResult | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { invoice, error: found } = parseInvoice(text);
    setError(found);
    setFailure(null);
    if (!invoice) return;
    setBusy(true);
    try {
      setResult(await buildPeppolInvoice(invoice));
    } catch (err) {
      setResult(null);
      setFailure(describeProblem(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Peppol invoice"
        description="Build a Peppol BIS Billing 3.0 UBL invoice or credit note and check the required elements, VAT categories and totals before it is sent. Nothing is stored or sent."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <div className={styles.wide}>
            <FormField
              id="peppol-invoice"
              label="Invoice (JSON)"
              hint='Prefilled with a valid example. Set "creditNote": true for a credit note. Amounts, quantities and rates are decimal strings; VAT categories are S, Z, E, AE, K, G, O, L or M, and E, AE, K, G and O need an entry in "exemptionReasons".'
              error={error}
            >
              {(control) => <Textarea {...control} mono rows={18} value={text} onChange={(event) => setText(event.target.value)} invalid={Boolean(error)} />}
            </FormField>
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Build UBL
            </Button>
            <Button type="button" variant="ghost" onClick={() => setText(PEPPOL_EXAMPLE)}>
              Reset to example
            </Button>
          </div>
        </form>
      </Card>
      <ProblemAlert problem={failure} title="The invoice could not be read" />
      {result ? (
        result.valid ? (
          <Card title="UBL 2.1 document" headingLevel={2}>
            <div className={styles.stack}>
              <Alert tone="success" title="Ready for schematron" live="status">
                The required elements, VAT categories and totals hold. CEN and Peppol schematron validation and sending through the access point are not available yet.
              </Alert>
              <pre className={styles.payload} aria-label="UBL XML">
                {result.xml}
              </pre>
            </div>
          </Card>
        ) : (
          <Card title="Blocked" headingLevel={2}>
            <div className={styles.stack}>
              <Alert tone="danger" title="The invoice cannot be sent" live="status">
                Correct the findings below and build again. No XML is produced for a blocked invoice.
              </Alert>
              <IssueList issues={result.issues} label="Blocking findings" />
            </div>
          </Card>
        )
      ) : null}
    </div>
  );
}
