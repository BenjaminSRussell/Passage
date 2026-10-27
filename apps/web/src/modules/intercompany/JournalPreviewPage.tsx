import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  previewJournal,
  type JournalLeg,
  type JournalPreview,
  type JournalPreviewRequest,
  type MatrixRow,
  type Problem,
  type Side,
} from "./api/client";
import { amount, dash, isCurrency, isPositive } from "./format";
import { RowGrid, type RowField } from "./RowGrid";
import styles from "./pages.module.css";

type MatrixKey = keyof MatrixRow;

export interface JournalValues {
  entity: string;
  account: string;
  side: Side;
  amount: string;
  currency: string;
  offsetEntity: string;
  offsetAccount: string;
  counterCurrency: string;
  rate: string;
  matrix: Record<MatrixKey, string>[];
}

const matrixFields: RowField<MatrixKey>[] = [
  { name: "entityA", label: "Entity A", mono: true },
  { name: "entityB", label: "Entity B", mono: true },
  { name: "dueFrom", label: "Due from B (in A)", mono: true },
  { name: "dueTo", label: "Due to A (in B)", mono: true },
];

const emptyRow = (): Record<MatrixKey, string> => ({ entityA: "", entityB: "", dueFrom: "", dueTo: "" });

/** AC-40.4: a USD 1,000.00 expense in entity A offset to entity B's cash. */
export const ac404: JournalValues = {
  entity: "A",
  account: "6100-EXPENSE",
  side: "debit",
  amount: "1000.00",
  currency: "USD",
  offsetEntity: "B",
  offsetAccount: "1000-CASH",
  counterCurrency: "",
  rate: "",
  matrix: [{ entityA: "A", entityB: "B", dueFrom: "1500-DUE-FROM-B", dueTo: "2500-DUE-TO-A" }],
};

