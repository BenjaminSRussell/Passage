import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import { ADDITION_TYPES, addAddition, describeProblem, getAsset, newKey, type AdditionType, type Book, type NewAddition, type Problem } from "./api/client";
import { DATE, DECIMAL, isZero, money, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface AdditionValues {
  type: AdditionType;
  description: string;
  quantity: string;
  serialNumber: string;
  addedDate: string;
  financial: boolean;
  cost: string;
  /** Open books the financial addition posts to. */
  bookIds: string[];
}

type Errors = Partial<Record<keyof AdditionValues | "currency", string>>;

const QUANTITY = /^\d+(\.\d{1,4})?$/;

/**
 * Checks that mirror the service (internal/api addAddition). A financial
 * addition posts on the selected open books, which must share one currency;
 * selecting every open book sends no bookIds (the service default).
 */
export function validateAddition(values: AdditionValues, openBooks: Book[]): { errors: Errors; input?: NewAddition } {
  const errors: Errors = {};
  const description = values.description.trim();
  const quantity = values.quantity.trim();
  const serial = values.serialNumber.trim();
  const cost = values.cost.trim();
  if (description.length < 1 || description.length > 200) errors.description = "Describe the addition in up to 200 characters.";
  if (quantity !== "" && (!QUANTITY.test(quantity) || isZero(quantity))) errors.quantity = "Enter a quantity above 0 with at most 4 decimals.";
  if (serial.length > 64) errors.serialNumber = "Use at most 64 characters.";
  if (!DATE.test(values.addedDate)) errors.addedDate = "Choose the date of the addition.";
  const selected = openBooks.filter((b) => values.bookIds.includes(b.id));
  const currencies = new Set(selected.map((b) => b.currency));
  if (values.financial) {
    if (!DECIMAL.test(cost) || isZero(cost)) errors.cost = "Enter the cost above 0, such as 2000.00.";
    if (selected.length === 0) errors.bookIds = "Choose at least one open book.";
    else if (currencies.size > 1) errors.bookIds = "Choose books in one currency.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  const input: NewAddition = { type: values.type, description, addedDate: values.addedDate, financial: values.financial };
  if (quantity !== "") input.quantity = quantity;
  if (serial !== "") input.serialNumber = serial;
  if (values.financial) {
    input.cost = cost;
    input.currency = selected[0].currency;
    if (selected.length < openBooks.length) input.bookIds = selected.map((b) => b.id);
  }
  return { errors, input };
}

/** Record an improvement, component or accessory under an asset (FR-37.14). */
export function NewAdditionPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getAsset(id, signal), id);
  const [values, setValues] = useState<AdditionValues | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="New addition" />
        <Alert tone="danger" title="Could not load the asset" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="New addition" />
        <span className="visually-hidden">Loading the asset…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const asset = state.data.value;
  const open = asset.books.filter((b) => b.status === "Open");
  const form: AdditionValues = values ?? {
    type: "Improvement",
    description: "",
    quantity: "1",
    serialNumber: "",
    addedDate: today(),
    financial: false,
    cost: "",
    bookIds: open.map((b) => b.id),
  };

  function update(patch: Partial<AdditionValues>) {
    setValues({ ...form, ...patch });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateAddition(form, open);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await addAddition(id, input, key.current);
      navigate(`/assets/${id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as Errors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: "description" | "quantity" | "serialNumber" | "cost", label: string, hint?: string) => (
    <FormField id={`addition-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => <Input {...control} value={form[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`New addition to ${asset.assetNumber}`}
        description="Record an improvement, major component or accessory. A financial addition also posts its cost as an acquisition adjustment on the chosen open books; a descriptive one changes no balance."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New addition">
          <FormField id="addition-type" label="Type">
            {(control) => (
              <Select {...control} value={form.type} onChange={(e) => update({ type: e.target.value as AdditionType })}>
                {ADDITION_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("description", "Description")}
          {text("quantity", "Quantity")}
          {text("serialNumber", "Serial number", "Optional.")}
          <FormField id="addition-date" label="Date added" error={errors.addedDate}>
            {(control) => <Input {...control} type="date" value={form.addedDate} onChange={(e) => update({ addedDate: e.target.value })} invalid={Boolean(errors.addedDate)} />}
          </FormField>
          <div className={styles.formActions}>
            <label className={styles.check} htmlFor="addition-financial">
              <Input id="addition-financial" type="checkbox" checked={form.financial} disabled={open.length === 0} onChange={(e) => update({ financial: e.target.checked })} />
              Post the cost to the books
            </label>
            {open.length === 0 ? <span className={styles.note}>The asset has no open book, so only a descriptive addition is possible.</span> : null}
          </div>
          {form.financial ? (
            <>
              {text("cost", "Cost", `In ${open.find((b) => form.bookIds.includes(b.id))?.currency ?? "the books' currency"}.`)}
              <fieldset className={styles.fieldset} aria-describedby={errors.bookIds ? "addition-books-error" : undefined}>
                <legend className={styles.term}>Books</legend>
                {open.map((b) => (
                  <label key={b.id} className={styles.check} htmlFor={`addition-book-${b.id}`}>
                    <Input
                      id={`addition-book-${b.id}`}
                      type="checkbox"
                      checked={form.bookIds.includes(b.id)}
                      onChange={(e) => update({ bookIds: e.target.checked ? [...form.bookIds, b.id] : form.bookIds.filter((x) => x !== b.id) })}
                    />
                    {`${b.bookCode} (${money(b.cost, b.currency)})`}
                  </label>
                ))}
                {errors.bookIds ? (
                  <span id="addition-books-error" className={styles.error}>
                    {errors.bookIds}
                  </span>
                ) : null}
              </fieldset>
            </>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "AST-0020" ? "The asset has 500 additions already" : "Could not record the addition"} live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Record addition
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
