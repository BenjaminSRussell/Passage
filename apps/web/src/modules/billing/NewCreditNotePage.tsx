import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select, Skeleton } from "../../shell/ui";
import { BillingApiError, createCreditNote, describeProblem, getInvoice, newKey, type CreditNoteInput, type Invoice, type Problem } from "./api/client";
import { DAY, DECIMAL, invoiceName, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;
export type CreditScope = "WHOLE" | "LINES";

export interface CreditValues {
  scope: CreditScope;
  creditDate: string;
  reason: string;
  chargeAmount: string;
  /** Quantity to credit per original line number; empty means "not this line". */
  qty: Record<number, string>;
}

export function emptyCredit(day = today()): CreditValues {
  return { scope: "WHOLE", creditDate: day, reason: "", chargeAmount: "", qty: {} };
}

/** What is left to credit on a line of the original. */
export function remainingQty(line: { qty: string; creditedQty?: string }): number {
  return Number(line.qty) - Number(line.creditedQty ?? 0);
}

/** Client checks that mirror the service (FR-29.5); the service has the last word on the amounts (BIL-0005). */
export function validateCredit(v: CreditValues, original: Invoice): Errors {
  const errors: Errors = {};
  if (!DAY.test(v.creditDate)) errors.creditDate = "Enter the credit date as YYYY-MM-DD.";
  else if (v.creditDate < original.invoiceDate) errors.creditDate = `The credit date cannot be before the invoice date ${original.invoiceDate}.`;
  const reason = v.reason.trim();
  if (reason === "" || reason.length > 200) errors.reason = "Enter the reason printed on the credit note, up to 200 characters.";
  if (v.scope === "WHOLE") return errors;
  let any = false;
  original.lines.forEach((l, i) => {
    const raw = (v.qty[l.lineNo] ?? "").trim();
    if (raw === "") return;
    any = true;
    if (!DECIMAL.test(raw) || Number(raw) <= 0) errors[`lines[${i}].qty`] = "Enter a quantity above zero, or leave it empty.";
    else if (Number(raw) > remainingQty(l)) errors[`lines[${i}].qty`] = `At most ${remainingQty(l)} can still be credited on this line.`;
  });
  if (v.chargeAmount.trim() !== "" && !DECIMAL.test(v.chargeAmount.trim())) errors.chargeAmount = "Enter the charge as a number such as 5.00, or leave it empty.";
  if (!any) errors.lines = "Enter a quantity on at least one line.";
  return errors;
}

export function toCreditInput(v: CreditValues, original: Invoice): CreditNoteInput {
  const base = { originalInvoiceId: original.id, creditDate: v.creditDate, reason: v.reason.trim() };
  if (v.scope === "WHOLE") return base;
  const charge = v.chargeAmount.trim();
  return {
    ...base,
    ...(charge ? { chargeAmount: charge } : {}),
    lines: original.lines.filter((l) => (v.qty[l.lineNo] ?? "").trim() !== "").map((l) => ({ lineNo: l.lineNo, qty: v.qty[l.lineNo].trim() })),
  };
}

/** Maps the service's field errors (lines[n] counts request lines) back to the form's original lines. */
function serviceErrors(fields: Record<string, string>, input: CreditNoteInput, original: Invoice): Errors {
  const out: Errors = {};
  for (const [name, message] of Object.entries(fields)) {
    const m = /^lines\[(\d+)\]\.(\w+)$/.exec(name);
    const requested = m ? input.lines?.[Number(m[1])] : undefined;
    const index = requested ? original.lines.findIndex((l) => l.lineNo === requested.lineNo) : -1;
    out[index >= 0 ? `lines[${index}].qty` : name] = message;
  }
  return out;
}

function CreditForm({ original }: { original: Invoice }) {
  const navigate = useNavigate();
  const [values, setValues] = useState<CreditValues>(() => emptyCredit(today() < original.invoiceDate ? original.invoiceDate : today()));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());
  const currency = original.currency;

  function update(patch: Partial<CreditValues>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateCredit(values, original);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    const input = toCreditInput(values, original);
    try {
      const created = await createCreditNote(input, key.current);
      navigate(`/billing/invoices/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(serviceErrors(problem.fields, input, original));
      else setFailure(problem);
      // A refused request is final; only a network or server failure may be retried with the same key.
      if (error instanceof BillingApiError && error.status < 500) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="cn-scope" label="What to credit">
          {(c) => (
            <Select {...c} value={values.scope} onChange={(e) => update({ scope: e.target.value as CreditScope })}>
              <option value="WHOLE">{`Everything left to credit (${original.credited?.remaining ?? original.totals.gross} ${currency})`}</option>
              <option value="LINES">Selected line quantities</option>
            </Select>
          )}
        </FormField>
        <FormField id="cn-date" label="Credit date" error={errors.creditDate}>
          {(c) => (
            <Input {...c} type="date" className={styles.mono} value={values.creditDate} onChange={(e) => update({ creditDate: e.target.value })} invalid={Boolean(errors.creditDate)} />
          )}
        </FormField>
        <FormField id="cn-reason" label="Reason" hint="Printed on the credit note, for example Goods returned damaged." error={errors.reason}>
          {(c) => <Input {...c} value={values.reason} onChange={(e) => update({ reason: e.target.value })} invalid={Boolean(errors.reason)} />}
        </FormField>
        {values.scope === "LINES" ? (
          <>
            {original.lines.map((l, i) => {
              const left = remainingQty(l);
              const name = `lines[${i}].qty`;
              return (
                <fieldset key={l.lineNo} className={styles.lineSet}>
                  <legend className={styles.groupsLegend}>{`Line ${l.lineNo}: ${l.description}`}</legend>
                  <p className={styles.note}>{`Invoiced ${l.qty} at ${l.unitPrice} ${currency} (net ${l.net}, tax ${l.tax} ${l.taxCode}). Left to credit: ${left}.`}</p>
                  <FormField id={`cn-line-${l.lineNo}`} label={`Quantity to credit on line ${l.lineNo}`} optional error={errors[name]}>
                    {(c) => (
                      <Input
                        {...c}
                        className={styles.mono}
                        inputMode="decimal"
                        disabled={left <= 0}
                        value={values.qty[l.lineNo] ?? ""}
                        onChange={(e) => update({ qty: { ...values.qty, [l.lineNo]: e.target.value } })}
                        invalid={Boolean(errors[name])}
                      />
                    )}
                  </FormField>
                </fieldset>
              );
            })}
            <FormField id="cn-charge" label="Header charges to credit" optional hint={`The invoice charged ${original.totals.charges} ${currency}.`} error={errors.chargeAmount}>
              {(c) => (
                <Input
                  {...c}
                  className={styles.mono}
                  inputMode="decimal"
                  value={values.chargeAmount}
                  onChange={(e) => update({ chargeAmount: e.target.value })}
                  invalid={Boolean(errors.chargeAmount)}
                />
              )}
            </FormField>
            {errors.lines ? (
              <div className={styles.formActions}>
                <Alert tone="danger" live="alert">
                  {errors.lines}
                </Alert>
              </div>
            ) : null}
          </>
        ) : null}
        <div className={styles.formActions}>
          <span className={styles.note}>Tax is credited at the original invoice's amounts, pro rata to the quantity; it is not quoted again.</span>
        </div>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title={failure.code === "BIL-0005" ? "The credit exceeds what the invoice still allows" : "Could not save the credit note"} live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save credit note proposal
          </Button>
          <Link className={buttonClassName("ghost")} to={`/billing/invoices/${original.id}`}>
            Cancel
          </Link>
        </div>
      </form>
    </Card>
  );
}

/** New credit note against a posted invoice (FR-29.5): the whole remainder or chosen line quantities, saved as a proposal. */
export function NewCreditNotePage() {
  const [params] = useSearchParams();
  const invoiceId = params.get("invoice") ?? "";
  const state = useLoad((signal) => (invoiceId ? getInvoice(invoiceId, signal) : Promise.resolve(null)), invoiceId);
  const title = "New credit note";

  if (invoiceId === "") {
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <Alert tone="info" title="Choose the invoice to credit" action={<Link className={buttonClassName("secondary", "sm")} to="/billing">Invoices</Link>}>
          A credit note always references a posted invoice. Open the invoice and choose Credit invoice.
        </Alert>
      </div>
    );
  }
  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <Alert tone="danger" title="Could not load the invoice to credit" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  const original = state.data;
  if (!original) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title={title} />
        <span className="visually-hidden">Loading the invoice to credit…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const name = invoiceName(original);
  const creditable = original.credited !== undefined && Number(original.credited.remaining) > 0;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={title}
        description={`Credit ${name}: ${original.totals.gross} ${original.currency} invoiced${original.credited ? `, ${original.credited.remaining} ${original.currency} left to credit` : ""}. The credit note is saved as a proposal; you post it from the next page.`}
      />
      {creditable ? (
        <CreditForm original={original} />
      ) : (
        <Alert tone="warning" title={`${name} cannot be credited`} action={<Link className={buttonClassName("secondary", "sm")} to={`/billing/invoices/${original.id}`}>Open the invoice</Link>}>
          {original.credited ? "It has been credited in full." : "Only a posted invoice can be credited; post the proposal first."}
        </Alert>
      )}
    </div>
  );
}
