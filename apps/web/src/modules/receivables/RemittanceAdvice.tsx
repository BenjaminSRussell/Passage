import { useState, type ChangeEvent, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Textarea, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { ADVICE_FORMATS, describeProblem, importAdvice, PAYMENT_MODES, type AdviceFormat, type AdviceInput, type AdviceLineResult, type AdviceOutcome, type AdviceResult, type PaymentMode, type Problem } from "./api/client";
import { CURRENCY, DAY, MODE_LABELS, UUID } from "./format";
import { CommandFailure } from "./parts";
import styles from "./pages.module.css";

const MAX_BYTES = 3 * 1024 * 1024;

const FORMAT_LABELS: Record<AdviceFormat, string> = { CSV: "CSV", JSON: "JSON", EDI820: "EDI 820" };

export const OUTCOME_LABELS: Record<AdviceOutcome, string> = {
  ATTACHED: "Attached to the payment",
  ADVISED: "Recorded as advised, no bank credit yet",
  UNMATCHED: "No payment with this reference",
  AMBIGUOUS: "Several payments have this reference",
  ALREADY_APPLIED: "The payment is already applied",
  INVALID: "Not used",
};

const OUTCOME_TONES: Record<AdviceOutcome, BadgeTone> = {
  ATTACHED: "success",
  ADVISED: "info",
  UNMATCHED: "warning",
  AMBIGUOUS: "warning",
  ALREADY_APPLIED: "neutral",
  INVALID: "danger",
};

type Errors = Record<string, string>;

export interface AdviceDraft {
  legalEntityId: string;
  format: AdviceFormat;
  content: string;
  autoApply: boolean;
  createMissing: boolean;
  currency: string;
  valueDate: string;
  paymentMode: PaymentMode;
}

export function validateAdvice(v: AdviceDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (v.content.trim() === "") errors.content = "Choose the advice file or paste its content.";
  else if (new Blob([v.content]).size > MAX_BYTES) errors.content = "The file is larger than 3 MB; split it.";
  if (v.createMissing) {
    if (!CURRENCY.test(v.currency.trim())) errors.currency = "Enter the three-letter currency of the payments, such as USD.";
    if (v.valueDate !== "" && !DAY.test(v.valueDate)) errors.valueDate = "Enter a date, or leave it empty for today.";
  }
  return errors;
}

const lineColumns: DataTableColumn<AdviceLineResult>[] = [
  { key: "paymentRef", header: "Payment reference", mono: true },
  { key: "outcome", header: "Result", render: (row) => <Badge tone={OUTCOME_TONES[row.outcome] ?? "neutral"}>{OUTCOME_LABELS[row.outcome] ?? row.outcome}</Badge> },
  { key: "detail", header: "Detail", render: (row) => row.detail ?? "" },
];

/**
 * Import a payer's remittance advice (CSV, JSON or EDI 820, FR-30.4). Each line names a payment by its reference and the invoices it
 * pays. The references go onto the payment, so the matcher can apply it; a line that names no payment can be recorded as an advised
 * payment that books no cash until the bank credit arrives (30-4).
 */
export function RemittanceAdvicePage() {
  const [values, setValues] = useState<AdviceDraft>({ legalEntityId: "", format: "CSV", content: "", autoApply: false, createMissing: false, currency: "", valueDate: "", paymentMode: "WIRE" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<AdviceResult | null>(null);
  const [busy, setBusy] = useState(false);

  function update(patch: Partial<AdviceDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
  }

  function chooseFile(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    if (!file) return;
    if (file.size > MAX_BYTES) {
      setErrors((prev) => ({ ...prev, content: "The file is larger than 3 MB; split it." }));
      return;
    }
    const name = file.name.toLowerCase();
    const guess: AdviceFormat | null = name.endsWith(".csv") ? "CSV" : name.endsWith(".json") ? "JSON" : name.endsWith(".edi") || name.endsWith(".x12") || name.endsWith(".820") ? "EDI820" : null;
    void file.text().then((content) => update({ content, ...(guess ? { format: guess } : {}) }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateAdvice(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const body: AdviceInput = { legalEntityId: values.legalEntityId.trim(), format: values.format, content: values.content };
      if (values.autoApply) body.autoApply = true;
      if (values.createMissing) {
        body.createMissing = true;
        body.currency = values.currency.trim();
        body.paymentMode = values.paymentMode;
        if (values.valueDate !== "") body.valueDate = values.valueDate;
      }
      setResult(await importAdvice(body));
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Remittance advice"
        description="The payer's own list of the invoices a payment covers. Importing it puts those invoice numbers on the payment, which lets the matcher apply it with confidence."
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/payments">
            Payments
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="ra-entity" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <EntityLookup entity="legalEntity" {...c} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="ra-format" label="Format">
            {(c) => (
              <Select {...c} value={values.format} onChange={(e) => update({ format: e.target.value as AdviceFormat })}>
                {ADVICE_FORMATS.map((f) => (
                  <option key={f} value={f}>
                    {FORMAT_LABELS[f]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="ra-file" label="Advice file" optional hint="Or paste the content below.">
            {(c) => <Input {...c} type="file" accept=".csv,.json,.edi,.x12,.820,text/plain,application/json" onChange={chooseFile} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="ra-content" label="File content" hint="CSV needs a header row with paymentRef, reference and amount." error={errors.content}>
              {(c) => <Textarea {...c} mono rows={9} spellCheck={false} value={values.content} onChange={(e) => update({ content: e.target.value })} invalid={Boolean(errors.content)} />}
            </FormField>
          </div>
          <span className={styles.check}>
            <input id="ra-auto" type="checkbox" checked={values.autoApply} onChange={(e) => update({ autoApply: e.target.checked })} />
            <label htmlFor="ra-auto">Apply the payments the matcher is sure of</label>
          </span>
          <span className={styles.check}>
            <input id="ra-missing" type="checkbox" checked={values.createMissing} onChange={(e) => update({ createMissing: e.target.checked })} />
            <label htmlFor="ra-missing">Record lines with no bank credit yet as advised payments</label>
          </span>
          {values.createMissing ? (
            <>
              <FormField id="ra-currency" label="Currency" hint="Advice files do not name one." error={errors.currency}>
                {(c) => <Input {...c} className={styles.mono} value={values.currency} onChange={(e) => update({ currency: e.target.value.toUpperCase() })} invalid={Boolean(errors.currency)} />}
              </FormField>
              <FormField id="ra-date" label="Value date" optional hint="Leave empty for today." error={errors.valueDate}>
                {(c) => <Input {...c} type="date" value={values.valueDate} onChange={(e) => update({ valueDate: e.target.value })} invalid={Boolean(errors.valueDate)} />}
              </FormField>
              <FormField id="ra-mode" label="Payment mode">
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
            </>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <CommandFailure title="Could not import the advice" problem={failure} />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Import advice
            </Button>
          </div>
        </form>
      </Card>
      {result ? (
        <>
          <Alert tone="success" title="Advice imported" live="status">
            {result.attached} attached
            {result.created > 0 ? `, ${result.created} recorded as advised` : ""}, {result.skipped - result.created} not used
            {result.autoApply ? `. The matcher applied ${result.autoApply.applied} payment(s); ${result.autoApply.suggested} are suggestions and ${result.autoApply.onAccount} stay on account` : ""}.
          </Alert>
          <Card title="Result per payment" headingLevel={3}>
            <DataTable label="Result per payment" columns={lineColumns} rows={result.lines} getRowKey={(row, i) => `${row.paymentRef}-${i}`} empty={{ title: "No lines", description: "" }} />
          </Card>
        </>
      ) : null}
    </div>
  );
}
