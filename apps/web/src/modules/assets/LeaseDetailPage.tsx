import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getLease,
  getLeasePostings,
  modifyLease,
  newKey,
  type Lease,
  type LeaseLine,
  type LeaseModification,
  type LeaseModificationResult,
  type LeaseTermination,
  type LeasePostings,
  type ModifyLeaseInput,
  type Problem,
} from "./api/client";
import { StatusBadge } from "./badges";
import { DECIMAL, isZero, money, percent } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function Detail({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

const columns: DataTableColumn<LeaseLine>[] = [
  { key: "periodNo", header: "Period", align: "end" },
  { key: "opening", header: "Opening liability", align: "end" },
  { key: "remeasurement", header: "Remeasurement", align: "end", render: (l) => (l.remeasurement && !isZero(l.remeasurement) ? l.remeasurement : "") },
  { key: "interest", header: "Interest", align: "end" },
  { key: "payment", header: "Payment", align: "end" },
  { key: "closing", header: "Closing liability", align: "end" },
  { key: "rouDepreciation", header: "ROU depreciation", align: "end" },
];

function modificationColumns(currency: string): DataTableColumn<LeaseModification>[] {
  return [
    { key: "effectivePeriod", header: "From period", align: "end", render: (m) => (m.superseded ? `${m.effectivePeriod} (superseded)` : String(m.effectivePeriod)) },
    { key: "paymentAmount", header: "Payment", align: "end", render: (m) => money(m.paymentAmount, currency) },
    { key: "termMonths", header: "Term (months)", align: "end" },
    { key: "ibrRate", header: "Rate", align: "end", render: (m) => `${percent(m.ibrRate)}${m.scopeChange ? " (revised)" : ""}` },
    { key: "liabilityAfter", header: "Liability after", align: "end", render: (m) => money(m.liabilityAfter, currency) },
    { key: "adjustment", header: "Adjustment", align: "end", render: (m) => money(m.adjustment, currency) },
    { key: "rouAfter", header: "ROU after", align: "end", render: (m) => money(m.rouAfter, currency) },
    { key: "profitLossExcess", header: "To profit or loss", align: "end", render: (m) => (isZero(m.profitLossExcess) ? "" : money(m.profitLossExcess, currency)) },
  ];
}

type PostingRow = { key: string; label: string; date: string; journalId: string | null };

const postingColumns: DataTableColumn<PostingRow>[] = [
  { key: "label", header: "Journal" },
  { key: "date", header: "Posting date", mono: true },
  { key: "journalId", header: "Ledger entry", mono: true, render: (r) => (r.journalId ? r.journalId : "Not booked") },
];

/** Which journals of the lease are in the general ledger (D4d). */
function LedgerCard({ lease }: { lease: Lease }) {
  const state = useLoad((signal) => getLeasePostings(lease.id, signal), `${lease.id}:${lease.version}`);
  const link = `/assets/leases/posting?legalEntityId=${lease.legalEntityId}&leaseId=${lease.id}`;
  const action = (
    <Link className={buttonClassName("ghost", "sm")} to={link}>
      Post to the ledger
    </Link>
  );
  if (state.error) {
    return (
      <Card title="Ledger postings" actions={action}>
        <Alert tone="danger" title="Could not load the ledger postings" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </Card>
    );
  }
  if (!state.data) {
    return (
      <Card title="Ledger postings" actions={action}>
        <div aria-busy="true">
          <span className="visually-hidden">Loading the ledger postings…</span>
          <Skeleton height="tile" />
        </div>
      </Card>
    );
  }
  const postings: LeasePostings = state.data;
  const rows: PostingRow[] = [
    // A lease under an election is never recognized, so it has no recognition journal to wait for.
    ...(isExempt(lease) ? [] : [{ key: "0", label: "Recognition", date: lease.commencementDate, journalId: postings.recognitionJournalId }]),
    ...postings.periods.map((p) => ({ key: String(p.periodNo), label: `Period ${p.periodNo}`, date: p.periodEnd, journalId: p.postedJournalId })),
    ...(lease.termination
      ? [{ key: "termination", label: "Termination", date: lease.termination.terminationDate, journalId: postings.terminationJournalId ?? null }]
      : []),
  ];
  const booked = rows.filter((r) => r.journalId).length;
  return (
    <Card title="Ledger postings" actions={action}>
      <div className={styles.stack}>
        <p className={styles.note}>{`${booked} of ${rows.length} journals are in the ledger. A period that is booked cannot be remeasured.`}</p>
        <DataTable label="Ledger postings" columns={postingColumns} rows={rows} getRowKey={(r) => r.key} empty={{ title: "No journals" }} />
      </div>
    </Card>
  );
}

/** The early termination of a lease and what it derecognized (D4c). */
function TerminationCard({ lease, termination }: { lease: Lease; termination: LeaseTermination }) {
  const gain = !termination.gainLoss.startsWith("-");
  return (
    <Card title="Termination">
      <div className={styles.stack}>
        <dl className={styles.details}>
          <Detail term="Terminated on">{termination.terminationDate} (after period {termination.afterPeriod})</Detail>
          <Detail term="Liability derecognized">{money(termination.liabilityDerecognized, lease.currency)}</Detail>
          <Detail term="ROU asset derecognized">{money(termination.rouCarrying, lease.currency)} carrying ({money(termination.rouGross, lease.currency)} cost less {money(termination.rouAccumulated, lease.currency)} depreciation)</Detail>
          <Detail term="Termination payment">{money(termination.terminationPayment, lease.currency)}</Detail>
          <Detail term={gain ? "Gain" : "Loss"}>{money(gain ? termination.gainLoss : termination.gainLoss.slice(1), lease.currency)}</Detail>
          {termination.reason ? <Detail term="Reason">{termination.reason}</Detail> : null}
        </dl>
        <p className={styles.note}>{termination.journalId ? "The termination journal is in the ledger." : "The termination journal is booked with the lease postings, after every period up to the termination."}</p>
      </div>
    </Card>
  );
}

/** A lease under a short-term or low-value election has nothing to remeasure or derecognize (AST-0037). */
export const isExempt = (lease: Lease) => lease.election === "ShortTerm" || lease.election === "LowValue";

export const canModify = (lease: Lease) => (lease.status === "Active" || lease.status === "Modified") && !isExempt(lease);

/** Lease detail: recognized liability and ROU asset, the schedule in force and its remeasurements (FR-37.10). */
export function LeaseDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getLease(id, signal), id);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Lease" />
        <Alert tone="danger" title="Could not load the lease" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link className={buttonClassName("ghost")} to="/assets/leases">
          Back to leases
        </Link>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Lease" />
        <span className="visually-hidden">Loading the lease…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const lease = state.data.value;
  const schedule = lease.schedule ?? [];
  const modifications = lease.modifications ?? [];
  const last = schedule[schedule.length - 1];
  const latest = modifications[modifications.length - 1];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Lease ${lease.leaseNumber}`}
        meta={<StatusBadge status={lease.status} />}
        actions={
          canModify(lease) ? (
            <>
              <Link className={buttonClassName("primary")} to={`/assets/leases/${lease.id}/modify`}>
                Modify lease
              </Link>
              <Link className={buttonClassName("ghost")} to={`/assets/leases/${lease.id}/terminate`}>
                Terminate lease
              </Link>
            </>
          ) : undefined
        }
      />
      {isExempt(lease) ? (
        <Alert tone="info" title="Not recognized on the balance sheet">
          This lease is under a {lease.election === "ShortTerm" ? "short-term" : "low-value"} election (IFRS 16.5-8): it has no liability or right-of-use asset, and each payment is expensed when its period is posted. It cannot be modified or terminated.
        </Alert>
      ) : null}
      <StatGroup label="Lease balances">
        <StatTile label="Recognized liability" value={money(lease.liability, lease.currency)} />
        <StatTile label="Recognized ROU asset" value={money(lease.rouAsset, lease.currency)} />
        {latest ? <StatTile label="Liability after last remeasurement" value={money(latest.liabilityAfter, lease.currency)} /> : null}
        <StatTile label="Periods" value={schedule.length} />
      </StatGroup>
      <Card title="Terms in force">
        <dl className={styles.details}>
          <Detail term="Commencement">{lease.commencementDate}</Detail>
          <Detail term="Term">{lease.termMonths} months</Detail>
          <Detail term="Payment">{money(lease.paymentAmount, lease.currency)}</Detail>
          <Detail term="Payments per year">{lease.paymentsPerYear}</Detail>
          <Detail term="Timing">{lease.paymentTiming === "advance" ? "In advance" : "In arrears"}</Detail>
          <Detail term="Incremental borrowing rate">{percent(lease.ibrRate)}</Detail>
          <Detail term="Periodic rate">{lease.rateMethod === "Effective" ? "Effective (compounds to the annual rate)" : "Nominal (annual rate / payments per year)"}</Detail>
          {isExempt(lease) ? (
            <Detail term="Recognition exemption">
              {lease.election === "ShortTerm" ? "Short-term lease" : `Low-value asset (${money(lease.underlyingAssetValue ?? "0", lease.underlyingAssetCurrency ?? lease.currency)} when new)`}
            </Detail>
          ) : null}
          {lease.purchaseOption ? <Detail term="Purchase option">The lease contains a purchase option</Detail> : null}
          <Detail term="Legal entity">
            <span className={styles.mono}>{lease.legalEntityId}</span>
          </Detail>
        </dl>
      </Card>
      {modifications.length > 0 ? (
        <Card title="Remeasurements">
          <DataTable label="Remeasurements" columns={modificationColumns(lease.currency)} rows={modifications} getRowKey={(m) => m.id} empty={{ title: "No remeasurements" }} />
        </Card>
      ) : null}
      {lease.termination ? <TerminationCard lease={lease} termination={lease.termination} /> : null}
      <LedgerCard lease={lease} />
      <Card title="Liability and ROU schedule">
        <div className={styles.stack}>
          {last ? <p className={styles.note}>The final period closes the liability at {money(last.closing, lease.currency)}.</p> : null}
          <DataTable
            label="Lease schedule"
            columns={columns}
            rows={schedule}
            getRowKey={(l) => String(l.periodNo)}
            empty={{ title: "No schedule", description: "The service returns the schedule with the lease." }}
          />
        </div>
      </Card>
    </div>
  );
}

export interface ModifyValues {
  effectivePeriod: string;
  paymentAmount: string;
  scopeChange: boolean;
  termMonths: string;
  ibrRate: string;
}

type ModifyErrors = Partial<Record<keyof ModifyValues, string>>;

const RATE = /^0(\.\d{1,6})?$/;

/** Checks that mirror the service (internal/api modifyLease, domain.Remeasure). */
export function validateModification(values: ModifyValues, lease: Pick<Lease, "termMonths" | "paymentsPerYear"> & { periods: number }): { errors: ModifyErrors; input?: ModifyLeaseInput } {
  const errors: ModifyErrors = {};
  const period = Number(values.effectivePeriod);
  if (!/^\d+$/.test(values.effectivePeriod) || period < 1 || period > lease.periods) {
    errors.effectivePeriod = `Enter the first period under the new terms, 1 to ${lease.periods}.`;
  }
  if (!DECIMAL.test(values.paymentAmount) || isZero(values.paymentAmount)) errors.paymentAmount = "Enter a payment above 0, such as 12000.00.";
  let term: number | undefined;
  if (values.scopeChange) {
    if (values.termMonths.trim() !== "") {
      term = Number(values.termMonths);
      if (!/^\d+$/.test(values.termMonths) || term < 1 || term > 600) errors.termMonths = "Enter a term of 1 to 600 months, or leave it empty to keep the term.";
      else if ((term * lease.paymentsPerYear) % 12 !== 0) errors.termMonths = "The term must be a whole number of payment periods.";
      else if (!errors.effectivePeriod && (term * lease.paymentsPerYear) / 12 < period) errors.termMonths = "The revised term must leave at least the effective period.";
    }
    if (!RATE.test(values.ibrRate) || isZero(values.ibrRate) || Number(values.ibrRate) > 0.5) {
      errors.ibrRate = "Enter the revised annual rate as a decimal above 0 and at most 0.5, such as 0.06.";
    }
  }
  if (Object.keys(errors).length > 0) return { errors };
  const input: ModifyLeaseInput = { effectivePeriod: period, paymentAmount: values.paymentAmount, scopeChange: values.scopeChange };
  if (values.scopeChange) {
    input.ibrRate = values.ibrRate;
    if (term !== undefined && term !== lease.termMonths) input.termMonths = term;
  }
  return { errors, input };
}

/** Modify a lease (BR-37.8): a payment change keeps the rate, a scope change uses the revised rate. */
export function ModifyLeasePage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getLease(id, signal), id);
  const [values, setValues] = useState<ModifyValues>({ effectivePeriod: "", paymentAmount: "", scopeChange: false, termMonths: "", ibrRate: "" });
  const [errors, setErrors] = useState<ModifyErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<LeaseModificationResult | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Modify lease" />
        <Alert tone="danger" title="Could not load the lease" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
        <Link className={buttonClassName("ghost")} to="/assets/leases">
          Back to leases
        </Link>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Modify lease" />
        <span className="visually-hidden">Loading the lease…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: lease, etag } = state.data;
  const periods = lease.schedule?.length ?? 0;
  const back = `/assets/leases/${lease.id}`;

  function update(patch: Partial<ModifyValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateModification(values, { termMonths: lease.termMonths, paymentsPerYear: lease.paymentsPerYear, periods });
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult(await modifyLease(lease.id, etag, input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as ModifyErrors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (result) {
    const m = result.modification;
    return (
      <div className={styles.stack}>
        <PageHeader title={`Lease ${lease.leaseNumber} remeasured`} meta={<StatusBadge status={result.lease.status} />} />
        <Alert tone="success" title="Lease remeasured" live="status">
          {result.messages.join(" ")}
        </Alert>
        <StatGroup label="Remeasurement">
          <StatTile label="Liability before" value={money(m.liabilityBefore, lease.currency)} />
          <StatTile label="Liability after" value={money(m.liabilityAfter, lease.currency)} />
          <StatTile label="ROU after" value={money(m.rouAfter, lease.currency)} />
          {isZero(m.profitLossExcess) ? null : <StatTile label="To profit or loss" value={money(m.profitLossExcess, lease.currency)} />}
        </StatGroup>
        <div className={styles.actions}>
          <Link className={buttonClassName("primary")} to={back}>
            View the new schedule
          </Link>
        </div>
      </div>
    );
  }

  if (!canModify(lease)) {
    return (
      <div className={styles.stack}>
        <PageHeader title={`Modify lease ${lease.leaseNumber}`} meta={<StatusBadge status={lease.status} />} />
        <Alert tone="warning" title="This lease cannot be modified">
          {isExempt(lease)
            ? "A lease under a short-term or low-value election has no liability to remeasure; record a change of its payments or term as a new lease."
            : "Only an active or modified lease can be remeasured."}
        </Alert>
        <Link className={buttonClassName("ghost")} to={back}>
          Back to the lease
        </Link>
      </div>
    );
  }

  const field = (name: "effectivePeriod" | "paymentAmount" | "termMonths" | "ibrRate", label: string, hint?: string, type = "text") => (
    <FormField id={`modify-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => <Input {...control} type={type} value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Modify lease ${lease.leaseNumber}`}
        description="The liability is remeasured as the present value of the remaining payments from the effective period on. The ROU asset moves by the same amount; any part that would take it below zero goes to profit or loss."
      />
      <Card title="Terms in force">
        <dl className={styles.details}>
          <Detail term="Payment">{money(lease.paymentAmount, lease.currency)}</Detail>
          <Detail term="Term">{lease.termMonths} months ({periods} periods)</Detail>
          <Detail term="Incremental borrowing rate">{percent(lease.ibrRate)}</Detail>
        </dl>
      </Card>
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Modify lease">
          {field("effectivePeriod", "Effective period", `First schedule period under the new terms (1 to ${periods}); earlier periods are kept.`, "number")}
          {field("paymentAmount", "New payment amount", `Per payment, in ${lease.currency}.`)}
          <div className={styles.formActions}>
            <span className={styles.check}>
              <Input id="modify-scopeChange" type="checkbox" checked={values.scopeChange} onChange={(e) => update({ scopeChange: e.target.checked })} />
              <label htmlFor="modify-scopeChange">Scope change</label>
            </span>
            <span className={styles.note}>A change in scope, such as a longer term, is discounted at a revised rate. A payment change alone keeps the lease's rate.</span>
          </div>
          {values.scopeChange ? (
            <>
              {field("termMonths", "Revised term (months)", "Full term from commencement; leave empty to keep it.", "number")}
              {field("ibrRate", "Revised borrowing rate", "Annual rate as a decimal: 0.06 is 6%.")}
            </>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert
                tone="danger"
                title={failure.code === "AST-0006" ? "The lease changed since you opened it" : "Could not modify the lease"}
                live="alert"
                action={failure.code === "AST-0006" ? <Button size="sm" onClick={state.reload}>Reload</Button> : undefined}
              >
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Remeasure lease
            </Button>
            <Link className={buttonClassName("ghost")} to={back}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
