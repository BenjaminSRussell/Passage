import { useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import {
  describeProblem,
  listLegalHolds,
  listLibrarySweeps,
  listRunSweeps,
  newKey,
  placeLegalHold,
  type LegalHold,
  type LibrarySweep,
  type RunSweep,
  type Problem,
} from "./api/client";
import { formatBytes, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const MAX_HOLD_REASON = 500;

export interface HoldValues {
  legalEntityId: string;
  reason: string;
}

/** The fields the service would refuse (REPORT-0049 and a malformed entity id). */
export function validateHold(v: HoldValues): Partial<Record<keyof HoldValues, string>> {
  const errors: Partial<Record<keyof HoldValues, string>> = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id as a UUID.";
  const reason = v.reason.trim();
  if (reason === "") errors.reason = "Say why the library is held, such as the matter or audit reference.";
  else if (reason.length > MAX_HOLD_REASON) errors.reason = `Keep the reason within ${MAX_HOLD_REASON} characters.`;
  return errors;
}

export const holdColumns: DataTableColumn<LegalHold>[] = [
  { key: "legalEntityId", header: "Legal entity", mono: true },
  { key: "reason", header: "Reason" },
  { key: "active", header: "Status", render: (h) => <Badge tone={h.active ? "warning" : "neutral"}>{h.active ? "In force" : "Released"}</Badge> },
  { key: "placedAt", header: "Placed", mono: true, render: (h) => <time dateTime={h.placedAt}>{h.placedAt}</time> },
  { key: "placedBy", header: "Placed by", mono: true },
];

export const sweepColumns: DataTableColumn<LibrarySweep>[] = [
  { key: "sweptAt", header: "Swept", mono: true, render: (w) => <time dateTime={w.sweptAt}>{w.sweptAt}</time> },
  { key: "legalEntityId", header: "Legal entity", mono: true },
  { key: "deletedCount", header: "Files deleted", mono: true, align: "end" },
  { key: "deletedBytes", header: "Size", mono: true, align: "end", render: (w) => formatBytes(w.deletedBytes) },
  { key: "heldCount", header: "Kept by hold", mono: true, align: "end" },
];

export const runSweepColumns: DataTableColumn<RunSweep>[] = [
  { key: "sweptAt", header: "Swept", mono: true, render: (w) => <time dateTime={w.sweptAt}>{w.sweptAt}</time> },
  { key: "legalEntityId", header: "Legal entity", mono: true },
  { key: "retentionDays", header: "Retention (days)", mono: true, align: "end" },
  { key: "deletedRuns", header: "Runs deleted", mono: true, align: "end" },
  { key: "deletedCells", header: "Cells deleted", mono: true, align: "end" },
  { key: "heldRuns", header: "Kept by hold", mono: true, align: "end" },
];

/** Legal holds on the report library (FR-15.8) and the passes of the retention sweep they suspend. */
export function LegalHoldsPage() {
  const navigate = useNavigate();
  const canHold = holdsDuty(usePrincipalDuties(), "reporting.library.hold");
  const [values, setValues] = useState<HoldValues>({ legalEntityId: "", reason: "" });
  const [errors, setErrors] = useState<Partial<Record<keyof HoldValues, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per request, so a retried click after a timeout returns the hold already stored.
  const attempt = useRef<{ signature: string; key: string } | null>(null);
  const holds = useLoad((signal) => listLegalHolds(signal), "holds");
  const sweeps = useLoad((signal) => listLibrarySweeps(signal), "sweeps");
  const runSweeps = useLoad((signal) => listRunSweeps(signal), "run sweeps");

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateHold(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    const entity = values.legalEntityId.trim().toLowerCase();
    const reason = values.reason.trim();
    const signature = `${entity}|${reason}`;
    if (attempt.current?.signature !== signature) attempt.current = { signature, key: newKey() };
    setBusy(true);
    try {
      const hold = await placeLegalHold(entity, reason, attempt.current.key);
      attempt.current = null;
      navigate(`/finreport/legal-holds/${hold.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Legal holds"
        description="A legal hold keeps every file of a legal entity's report library, past its retention date or not. The weekly retention sweep deletes nothing for an entity until its hold is released."
      />
      {canHold ? (
        <Card title="Put a library on hold">
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Put a library on hold">
            <FormField id="fr-hold-entity" label="Legal entity id" error={errors.legalEntityId} hint="One hold can be in force for a legal entity.">
              {(c) => <Input {...c} className={styles.mono} value={values.legalEntityId} invalid={Boolean(errors.legalEntityId)} onChange={(e) => setValues({ ...values, legalEntityId: e.target.value })} />}
            </FormField>
            <FormField id="fr-hold-reason" label="Reason" error={errors.reason} hint={`The matter, case or audit reference. At most ${MAX_HOLD_REASON} characters.`}>
              {(c) => <Input {...c} value={values.reason} invalid={Boolean(errors.reason)} onChange={(e) => setValues({ ...values, reason: e.target.value })} />}
            </FormField>
            {failure ? (
              <Alert tone="danger" title="The hold could not be placed" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" disabled={busy}>
                {busy ? "Placing…" : "Place hold"}
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {holds.error ? (
        <Alert tone="danger" title="Could not load the legal holds" live="alert" action={<Button size="sm" onClick={holds.reload}>Try again</Button>}>
          {holds.error.message} ({holds.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Legal holds"
          columns={holdColumns}
          rows={holds.data ?? []}
          getRowKey={(h) => h.id}
          loading={holds.loading}
          empty={{ title: "No legal hold has been placed", description: "Every legal entity's library is subject to the retention sweep." }}
          onRowOpen={(h) => navigate(`/finreport/legal-holds/${h.id}`)}
        />
      )}
      <Card title="Retention sweeps">
      {sweeps.error ? (
        <Alert tone="danger" title="Could not load the retention sweeps" live="alert" action={<Button size="sm" onClick={sweeps.reload}>Try again</Button>}>
          {sweeps.error.message} ({sweeps.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Retention sweeps"
          columns={sweepColumns}
          rows={sweeps.data ?? []}
          getRowKey={(w) => w.id}
          loading={sweeps.loading}
          empty={{ title: "No library file has passed its retention date", description: "A sweep pass is recorded when it deletes files, or when a legal hold keeps files that are past retention." }}
        />
      )}
      </Card>
      <Card title="Ad-hoc run sweeps">
        {runSweeps.error ? (
          <Alert tone="danger" title="Could not load the ad-hoc run sweeps" live="alert" action={<Button size="sm" onClick={runSweeps.reload}>Try again</Button>}>
            {runSweeps.error.message} ({runSweeps.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Ad-hoc run sweeps"
            columns={runSweepColumns}
            rows={runSweeps.data ?? []}
            getRowKey={(w) => w.id}
            loading={runSweeps.loading}
            empty={{ title: "No ad-hoc run has passed its retention", description: "Statement runs that no schedule produced are deleted with their cells after the retention days, unless a legal hold keeps them. A pass is recorded when it deletes runs or a hold keeps them." }}
          />
        )}
      </Card>
    </div>
  );
}
