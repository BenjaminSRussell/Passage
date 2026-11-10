import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, ConfirmDialog, FormField, Input, PageHeader, Skeleton, StatGroup, StatTile } from "../../shell/ui";
import { describeProblem, getLease, newKey, terminateLease, type Lease, type LeaseTerminationResult, type Problem, type TerminateLeaseInput } from "./api/client";
import { StatusBadge } from "./badges";
import { isExempt } from "./LeaseDetailPage";
import { DECIMAL, money } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

export interface TerminateValues {
  afterPeriod: string;
  terminationPayment: string;
  reason: string;
}

type TerminateErrors = Partial<Record<keyof TerminateValues, string>>;

/** Checks that mirror the service (internal/api terminateLease, domain.ValidTerminationPeriod). */
export function validateTermination(values: TerminateValues, periods: number): { errors: TerminateErrors; input?: TerminateLeaseInput } {
  const errors: TerminateErrors = {};
  const period = Number(values.afterPeriod);
  if (!/^\d+$/.test(values.afterPeriod) || period < 1 || period >= periods) {
    errors.afterPeriod = `Enter the last period the lease stays in force, 1 to ${periods - 1}.`;
  }
  const payment = values.terminationPayment.trim();
  if (payment !== "" && !DECIMAL.test(payment)) errors.terminationPayment = "Enter the payment as a decimal of 0 or more, such as 1500.00, or leave it empty.";
  if (values.reason.trim().length > 500) errors.reason = "Use at most 500 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: TerminateLeaseInput = { afterPeriod: period };
  if (payment !== "") input.terminationPayment = payment;
  if (values.reason.trim() !== "") input.reason = values.reason.trim();
  return { errors, input };
}

/** The last day of payment period `period` (1-based), as the service dates it (domain.LeasePeriodEnd): commencement plus the period's months,
 * a month too short for the day ending on its last day, less one day. */
export function leasePeriodEnd(commencement: string, paymentsPerYear: number, period: number): string {
  const [y, m, d] = commencement.split("-").map(Number);
  const months = m - 1 + Math.floor((period * 12) / paymentsPerYear);
  const lastDay = new Date(Date.UTC(y, months + 1, 0)).getUTCDate();
  const end = new Date(Date.UTC(y, months, Math.min(d, lastDay)));
  end.setUTCDate(end.getUTCDate() - 1);
  return end.toISOString().slice(0, 10);
}

const canTerminate = (lease: Lease) => (lease.status === "Active" || lease.status === "Modified") && !isExempt(lease);

/** Terminate a lease early (D4c): the liability and the ROU asset are derecognized with the gain or loss. */
export function TerminateLeasePage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getLease(id, signal), id);
  const [values, setValues] = useState<TerminateValues>({ afterPeriod: "", terminationPayment: "", reason: "" });
  const [errors, setErrors] = useState<TerminateErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<LeaseTerminationResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<TerminateLeaseInput | null>(null);
  const key = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Terminate lease" />
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
        <PageHeader title="Terminate lease" />
        <span className="visually-hidden">Loading the lease…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: lease, etag } = state.data;
  const periods = lease.schedule?.length ?? 0;
  const back = `/assets/leases/${lease.id}`;

  function update(patch: Partial<TerminateValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateTermination(values, periods);
    setErrors(found);
    setFailure(null);
    if (input) setPending(input);
  }

  async function confirm(input: TerminateLeaseInput) {
    setPending(null);
    setBusy(true);
    try {
      setResult(await terminateLease(lease.id, etag, input, key.current));
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as TerminateErrors);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (result) {
    const t = result.termination;
    const gain = !t.gainLoss.startsWith("-");
    return (
      <div className={styles.stack}>
        <PageHeader title={`Lease ${lease.leaseNumber} terminated`} meta={<StatusBadge status={result.lease.status} />} />
        <Alert tone="success" title="Lease terminated" live="status">
          {result.messages.join(" ")}
        </Alert>
        <StatGroup label="Termination">
          <StatTile label="Liability derecognized" value={money(t.liabilityDerecognized, lease.currency)} />
          <StatTile label="ROU asset derecognized" value={money(t.rouCarrying, lease.currency)} />
          <StatTile label="Termination payment" value={money(t.terminationPayment, lease.currency)} />
          <StatTile label={gain ? "Gain" : "Loss"} value={money(gain ? t.gainLoss : t.gainLoss.slice(1), lease.currency)} />
        </StatGroup>
        <p className={styles.note}>Post the lease to the ledger to book the termination journal after the periods up to {t.terminationDate}.</p>
        <div className={styles.actions}>
          <Link className={buttonClassName("primary")} to={back}>
            View the lease
          </Link>
          <Link className={buttonClassName("ghost")} to={`/assets/leases/posting?legalEntityId=${lease.legalEntityId}&leaseId=${lease.id}`}>
            Post to the ledger
          </Link>
        </div>
      </div>
    );
  }

  if (!canTerminate(lease)) {
    return (
      <div className={styles.stack}>
        <PageHeader title={`Terminate lease ${lease.leaseNumber}`} meta={<StatusBadge status={lease.status} />} />
        <Alert tone="warning" title="This lease cannot be terminated">
          {isExempt(lease)
            ? "A lease under a short-term or low-value election has no liability or right-of-use asset to derecognize (AST-0037)."
            : "Only an active or modified lease can be terminated early."}
        </Alert>
        <Link className={buttonClassName("ghost")} to={back}>
          Back to the lease
        </Link>
      </div>
    );
  }

  const field = (name: keyof TerminateValues, label: string, hint?: string, type = "text") => (
    <FormField id={`terminate-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => <Input {...control} type={type} value={values[name]} onChange={(e) => update({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Terminate lease ${lease.leaseNumber}`}
        description="The schedule after the last period in force is dropped. The liability at that point and the ROU asset carrying amount leave the books; the difference, less any termination payment, is a gain or loss."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Terminate lease">
          {field("afterPeriod", "Last period in force", `The lease ends on the last day of this period (1 to ${Math.max(periods - 1, 1)}), at the latest the period running today; to end it at a later date, modify the lease with the shorter term. A period already posted to the ledger after it blocks the termination.`, "number")}
          {field("terminationPayment", "Termination payment", `What you pay to end the lease, in ${lease.currency}; leave empty for none.`)}
          {field("reason", "Reason", "Optional, up to 500 characters.")}
          {failure ? (
            <div className={styles.formActions}>
              <Alert
                tone="danger"
                title={failure.code === "AST-0006" ? "The lease changed since you opened it" : "Could not terminate the lease"}
                live="alert"
                action={failure.code === "AST-0006" ? <Button size="sm" onClick={state.reload}>Reload</Button> : undefined}
              >
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Terminate lease
            </Button>
            <Link className={buttonClassName("ghost")} to={back}>
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {pending ? (
        <ConfirmDialog
          title={`Terminate lease ${lease.leaseNumber}?`}
          description={`The lease ends on ${leasePeriodEnd(lease.commencementDate, lease.paymentsPerYear, pending.afterPeriod)}, after period ${pending.afterPeriod}. The schedule after it is dropped and the liability and ROU asset are derecognized. A termination cannot be undone.`}
          confirmLabel="Confirm termination"
          cancelLabel="Keep the lease"
          onConfirm={() => void confirm(pending)}
          onCancel={() => setPending(null)}
        />
      ) : null}
    </div>
  );
}
