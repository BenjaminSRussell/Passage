import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import { ADJUSTMENT_TYPES, adjustBook, describeProblem, getAsset, newKey, type AdjustInput, type AdjustmentType, type AdjustResult, type Book, type Problem } from "./api/client";
import { DATE, isZero, money, sentence, SIGNED_DECIMAL, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface AdjustValues {
  bookId: string;
  type: AdjustmentType;
  transactionDate: string;
  amount: string;
}

type Errors = Partial<Record<keyof AdjustValues, string>>;

const hints: Record<AdjustmentType, string> = {
  AcquisitionAdj: "Changes cost: positive for an addition, negative for a credit note.",
  DepreciationAdj: "Changes accumulated depreciation, positive or negative.",
  Revaluation: "IAS 16 revaluation: changes cost, up or down.",
  Impairment: "Positive writes the book down; negative reverses an earlier impairment, capped at what was recorded.",
};

/** Checks that mirror the service (internal/api adjust and domain.Adjust). */
export function validateAdjustment(values: AdjustValues, book: Book | undefined): { errors: Errors; input?: AdjustInput } {
  const errors: Errors = {};
  const amount = values.amount.trim();
  if (!book) errors.bookId = "Choose an open book.";
  if (!DATE.test(values.transactionDate)) errors.transactionDate = "Choose the transaction date.";
  else if (book && values.transactionDate < book.inServiceDate) errors.transactionDate = `The date cannot be before the book's in-service date ${book.inServiceDate}.`;
  if (!SIGNED_DECIMAL.test(amount) || isZero(amount)) errors.amount = "Enter a non-zero amount, such as 500.00 or -500.00.";
  if (Object.keys(errors).length > 0 || !book) return { errors };
  return { errors, input: { bookId: book.id, type: values.type, transactionDate: values.transactionDate, amount } };
}

/** Post an acquisition or depreciation adjustment, a revaluation or an impairment on one book (FR-37.9). */
export function AdjustPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAsset(id, signal), id);
  const [values, setValues] = useState<AdjustValues>(() => ({ bookId: "", type: "Impairment", transactionDate: today(), amount: "" }));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<AdjustResult | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<AdjustValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Adjust book" />
        <Alert tone="danger" title="Could not load the asset" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Adjust book" />
        <span className="visually-hidden">Loading the asset…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const asset = state.data.value;
  const open = asset.books.filter((b) => b.status === "Open");
  const book = open.find((b) => b.id === values.bookId) ?? (values.bookId === "" ? open[0] : undefined);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAdjustment(values, book);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult(await adjustBook(id, input, key.current));
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
    const b = result.book;
    return (
      <div className={styles.stack}>
        <PageHeader title={`Adjust ${asset.assetNumber}`} />
        <Alert tone="success" title={`${sentence(result.transaction.type)} posted on ${b.bookCode}`} live="status">
          {`Cost ${money(b.cost, b.currency)}, accumulated ${money(b.accumulatedDepreciation, b.currency)}, NBV ${money(b.nbv, b.currency)}.`}
        </Alert>
        {result.warnings.map((w) => (
          <Alert key={w} tone="warning" title="Reversal limited" live="off">
            {w}
          </Alert>
        ))}
        <div className={styles.actions}>
          <Link className={buttonClassName("primary")} to={`/assets/${id}`}>
            Back to the asset
          </Link>
          <Button
            variant="ghost"
            onClick={() => {
              setResult(null);
              update({ amount: "" });
              state.reload();
            }}
          >
            Post another
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Adjust ${asset.assetNumber}`}
        description="Every adjustment keeps accumulated depreciation between 0 and cost, so NBV never goes negative. Later depreciation runs spread the new basis over the remaining periods."
      />
      {open.length === 0 ? (
        <Alert tone="info" title="No open book" live="off">
          Only an open book takes adjustments. Add a book first, or the asset is sold or scrapped.
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Adjust book">
          <FormField id="adjust-book" label="Book" error={errors.bookId}>
            {(control) => (
              <Select {...control} value={book?.id ?? ""} onChange={(e) => update({ bookId: e.target.value })} invalid={Boolean(errors.bookId)}>
                {open.map((b) => (
                  <option key={b.id} value={b.id}>
                    {`${b.bookCode} (NBV ${money(b.nbv, b.currency)})`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="adjust-type" label="Type" hint={hints[values.type]}>
            {(control) => (
              <Select {...control} value={values.type} onChange={(e) => update({ type: e.target.value as AdjustmentType })}>
                {ADJUSTMENT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {sentence(t)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="adjust-date" label="Transaction date" error={errors.transactionDate}>
            {(control) => <Input {...control} type="date" value={values.transactionDate} onChange={(e) => update({ transactionDate: e.target.value })} invalid={Boolean(errors.transactionDate)} />}
          </FormField>
          <FormField id="adjust-amount" label="Amount" hint={book ? `In ${book.currency}; negative to reduce.` : undefined} error={errors.amount}>
            {(control) => <Input {...control} value={values.amount} onChange={(e) => update({ amount: e.target.value })} invalid={Boolean(errors.amount)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not post the adjustment" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy} disabled={open.length === 0}>
              Post adjustment
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
