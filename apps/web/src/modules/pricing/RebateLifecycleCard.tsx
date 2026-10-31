import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, ConfirmDialog, FormField, Input } from "../../shell/ui";
import { closeRebateAgreement, describeProblem, newKey, PricingApiError, reviseRebateAgreement, type Problem, type RebateAgreement } from "./api/client";
import { DAY } from "./format";
import styles from "./pages.module.css";

/** What a closed or revised agreement tells the planner, by service code. */
function failureTitle(code: string): string {
  if (code === "PRC-0048") return "That date is before an invoice this agreement already accrued";
  if (code === "PRC-0049") return "Tiers cannot change after invoices have accrued";
  if (code === "PRC-0031") return "The agreement is closed";
  if (code === "conflict_version") return "The agreement changed since you opened it";
  return "The change failed";
}

type Kind = "revise" | "close";

/**
 * Revise (name, end date) and close an ACTIVE rebate agreement (FR-26.11). Tiers cannot change
 * once invoices have accrued (PRC-0049); the service refuses, and the note says to create a new agreement.
 * Closing cannot be undone, so it is confirmed first.
 */
export function RebateLifecycleCard({ agreement, onChange }: { agreement: RebateAgreement; onChange: (next: RebateAgreement) => void }) {
  const [name, setName] = useState(agreement.name);
  const [validTo, setValidTo] = useState(agreement.validTo);
  const [closeDate, setCloseDate] = useState(agreement.validTo);
  const [errors, setErrors] = useState<{ name?: string; validTo?: string; closeDate?: string }>({});
  const [busy, setBusy] = useState<Kind | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  // One key per command: a retried revise must not collide with a later close under the same key.
  const keys = useRef<Record<Kind, string>>({ revise: newKey(), close: newKey() });
  const version = agreement.version;

  if (agreement.status !== "ACTIVE") {
    return (
      <Card title="Change or close">
        <p className={styles.note}>This agreement is closed. Invoices dated after {agreement.validTo} no longer accrue; the accruals already booked stay.</p>
      </Card>
    );
  }

  function check(kind: Kind): boolean {
    setFailure(null);
    setDone("");
    const next: typeof errors = {};
    if (kind === "revise") {
      if (name.trim() === "" || name.length > 120) next.name = "Enter a name of 1 to 120 characters.";
      if (!DAY.test(validTo)) next.validTo = "Enter the end date as YYYY-MM-DD.";
      else if (validTo < agreement.validFrom) next.validTo = "The end date cannot be before the start date.";
      if (!next.name && !next.validTo && name.trim() === agreement.name && validTo === agreement.validTo) next.name = "Change the name or the end date.";
    } else if (!DAY.test(closeDate)) {
      next.closeDate = "Enter the close date as YYYY-MM-DD.";
    } else if (closeDate < agreement.validFrom || closeDate > agreement.validTo) {
      next.closeDate = `Choose a day from ${agreement.validFrom} to ${agreement.validTo}.`;
    }
    setErrors(next);
    return Object.keys(next).length === 0;
  }

  async function run(kind: Kind) {
    setConfirming(false);
    setBusy(kind);
    try {
      const changed =
        kind === "revise"
          ? await reviseRebateAgreement(
              agreement.id,
              { version, name: name.trim() !== agreement.name ? name.trim() : undefined, validTo: validTo !== agreement.validTo ? validTo : undefined },
              keys.current.revise,
            )
          : await closeRebateAgreement(agreement.id, version, closeDate, keys.current.close);
      onChange(changed);
      setName(changed.name);
      setValidTo(changed.validTo);
      setCloseDate(changed.validTo);
      setDone(kind === "revise" ? "The agreement is updated." : `The agreement is closed; invoices dated after ${changed.validTo} no longer accrue.`);
      keys.current[kind] = newKey();
    } catch (error) {
      setFailure(describeProblem(error));
      // A refused command is final; only a network or server failure may be retried with the same key.
      if (error instanceof PricingApiError && error.status < 500) keys.current[kind] = newKey();
    } finally {
      setBusy(null);
    }
  }

  function submitRevise(event: FormEvent) {
    event.preventDefault();
    if (check("revise")) void run("revise");
  }

  function submitClose(event: FormEvent) {
    event.preventDefault();
    if (check("close")) setConfirming(true);
  }

  return (
    <Card title="Change or close">
      <div className={styles.stack}>
        <Alert tone="success" live="status">
          {done}
        </Alert>
        {failure ? (
          <Alert tone="danger" title={failureTitle(failure.code)} live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        <form className={styles.form} onSubmit={submitRevise} noValidate>
          <FormField id="rb-revise-name" label="Name" error={errors.name}>
            {(c) => <Input {...c} value={name} maxLength={120} onChange={(e) => { setName(e.target.value); keys.current.revise = newKey(); }} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="rb-revise-valid-to" label="Valid to" error={errors.validTo}>
            {(c) => <Input {...c} type="date" value={validTo} onChange={(e) => { setValidTo(e.target.value); keys.current.revise = newKey(); }} invalid={Boolean(errors.validTo)} />}
          </FormField>
          <p className={`${styles.note} ${styles.wide}`}>The end date cannot move before an invoice already accrued. Tiers cannot change once invoices have accrued: close the agreement and create a new one.</p>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy === "revise"}>
              Save changes
            </Button>
          </div>
        </form>
        <form className={styles.toolbar} onSubmit={submitClose} noValidate>
          <FormField id="rb-close-date" label="Close date" hint="The last day on which invoices accrue." error={errors.closeDate}>
            {(c) => <Input {...c} type="date" value={closeDate} onChange={(e) => { setCloseDate(e.target.value); keys.current.close = newKey(); }} invalid={Boolean(errors.closeDate)} />}
          </FormField>
          <Button type="submit" variant="danger" loading={busy === "close"}>
            Close agreement
          </Button>
        </form>
      </div>
      {confirming ? (
        <ConfirmDialog
          title="Close this rebate agreement?"
          description={`Invoices dated after ${closeDate} will no longer accrue. The accruals already booked stay. A closed agreement cannot be reopened or changed.`}
          confirmLabel={`Close on ${closeDate}`}
          cancelLabel="Keep it open"
          onConfirm={() => void run("close")}
          onCancel={() => setConfirming(false)}
        />
      ) : null}
    </Card>
  );
}
