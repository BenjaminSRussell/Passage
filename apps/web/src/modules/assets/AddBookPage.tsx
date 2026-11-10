import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { addBook, CONVENTIONS, describeProblem, METHODS, newKey, type Method, type NewBook, type Problem } from "./api/client";
import { DATE, DECIMAL, sentence, today } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

export interface BookValues {
  bookCode: string;
  currency: string;
  method: Method;
  factor: string;
  switchToStraightLine: boolean;
  serviceLifePeriods: string;
  salvageValue: string;
  convention: string;
  inServiceDate: string;
  cost: string;
}

type Errors = Partial<Record<keyof BookValues, string>>;

const CODE = /^[A-Z0-9][A-Z0-9_-]{0,19}$/;

export function blankBook(now: Date = new Date()): BookValues {
  return {
    bookCode: "IFRS",
    currency: "USD",
    method: "StraightLine",
    factor: "2",
    switchToStraightLine: true,
    serviceLifePeriods: "60",
    salvageValue: "0",
    convention: "None",
    inServiceDate: today(now),
    cost: "",
  };
}

/** Checks that mirror the service (internal/api addBook and domain.Schedule). */
export function validateBook(values: BookValues): { errors: Errors; input?: NewBook } {
  const errors: Errors = {};
  const declining = values.method === "DecliningBalance";
  const life = Number(values.serviceLifePeriods);
  const maxLife = declining ? 100 : 1200;
  if (!CODE.test(values.bookCode)) errors.bookCode = "Use capital letters, digits, hyphens or underscores, such as IFRS or TAX.";
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  if (!/^\d+$/.test(values.serviceLifePeriods) || life < 1 || life > maxLife) {
    errors.serviceLifePeriods = declining ? "Enter a life of 1 to 100 years." : "Enter a life of 1 to 1200 months.";
  }
  if (!CONVENTIONS[values.method].includes(values.convention)) errors.convention = `Choose a convention that applies to ${sentence(values.method).toLowerCase()}.`;
  if (!DATE.test(values.inServiceDate)) errors.inServiceDate = "Choose the in-service date.";
  if (!DECIMAL.test(values.cost)) errors.cost = "Enter the cost as a decimal, such as 12000.00.";
  if (!DECIMAL.test(values.salvageValue)) errors.salvageValue = "Enter the salvage value as a decimal, or 0.";
  if (declining && (!DECIMAL.test(values.factor) || Number(values.factor) < 1 || Number(values.factor) > 3)) {
    errors.factor = "Enter a factor from 1 to 3, such as 2 for 200%.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      bookCode: values.bookCode,
      currency: values.currency,
      method: values.method,
      ...(declining ? { factor: values.factor } : {}),
      switchToStraightLine: declining && values.switchToStraightLine,
      serviceLifePeriods: life,
      salvageValue: values.salvageValue,
      convention: values.convention,
      inServiceDate: values.inServiceDate,
      cost: values.cost,
    },
  };
}

/** Add a depreciation book (FR-37.2). Saving posts the acquisition for the cost. */
export function AddBookPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [values, setValues] = useState<BookValues>(() => blankBook());
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const declining = values.method === "DecliningBalance";

  function update(patch: Partial<BookValues>) {
    setValues((v) => {
      const next = { ...v, ...patch };
      if (patch.method && !CONVENTIONS[next.method].includes(next.convention)) next.convention = CONVENTIONS[next.method][0];
      if (patch.method) next.serviceLifePeriods = next.method === "DecliningBalance" ? "5" : "60";
      return next;
    });
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateBook(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await addBook(id, input, key.current);
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

  const text = (name: Exclude<keyof BookValues, "method" | "switchToStraightLine">, label: string, hint?: string, extra: { type?: string; mono?: boolean; upper?: boolean } = {}) => (
    <FormField id={`book-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => (
        <Input
          {...control}
          type={extra.type ?? "text"}
          className={extra.mono ? styles.mono : undefined}
          value={values[name]}
          onChange={(e) => update({ [name]: extra.upper ? e.target.value.toUpperCase() : e.target.value })}
          invalid={Boolean(errors[name])}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="Add book" description="Each book depreciates the asset under its own method, life and convention. Saving posts the acquisition for the cost." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Add book">
          {text("bookCode", "Book", "For example IFRS, LOCAL or TAX.", { mono: true, upper: true })}
          {text("currency", "Currency", undefined, { upper: true })}
          <FormField id="book-method" label="Method" error={errors.method}>
            {(control) => (
              <Select {...control} value={values.method} onChange={(e) => update({ method: e.target.value as Method })}>
                {METHODS.map((m) => (
                  <option key={m} value={m}>
                    {sentence(m)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="book-convention" label="Convention" error={errors.convention}>
            {(control) => (
              <Select {...control} value={values.convention} onChange={(e) => update({ convention: e.target.value })}>
                {CONVENTIONS[values.method].map((c) => (
                  <option key={c} value={c}>
                    {c === "MidMonth15" ? "Mid-month (15th)" : sentence(c)}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {text("serviceLifePeriods", declining ? "Service life (years)" : "Service life (months)", undefined, { type: "number" })}
          {declining ? text("factor", "Declining-balance factor", "2 is 200% (double declining); MACRS uses 2 with the switch.") : null}
          {text("inServiceDate", "In-service date", undefined, { type: "date" })}
          {text("cost", "Cost", "Acquisition cost in the book currency.")}
          {text("salvageValue", "Salvage value")}
          {declining ? (
            <div className={styles.formActions}>
              <span className={styles.check}>
                <Input id="book-switch" type="checkbox" checked={values.switchToStraightLine} onChange={(e) => update({ switchToStraightLine: e.target.checked })} />
                <label htmlFor="book-switch">Switch to straight line when it gives more</label>
              </span>
            </div>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not add the book" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Add book
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
