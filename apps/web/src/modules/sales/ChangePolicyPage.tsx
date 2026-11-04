import { useEffect, useState, type FormEvent } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, FormField, Input, PageHeader, Skeleton, Textarea } from "../../shell/ui";
import { describeProblem, etagOf, getChangePolicy, putChangePolicy, type ChangePolicyInput, type Problem } from "./api/client";
import { DUTY_ORDER_APPROVE } from "./ChangeRequestDetailPage";
import { formatDateTime, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const MAX_APPROVERS = 20;

/** One user id per line or comma-separated; blanks and repeats (in any case) dropped. */
function parseApprovers(text: string): string[] {
  return [...new Set(text.split(/[\n,]/).map((u) => u.trim().toLowerCase()).filter(Boolean))];
}

/** A threshold is empty (every change needs approval) or an amount above 0 with at most 4 decimals. */
function thresholdError(text: string): string | undefined {
  const t = text.trim();
  if (!t) return undefined;
  return /^\d+(\.\d{1,4})?$/.test(t) && Number(t) > 0 ? undefined : "Enter an amount above 0 with at most 4 decimals.";
}

interface Scope {
  legalEntityId: string;
  customerId: string;
}

function PolicyForm({ scope, canSave }: { scope: Scope; canSave: boolean }) {
  const state = useLoad((signal) => getChangePolicy(scope.legalEntityId, scope.customerId, signal), `${scope.legalEntityId}:${scope.customerId}`);
  const [required, setRequired] = useState(false);
  const [threshold, setThreshold] = useState("");
  const [currency, setCurrency] = useState("");
  const [approvers, setApprovers] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);
  const loaded = state.data?.value;

  useEffect(() => {
    if (!loaded) return;
    setRequired(loaded.approvalRequired);
    setThreshold(loaded.approvalThreshold ?? "");
    setCurrency(loaded.thresholdCurrency ?? "");
    setApprovers((loaded.approverIds ?? []).join("\n"));
  }, [loaded]);

  const errors = {
    threshold: thresholdError(threshold),
    currency: threshold.trim() && !/^[A-Z]{3}$/.test(currency.trim()) ? "Enter a three-letter currency such as USD." : !threshold.trim() && currency.trim() ? "Enter the threshold this currency belongs to." : undefined,
    approvers: parseApprovers(approvers).some((u) => !UUID.test(u))
      ? "Enter each approver as a user id (UUID)."
      : parseApprovers(approvers).length > MAX_APPROVERS
        ? `Name at most ${MAX_APPROVERS} approvers.`
        : undefined,
  };

  if (state.error) {
    return (
      <Alert tone="danger" title="Could not load the change approval setting" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        {state.error.message} ({state.error.code})
      </Alert>
    );
  }
  if (!state.data || !loaded) {
    return (
      <div aria-busy="true">
        <span className="visually-hidden">Loading the change approval setting…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    if (errors.threshold || errors.currency || errors.approvers) return;
    setFailure(null);
    setDone("");
    setBusy(true);
    try {
      // The form shows the whole policy, so it names every member: null and []
      // clear what the user emptied (a member left out would be kept).
      const policy: ChangePolicyInput = {
        approvalRequired: required,
        approvalThreshold: threshold.trim() || null,
        thresholdCurrency: threshold.trim() ? currency.trim() : null,
        approverIds: parseApprovers(approvers),
      };
      await putChangePolicy(scope.legalEntityId, scope.customerId, policy, state.data?.etag || etagOf(loaded?.version ?? 0));
      setDone(required ? "Changes to this customer's confirmed orders now need approval." : "Changes to this customer's confirmed orders no longer need approval.");
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.stack} onSubmit={save} noValidate aria-label="Change approval">
      <Card title="Change approval">
        {loaded.version === 0 ? <p className={styles.note}>This customer has no setting saved; changes apply at once, the default.</p> : null}
        <FormField id="policy-required" label="Changes to confirmed orders need approval" hint="When on, a change by anyone without the approve duty waits in the change request queue.">
          {(control) => <Input {...control} type="checkbox" checked={required} disabled={!canSave} onChange={(e) => setRequired(e.target.checked)} />}
        </FormField>
        <FormField id="policy-threshold" label="Approval threshold" error={errors.threshold} hint="Optional. Changes apply at once while what they move, added up line by line and over every change made without approval, stays below this; a cancellation counts the whole order total. Leave empty to approve every change.">
          {(control) => <Input {...control} inputMode="decimal" value={threshold} disabled={!canSave || !required} invalid={Boolean(errors.threshold)} onChange={(e) => setThreshold(e.target.value)} />}
        </FormField>
        <FormField id="policy-currency" label="Threshold currency" error={errors.currency} hint="An order in another currency always needs approval, as no rate converts it yet.">
          {(control) => <Input {...control} maxLength={3} value={currency} disabled={!canSave || !required} invalid={Boolean(errors.currency)} onChange={(e) => setCurrency(e.target.value.toUpperCase())} />}
        </FormField>
        <FormField id="policy-approvers" label="Named approvers" error={errors.approvers} hint="Optional user ids (UUIDs), one per line. When set, only these users can approve or reject this customer's requests or change its confirmed orders without a request; leave empty to let anyone with the approve duty decide.">
          {(control) => <Textarea {...control} rows={4} className={styles.mono} value={approvers} disabled={!canSave} invalid={Boolean(errors.approvers)} onChange={(e) => setApprovers(e.target.value)} />}
        </FormField>
      </Card>
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.code === "SLS-0006" ? "Someone else changed this setting" : "Could not save the setting"} live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <div className={styles.actions}>
        {canSave ? (
          <Button type="submit" variant="primary" loading={busy}>
            Save setting
          </Button>
        ) : (
          <span className={styles.muted}>Changing it needs the {DUTY_ORDER_APPROVE} duty.</span>
        )}
        {loaded.updatedAt ? <span className={styles.muted}>Last changed {formatDateTime(loaded.updatedAt)} by {loaded.updatedBy}</span> : null}
      </div>
    </form>
  );
}

