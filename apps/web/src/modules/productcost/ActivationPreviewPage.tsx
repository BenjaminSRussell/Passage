import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, previewActivation, type ActivationPreview, type JournalLine, type Problem, type RevalLine } from "./api/client";
import { amount, dash, isDecimal, isSignedDecimal } from "./format";
import { LineGrid, type LineField } from "./LineGrid";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type LineKey = keyof RevalLine;
type LineValues = Record<LineKey, string>;

interface Values {
  currency: string;
  lines: LineValues[];
}

const lineFields: LineField<LineKey>[] = [
  { name: "itemId", label: "Item", mono: true },
  { name: "siteId", label: "Site", mono: true },
  { name: "qty", label: "On-hand quantity", decimal: true },
  { name: "oldStd", label: "Current standard", decimal: true },
  { name: "newStd", label: "New standard", decimal: true },
];

const emptyLine = (): LineValues => ({ itemId: "", siteId: "", qty: "", oldStd: "", newStd: "" });

/** AC-39.4: 1,000 on hand at 10.00 with a new standard of 10.50. */
export const ac394: Values = {
  currency: "USD",
  lines: [{ itemId: "RM-301", siteId: "PLANT-1", qty: "1000", oldStd: "10.00", newStd: "10.50" }],
};

/** Checks that mirror the service, and the request the form sends. */
export function validatePreview(values: Values): { errors: Record<string, string>; lines?: RevalLine[]; currency?: string } {
  const errors: Record<string, string> = {};
  const currency = values.currency.trim().toUpperCase();
  if (currency !== "" && !/^[A-Z]{3}$/.test(currency))
    errors.currency = "Enter a three-letter currency code such as USD, or leave it empty.";
  if (values.lines.length === 0) errors.lines = "Add at least one position.";
  const lines = values.lines.map((ln, i) => {
    const at = (k: LineKey) => `lines[${i}].${k}`;
    if (ln.itemId.trim() === "") errors[at("itemId")] = "Enter the item.";
    if (ln.siteId.trim() === "") errors[at("siteId")] = "Enter the site.";
    if (!isSignedDecimal(ln.qty)) errors[at("qty")] = "Enter the on-hand quantity.";
    if (!isDecimal(ln.oldStd)) errors[at("oldStd")] = "Enter the current standard, 0 or more.";
    if (!isDecimal(ln.newStd)) errors[at("newStd")] = "Enter the new standard, 0 or more.";
    return { itemId: ln.itemId.trim(), siteId: ln.siteId.trim(), qty: ln.qty.trim(), oldStd: ln.oldStd.trim(), newStd: ln.newStd.trim() };
  });
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, lines, currency: currency || undefined };
}

type PreviewRow = ActivationPreview["lines"][number];

const lineColumns: DataTableColumn<PreviewRow>[] = [
  { key: "itemId", header: "Item", mono: true },
  { key: "siteId", header: "Site", mono: true },
  { key: "qty", header: "On hand", align: "end" },
  { key: "oldStd", header: "Old standard", align: "end", render: (r) => amount(r.oldStd) },
  { key: "newStd", header: "New standard", align: "end", render: (r) => amount(r.newStd) },
  { key: "amount", header: "Revaluation", align: "end", render: (r) => amount(r.amount) },
];

const journalColumns: DataTableColumn<JournalLine>[] = [
  { key: "account", header: "Account" },
  { key: "debit", header: "Debit", align: "end", render: (j) => (j.debit === "0" ? dash : amount(j.debit)) },
  { key: "credit", header: "Credit", align: "end", render: (j) => (j.credit === "0" ? dash : amount(j.credit)) },
];

/** Preview the on-hand revaluation of activating new standards (FR-39.6, CST-0712). Nothing is posted. */
export function ActivationPreviewPage() {
  const [values, setValues] = useState<Values>(ac394);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<ActivationPreview | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, lines, currency } = validatePreview(values);
    setErrors(found);
    setFailure(null);
    if (!lines) return;
    setBusy(true);
    try {
      setPreview(await previewActivation({ currency, lines }));
    } catch (err) {
      const problem = describeProblem(err);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      setPreview(null);
    } finally {
      setBusy(false);
    }
  }

  const currencyLabel = preview?.currency ? ` ${preview.currency}` : "";

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Activation preview"
        description="Revaluation is on-hand quantity times the change in standard, per item and site. A gain debits inventory and credits the cost change variance; a loss does the reverse. Activation and posting arrive with costing versions."
      />
      <Card title="On-hand positions" headingLevel={3}>
        <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate>
          <FormField
            id="activation-currency"
            label="Currency"
            hint="Sets the posting precision: two decimals by default, none for JPY."
            optional
            error={errors.currency}
          >
            {(control) => (
              <Input
                {...control}
                value={values.currency}
                onChange={(e) => setValues({ ...values, currency: e.target.value })}
                invalid={Boolean(errors.currency)}
              />
            )}
          </FormField>
          <LineGrid
            idPrefix="activation-line"
            noun="Position"
            fields={lineFields}
            lines={values.lines}
            errors={errors}
            newLine={emptyLine}
            onChange={(lines) => setValues({ ...values, lines })}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The preview was not computed" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Preview revaluation
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable
          label="Revaluation by item and site"
          columns={lineColumns}
          rows={[]}
          getRowKey={(r) => `${r.itemId}-${r.siteId}`}
          loading
        />
      ) : preview ? (
        <>
          <Alert tone="info" title="Activation will revalue inventory">
            Total revaluation {amount(preview.total)}
            {currencyLabel} (CST-0712). Confirm this amount when you activate.
          </Alert>
          <DataTable
            label="Revaluation by item and site"
            columns={lineColumns}
            rows={preview.lines}
            getRowKey={(r, i) => `${r.itemId}-${r.siteId}-${i}`}
            empty={{ title: "No positions", description: "Nothing on hand to revalue." }}
          />
          <DataTable
            label="Revaluation journal"
            columns={journalColumns}
            rows={preview.journal}
            getRowKey={(j) => j.account}
            empty={{ title: "Nothing to post", description: "The new standards equal the current ones, so no journal posts." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="receipt"
            headingLevel={3}
            title="No preview yet"
            description="Enter the on-hand positions with their current and new standards to see the revaluation and its journal."
          />
        </Card>
      )}
    </div>
  );
}
