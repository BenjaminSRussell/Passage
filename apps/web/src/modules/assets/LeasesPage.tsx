import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { createLease, describeProblem, LEASE_ELECTIONS, listLeases, LOW_VALUE_THRESHOLDS, newKey, PAYMENTS_PER_YEAR, RATE_METHODS, type Lease, type NewLease, type LeaseElection, type LowValueCurrency, type Problem, type RateMethod } from "./api/client";
import { StatusBadge } from "./badges";
import { DATE, DECIMAL, money, percent, today, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const frequency: Record<number, string> = { 1: "Annual", 2: "Semi-annual", 4: "Quarterly", 12: "Monthly" };

const columns: DataTableColumn<Lease>[] = [
  { key: "leaseNumber", header: "Lease", mono: true },
  { key: "commencementDate", header: "Commencement", mono: true },
  { key: "termMonths", header: "Term (months)", align: "end" },
  { key: "paymentAmount", header: "Payment", align: "end", render: (l) => `${money(l.paymentAmount, l.currency)} ${frequency[l.paymentsPerYear]?.toLowerCase() ?? ""}` },
  { key: "ibrRate", header: "IBR", align: "end", render: (l) => percent(l.ibrRate) },
  { key: "liability", header: "Liability", align: "end", render: (l) => money(l.liability, l.currency) },
  { key: "election", header: "Exemption", render: (l) => (l.election === "ShortTerm" ? "Short-term" : l.election === "LowValue" ? "Low-value" : "None") },
  { key: "status", header: "Status", render: (l) => <StatusBadge status={l.status} /> },
];

/** Leases recognized under IFRS 16, ordered by lease number. */
export function LeasesPage() {
  const navigate = useNavigate();
  const state = useLoad((signal) => listLeases(undefined, signal), "leases");
  const [more, setMore] = useState<Lease[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setCursor(state.data?.nextCursor);
  }, [state.data]);

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listLeases(cursor);
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Leases"
        description="Leases recognized under IFRS 16 with their liability and right-of-use asset. Open a lease to see its schedule."
        actions={
          <Link className={buttonClassName("primary")} to="/assets/leases/new">
            New lease
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load leases" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Leases"
          columns={columns}
          rows={[...(state.data?.items ?? []), ...more]}
          getRowKey={(l) => l.id}
          loading={state.loading}
          empty={{ title: "No leases yet", description: "Leases you recognize appear here with their liability and ROU asset." }}
          onRowOpen={(l) => navigate(`/assets/leases/${l.id}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more leases" live="alert">
          <ProblemMessage problem={moreError} />
        </Alert>
      ) : null}
      {cursor && !state.error ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

export interface LeaseValues {
  legalEntityId: string;
  leaseNumber: string;
  commencementDate: string;
  termMonths: string;
  paymentsPerYear: string;
  paymentAmount: string;
  paymentTiming: "arrears" | "advance";
  ibrRate: string;
  rateMethod?: RateMethod;
  currency: string;
  election?: LeaseElection;
  purchaseOption?: boolean;
  underlyingAssetValue?: string;
  underlyingAssetCurrency?: LowValueCurrency;
}

const LOW_VALUE_CURRENCIES = Object.keys(LOW_VALUE_THRESHOLDS) as LowValueCurrency[];
export const ELECTION_LABELS: Record<LeaseElection, string> = { None: "None: recognize the lease", ShortTerm: "Short-term (12 months or less)", LowValue: "Low-value asset" };

type Errors = Partial<Record<keyof LeaseValues, string>>;

const RATE = /^0(\.\d{1,6})?$/;

/** Checks that mirror the service (internal/api createLease and domain.ComputeLease). */
export function validateLease(values: LeaseValues): { errors: Errors; input?: NewLease } {
  const errors: Errors = {};
  const number = values.leaseNumber.trim();
  const term = Number(values.termMonths);
  const ppy = Number(values.paymentsPerYear);
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (number.length < 1 || number.length > 40) errors.leaseNumber = "Enter a lease number of up to 40 characters.";
  if (!DATE.test(values.commencementDate)) errors.commencementDate = "Choose the commencement date.";
  if (!/^\d+$/.test(values.termMonths) || term < 1 || term > 600) errors.termMonths = "Enter a term of 1 to 600 months.";
  else if ((term * ppy) % 12 !== 0) errors.termMonths = "The term must be a whole number of payment periods.";
  if (!DECIMAL.test(values.paymentAmount) || /^0+(\.0+)?$/.test(values.paymentAmount)) errors.paymentAmount = "Enter a payment above 0, such as 10000.00.";
  if (!RATE.test(values.ibrRate) || /^0(\.0+)?$/.test(values.ibrRate) || Number(values.ibrRate) > 0.5) {
    errors.ibrRate = "Enter the annual rate as a decimal above 0 and at most 0.5, such as 0.05 for 5%.";
  }
  if (!/^[A-Z]{3}$/.test(values.currency)) errors.currency = "Enter a three-letter ISO currency code, such as USD.";
  const election = values.election ?? "None";
  if (election === "ShortTerm" && /^\d+$/.test(values.termMonths) && term > 12) errors.election = "A short-term lease has a term of 12 months or less.";
  else if (election === "ShortTerm" && values.purchaseOption) errors.election = "A lease that contains a purchase option is not a short-term lease.";
  const underlying = values.underlyingAssetValue ?? "";
  const valueCurrency = values.underlyingAssetCurrency ?? "USD";
  if (election === "LowValue") {
    const limit = LOW_VALUE_THRESHOLDS[valueCurrency];
    if (!DECIMAL.test(underlying) || /^0+(\.0+)?$/.test(underlying)) errors.underlyingAssetValue = "Enter the value of the underlying asset when new, such as 4200.00.";
    else if (Number(underlying) > limit) errors.underlyingAssetValue = `A low-value asset is worth at most ${valueCurrency} ${limit} when new.`;
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      legalEntityId: values.legalEntityId.trim(),
      leaseNumber: number,
      commencementDate: values.commencementDate,
      termMonths: term,
      paymentsPerYear: ppy,
      paymentAmount: values.paymentAmount,
      paymentTiming: values.paymentTiming,
      ibrRate: values.ibrRate,
      rateMethod: values.rateMethod ?? "Nominal",
      currency: values.currency,
      ...(election === "None" ? {} : { election }),
      ...(values.purchaseOption ? { purchaseOption: true } : {}),
      ...(election === "LowValue" ? { underlyingAssetValue: underlying, underlyingAssetCurrency: valueCurrency } : {}),
    },
  };
}

/** Recognize a lease (FR-37.10): the service computes the liability, ROU asset and schedule. */
export function NewLeasePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<LeaseValues>(() => ({
    legalEntityId: "",
    leaseNumber: "",
    commencementDate: today(),
    termMonths: "60",
    paymentsPerYear: "12",
    paymentAmount: "",
    paymentTiming: "arrears",
    ibrRate: "0.05",
    rateMethod: "Nominal",
    currency: "USD",
    election: "None",
    purchaseOption: false,
    underlyingAssetValue: "",
    underlyingAssetCurrency: "USD",
  }));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<LeaseValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateLease(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createLease(input, key.current);
      navigate(`/assets/leases/${created.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as Errors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const text = (name: Exclude<keyof LeaseValues, "paymentTiming" | "paymentsPerYear" | "rateMethod" | "election" | "purchaseOption" | "underlyingAssetCurrency">, label: string, hint?: string, extra: { type?: string; mono?: boolean; upper?: boolean } = {}) => (
    <FormField id={`lease-${name}`} label={label} hint={hint} error={errors[name]}>
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
      <PageHeader title="New lease" description="Saving recognizes the lease: the liability is the present value of the payments at the incremental borrowing rate, and the ROU asset starts at the same amount." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New lease">
          {text("legalEntityId", "Legal entity", "UUID of the lessee legal entity.", { mono: true })}
          {text("leaseNumber", "Lease number", undefined, { mono: true })}
          {text("commencementDate", "Commencement date", undefined, { type: "date" })}
          {text("termMonths", "Term (months)", undefined, { type: "number" })}
          <FormField id="lease-paymentsPerYear" label="Payment frequency">
            {(control) => (
              <Select {...control} value={values.paymentsPerYear} onChange={(e) => update({ paymentsPerYear: e.target.value })}>
                {PAYMENTS_PER_YEAR.map((n) => (
                  <option key={n} value={String(n)}>
                    {frequency[n]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="lease-paymentTiming" label="Payment timing">
            {(control) => (
              <Select {...control} value={values.paymentTiming} onChange={(e) => update({ paymentTiming: e.target.value as LeaseValues["paymentTiming"] })}>
                <option value="arrears">In arrears</option>
                <option value="advance">In advance</option>
              </Select>
            )}
          </FormField>
          {text("paymentAmount", "Payment amount", "Per payment, in the lease currency.")}
          {text("currency", "Currency", undefined, { upper: true })}
          {text("ibrRate", "Incremental borrowing rate", "Annual rate as a decimal: 0.05 is 5%.")}
          <FormField id="lease-rateMethod" label="Periodic rate" hint="Nominal divides the annual rate by the payments per year; effective compounds back to it.">
            {(control) => (
              <Select {...control} value={values.rateMethod} onChange={(e) => update({ rateMethod: e.target.value as RateMethod })}>
                {RATE_METHODS.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="lease-election" label="Recognition exemption" hint="A short-term or low-value lease is not recognized: no liability and no ROU asset, and each payment is expensed." error={errors.election}>
            {(control) => (
              <Select {...control} value={values.election} onChange={(e) => update({ election: e.target.value as LeaseElection })}>
                {LEASE_ELECTIONS.map((e) => (
                  <option key={e} value={e}>
                    {ELECTION_LABELS[e]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="lease-purchaseOption" type="checkbox" checked={Boolean(values.purchaseOption)} onChange={(e) => update({ purchaseOption: e.target.checked })} />
              <label htmlFor="lease-purchaseOption">The lease contains a purchase option (it cannot then be short-term)</label>
            </span>
          </div>
          {values.election === "LowValue" ? (
            <>
              {text("underlyingAssetValue", "Underlying asset value when new", `Assessed on the asset when new, whatever its age: at most ${LOW_VALUE_THRESHOLDS[values.underlyingAssetCurrency ?? "USD"]} in the currency below.`)}
              <FormField id="lease-underlyingAssetCurrency" label="Value stated in" hint="The low-value threshold is set in USD and EUR; convert a value known in another currency at the date of the assessment.">
                {(control) => (
                  <Select {...control} value={values.underlyingAssetCurrency} onChange={(e) => update({ underlyingAssetCurrency: e.target.value as LowValueCurrency })}>
                    {LOW_VALUE_CURRENCIES.map((c) => (
                      <option key={c} value={c}>
                        {c}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            </>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title={failure.code === "AST-0007" ? "That lease number is taken" : "Could not recognize the lease"} live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Recognize lease
            </Button>
            <Link className={buttonClassName("ghost")} to="/assets/leases">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
