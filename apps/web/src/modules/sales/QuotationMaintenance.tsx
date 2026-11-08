import { useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, Textarea } from "../../shell/ui";
import { cancelQuotation, changeQuotation, describeProblem, type ChangeQuotationInput, type Problem, type Quotation } from "./api/client";
import { DECIMAL, today } from "./format";
import { validateLines, type LineValues } from "./lines";
import { LinesEditor } from "./LinesEditor";
import styles from "./pages.module.css";

const REASON_MAX = 200;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

type Errors = Record<string, string>;

export interface ChangeValues {
  validUntil: string;
  probabilityPct: string;
  lines: LineValues[];
  /** Sent with replaced lines only; needs sales.order.approve. */
  listingOverrideReason?: string;
}

/** The form's starting values: the quotation as stored. */
export function changeValuesOf(q: Quotation): ChangeValues {
  return {
    validUntil: q.validUntil,
    probabilityPct: String(q.probabilityPct),
    lines: q.lines.map((l) => ({
      productId: l.productId,
      quantity: l.quantity,
      unit: l.unit,
      siteId: l.siteId,
      warehouseId: l.warehouseId,
      unitPrice: l.unitPrice,
      discPct: l.discPct,
      requestedShipDate: l.requestedShipDate,
    })),
  };
}

/**
 * Checks that mirror the service (api/quotations.go buildQuotationChange) and
 * returns only what differs from the stored quotation; lines are sent only for
 * a DRAFT and only when they differ. No difference is an error on `body`.
 */
export function validateChange(values: ChangeValues, q: Quotation, on: string = today()): { errors: Errors; input?: ChangeQuotationInput } {
  const errors: Errors = {};
  const input: ChangeQuotationInput = {};
  if (!DATE.test(values.validUntil)) errors.validUntil = "Choose a date.";
  else if (values.validUntil < on) errors.validUntil = "The quotation must be valid at least until today.";
  else if (values.validUntil !== q.validUntil) input.validUntil = values.validUntil;
  const probability = values.probabilityPct.trim();
  if (!/^\d{1,3}$/.test(probability) || Number(probability) > 100) errors.probabilityPct = "Enter a whole percentage from 0 to 100.";
  else if (Number(probability) !== q.probabilityPct) input.probabilityPct = Number(probability);
  if (q.status === "DRAFT") {
    const lines = validateLines(values.lines, errors);
    if (!sameLines(changeValuesOf(q).lines, values.lines)) {
      input.lines = lines;
      const override = (values.listingOverrideReason ?? "").trim();
      if (override.length > 500) errors.listingOverrideReason = "Use at most 500 characters.";
      else if (override) input.listingOverrideReason = override;
    }
  }
  if (Object.keys(errors).length > 0) return { errors };
  if (Object.keys(input).length === 0) return { errors: { body: "Nothing differs from the stored quotation." } };
  return { errors, input };
}

/** True when two line sets hold the same lines in the same order. */
function sameLines(a: LineValues[], b: LineValues[]): boolean {
  return a.length === b.length && a.every((l, i) => (Object.keys(l) as (keyof LineValues)[]).every((k) => numeric(l[k]) === numeric(b[i][k])));
}

/**
 * Compares decimals by value ("3" and "3.000000" are the same quantity) without
 * going through a float, so long amounts compare exactly.
 */
function numeric(v: string | undefined): string {
  const t = (v ?? "").trim();
  if (!DECIMAL.test(t)) return t;
  const [whole, fraction = ""] = t.split(".");
  const digits = whole.replace(/^0+(?=\d)/, "");
  const decimals = fraction.replace(/0+$/, "");
  return decimals ? `${digits}.${decimals}` : digits;
}

/**
 * After the quotation was re-read (a version conflict, or another action on
 * the page), keeps every field the user changed and takes the stored value for
 * the fields the user left alone, so a save does not undo someone else's change.
 */
export function rebaseChange(values: ChangeValues, before: Quotation, after: Quotation): ChangeValues {
  const was = changeValuesOf(before);
  const now = changeValuesOf(after);
  return {
    validUntil: values.validUntil === was.validUntil ? now.validUntil : values.validUntil,
    probabilityPct: values.probabilityPct.trim() === was.probabilityPct ? now.probabilityPct : values.probabilityPct,
    lines: sameLines(values.lines, was.lines) ? now.lines : values.lines,
    listingOverrideReason: values.listingOverrideReason,
  };
}

