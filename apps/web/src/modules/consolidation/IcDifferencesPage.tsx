import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, EmptyState, PageHeader, type DataTableColumn } from "../../shell/ui";
import { previewIcDifferences, type Entry, type IcDifferencePreview, type IcDifferenceRequest } from "./api/client";
import { entryColumns } from "./entries";
import { Fields, type FieldSpec } from "./Fields";
import { amount, dash, isCurrency, isDecimal, isSignedDecimal } from "./format";
import { RowGrid, type RowField } from "./RowGrid";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";

type HeadKey = "currency" | "threshold";
type PairKey = "pair" | "receivable" | "payable";

export interface IcDifferenceValues {
  head: Record<HeadKey, string>;
  pairs: Record<PairKey, string>[];
}

const headFields: FieldSpec<HeadKey>[] = [
  { name: "currency", label: "Group currency", mono: true },
  { name: "threshold", label: "Difference threshold", decimal: true, optional: true, hint: "Defaults to 1,000.00 in the group currency." },
];

const pairFields: RowField<PairKey>[] = [
  { name: "pair", label: "Partner pair", mono: true },
  { name: "receivable", label: "Receivable side", decimal: true },
  { name: "payable", label: "Payable side", decimal: true },
];

/** BR-40.15: one pair within the default threshold, one above it. */
export const samplePairs: IcDifferenceValues = {
  head: { currency: "USD", threshold: "" },
  pairs: [
    { pair: "P-S1", receivable: "50000.00", payable: "49400.00" },
    { pair: "P-S2", receivable: "20000.00", payable: "22500.00" },
  ],
};

/** Checks that mirror the service, and the request the form sends. */
export function validateIcDifferences(values: IcDifferenceValues): Checked<IcDifferenceRequest> {
  const errors: Record<string, string> = {};
  const currency = values.head.currency.trim().toUpperCase();
  const threshold = values.head.threshold.trim();
  if (!isCurrency(currency)) errors.currency = "Enter a three-letter currency code such as USD.";
  if (threshold !== "" && !isDecimal(threshold)) errors.threshold = "Enter a threshold, 0 or more, or leave it empty.";
  const pairs = values.pairs.map((p, i) => {
    const at = (k: PairKey) => `pairs[${i}].${k}`;
    if (p.pair.trim() === "") errors[at("pair")] = "Enter the partner pair.";
    if (!isSignedDecimal(p.receivable)) errors[at("receivable")] = "Enter the receivable side.";
    if (!isSignedDecimal(p.payable)) errors[at("payable")] = "Enter the payable side.";
    return { pair: p.pair.trim(), receivable: p.receivable.trim(), payable: p.payable.trim() };
  });
  if (pairs.length === 0) errors.pairs = "Add at least one partner pair.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, request: { currency, threshold: threshold || undefined, pairs } };
}

type PairRow = IcDifferencePreview["pairs"][number];

const pairColumns: DataTableColumn<PairRow>[] = [
  { key: "pair", header: "Partner pair", mono: true },
  { key: "receivable", header: "Receivable", align: "end", render: (p) => amount(p.receivable) },
  { key: "payable", header: "Payable", align: "end", render: (p) => amount(p.payable) },
  { key: "difference", header: "Difference", align: "end", render: (p) => amount(p.difference) },
  {
    key: "status",
    header: "Status",
    render: (p) => (p.status === "Eliminated" ? <Badge tone="success">Eliminated</Badge> : <Badge tone="danger">Above threshold</Badge>),
  },
  { key: "message", header: "Message", render: (p) => (p.message ? `${p.message} (${p.code})` : dash) },
];

type PairEntry = Entry & { pair: string };

const pairEntryColumns = entryColumns<PairEntry>([{ key: "pair", header: "Partner pair", mono: true }]);

/** Two-sided intercompany elimination per partner pair with the difference threshold (FR-40.17, BR-40.15). */
export function IcDifferencesPage() {
  const [values, setValues] = useState<IcDifferenceValues>(samplePairs);
  const { errors, failure, busy, result, run } = usePreview(previewIcDifferences);

  function submit(event: FormEvent) {
    event.preventDefault();
    void run(validateIcDifferences(values));
  }

  const entries: PairEntry[] = result ? result.pairs.flatMap((p) => p.entries.map((e) => ({ ...e, pair: p.pair }))) : [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Intercompany differences"
        description="Each partner pair's receivable side is eliminated against its payable side after translation. A difference up to the threshold goes to the intercompany difference account; a larger one stops the elimination task until it is resolved in reconciliation. Running this over the consolidated balances arrives with consolidation runs."
      />
      <Card title="Partner pairs" headingLevel={3}>
        <form className={styles.form} onSubmit={submit} noValidate>
          <Fields idPrefix="icd" fields={headFields} values={values.head} errors={errors} onChange={(head) => setValues({ ...values, head })} />
          <RowGrid
            idPrefix="icd-pair"
            path="pairs"
            noun="Pair"
            fields={pairFields}
            rows={values.pairs}
            errors={errors}
            newRow={() => ({ pair: "", receivable: "", payable: "" })}
            onChange={(pairs) => setValues({ ...values, pairs })}
          />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The elimination was not built" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Eliminate pairs
            </Button>
          </div>
        </form>
      </Card>
      {busy ? (
        <DataTable label="Partner pairs" columns={pairColumns} rows={[]} getRowKey={(p) => p.pair} loading />
      ) : result ? (
        <>
          {result.taskStatus === "Error" ? (
            <Alert tone="danger" title="Elimination task ends in Error">
              At least one pair differs by more than {amount(result.threshold)} {result.currency}. Resolve it in intercompany reconciliation and run again.
            </Alert>
          ) : (
            <Alert tone="success" title="Every pair eliminated">
              Differences up to {amount(result.threshold)} {result.currency} were posted to the intercompany difference account.
            </Alert>
          )}
          <DataTable label="Partner pairs" columns={pairColumns} rows={result.pairs} getRowKey={(p, i) => `${p.pair}-${i}`} />
          <DataTable
            label="Elimination entries"
            columns={pairEntryColumns}
            rows={entries}
            getRowKey={(e, i) => `${e.pair}-${e.account}-${i}`}
            empty={{ title: "No entries", description: "No pair was within the threshold." }}
          />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="grid"
            headingLevel={3}
            title="No elimination yet"
            description="The form holds two pairs: one within the default threshold and one above it. Eliminate them, or enter your own."
          />
        </Card>
      )}
    </div>
  );
}
