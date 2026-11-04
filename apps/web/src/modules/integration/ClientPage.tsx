import { useRef, useState, type FormEvent } from "react";
import { useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Skeleton, StatusPill, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  BURST_MAX,
  BURST_MIN,
  DUTY_HUB_CLIENT_WRITE,
  getClient,
  getClientUsage,
  QUOTA_MAX,
  QUOTA_MIN,
  revokeClient,
  updateClient,
  type ApiClient,
  type UsageMinute,
} from "./api/hubClient";
import { dash, formatDateTime } from "./format";
import { parseCount } from "./NewClientPage";
import styles from "./pages.module.css";
import { ProblemAlert } from "./parts";
import { useLoad } from "./useLoad";

const usageColumns: DataTableColumn<UsageMinute>[] = [
  { key: "minute", header: "Minute", render: (row) => formatDateTime(row.minute) },
  { key: "allowed", header: "Admitted", align: "end" },
  { key: "throttled", header: "Refused (429)", align: "end" },
];

function Term({ term, children, mono }: { term: string; children: React.ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** The quota form: a new rate and burst, sent with the version last read (409 when someone else changed the client). */
function QuotaCard({ client, onSaved }: { client: ApiClient; onSaved(): void }) {
  const [rate, setRate] = useState(String(client.quotaPerMin));
  const [burst, setBurst] = useState(String(client.burst));
  const [errors, setErrors] = useState<{ rate?: string; burst?: string }>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const saving = useRef(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (saving.current) return;
    const quotaPerMin = parseCount(rate, QUOTA_MIN, QUOTA_MAX);
    const burstCount = parseCount(burst, BURST_MIN, BURST_MAX);
    setErrors({
      ...(quotaPerMin === null ? { rate: `Enter a whole number from ${QUOTA_MIN} to ${QUOTA_MAX}.` } : {}),
      ...(burstCount === null ? { burst: `Enter a whole number from ${BURST_MIN} to ${BURST_MAX}.` } : {}),
    });
    setFailure(null);
    if (quotaPerMin === null || burstCount === null) return;
    saving.current = true;
    setBusy(true);
    try {
      await updateClient(client.id, { version: client.version, quotaPerMin, burst: burstCount });
      onSaved();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem.code === "HUB-0409" ? { ...problem, message: "The client changed since you opened it. Reload and try again." } : problem);
    } finally {
      saving.current = false;
      setBusy(false);
    }
  }

  return (
    <Card title="Quota">
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="client-edit-rate" label="Requests per minute" error={errors.rate}>
          {(control) => <Input {...control} inputMode="numeric" value={rate} onChange={(event) => setRate(event.target.value)} invalid={Boolean(errors.rate)} />}
        </FormField>
        <FormField id="client-edit-burst" label="Burst" error={errors.burst}>
          {(control) => <Input {...control} inputMode="numeric" value={burst} onChange={(event) => setBurst(event.target.value)} invalid={Boolean(errors.burst)} />}
        </FormField>
        {failure ? (
          <div className={styles.wide}>
            <ProblemAlert problem={failure} title="Could not save the quota" />
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" disabled={busy} loading={busy}>
            Save quota
          </Button>
        </div>
      </form>
    </Card>
  );
}

function UsageCard({ id }: { id: string }) {
  const usage = useLoad((signal) => getClientUsage(id, 60, signal), `hub-client-usage:${id}`);
  const rows = usage.data?.minutes ?? [];
  const totals = rows.reduce((sum, row) => ({ allowed: sum.allowed + row.allowed, throttled: sum.throttled + row.throttled }), { allowed: 0, throttled: 0 });
  return (
    <Card title="Usage, last hour">
      {usage.error ? (
        <Alert tone="danger" title="Could not load usage" live="alert" action={<Button size="sm" onClick={usage.reload}>Try again</Button>}>
          {usage.error.message} ({usage.error.code})
        </Alert>
      ) : (
        <div className={styles.stack}>
          {!usage.loading ? <p className={styles.note}>{totals.allowed} admitted, {totals.throttled} refused with 429.</p> : null}
          <DataTable
            label="Client usage"
            columns={usageColumns}
            rows={rows}
            getRowKey={(row) => row.minute}
            loading={usage.loading}
            empty={{ title: "No requests in the last hour" }}
          />
        </div>
      )}
    </Card>
  );
}

/** One API client: settings, quota, usage per minute, and revoke (FR-35.8). */
export function ClientPage() {
  const { id = "" } = useParams();
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_CLIENT_WRITE);
  const state = useLoad((signal) => getClient(id, signal), `hub-client:${id}`);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const revoking = useRef(false);

  async function revoke() {
    if (revoking.current) return;
    revoking.current = true;
    setConfirming(false);
    setBusy(true);
    setFailure(null);
    try {
      await revokeClient(id);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      revoking.current = false;
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="API client" />
        <Alert tone="danger" title="Could not load the API client" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  const client = state.data;
  if (!client) {
    return (
      <div className={styles.stack}>
        <PageHeader title="API client" />
        <Skeleton />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader
        title={client.name}
        description="An external system that calls the public APIs."
        actions={
          canWrite && client.status === "active" ? (
            <Button variant="danger" onClick={() => setConfirming(true)} disabled={busy}>
              Revoke client
            </Button>
          ) : undefined
        }
      />
      {client.status === "revoked" ? (
        <Alert tone="warning" title="Revoked" live="status">
          The hub's quota check refuses this client. Disable the Keycloak client too: the gateway does not ask the quota check yet.
        </Alert>
      ) : null}
      {failure ? <ProblemAlert problem={failure} title="Could not revoke the client" /> : null}
      <Card title="Settings">
        <dl className={styles.details}>
          <Term term="Status">
            <StatusPill status={client.status} />
          </Term>
          <Term term="Client id" mono>
            {client.clientId}
          </Term>
          <Term term="Scopes" mono>
            {client.scopes.join(", ")}
          </Term>
          <Term term="Quota">
            {client.quotaPerMin} requests per minute, burst {client.burst}
          </Term>
          <Term term="Sandbox tenant" mono>
            {client.sandboxTenantId ?? dash}
          </Term>
          <Term term="Owner">{client.ownerContact}</Term>
          <Term term="Registered">{formatDateTime(client.createdAt)}</Term>
          {client.revokedAt ? <Term term="Revoked">{formatDateTime(client.revokedAt)}</Term> : null}
        </dl>
      </Card>
      {canWrite && client.status === "active" ? <QuotaCard key={client.version} client={client} onSaved={state.reload} /> : null}
      <UsageCard id={client.id} />
      {confirming ? (
        <ConfirmDialog
          title="Revoke this client?"
          description={`The hub's quota check will refuse ${client.name} at once; disable its Keycloak client too. This cannot be undone; register a new client to restore access.`}
          confirmLabel="Revoke client"
          cancelLabel="Keep client"
          onConfirm={revoke}
          onCancel={() => setConfirming(false)}
        />
      ) : null}
    </div>
  );
}
