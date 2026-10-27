import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, PageHeader, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { previewGroupJournal, type DocumentType, type GroupJournalLine, type GroupJournalRequest } from "./api/client";
import { Fields, type FieldSpec } from "./Fields";
import { amount, isCount, isCurrency, isDecimal, isPositive } from "./format";
import { RowGrid, type RowField } from "./RowGrid";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";

type HeadKey =
  | "periodKey"
  | "documentType"
  | "unitId"
  | "currency"
  | "reason"
  | "attachmentRef"
  | "carryForward"
  | "fiscalYearStartMonth"
  | "approvalThreshold";
type LineKey = "fsItem" | "debit" | "credit" | "partnerUnit";

export interface JournalValues {
  head: Record<HeadKey, string>;
  lines: Record<LineKey, string>[];
}

const documentTypes: { value: DocumentType; label: string }[] = [
  { value: "Adjustment", label: "Adjustment" },
  { value: "Reclassification", label: "Reclassification" },
  { value: "Elimination", label: "Elimination" },
  { value: "CoI", label: "Consolidation of investments" },
  { value: "Cumulative", label: "Cumulative (carries past year end)" },
];

const headFields: FieldSpec<HeadKey>[] = [
  { name: "periodKey", label: "Period", mono: true, hint: "Such as 2026-11." },
  { name: "documentType", label: "Document type", options: documentTypes },
  { name: "unitId", label: "Unit", mono: true, optional: true, hint: "Leave empty for a group-level journal." },
  { name: "currency", label: "Group currency", mono: true },
  { name: "reason", label: "Reason" },
  { name: "attachmentRef", label: "Attachment reference", mono: true },
  {
    name: "carryForward",
    label: "Carry forward",
    options: [
      { value: "no", label: "This period only" },
      { value: "yes", label: "Later periods of the fiscal year" },
    ],
  },
  { name: "fiscalYearStartMonth", label: "Fiscal year starts in month", optional: true, hint: "1 to 12; defaults to January." },
  { name: "approvalThreshold", label: "Approval threshold", decimal: true, optional: true, hint: "Defaults to 100,000.00." },
];

const lineFields: RowField<LineKey>[] = [
  { name: "fsItem", label: "FS item", mono: true },
  { name: "debit", label: "Debit", decimal: true, optional: true },
  { name: "credit", label: "Credit", decimal: true, optional: true },
  { name: "partnerUnit", label: "Partner unit", mono: true, optional: true },
];

const emptyLine = (): Record<LineKey, string> => ({ fsItem: "", debit: "", credit: "", partnerUnit: "" });

/** A USD 120,000.00 group audit accrual carried forward in a fiscal year that starts in October. */
export const sampleJournal: JournalValues = {
  head: {
    periodKey: "2026-11",
    documentType: "Adjustment",
    unitId: "",
    currency: "USD",
    reason: "Accrue group audit fee",
    attachmentRef: "ENG-LETTER-2026",
    carryForward: "yes",
    fiscalYearStartMonth: "10",
    approvalThreshold: "",
  },
  lines: [
    { fsItem: "AUDIT_FEES", debit: "120000.00", credit: "", partnerUnit: "" },
    { fsItem: "ACCRUALS", debit: "", credit: "120000.00", partnerUnit: "" },
  ],
};