/** Whether a customer's changes to confirmed orders need approval (PRD §5.27.13 change_approval_required). */
export function ChangePolicyPage() {
  const canSave = holdsDuty(usePrincipalDuties(), DUTY_ORDER_APPROVE);
  const [draft, setDraft] = useState({ legalEntityId: "", customerId: "" });
  const [scope, setScope] = useState<Scope | null>(null);
  const errors = {
    legalEntityId: draft.legalEntityId.trim() && !UUID.test(draft.legalEntityId.trim()) ? "Enter a legal entity UUID." : undefined,
    customerId: draft.customerId.trim() && !UUID.test(draft.customerId.trim()) ? "Enter a customer UUID." : undefined,
  };

  return (
    <div className={styles.stack}>
      <PageHeader title="Customer change approval" description="Choose whether a customer's changes to confirmed orders wait for a sales manager's approval." />
      <form
        className={styles.toolbar}
        onSubmit={(e) => {
          e.preventDefault();
          const le = draft.legalEntityId.trim();
          const customer = draft.customerId.trim();
          if (UUID.test(le) && UUID.test(customer)) setScope({ legalEntityId: le, customerId: customer });
        }}
      >
        <FormField id="policy-le" label="Legal entity" error={errors.legalEntityId}>
          {(control) => <Input {...control} className={styles.mono} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
        </FormField>
        <FormField id="policy-customer" label="Customer" error={errors.customerId}>
          {(control) => <Input {...control} className={styles.mono} value={draft.customerId} onChange={(e) => setDraft({ ...draft, customerId: e.target.value })} invalid={Boolean(errors.customerId)} />}
        </FormField>
        <Button type="submit">Open</Button>
      </form>
      {scope ? (
        <PolicyForm key={`${scope.legalEntityId}:${scope.customerId}`} scope={scope} canSave={canSave} />
      ) : (
        <Alert tone="info" title="Choose a customer">
          Enter the legal entity and customer UUIDs to review or change the setting.
        </Alert>
      )}
    </div>
  );
}
