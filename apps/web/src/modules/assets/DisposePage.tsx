import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, type DataTableColumn } from "../../shell/ui";
import { describeProblem, disposeAsset, getAsset, newKey, type Book, type DisposeInput, type Disposal, type Problem, type Transaction } from "./api/client";
import { DATE, DECIMAL, money, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface DisposalValues {
  kind: "sale" | "scrap";
  percentage: string;
  proceeds: string;
  transactionDate: string;
}

type Errors = Partial<Record<keyof DisposalValues, string>>;

const PERCENT = /^\d{1,3}(\.\d{1,4})?$/;

/** Checks that mirror the service (internal/api dispose): percentage in (0, 100], no proceeds on a scrap. */
export function validateDisposal(values: DisposalValues): { errors: Errors; input?: DisposeInput } {
  const errors: Errors = {};
  const pct = values.percentage.trim();
  if (!PERCENT.test(pct) || /^0+(\.0+)?$/.test(pct) || Number(pct) > 100) errors.percentage = "Enter a percentage above 0 and at most 100.";
  const proceeds = values.proceeds.trim();
  if (values.kind === "sale" && !DECIMAL.test(proceeds)) errors.proceeds = "Enter the sale proceeds as a decimal, or 0.";
  if (!DATE.test(values.transactionDate)) errors.transactionDate = "Choose the disposal date.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      kind: values.kind,
      percentage: pct,
      ...(values.kind === "sale" ? { proceeds } : {}),
      transactionDate: values.transactionDate,
    },
  };
}

const bookColumns: DataTableColumn<Book>[] = [
  { key: "bookCode", header: "Book", mono: true },
  { key: "cost", header: "Cost", align: "end", render: (b) => money(b.cost, b.currency) },
  { key: "accumulatedDepreciation", header: "Accumulated", align: "end", render: (b) => money(b.accumulatedDepreciation, b.currency) },
  { key: "nbv", header: "NBV", align: "end", render: (b) => money(b.nbv, b.currency) },
];

const resultColumns: DataTableColumn<Transaction>[] = [
  { key: "bookCode", header: "Book", mono: true },
  { key: "costAmount", header: "Cost", align: "end", render: (t) => money(t.costAmount, t.currency) },
  { key: "accumulatedAmount", header: "Accumulated", align: "end", render: (t) => money(t.accumulatedAmount, t.currency) },
  { key: "proceedsAmount", header: "Proceeds", align: "end", render: (t) => money(t.proceedsAmount, t.currency) },
  { key: "gainLoss", header: "Gain or loss", align: "end", render: (t) => money(t.gainLoss, t.currency) },
];

/** Dispose of an asset by sale or scrap, fully or in part (FR-37.8), on every Open book. */
export function DisposePage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAsset(id, signal), id);
  const [values, setValues] = useState<DisposalValues>(() => ({ kind: "sale", percentage: "100", proceeds: "0", transactionDate: today() }));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<Disposal | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<DisposalValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Dispose of asset" />
        <Alert tone="danger" title="Could not load the asset" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Dispose of asset" />
        <span className="visually-hidden">Loading the asset…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: asset, etag } = state.data;
  const openBooks = asset.books.filter((b) => b.status === "Open");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateDisposal(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult(await disposeAsset(id, etag, input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as Errors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (result) {
    return (
      <div className={styles.stack}>
        <PageHeader title={`Dispose of ${asset.assetNumber}`} />
        <Alert tone="success" title="Disposal posted" live="status">
          {result.asset.status === "Open" ? "The remaining share of the asset stays open." : `The asset is now ${result.asset.status.toLowerCase()}.`}
        </Alert>
        <Card title="Posted per book">
          <DataTable label="Disposal by book" columns={resultColumns} rows={result.transactions} getRowKey={(t) => t.id} />
        </Card>
        <div>
          <Link className={buttonClassName("primary")} to={`/assets/${id}`}>
            Back to the asset
          </Link>
        </div>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Dispose of ${asset.assetNumber}`}
        description="The disposal applies to every open book. Each book reverses the disposed share of cost and accumulated depreciation and posts its own gain or loss against the proceeds."
      />
      <Card title="Open books">
        <DataTable
          label="Open books"
          columns={bookColumns}
          rows={openBooks}
          getRowKey={(b) => b.id}
          empty={{ title: "Nothing to dispose of", description: "The asset has no open book." }}
        />
      </Card>
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Dispose of asset">
          <FormField id="dispose-kind" label="Disposal type">
            {(control) => (
              <Select {...control} value={values.kind} onChange={(e) => update({ kind: e.target.value as DisposalValues["kind"] })}>
                <option value="sale">Sale</option>
                <option value="scrap">Scrap</option>
              </Select>
            )}
          </FormField>
          <FormField id="dispose-percentage" label="Percentage" hint="100 disposes of the whole asset." error={errors.percentage}>
            {(control) => <Input {...control} value={values.percentage} onChange={(e) => update({ percentage: e.target.value })} invalid={Boolean(errors.percentage)} />}
          </FormField>
          {values.kind === "sale" ? (
            <FormField id="dispose-proceeds" label="Proceeds" hint="Sale price in each book's currency." error={errors.proceeds}>
              {(control) => <Input {...control} value={values.proceeds} onChange={(e) => update({ proceeds: e.target.value })} invalid={Boolean(errors.proceeds)} />}
            </FormField>
          ) : null}
          <FormField id="dispose-date" label="Disposal date" error={errors.transactionDate}>
            {(control) => (
              <Input {...control} type="date" value={values.transactionDate} onChange={(e) => update({ transactionDate: e.target.value })} invalid={Boolean(errors.transactionDate)} />
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "AST-0006" ? "The asset changed since you opened it" : "Could not post the disposal"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="danger" loading={busy} disabled={openBooks.length === 0}>
              Post disposal
            </Button>
            <Link className={buttonClassName("ghost")} to={`/assets/${id}`}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