/** Checks that mirror the service, and the request the form sends. */
export function validateJournal(values: JournalValues): { errors: Record<string, string>; request?: JournalPreviewRequest } {
  const errors: Record<string, string> = {};
  const entity = values.entity.trim();
  const offsetEntity = values.offsetEntity.trim();
  const currency = values.currency.trim().toUpperCase();
  const counter = values.counterCurrency.trim().toUpperCase();
  const rate = values.rate.trim();
  if (entity === "") errors.entity = "Enter the entity that books the line.";
  if (values.account.trim() === "") errors.account = "Enter the account of the line.";
  if (offsetEntity === "") errors.offsetEntity = "Enter the offset entity.";
  else if (offsetEntity === entity) errors.offsetEntity = "The offset entity must differ from the entity.";
  if (values.offsetAccount.trim() === "") errors.offsetAccount = "Enter the account in the offset entity.";
  if (!isPositive(values.amount)) errors.amount = "Enter an amount above 0 with at most 6 decimals.";
  if (!isCurrency(currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (counter !== "" && !isCurrency(counter)) errors.counterCurrency = "Enter a three-letter currency code, or leave it empty.";
  if (rate !== "" && !isPositive(rate)) errors.rate = "Enter a rate above 0.";
  if (rate === "" && counter !== "" && counter !== currency) errors.rate = "Enter the document rate when the offset entity's currency differs.";
  const matrix = values.matrix.map((row, i) => {
    for (const f of matrixFields) {
      if (row[f.name].trim() === "") errors[`matrix[${i}].${f.name}`] = `Enter ${f.label.toLowerCase()}.`;
    }
    return { entityA: row.entityA.trim(), entityB: row.entityB.trim(), dueFrom: row.dueFrom.trim(), dueTo: row.dueTo.trim() };
  });
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    request: {
      entity,
      account: values.account.trim(),
      side: values.side,
      amount: values.amount.trim(),
      currency,
      offsetEntity,
      offsetAccount: values.offsetAccount.trim(),
      counterCurrency: counter || undefined,
      rate: rate || undefined,
      matrix,
    },
  };
}

const legColumns: DataTableColumn<JournalLeg>[] = [
  { key: "entity", header: "Entity", mono: true },
  { key: "account", header: "Account", mono: true },
  { key: "debit", header: "Debit", align: "end", render: (l) => (l.side === "debit" ? amount(l.amount) : dash) },
  { key: "credit", header: "Credit", align: "end", render: (l) => (l.side === "credit" ? amount(l.amount) : dash) },
  { key: "currency", header: "Currency", mono: true },
];

type TextKey = Exclude<keyof JournalValues, "side" | "matrix">;

const lineFields: { name: TextKey; label: string; hint?: string; optional?: boolean; mono?: boolean; decimal?: boolean }[] = [
  { name: "entity", label: "Entity", mono: true },
  { name: "account", label: "Account", mono: true },
  { name: "amount", label: "Amount", decimal: true },
  { name: "currency", label: "Currency", mono: true },
  { name: "offsetEntity", label: "Offset entity", mono: true },
  { name: "offsetAccount", label: "Offset account", mono: true, hint: "The account in the offset entity that takes the opposite side." },
  { name: "counterCurrency", label: "Offset entity currency", optional: true, mono: true, hint: "Leave empty when both entities book in the same currency." },
  { name: "rate", label: "Document rate", optional: true, decimal: true, hint: "From the line currency to the offset entity's currency." },
];

/** Derive the due-to / due-from journal of one intercompany line (BR-40.4, AC-40.4). Nothing is posted. */
export function JournalPreviewPage() {
  const [values, setValues] = useState<JournalValues>(ac404);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<JournalPreview | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, request } = validateJournal(values);
    setErrors(found);
    setFailure(null);
    if (!request) return;
    setBusy(true);
    try {
      setPreview(await previewJournal(request));
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setPreview(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Intercompany journal"
        description="The line posts in its entity against Due from the offset entity; the offset entity posts Due to against its own account, converted at the document rate. Accounts come from the intercompany accounting matrix. Posting arrives with the journal slice."
      />
      <Card title="Intercompany line" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <FormField id="ic-side" label="Side">
            {(control) => (
              <Select {...control} value={values.side} onChange={(e) => setValues({ ...values, side: e.target.value as Side })}>
                <option value="debit">Debit</option>
                <option value="credit">Credit</option>
              </Select>
            )}
          </FormField>
          {lineFields.map((f) => (
            <FormField key={f.name} id={`ic-${f.name}`} label={f.label} hint={f.hint} optional={f.optional} error={errors[f.name]}>
              {(control) => (
                <Input
                  {...control}
                  inputMode={f.decimal ? "decimal" : undefined}
                  className={f.mono ? styles.mono : undefined}
                  value={values[f.name]}
                  onChange={(e) => setValues({ ...values, [f.name]: e.target.value })}
                  invalid={Boolean(errors[f.name])}
                />
              )}
            </FormField>
          ))}
          <h4 className={styles.section}>Accounting matrix</h4>
          <RowGrid
            idPrefix="ic-matrix"
            path="matrix"
            noun="Matrix row"
            fields={matrixFields}
            rows={values.matrix}
            errors={errors}
            newRow={emptyRow}
            onChange={(matrix) => setValues({ ...values, matrix })}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The journal was not derived" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview journal
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Journal legs" columns={legColumns} rows={[]} getRowKey={(_, i) => String(i)} loading />
      ) : preview ? (
        <>
          <Alert tone={preview.balanced ? "success" : "danger"} title={preview.balanced ? "Balanced in each entity" : "Not balanced"}>
            Both entities post under pair <span className={styles.mono}>{preview.pairId}</span>.
          </Alert>
          <DataTable
            label="Journal legs"
            columns={legColumns}
            rows={preview.legs}
            getRowKey={(l, i) => `${l.entity}-${l.account}-${i}`}
            empty={{ title: "No legs", description: "The service returned no journal legs." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="receipt"
            headingLevel={3}
            title="No journal yet"
            description="Enter an intercompany line and the matrix row for its entity pair to see the legs both entities would post."
          />
        </Card>
      )}
    </div>
  );
}
