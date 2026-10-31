import { useEffect, useState, type FormEvent } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import { describeProblem, etagOf, getChangePolicy, putChangePolicy, type Problem } from "./api/client";
import { DUTY_ORDER_APPROVE } from "./ChangeRequestDetailPage";
import { formatDateTime, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Scope {
  legalEntityId: string;
  customerId: string;
}

function PolicyForm({ scope, canSave }: { scope: Scope; canSave: boolean }) {
  const state = useLoad((signal) => getChangePolicy(scope.legalEntityId, scope.customerId, signal), `${scope.legalEntityId}:${scope.customerId}`);
  const [required, setRequired] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);
  const loaded = state.data?.value;

  useEffect(() => {
    if (loaded) setRequired(loaded.approvalRequired);
  }, [loaded]);

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
    setFailure(null);
    setDone("");
    setBusy(true);
    try {
      await putChangePolicy(scope.legalEntityId, scope.customerId, required, state.data?.etag || etagOf(loaded?.version ?? 0));
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
