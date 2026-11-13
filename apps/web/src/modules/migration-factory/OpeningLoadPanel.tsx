import { useState, type FormEvent } from "react";
import { ProblemMessage } from "../../shell/errors";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, StatGroup, StatTile, StatusPill, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getOpeningLoad, runOpeningLoad, type OpeningLoadDetail, type Problem, type TargetLoadStatus } from "./api/client";
import { dash, formatDateTime, formatKey, money, type ControlTotal } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const STATUS: Record<TargetLoadStatus, { label: string; tone: BadgeTone }> = {
  STAGED: { label: "Ready to load", tone: "neutral" },
  LOADING: { label: "Loading", tone: "info" },
  PARTIAL: { label: "Partly loaded", tone: "warning" },
  UNRECONCILED: { label: "Totals not read", tone: "warning" },
  MISMATCH: { label: "Totals differ", tone: "danger" },
  RECONCILED: { label: "Reconciled", tone: "success" },
  FAILED: { label: "Failed", tone: "danger" },
};

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

interface MapRow {
  legacy: string;
  id: string;
}

export interface OpeningForm {
  legalEntityId: string;
  postingDate: string;
  rows: MapRow[];
}

/** Checks the form like the service: a legal entity, the cutover date, and each mapping row complete and named once. */
export function validateOpeningForm(f: OpeningForm): Record<string, string> {
  const e: Record<string, string> = {};
  if (!UUID.test(f.legalEntityId)) e.legalEntityId = "Choose the legal entity the balances open in.";
  if (!DATE.test(f.postingDate)) e.postingDate = "Enter the cutover date.";
  const seen = new Set<string>();
  f.rows.forEach((r, i) => {
    const legacy = r.legacy.trim();
    if (legacy === "" && r.id === "") return;
    if (legacy === "") e[`rows.${i}.legacy`] = "Enter the legacy code.";
    else if (seen.has(legacy)) e[`rows.${i}.legacy`] = "This legacy code is mapped twice.";
    seen.add(legacy);
    if (r.id === "") e[`rows.${i}.id`] = "Choose what it maps to.";
  });
  return e;
}

/** The request of the form: the mapping rows as a map, empty rows left out. */
export function toOpeningRequest(f: OpeningForm, pack: "onhand" | "bank_balances") {
  const map: Record<string, string> = {};
  for (const r of f.rows) if (r.legacy.trim() !== "" && r.id !== "") map[r.legacy.trim()] = r.id;
  return { legalEntityId: f.legalEntityId, postingDate: f.postingDate, ...(pack === "onhand" ? { products: map } : { bankAccounts: map }) };
}