interface Props {
  quotation: Quotation;
  etag: string;
  /** Called with a confirmation after a change or a cancellation went through, so the page reloads and says so. */
  onDone(message: string): void;
  /** Called when the service refused a stale version (412): the page re-reads the quotation and the form keeps the user's edits. */
  onConflict(): void;
}

/** Change and cancel an open quotation (FR-27.1, BR-27.5). A SENT quotation keeps its lines; only validity and probability move. */
export function QuotationMaintenance({ quotation, etag, onDone, onConflict }: Props) {
  const [values, setValues] = useState<ChangeValues>(() => changeValuesOf(quotation));
  const [base, setBase] = useState<Quotation>(quotation);
  if (quotation !== base) {
    // A re-read quotation: rebase the form on it during render (no stale frame).
    setBase(quotation);
    if (quotation.version !== base.version) setValues(rebaseChange(values, base, quotation));
  }
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<"change" | "cancel" | null>(null);
  const draft = quotation.status === "DRAFT";

  async function run(kind: "change" | "cancel", call: () => Promise<unknown>, message: string) {
    setBusy(kind);
    setFailure(null);
    try {
      await call();
      onDone(message);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.status === 412) onConflict();
    } finally {
      setBusy(null);
    }
  }

  function change(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateChange(values, quotation);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    void run("change", () => changeQuotation(quotation.id, etag, input), "Quotation changed.");
  }

  function cancel(event: FormEvent) {
    event.preventDefault();
    const text = reason.trim();
    if (text.length === 0 || text.length > REASON_MAX) {
      setErrors({ cancelReason: `Give a reason of 1 to ${REASON_MAX} characters.` });
      return;
    }
    setErrors({});
    void run("cancel", () => cancelQuotation(quotation.id, etag, text), "Quotation cancelled.");
  }

  return (
    <>
      {failure ? (
        <Alert tone="danger" title="The quotation was not saved" live="alert">
          {failure.status === 412
            ? "Someone else changed this quotation; it has been reloaded and your changes are kept. Check them and save again. "
            : null}
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card title="Change the quotation">
        <form className={styles.form} onSubmit={change} noValidate aria-label="Change the quotation">
          <FormField id="change-validUntil" label="Valid until" hint="Extend or shorten the validity, up to a year from today." error={errors.validUntil}>
            {(control) => <Input {...control} type="date" value={values.validUntil} onChange={(e) => setValues({ ...values, validUntil: e.target.value })} invalid={Boolean(errors.validUntil)} />}
          </FormField>
          <FormField id="change-probabilityPct" label="Win probability %" error={errors.probabilityPct}>
            {(control) => <Input {...control} inputMode="numeric" value={values.probabilityPct} onChange={(e) => setValues({ ...values, probabilityPct: e.target.value })} invalid={Boolean(errors.probabilityPct)} />}
          </FormField>
          {draft ? (
            <>
              <LinesEditor lines={values.lines} errors={errors} newLineDate={today()} onChange={(lines) => setValues({ ...values, lines })} legend="Quotation line" idPrefix="change-line" />
              <FormField id="change-listingOverrideReason" label="Listing override reason" hint="Needs the sales.order.approve duty. Accepts replaced lines the product listing denies and logs the reason." error={errors.listingOverrideReason} optional>
                {(control) => <Input {...control} value={values.listingOverrideReason ?? ""} onChange={(e) => setValues({ ...values, listingOverrideReason: e.target.value })} invalid={Boolean(errors.listingOverrideReason)} />}
              </FormField>
            </>
          ) : (
            <p className={`${styles.note} ${styles.formActions}`}>A sent quotation keeps its lines and prices, which are what the customer holds. Cancel it to quote again.</p>
          )}
          {errors.body ? (
            <p role="alert" className={`${styles.fieldError} ${styles.formActions}`}>
              {errors.body}
            </p>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "change"} disabled={busy !== null && busy !== "change"}>
              Save changes
            </Button>
          </div>
        </form>
      </Card>
      <Card title="Cancel the quotation">
        <form className={styles.form} onSubmit={cancel} noValidate aria-label="Cancel the quotation">
          <FormField id="cancel-reason" label="Cancellation reason" hint="For example the customer withdrew the request." error={errors.cancelReason}>
            {(control) => <Textarea {...control} rows={2} maxLength={REASON_MAX} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(errors.cancelReason)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="danger" loading={busy === "cancel"} disabled={busy !== null && busy !== "cancel"}>
              Cancel quotation
            </Button>
          </div>
        </form>
      </Card>
    </>
  );
}
