import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, StatusPill, type DataTableColumn } from "../../shell/ui";
import { createRun, describeProblem, getRun, listLedger, listRuns, listSpecs, listTemplates, newKey, rerunRun, type LedgerRow, type MappingSpec, type MigrationRun, type Problem, type RowIssue } from "./api/client";
import { formatDateTime, MODE_LABELS, MODES, RUN_STATUSES, SOURCE_LABELS, SOURCE_SYSTEMS } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const DUTY_RUN = "migration.run";

const columns: DataTableColumn<MigrationRun>[] = [
  { key: "createdAt", header: "Started", render: (row) => formatDateTime(row.createdAt) },
  { key: "entity", header: "Entity", mono: true },
  { key: "sourceSystem", header: "Source", render: (row) => SOURCE_LABELS[row.sourceSystem] ?? row.sourceSystem },
  { key: "mode", header: "Mode", render: (row) => `${MODE_LABELS[row.mode] ?? row.mode}${row.dryRun ? " (dry run)" : ""}` },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "extracted", header: "Extracted", align: "end", render: (row) => row.counts.extracted },
  { key: "loaded", header: "Loaded", align: "end", render: (row) => row.counts.created + row.counts.updated },
  { key: "rejected", header: "Rejected", align: "end", render: (row) => row.counts.rejected },
];

/** Migration runs, newest first, with the exception count that needs attention. */
export function RunsPage() {
  const navigate = useNavigate();
  const canRun = holdsDuty(usePrincipalDuties(), DUTY_RUN);
  const [status, setStatus] = useState("");
  const [entity, setEntity] = useState("");
  const state = useLoad((signal) => listRuns({ status, entity: entity.trim() }, signal), `${status}|${entity.trim()}`);
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Migration runs"
        description="Each run extracts one entity from a legacy system, maps and validates it, loads it through the target APIs and reconciles the result."
        actions={
          <>
            <Link className={buttonClassName("ghost")} to="/migration/specs">
              Mapping specs
            </Link>
            {canRun ? (
              <Link className={buttonClassName("primary")} to="/migration/runs/new">
                Start run
              </Link>
            ) : null}
          </>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="runs-entity" label="Entity">
          {(control) => <Input {...control} value={entity} onChange={(event) => setEntity(event.target.value)} placeholder="customer" />}
        </FormField>
        <FormField id="runs-status" label="Status">
          {(control) => (
            <Select {...control} value={status} onChange={(event) => setStatus(event.target.value)}>
              <option value="">All statuses</option>
              {RUN_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load migration runs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Migration runs"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No migration runs yet", description: "Publish a mapping spec, then start a run to see counts and rejects." }}
          onRowOpen={(row) => navigate(`/migration/runs/${row.id}`)}
        />
      )}
    </div>
  );
}

