import { useState, type FormEvent, type ReactNode } from "react";
import { Alert, Button, Card, EmptyState, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { describeProblem, PAYEE_TYPES, TDS_SECTIONS, withholdTds, type PayeeType, type Problem, type TdsInput, type TdsResult, type TdsSection } from "./api/client";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const sectionLabel: Record<TdsSection, string> = {
  "194C": "194C Contractors",
  "194J": "194J Professional fees",
  "194I": "194I Rent of land, building or furniture",
  "194H": "194H Commission or brokerage",
  "194Q": "194Q Purchase of goods",
};

const payeeLabel: Record<PayeeType, string> = { INDIVIDUAL_HUF: "Individual or HUF", COMPANY: "Company or other" };

const reasonText: Record<TdsResult["reason"], string> = {
  BELOW_THRESHOLD: "Below the section threshold: nothing to deduct.",
  THRESHOLD_CROSSED: "Threshold crossed: deducted at the section rate.",
  PAN_MISSING_206AA: "No PAN: deducted at the higher section 206AA rate.",
  LOWER_DEDUCTION_CERTIFICATE: "Deducted at the section 197 certificate rate.",
  EXCESS_OVER_THRESHOLD: "Deducted on the purchases above 50 lakh this year.",
};

const AMOUNT = /^\d+(\.\d{1,2})?$/;
const RATE = /^\d+(\.\d+)?$/;

interface Values {
  section: TdsSection;
  payeeType: PayeeType;
  amount: string;
  ytdPaid: string;
  ytdTaxed: string;
  pan: boolean;
  lowerRate: string;
}

type Errors = Partial<Record<"amount" | "ytdPaid" | "ytdTaxed" | "lowerRate", string>>;

/** Checks the form and builds the request; amounts stay decimal strings. */
export function toTdsInput(values: Values): { input?: TdsInput; errors: Errors } {
  const errors: Errors = {};
  const amount = values.amount.trim();
  const ytdPaid = values.ytdPaid.trim();
  const ytdTaxed = values.ytdTaxed.trim();
  const lowerRate = values.lowerRate.trim();
  if (!AMOUNT.test(amount) || Number(amount) <= 0) errors.amount = "Enter a positive amount in rupees, at most 2 decimals.";
  if (ytdPaid && !AMOUNT.test(ytdPaid)) errors.ytdPaid = "Enter an amount in rupees, at most 2 decimals.";
  if (ytdTaxed && !AMOUNT.test(ytdTaxed)) errors.ytdTaxed = "Enter an amount in rupees, at most 2 decimals.";
  else if (ytdTaxed && Number(ytdTaxed) > Number(ytdPaid || "0")) errors.ytdTaxed = "Cannot be more than paid so far this year.";
  if (lowerRate && (!RATE.test(lowerRate) || Number(lowerRate) > 100)) errors.lowerRate = "Enter a percentage from 0 to 100.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: TdsInput = { section: values.section, amount, panPresent: values.pan };
  if (values.section === "194C") input.payeeType = values.payeeType;
  if (ytdPaid) input.ytdPaid = ytdPaid;
  if (ytdTaxed) input.ytdTaxed = ytdTaxed;
  if (lowerRate) input.lowerRatePct = lowerRate;
  return { input, errors };
}

/** "20000" → "₹20,000.00", grouped the Indian way, from the decimal string. */
export function formatInr(value: string): string {
  const [whole, fraction = ""] = value.split(".");
  const negative = whole.startsWith("-");
  const digits = negative ? whole.slice(1) : whole;
  const last3 = digits.slice(-3);
  const rest = digits.slice(0, -3).replace(/\B(?=(\d{2})+(?!\d))/g, ",");
  return `${negative ? "-" : ""}₹${rest ? `${rest},${last3}` : last3}.${(fraction + "00").slice(0, 2)}`;
}

/**
 * India TDS calculator (FR-48.27, localization.admin): the deduction on one
 * payment to a resident deductee, before the payment is posted.
 */
export function TdsPage() {
  const [values, setValues] = useState<Values>({ section: "194J", payeeType: "COMPANY", amount: "", ytdPaid: "", ytdTaxed: "", pan: true, lowerRate: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<TdsResult | null>(null);

  function field<K extends keyof Values>(name: K, value: Values[K]) {
    setValues((v) => ({ ...v, [name]: value }));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { input, errors: found } = toTdsInput(values);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    setFailure(null);
    try {
      setResult(await withholdTds(input));
    } catch (error) {
      setFailure(describeProblem(error));
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const facts: [string, ReactNode][] = result
    ? [
        ["TDS to deduct", formatInr(result.tdsAmount)],
        ["Rate", `${result.ratePct}%`],
        ["Taxable amount", formatInr(result.taxableAmount)],
        ["Section", <span className={styles.mono}>{result.section}</span>],
        ["Return", <span className={styles.mono}>{result.returnForm}</span>],
        ["Challan", <span className={styles.mono}>{result.challanForm}</span>],
      ]
    : [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="India TDS calculator"
        description="Work out the tax to deduct at source on one payment under the India pack. Rates and thresholds are FY 2025-26 values awaiting tax-counsel sign-off. Nothing is posted."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="India TDS calculator">
          <FormField id="tds-section" label="Section">
            {(control) => (
              <Select {...control} value={values.section} onChange={(e) => field("section", e.target.value as TdsSection)}>
                {TDS_SECTIONS.map((s) => (
                  <option key={s} value={s}>
                    {sectionLabel[s]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.section === "194C" ? (
            <FormField id="tds-payee" label="Payee type" hint="194C deducts 1% from individuals and HUFs, 2% from others.">
              {(control) => (
                <Select {...control} value={values.payeeType} onChange={(e) => field("payeeType", e.target.value as PayeeType)}>
                  {PAYEE_TYPES.map((p) => (
                    <option key={p} value={p}>
                      {payeeLabel[p]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
          <FormField id="tds-amount" label="Payment amount (INR)" error={errors.amount}>
            {(control) => <Input {...control} inputMode="decimal" value={values.amount} onChange={(e) => field("amount", e.target.value)} invalid={Boolean(errors.amount)} />}
          </FormField>
          <FormField id="tds-ytd-paid" label="Paid so far this year (INR)" hint="Earlier credits to this deductee under the section." error={errors.ytdPaid} optional>
            {(control) => <Input {...control} inputMode="decimal" value={values.ytdPaid} onChange={(e) => field("ytdPaid", e.target.value)} invalid={Boolean(errors.ytdPaid)} />}
          </FormField>
          <FormField id="tds-ytd-taxed" label="Of which already taxed (INR)" hint="The part that already bore TDS; the rest is caught up when the yearly threshold is crossed." error={errors.ytdTaxed} optional>
            {(control) => <Input {...control} inputMode="decimal" value={values.ytdTaxed} onChange={(e) => field("ytdTaxed", e.target.value)} invalid={Boolean(errors.ytdTaxed)} />}
          </FormField>
          <FormField id="tds-lower-rate" label="Lower-deduction certificate rate (%)" hint="From a section 197 certificate; used only with a PAN." error={errors.lowerRate} optional>
            {(control) => <Input {...control} inputMode="decimal" value={values.lowerRate} onChange={(e) => field("lowerRate", e.target.value)} invalid={Boolean(errors.lowerRate)} />}
          </FormField>
          <span className={`${styles.check} ${styles.wide}`}>
            <Input id="tds-pan" type="checkbox" checked={values.pan} onChange={(e) => field("pan", e.target.checked)} />
            <label htmlFor="tds-pan">The deductee has given a valid PAN</label>
          </span>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Calculate TDS
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="danger" title={failure ? "TDS could not be calculated" : undefined} live="alert">
        {failure ? <ProblemMessage problem={failure} /> : null}
      </Alert>
      <Alert tone="success" live="status">
        {result ? reasonText[result.reason] : null}
      </Alert>
      {result ? (
        <Card title="Deduction">
          <dl className={styles.details}>
            {facts.map(([term, value]) => (
              <div key={term} className={styles.entry}>
                <dt className={styles.term}>{term}</dt>
                <dd className={styles.value}>{value}</dd>
              </div>
            ))}
          </dl>
        </Card>
      ) : (
        <Card>
          <EmptyState icon="info" title="No calculation yet" description="Choose a section and enter the payment to see the deduction." compact />
        </Card>
      )}
    </div>
  );
}