const reconciliationColumns = (target: string): DataTableColumn<ControlTotal>[] => [
  { key: "name", header: "Reconciliation", mono: true, render: (row) => row.name.replace(/^target\.(onhand|bank_balances)\./, "") },
  { key: "key", header: "Key", render: (row) => formatKey(row.key) },
  { key: "source", header: "Sent by the factory", align: "end", mono: true, render: (row) => money(row.source) },
  { key: "target", header: `Held by ${target}`, align: "end", mono: true, render: (row) => money(row.target) },
  { key: "delta", header: "Delta", align: "end", mono: true, render: (row) => money(row.delta) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

const rejectColumns: DataTableColumn<OpeningLoadDetail["rejects"][number]>[] = [
  { key: "legacyId", header: "Record", mono: true },
  { key: "code", header: "Code", mono: true },
  { key: "detail", header: "Reason", render: (row) => row.detail || dash },
];

/**
 * The opening load of an onhand run into the inventory ledger or of a bank_balances run into treasury (H-S9-1, H-S10-1): the
 * legal entity and cutover date, the legacy codes that are not already Passage ids mapped to a product or bank account by name,
 * and the result with the target's totals read back.
 */
export function OpeningLoadPanel({ runId, pack }: { runId: string; pack: "onhand" | "bank_balances" }) {
  const [version, setVersion] = useState(0);
  const state = useLoad((signal) => getOpeningLoad(runId, signal), `${runId}|${version}`);
  const [form, setForm] = useState<OpeningForm>({ legalEntityId: "", postingDate: "", rows: [{ legacy: "", id: "" }] });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const onhand = pack === "onhand";
  const target = onhand ? "inventory" : "treasury";
  const load = state.data ?? null;
  const legalEntityId = form.legalEntityId || load?.legalEntityId || "";

  async function submit(event: FormEvent) {
    event.preventDefault();
    const f = { ...form, legalEntityId };
    const found = validateOpeningForm(f);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await runOpeningLoad(runId, toOpeningRequest(f, pack));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
      setVersion((v) => v + 1);
    }
  }

  const setRow = (i: number, patch: Partial<MapRow>) => setForm({ ...form, rows: form.rows.map((r, j) => (j === i ? { ...r, ...patch } : r)) });
  const status = load ? (STATUS[load.status] ?? STATUS.STAGED) : null;

  return (
    <Card title={onhand ? "Load into the inventory ledger" : "Load into treasury"}>
      <div className={styles.stack}>
        <p className={styles.note}>
          {onhand
            ? "Sends the staged stock as opening batches, one per currency and 1,000 lines, and reads the batches back to reconcile quantity and value. Repeat the load to resume after an outage; stock already sent is not sent twice."
            : "Sends the staged balances as opening bank balances, 1,000 accounts a batch, and reads them back to reconcile the count and the balance per currency. The cash position and the bank reconciliation start from them."}
        </p>
        {load && status ? (
          <>
            <p>
              <Badge tone={status.tone}>{status.label}</Badge> in <EntityName entity="legalEntity" id={load.legalEntityId} />. Last pass {formatDateTime(load.finishedAt ?? load.startedAt)}; {load.attempts} pass(es).
              {load.lastError ? ` ${load.lastError}` : ""}
            </p>
            <StatGroup label="Opening load results">
              <StatTile label="Staged" value={load.staged} />
              <StatTile label="Loaded" value={load.loaded} />
              <StatTile label="Pending" value={load.pending} tone={load.pending > 0 ? "warning" : "neutral"} />
              <StatTile label="Rejected" value={load.rejected} tone={load.rejected > 0 ? "danger" : "neutral"} />
            </StatGroup>
          </>
        ) : null}
        {state.error ? (
          <Alert tone="danger" title="Could not read the opening load" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            <ProblemMessage problem={state.error} />
          </Alert>
        ) : null}
        {load?.status !== "RECONCILED" || load.pending > 0 ? (
          <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label={`Load into ${target}`}>
            <LookupFormField id={`ol-${runId}-le`} label="Legal entity" entity="legalEntity" value={legalEntityId} error={errors.legalEntityId} onChange={(id) => setForm({ ...form, legalEntityId: id })} />
            <FormField id={`ol-${runId}-date`} label={onhand ? "Posting date" : "Balances as of"} hint="The cutover date." error={errors.postingDate}>
              {(c) => <Input {...c} type="date" value={form.postingDate} onChange={(e) => setForm({ ...form, postingDate: e.target.value })} invalid={Boolean(errors.postingDate)} />}
            </FormField>
            <fieldset className={styles.wide}>
              <legend className={styles.note}>
                {onhand ? "Legacy items and the Passage product each one is" : "Legacy bank accounts and the treasury account each one is"}. Leave empty when the extract already holds Passage ids.
              </legend>
              {form.rows.map((r, i) => (
                <div key={i} className={styles.form} role="group" aria-label={`Mapping ${i + 1}`}>
                  <FormField id={`ol-${runId}-legacy-${i}`} label={onhand ? "Legacy item" : "Legacy bank account"} optional error={errors[`rows.${i}.legacy`]}>
                    {(c) => <Input {...c} className={styles.mono} value={r.legacy} onChange={(e) => setRow(i, { legacy: e.target.value })} invalid={Boolean(errors[`rows.${i}.legacy`])} />}
                  </FormField>
                  <FormField id={`ol-${runId}-id-${i}`} label={onhand ? "Product" : "Bank account"} optional error={errors[`rows.${i}.id`]}>
                    {(c) => (
                      <EntityLookup
                        {...c}
                        entity={onhand ? "product" : "bankAccount"}
                        scope={legalEntityId ? { legalEntityId } : undefined}
                        value={r.id}
                        invalid={Boolean(errors[`rows.${i}.id`])}
                        onChange={(e) => setRow(i, { id: e.target.value })}
                      />
                    )}
                  </FormField>
                </div>
              ))}
              <Button size="sm" onClick={() => setForm({ ...form, rows: [...form.rows, { legacy: "", id: "" }] })}>
                Map another
              </Button>
            </fieldset>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="The load did not run" live="alert">
                  <ProblemMessage problem={failure} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                {load ? "Load again" : `Load into ${target}`}
              </Button>
            </div>
          </form>
        ) : null}
        {load ? (
          <>
            <DataTable
              label={`Reconciliation with ${target}`}
              columns={reconciliationColumns(target)}
              rows={load.reconciliation ?? []}
              getRowKey={(row) => `${row.name}|${formatKey(row.key)}`}
              empty={{ title: "Not reconciled yet", description: `The comparison with ${target} appears once every line has been sent.` }}
            />
            <DataTable
              label="Rejected lines"
              columns={rejectColumns}
              rows={load.rejects ?? []}
              getRowKey={(row) => `${row.legacyId}|${row.code}`}
              empty={{ title: "No rejects", description: `Every line sent so far was accepted by ${target}.` }}
            />
          </>
        ) : null}
      </div>
    </Card>
  );
}