/** Checks that mirror the service, and the request the form sends. Balance is the service's check (CNS-0903). */
export function validateJournal(values: JournalValues): Checked<GroupJournalRequest> {
  const errors: Record<string, string> = {};
  const h = (k: HeadKey) => values.head[k].trim();
  const currency = h("currency").toUpperCase();
  if (!/^\d{4}-(0[1-9]|1[0-2])$/.test(h("periodKey"))) errors.periodKey = "Enter a period such as 2026-11.";
  if (!documentTypes.some((d) => d.value === h("documentType"))) errors.documentType = "Choose a document type.";
  if (!isCurrency(currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (h("reason") === "") errors.reason = "Every top-side entry needs a reason.";
  if (h("attachmentRef") === "") errors.attachmentRef = "Every top-side entry needs an attachment.";
  const month = h("fiscalYearStartMonth");
  if (month !== "" && !(isCount(month) && Number(month) >= 1 && Number(month) <= 12)) errors.fiscalYearStartMonth = "Enter a month from 1 to 12.";
  if (h("approvalThreshold") !== "" && !isPositive(h("approvalThreshold"))) errors.approvalThreshold = "Enter a threshold above 0, or leave it empty.";
  if (values.lines.length < 2) errors.lines = "A top-side journal needs at least two lines.";
  const lines: GroupJournalLine[] = values.lines.map((row, i) => {
    const at = (k: LineKey) => `lines[${i}].${k}`;
    const v = (k: LineKey) => row[k].trim();
    if (v("fsItem") === "") errors[at("fsItem")] = "Enter the FS item.";
    const debit = v("debit");
    const credit = v("credit");
    if (debit !== "" && !isDecimal(debit)) errors[at("debit")] = "Enter a debit, 0 or more.";
    else if (credit !== "" && !isDecimal(credit)) errors[at("credit")] = "Enter a credit, 0 or more.";
    else if (isPositive(debit) === isPositive(credit)) errors[at("debit")] = "Enter a debit or a credit, not both.";
    return { fsItem: v("fsItem"), debit: debit || undefined, credit: credit || undefined, partnerUnit: v("partnerUnit") || undefined };
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    request: {
      periodKey: h("periodKey"),
      documentType: h("documentType") as DocumentType,
      unitId: h("unitId") || undefined,
      currency,
      reason: h("reason"),
      attachmentRef: h("attachmentRef"),
      carryForward: h("carryForward") === "yes",
      fiscalYearStartMonth: month === "" ? undefined : Number(month),
      approvalThreshold: h("approvalThreshold") || undefined,
      lines,
    },
  };
}

const periodColumns: DataTableColumn<{ period: string }>[] = [{ key: "period", header: "Posts in period", mono: true }];

/** Check a top-side group journal before posting (FR-40.19, BR-40.18, CNS-0909). */
export function GroupJournalPage() {
  const [values, setValues] = useState<JournalValues>(sampleJournal);
  const { errors, failure, busy, result, run } = usePreview(previewGroupJournal);

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validateJournal(values));
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Top-side group journals"
        description="A consolidation accountant's manual entry for a group or one unit and period. Every entry needs a reason and an attachment, must balance, and needs approval at or above the threshold. A carried-forward entry posts again in each later period of the fiscal year; a cumulative one continues past year end. Posting, approval and re-applying entries to new run versions arrive with persistence."
      />
      <Card title="Journal" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <Fields idPrefix="gj" fields={headFields} values={values.head} errors={errors} onChange={(head) => setValues({ ...values, head })} />
          <h4 className={styles.section}>Lines</h4>
          <RowGrid idPrefix="gj-line" path="lines" noun="Line" fields={lineFields} rows={values.lines} errors={errors} newRow={emptyLine} onChange={(lines) => setValues({ ...values, lines })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The journal was not accepted" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Check journal
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Posting periods" columns={periodColumns} rows={[]} getRowKey={(_, i) => String(i)} loading />
      ) : result ? (
        <>
          <StatGroup label="Journal check">
            <StatTile label="Total" value={`${amount(result.total)} ${result.currency}`} />
            <StatTile label="Level" value={result.level} />
            <StatTile label="Approval" value={result.approvalRequired ? "Required" : "Not required"} />
          </StatGroup>
          {result.warnings.map((w) => (
            <Alert key={w.code} tone="warning" title="Approval required before posting">
              {w.message} ({w.code})
            </Alert>
          ))}
          {result.continuesPastYearEnd ? (
            <Alert tone="info" title="Cumulative entry">
              The entry keeps applying after the fiscal year end listed below.
            </Alert>
          ) : null}
          <DataTable
            label="Posting periods"
            columns={periodColumns}
            rows={result.periods.map((period) => ({ period }))}
            getRowKey={(p) => p.period}
            empty={{ title: "No periods", description: "The service returned no posting periods." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="note"
            headingLevel={3}
            title="No journal checked yet"
            description="The form holds a USD 120,000.00 group audit accrual carried forward to the fiscal year end. Check it, or enter your own journal."
          />
        </Card>
      )}
    </div>
  );
}