/** Start a run from a published mapping spec. */
export function NewRunPage() {
  const navigate = useNavigate();
  const [search] = useSearchParams();
  const specs = useLoad((signal) => listSpecs(undefined, signal), "specs");
  const [sourceSystem, setSourceSystem] = useState<MigrationRun["sourceSystem"]>("ax2012");
  const [mode, setMode] = useState<MigrationRun["mode"]>("rehearsal");
  const [dryRun, setDryRun] = useState(true);
  const [specId, setSpecId] = useState(search.get("spec") ?? "");
  const [templateId, setTemplateId] = useState("");
  const templates = useLoad((signal) => listTemplates(undefined, signal), "templates");
  const [error, setError] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  const options: MappingSpec[] = specs.data ?? [];
  const chosenEntity = options.find((s) => s.id === specId)?.entity;
  // Templates are optional, so a failed template load never blocks starting a run.
  const templateOptions = (Array.isArray(templates.data) ? templates.data : []).filter((t) => !chosenEntity || t.entity === chosenEntity);
  // A template chosen before the spec changed to another entity no longer applies, so it is dropped, not sent.
  const chosenTemplate = templateOptions.some((t) => t.id === templateId) ? templateId : "";

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    if (specId === "") {
      setError("Choose the mapping spec to run.");
      return;
    }
    setError("");
    setBusy(true);
    try {
      const run = await createRun({ sourceSystem, mode, dryRun, mappingSpecId: specId, ...(chosenTemplate ? { dataTemplateId: chosenTemplate } : {}) }, key.current);
      navigate(`/migration/runs/${run.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Start migration run" description="A dry run maps and validates without loading. Loaded rows are keyed by their legacy id, so a repeat load changes nothing." />
      {specs.error ? (
        <Alert tone="danger" title="Could not load mapping specs" live="alert" action={<Button size="sm" onClick={specs.reload}>Try again</Button>}>
          {specs.error.message} ({specs.error.code})
        </Alert>
      ) : (
        <Card>
          <form className={styles.form} onSubmit={submit} noValidate>
            <FormField id="run-spec" label="Mapping spec" error={error} hint={!specs.loading && options.length === 0 ? "No specs are published yet." : undefined}>
              {(control) => (
                <Select {...control} value={specId} invalid={error !== ""} disabled={specs.loading} onChange={(event) => setSpecId(event.target.value)}>
                  <option value="">{specs.loading ? "Loading..." : "Choose a spec"}</option>
                  {options.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.entity} v{s.version}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField
              id="run-template"
              label="Data template (optional)"
              hint={templates.error ? "Data templates could not be loaded; the run starts without one." : "Fills fields the source leaves empty before mapping."}
            >
              {(control) => (
                <Select {...control} value={chosenTemplate} disabled={templates.loading || Boolean(templates.error)} onChange={(event) => setTemplateId(event.target.value)}>
                  <option value="">No template</option>
                  {templateOptions.map((t) => (
                    <option key={t.id} value={t.id}>
                      {t.name} v{t.version} ({t.entity})
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="run-source" label="Source system">
              {(control) => (
                <Select {...control} value={sourceSystem} onChange={(event) => setSourceSystem(event.target.value as MigrationRun["sourceSystem"])}>
                  {SOURCE_SYSTEMS.map((s) => (
                    <option key={s} value={s}>
                      {SOURCE_LABELS[s]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="run-mode" label="Mode">
              {(control) => (
                <Select {...control} value={mode} onChange={(event) => setMode(event.target.value as MigrationRun["mode"])}>
                  {MODES.map((m) => (
                    <option key={m} value={m}>
                      {MODE_LABELS[m]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <div className={styles.check}>
              <input id="run-dry" type="checkbox" checked={dryRun} onChange={(event) => setDryRun(event.target.checked)} />
              <label htmlFor="run-dry">Dry run (do not load)</label>
            </div>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not run the migration" live="alert">
                  {failure.message} ({failure.code})
                  {failure.errors ? (
                    <ul>
                      {Object.entries(failure.errors).map(([name, reason]) => (
                        <li key={name}>
                          {name}: {reason}
                        </li>
                      ))}
                    </ul>
                  ) : null}
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Start run
              </Button>
              <Link className={buttonClassName("ghost")} to="/migration">
                Cancel
              </Link>
            </div>
          </form>
        </Card>
      )}
    </div>
  );
}

function issueText(issues: RowIssue[]): string {
  return issues.map((i) => `${i.field}: ${i.code}`).join("; ");
}

const rejectColumns: DataTableColumn<MigrationRun["rejects"][number]>[] = [
  { key: "sourceKey", header: "Source key", mono: true },
  { key: "reasons", header: "Reasons", render: (row) => issueText(row.rejects) },
];
const lineageColumns: DataTableColumn<LedgerRow>[] = [
  { key: "sourceKey", header: "Source key", mono: true },
  { key: "action", header: "Action" },
  { key: "fields", header: "Defaulted from template", mono: true, render: (row) => row.defaultFromTemplate.join(", ") },
];

/** The rows of a templated run that took a template default (default_from_template lineage). */
function TemplateLineage({ runId, templateId }: { runId: string; templateId: string }) {
  const state = useLoad((signal) => listLedger(runId, true, signal), runId);
  return (
    <Card title="Data template defaults">
      <p>
        Fields missing from the source were filled from <Link to={`/migration/templates/${templateId}`}>the data template</Link>.
      </p>
      {state.error ? (
        <Alert tone="danger" title="Could not load the lineage" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Rows that took template defaults"
          columns={lineageColumns}
          rows={state.data ?? []}
          getRowKey={(r) => r.sourceKey}
          loading={state.loading}
          empty={{ title: "No rows used a template default", description: "Every row had its own source values." }}
        />
      )}
    </Card>
  );
}

const warningColumns: DataTableColumn<MigrationRun["warnings"][number]>[] = [
  { key: "sourceKey", header: "Source key", mono: true },
  { key: "reasons", header: "Warnings", render: (row) => issueText(row.warnings) },
];
const checkColumns: DataTableColumn<NonNullable<MigrationRun["reconciliation"]>["checks"][number]>[] = [
  { key: "name", header: "Check", mono: true },
  { key: "ok", header: "Result", render: (row) => <Badge tone={row.ok ? "success" : "danger"}>{row.ok ? "Matches" : "Differs"}</Badge> },
  { key: "detail", header: "Detail" },
];

/** One run: counts, the exception queue, warnings and reconciliation, with a re-run of the rejects. */
export function RunDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const canRun = holdsDuty(usePrincipalDuties(), DUTY_RUN);
  const state = useLoad((signal) => getRun(id, signal), id);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const key = useRef(newKey());

  async function rerun() {
    setBusy(true);
    setFailure(null);
    try {
      const next = await rerunRun(id, key.current);
      key.current = newKey();
      navigate(`/migration/runs/${next.id}`);
    } catch (e) {
      setFailure(describeProblem(e));
      key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Migration run" />
        <Alert tone="danger" title="Could not load this run" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link className={buttonClassName("ghost")} to="/migration">
          Back to runs
        </Link>
      </div>
    );
  }
  // After a re-run the route id changes while the previous run is still in state; never show it under the new id.
  const run = state.data?.id === id ? state.data : null;
  if (!run) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Migration run" description="Loading..." />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${run.entity} migration`}
        description={`${SOURCE_LABELS[run.sourceSystem] ?? run.sourceSystem}, ${MODE_LABELS[run.mode] ?? run.mode}${run.dryRun ? ", dry run" : ""}. Started ${formatDateTime(run.createdAt)}.`}
        actions={
          <>
            <StatusPill status={run.status} />
            {canRun && run.counts.rejected > 0 ? (
              <Button variant="primary" loading={busy} onClick={rerun}>
                Re-run rejected rows
              </Button>
            ) : null}
          </>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not re-run" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <StatGroup label="Counts">
        <StatTile label="Extracted" value={run.counts.extracted} />
        <StatTile label="Created" value={run.counts.created} />
        <StatTile label="Updated" value={run.counts.updated} />
        <StatTile label="Unchanged" value={run.counts.unchanged} />
        {run.dataTemplateId ? <StatTile label="Used template defaults" value={run.counts.templated ?? 0} /> : null}
        <StatTile label="Rejected" value={run.counts.rejected} tone={run.counts.rejected > 0 ? "warning" : "neutral"} />
      </StatGroup>
      {run.parentRunId ? (
        <p>
          Re-run of <Link to={`/migration/runs/${run.parentRunId}`}>the original run</Link>.
        </p>
      ) : null}
      {run.mappingSpecId ? (
        <p>
          Mapping <Link to={`/migration/specs/${run.mappingSpecId}`}>{run.entity} v{run.mappingVersion}</Link>.
        </p>
      ) : null}
      {run.dataTemplateId ? <TemplateLineage runId={run.id} templateId={run.dataTemplateId} /> : null}
      <Card title="Exception queue">
        <DataTable label="Rejected rows" columns={rejectColumns} rows={run.rejects} getRowKey={(r) => r.sourceKey} empty={{ title: "No rejected rows", description: "Every extracted row passed its mapping rules." }} />
      </Card>
      {run.warnings.length > 0 ? (
        <Card title="Warnings">
          <DataTable label="Rows with warnings" columns={warningColumns} rows={run.warnings} getRowKey={(r) => r.sourceKey} />
        </Card>
      ) : null}
      <Card title="Reconciliation">
        {run.reconciliation ? (
          <DataTable label="Reconciliation checks" columns={checkColumns} rows={run.reconciliation.checks} getRowKey={(r) => r.name} />
        ) : (
          <p>No reconciliation was recorded for this run.</p>
        )}
      </Card>
      {run.absent.length > 0 ? <Alert tone="info" title={`${run.absent.length} loaded rows are missing from this extract`}>Rows are never deleted by a migration run.</Alert> : null}
    </div>
  );
}
