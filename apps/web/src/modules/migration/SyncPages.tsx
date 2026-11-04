import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, StatusPill, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getRun,
  getSync,
  listAbsent,
  listConflicts,
  listRunsPage,
  listSync,
  resolveConflict,
  updateSync,
  type ConflictResolution,
  type MigrationRun,
  type Problem,
  type SourceOfTruth,
  type SyncAbsent,
  type SyncConflict,
  type SyncStatus,
} from "./api/client";
import { formatDateTime } from "./domain";
import { LoadMore } from "./LoadMore";
import { DUTY_PROJECT_WRITE } from "./ProjectPages";
import { DUTY_RUN } from "./RunPages";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { usePaged } from "./usePaged";

const FILTERS = { all: "All schedules", failing: "Failing", paused: "Paused" } as const;
type FilterKey = keyof typeof FILTERS;

export const SOURCE_OF_TRUTH_LABELS: Record<SourceOfTruth, string> = { legacy: "Legacy system", passage: "Passage" };

/** One word for a binding's state: running, paused, failing (with its backoff), idle without a schedule, or ok. */
export function syncHealth(s: SyncStatus): { label: string; tone: BadgeTone } {
  if (s.running) return { label: "Running", tone: "info" };
  if (s.paused) return { label: "Paused", tone: "neutral" };
  if (s.bindingStatus === "disabled") return { label: "Binding disabled", tone: "neutral" };
  if (!s.scheduleCron) return { label: "Not scheduled", tone: "neutral" };
  if (s.consecutiveFailures >= 3) return { label: "Backing off", tone: "danger" };
  if (s.consecutiveFailures > 0) return { label: "Failing", tone: "warning" };
  if (s.openConflicts > 0) return { label: "Conflicts", tone: "warning" };
  return { label: "Healthy", tone: "success" };
}

const columns: DataTableColumn<SyncStatus>[] = [
  {
    key: "entity",
    header: "Entity",
    render: (row) => <Link to={`/migration/sync/${row.bindingId}`}>{row.entity}</Link>,
  },
  { key: "connectionProfileName", header: "Connection" },
  { key: "health", header: "State", render: (row) => <Badge tone={syncHealth(row).tone}>{syncHealth(row).label}</Badge> },
  { key: "sourceOfTruth", header: "Source of truth", render: (row) => SOURCE_OF_TRUTH_LABELS[row.sourceOfTruth] },
  { key: "scheduleCron", header: "Schedule", mono: true, render: (row) => row.scheduleCron || "" },
  { key: "lastSuccessAt", header: "Last success", render: (row) => (row.lastSuccessAt ? formatDateTime(row.lastSuccessAt) : "Never") },
  { key: "nextDueAt", header: "Next due", render: (row) => (row.paused || !row.nextDueAt ? "" : formatDateTime(row.nextDueAt)) },
  { key: "consecutiveFailures", header: "Failures", align: "end" },
  { key: "rowsLoaded", header: "Loaded", align: "end" },
  { key: "rowsRejected", header: "Rejected", align: "end" },
  { key: "openConflicts", header: "Conflicts", align: "end" },
  { key: "absentCount", header: "Missing in legacy", align: "end" },
];

/** Run monitor: every scheduled binding with its last run, watermark, next due time and failures. */
export function SyncPage() {
  const navigate = useNavigate();
  const [filter, setFilter] = useState<FilterKey>("all");
  const params = { paused: filter === "paused" ? "true" : undefined, failing: filter === "failing" ? "true" : undefined };
  const key = filter;
  const state = useLoad((signal) => listSync(params, signal), key);
  const paged = usePaged(state, key, (cursor) => listSync({ ...params, cursor }));
  const all = useLoad((signal) => listSync({ limit: "200" }, signal), "totals");
  const totals = all.data?.items ?? [];
  const failing = totals.filter((s) => s.consecutiveFailures > 0).length;
  const paused = totals.filter((s) => s.paused).length;
  const conflicts = totals.reduce((n, s) => n + s.openConflicts, 0);
  const missing = totals.reduce((n, s) => n + s.absentCount, 0);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Delta sync"
        description="Scheduled delta runs from your legacy ERP into Passage. A schedule comes from the cron of a migration project on the binding's connection; each run reads only rows changed after the binding's watermark."
        actions={
          <Link className={buttonClassName("ghost")} to="/migration/projects">
            Projects and schedules
          </Link>
        }
      />
      {totals.length > 0 ? (
        <StatGroup label="Delta sync totals">
          <StatTile label="Scheduled bindings" value={totals.length} />
          <StatTile label="Failing" value={failing} tone="danger" />
          <StatTile label="Paused" value={paused} tone="warning" />
          <StatTile label="Open conflicts" value={conflicts} tone="warning" />
          <StatTile label="Missing in legacy" value={missing} tone="warning" />
        </StatGroup>
      ) : null}
      <div className={styles.toolbar}>
        <FormField id="sync-filter" label="Show">
          {(control) => (
            <Select {...control} value={filter} onChange={(event) => setFilter(event.target.value as FilterKey)}>
              {(Object.keys(FILTERS) as FilterKey[]).map((k) => (
                <option key={k} value={k}>
                  {FILTERS[k]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load the sync status" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Delta sync status"
          columns={columns}
          rows={paged.rows}
          getRowKey={(row) => row.bindingId}
          loading={state.loading}
          empty={{
            title: filter === "all" ? "No scheduled bindings" : "Nothing matches this filter",
            description: filter === "all" ? "Give a migration project a schedule on a connection that has entity bindings, and its bindings appear here." : "Choose another filter to see the other bindings.",
          }}
          onRowOpen={(row) => navigate(`/migration/sync/${row.bindingId}`)}
        />
      )}
      <LoadMore paged={paged} noun="bindings" />
    </div>
  );
}

function Entry({ term, children }: { term: string; children: React.ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

/** One binding's sync: status, pause and resume, run now, source of truth, recent runs, rejects of the last run and conflicts. */
export function SyncDetailPage() {
  const { bindingId = "" } = useParams();
  const duties = usePrincipalDuties();
  const canEdit = holdsDuty(duties, DUTY_PROJECT_WRITE);
  const state = useLoad((signal) => getSync(bindingId, signal), bindingId);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Delta sync" />
        <Alert tone="danger" title="Could not load this binding's sync" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link className={buttonClassName("ghost")} to="/migration/sync">
          Back to delta sync
        </Link>
      </div>
    );
  }
  const sync = state.data?.bindingId === bindingId ? state.data : null;
  if (!sync) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Delta sync" description="Loading..." />
      </div>
    );
  }

  async function edit(current: SyncStatus, change: { paused?: boolean; scheduleCron?: string; sourceOfTruth?: SourceOfTruth; keyCheckHours?: number; keyCheckNow?: boolean; keyCheckAcceptLarge?: boolean; runNow?: boolean }) {
    setBusy(true);
    setFailure(null);
    try {
      state.set(await updateSync(current.bindingId, current.version, change));
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  const health = syncHealth(sync);
  const scheduled = Boolean(sync.scheduleCron);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${sync.entity} sync`}
        description={`From ${sync.connectionProfileName}. ${scheduled ? `Runs on the schedule ${sync.scheduleCron} (${sync.scheduleTimezone || "UTC"}).` : "Not scheduled: the binding has no schedule of its own and no active migration project on this connection has one."}`}
        actions={
          <>
            <Badge tone={health.tone}>{health.label}</Badge>
            {canEdit && scheduled ? (
              <>
                <Button loading={busy} onClick={() => edit(sync, { paused: !sync.paused })}>
                  {sync.paused ? "Resume schedule" : "Pause schedule"}
                </Button>
                <Button loading={busy} disabled={sync.paused || sync.running} onClick={() => edit(sync, { runNow: true })}>
                  Run now
                </Button>
              </>
            ) : null}
          </>
        }
      />
      {failure ? (
        <Alert tone="danger" title="Could not change the sync" live="alert" action={<Button size="sm" onClick={state.reload}>Reload</Button>}>
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {sync.consecutiveFailures > 0 ? (
        <Alert tone={sync.consecutiveFailures >= 3 ? "danger" : "warning"} title={`${sync.consecutiveFailures} failed run${sync.consecutiveFailures === 1 ? "" : "s"} in a row`}>
          {sync.lastError ? `${sync.lastError}. ` : ""}
          The watermark did not move, so the next run reads the same rows again.
          {sync.consecutiveFailures >= 3 && sync.nextDueAt ? ` After three failures the schedule backs off (15 minutes, doubling, at most 24 hours); the next attempt is ${formatDateTime(sync.nextDueAt)}.` : ""}
        </Alert>
      ) : null}
      <Card title="Status">
        <dl className={styles.details}>
          <Entry term="Last run">{sync.lastRunAt ? formatDateTime(sync.lastRunAt) : "Never"}</Entry>
          <Entry term="Last run result">{sync.lastStatus ? <StatusPill status={sync.lastStatus} /> : "None yet"}</Entry>
          <Entry term="Last success">{sync.lastSuccessAt ? formatDateTime(sync.lastSuccessAt) : "Never"}</Entry>
          <Entry term="Next due">{sync.paused ? "Paused" : sync.nextDueAt ? formatDateTime(sync.nextDueAt) : "Not scheduled"}</Entry>
          <Entry term="Watermark">{sync.watermark ? <span className={styles.mono}>{sync.watermark}</span> : "None: the next run reads everything"}</Entry>
          <Entry term="Consecutive failures">{sync.consecutiveFailures}</Entry>
          <Entry term="Rows loaded">{sync.rowsLoaded}</Entry>
          <Entry term="Rows rejected">{sync.rowsRejected}</Entry>
          <Entry term="Open conflicts">{sync.openConflicts}</Entry>
        </dl>
      </Card>
      <ScheduleCard sync={sync} canEdit={canEdit} busy={busy} onChange={(cron) => edit(sync, { scheduleCron: cron })} />
      <SourceOfTruthCard sync={sync} canEdit={canEdit} busy={busy} onChange={(value) => edit(sync, { sourceOfTruth: value })} />
      <DeletedRowsCard sync={sync} canEdit={canEdit && scheduled} busy={busy} onChange={(change) => edit(sync, change)} />
      <RecentRunsCard sync={sync} />
      {sync.lastRunId ? <LastRunRejectsCard runId={sync.lastRunId} /> : null}
      <ConflictsCard sync={sync} canResolve={holdsDuty(duties, DUTY_RUN)} onResolved={state.reload} />
      <Link className={buttonClassName("ghost")} to="/migration/sync">
        Back to delta sync
      </Link>
    </div>
  );
}

/** Reports why a cron cannot schedule runs, or "" when it can (the service also checks the 15-minute spacing). */
export function cronProblem(value: string): string {
  const cron = value.trim();
  if (cron.split(/\s+/).length !== 5) return "Enter five fields: minute hour day month weekday.";
  if (cron.length > 100) return "Use at most 100 characters.";
  return "";
}

/**
 * Each binding can run on its own clock; without one it follows the schedule of the migration project on its
 * connection. Saving a schedule does not reset the watermark.
 */
function ScheduleCard({ sync, canEdit, busy, onChange }: { sync: SyncStatus; canEdit: boolean; busy: boolean; onChange(cron: string): void }) {
  const own = sync.scheduleOverride ?? "";
  const fallback = sync.projectScheduleCron ?? "";
  const zone = sync.scheduleTimezone || "UTC";
  const [draft, setDraft] = useState<string | null>(null);
  const [confirmClear, setConfirmClear] = useState(false);
  const clear = () => {
    setConfirmClear(false);
    onChange("");
    setDraft(null);
  };
  const value = draft ?? own;
  const error = draft !== null && draft.trim() !== "" ? cronProblem(draft) : "";
  const save = draft !== null && draft.trim() !== "" && !error && draft.trim() !== own;
  return (
    <Card title="Schedule">
      <div className={styles.stack}>
        <p className={styles.note}>
          {own
            ? `This binding runs on its own schedule, ${own} (${zone}); ${fallback ? `the project's is ${fallback}.` : "its migration project has none."}`
            : sync.scheduleCron
              ? `This binding follows the schedule of its migration project, ${sync.scheduleCron} (${zone}).`
              : "This binding has no schedule: its migration project has none either."}{" "}
          Give it its own five-field cron (for example 0 3 * * * for every night at 03:00 in that zone, at least 15 minutes apart) when this table needs a different rhythm than the project. Changing it does not reset the watermark.
        </p>
        <FormField id="sync-cron" label={`Own schedule (cron, ${zone})`} error={error || undefined} hint="Leave empty to follow the project.">
          {(control) => <Input {...control} value={value} invalid={Boolean(error)} disabled={!canEdit || busy} onChange={(event) => setDraft(event.target.value)} />}
        </FormField>
        {canEdit ? (
          <div>
            <Button loading={busy} disabled={!save} onClick={() => { onChange(draft!.trim()); setDraft(null); }}>
              Save schedule
            </Button>{" "}
            {own ? (
              <Button loading={busy} onClick={() => (fallback ? clear() : setConfirmClear(true))}>
                Use the project schedule
              </Button>
            ) : null}
          </div>
        ) : null}
      </div>
      {confirmClear ? (
        <ConfirmDialog
          title="Stop scheduling this binding?"
          description="No active migration project on this connection has a schedule, so without its own cron the binding stops running until a project gets one. Its watermark is kept. To stop it for a while, pause the schedule instead."
          confirmLabel="Remove own schedule"
          cancelLabel="Cancel"
          onConfirm={clear}
          onCancel={() => setConfirmClear(false)}
        />
      ) : null}
    </Card>
  );
}

function SourceOfTruthCard({ sync, canEdit, busy, onChange }: { sync: SyncStatus; canEdit: boolean; busy: boolean; onChange(value: SourceOfTruth): void }) {
  return (
    <Card title="Source of truth">
      <div className={styles.stack}>
        <p className={styles.note}>
          Master data flows from the legacy system into Passage. With the legacy system as the source of truth, a changed legacy row updates the Passage record. With Passage as the source of truth, a legacy row that differs from the loaded record is held as a conflict and never written over it; new legacy rows still load.
        </p>
        <FormField id="sync-source" label="Source of truth">
          {(control) => (
            <Select {...control} value={sync.sourceOfTruth} disabled={!canEdit || busy} onChange={(event) => onChange(event.target.value as SourceOfTruth)}>
              {(Object.keys(SOURCE_OF_TRUTH_LABELS) as SourceOfTruth[]).map((k) => (
                <option key={k} value={k}>
                  {SOURCE_OF_TRUTH_LABELS[k]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
    </Card>
  );
}

const KEY_CHECK_PERIODS: { hours: number; label: string }[] = [
  { hours: 0, label: "Off" },
  { hours: 6, label: "Every 6 hours" },
  { hours: 24, label: "Every day" },
  { hours: 168, label: "Every week" },
];

const absentColumns: DataTableColumn<SyncAbsent>[] = [
  { key: "sourceKey", header: "Legacy key", mono: true },
  { key: "targetId", header: "Passage record", mono: true, render: (row) => row.targetId ?? "" },
  { key: "firstAbsentAt", header: "First missing", render: (row) => formatDateTime(row.firstAbsentAt) },
  { key: "lastCheckedAt", header: "Last checked", render: (row) => formatDateTime(row.lastCheckedAt) },
];

/**
 * Rows the legacy system deleted. A delta cannot see them, so the binding is read in full on a period and the keys
 * loaded earlier that the read did not find are listed. Nothing is deactivated in Passage.
 */
function DeletedRowsCard({ sync, canEdit, busy, onChange }: { sync: SyncStatus; canEdit: boolean; busy: boolean; onChange(change: { keyCheckHours?: number; keyCheckNow?: boolean; keyCheckAcceptLarge?: boolean }): void }) {
  const key = `${sync.bindingId}/${sync.lastKeyCheckAt ?? ""}/${sync.absentCount}`;
  const state = useLoad((signal) => listAbsent(sync.bindingId, {}, signal), key);
  const paged = usePaged(state, key, (cursor) => listAbsent(sync.bindingId, { cursor }));
  const periods = KEY_CHECK_PERIODS.some((p) => p.hours === sync.keyCheckHours) ? KEY_CHECK_PERIODS : [...KEY_CHECK_PERIODS, { hours: sync.keyCheckHours, label: `Every ${sync.keyCheckHours} hours` }];
  return (
    <Card
      title="Deleted in the legacy system"
      actions={
        canEdit && sync.keyCheckHours > 0 ? (
          <Button size="sm" disabled={busy} onClick={() => onChange({ keyCheckNow: true })}>
            Check at the next run
          </Button>
        ) : null
      }
    >
      <div className={styles.stack}>
        <p className={styles.note}>
          A delta run cannot see a row the legacy system deleted, so a binding with a change cursor is read in full on a period and every key loaded earlier that the read did not find is listed here. Nothing is deactivated in Passage.{" "}
          {sync.lastKeyCheckAt ? `Last full read: ${formatDateTime(sync.lastKeyCheckAt)}.` : "No full read has completed yet."}
        </p>
        {sync.keyCheckError ? (
          <Alert
            tone="warning"
            title="The last full read did not update this list"
            action={
              // The service words a refused large vanish with "newly absent (more than half)"; only that case can be accepted.
              canEdit && !sync.keyCheckAcceptLarge && /newly absent \(more than half\)/.test(sync.keyCheckError) ? (
                <Button size="sm" disabled={busy} onClick={() => onChange({ keyCheckAcceptLarge: true })}>
                  Accept the next full read once
                </Button>
              ) : undefined
            }
          >
            {sync.keyCheckError}
            {sync.keyCheckAcceptLarge ? " The next full read will be accepted whatever it lists." : ""}
          </Alert>
        ) : null}
        <FormField id="sync-key-check" label="Full read to find deleted rows">
          {(control) => (
            <Select {...control} value={sync.keyCheckHours} disabled={!canEdit || busy} onChange={(event) => onChange({ keyCheckHours: Number(event.target.value) })}>
              {periods.map((p) => (
                <option key={p.hours} value={p.hours}>
                  {p.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {state.error ? (
          <Alert tone="danger" title="Could not load the missing keys" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Keys missing in the legacy system"
            columns={absentColumns}
            rows={paged.rows}
            getRowKey={(row) => row.sourceKey}
            loading={state.loading}
            empty={{ title: "No missing keys", description: "Every key loaded earlier was found in the legacy source at the last full read." }}
          />
        )}
        <LoadMore paged={paged} noun="keys" />
      </div>
    </Card>
  );
}

const runColumns: DataTableColumn<MigrationRun>[] = [
  { key: "createdAt", header: "Started", render: (row) => <Link to={`/migration/runs/${row.id}`}>{formatDateTime(row.createdAt)}</Link> },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
  { key: "extracted", header: "Read", align: "end", render: (row) => row.counts.extracted },
  { key: "loaded", header: "Loaded", align: "end", render: (row) => row.counts.created + row.counts.updated },
  { key: "rejected", header: "Rejected", align: "end", render: (row) => row.counts.rejected },
  { key: "conflicted", header: "Conflicts", align: "end", render: (row) => row.counts.conflicted ?? 0 },
  { key: "watermark", header: "Watermark", mono: true, render: (row) => row.watermark ?? "" },
];

function RecentRunsCard({ sync }: { sync: SyncStatus }) {
  const navigate = useNavigate();
  const state = useLoad((signal) => listRunsPage({ bindingId: sync.bindingId, limit: "10" }, signal), `${sync.bindingId}/${sync.lastRunId ?? ""}`);
  const paged = usePaged(state, sync.bindingId, (cursor) => listRunsPage({ bindingId: sync.bindingId, cursor, limit: "10" }));
  return (
    <Card title="Recent runs">
      {state.error ? (
        <Alert tone="danger" title="Could not load the recent runs" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Recent sync runs"
          columns={runColumns}
          rows={paged.rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No runs yet", description: "The first run happens as soon as the scheduler sees this binding, and reads everything." }}
          onRowOpen={(row) => navigate(`/migration/runs/${row.id}`)}
        />
      )}
      <LoadMore paged={paged} noun="runs" />
    </Card>
  );
}

const MAX_REJECTS = 20;

function LastRunRejectsCard({ runId }: { runId: string }) {
  const state = useLoad((signal) => getRun(runId, signal), runId);
  const rows = (state.data?.rejects ?? []).flatMap((r) => r.rejects.map((issue) => ({ sourceKey: r.sourceKey, ...issue })));
  return (
    <Card title="Rejects of the last run">
      {state.error ? (
        <Alert tone="danger" title="Could not load the last run" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <div className={styles.stack}>
          <DataTable
            label="Rejected rows of the last run"
            columns={[
              { key: "sourceKey", header: "Legacy key", mono: true },
              { key: "code", header: "Code", mono: true },
              { key: "detail", header: "Reason" },
            ]}
            rows={rows.slice(0, MAX_REJECTS)}
            getRowKey={(row, i) => `${row.sourceKey}/${row.code}/${i}`}
            loading={state.loading}
            empty={{ title: "No rejected rows", description: "Every row of the last run was loaded or unchanged." }}
          />
          {rows.length > MAX_REJECTS ? (
            <p className={styles.note}>
              Showing {MAX_REJECTS} of {rows.length} rejects. <Link to={`/migration/runs/${runId}`}>Open the run</Link> for the rest.
            </p>
          ) : null}
        </div>
      )}
    </Card>
  );
}

function ConflictsCard({ sync, canResolve, onResolved }: { sync: SyncStatus; canResolve: boolean; onResolved(): void }) {
  const [status, setStatus] = useState<"open" | "all">("open");
  const key = `${sync.bindingId}/${status}/${sync.openConflicts}`;
  const filter = { bindingId: sync.bindingId, status: status === "open" ? "open" : undefined };
  const state = useLoad((signal) => listConflicts(filter, signal), key);
  const paged = usePaged(state, key, (cursor) => listConflicts({ ...filter, cursor }));
  const [pending, setPending] = useState<{ conflict: SyncConflict; resolution: ConflictResolution } | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function resolve(conflict: SyncConflict, resolution: ConflictResolution) {
    setBusy(true);
    setFailure(null);
    try {
      await resolveConflict(conflict.id, conflict.version, resolution);
      onResolved();
      state.reload();
    } catch (e) {
      setFailure(describeProblem(e));
      // A stale version means a run replaced the legacy values: show what is held now.
      state.reload();
    } finally {
      setBusy(false);
      setPending(null);
    }
  }

  const conflictColumns: DataTableColumn<SyncConflict>[] = [
    { key: "sourceKey", header: "Legacy key", mono: true },
    { key: "detectedAt", header: "Detected", render: (row) => formatDateTime(row.detectedAt) },
    { key: "legacyFields", header: "Legacy values not applied", render: (row) => <span className={styles.mono}>{summarize(row.legacyFields)}</span> },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    {
      key: "actions",
      header: "Resolve",
      render: (row) =>
        row.status === "open" && canResolve ? (
          <span className={styles.rowActions}>
            <Button size="sm" disabled={busy} onClick={() => setPending({ conflict: row, resolution: "keep_passage" })}>
              Keep Passage
            </Button>
            <Button size="sm" disabled={busy} onClick={() => setPending({ conflict: row, resolution: "accept_legacy" })}>
              Accept legacy
            </Button>
          </span>
        ) : (
          ""
        ),
    },
  ];

  return (
    <Card
      title="Conflicts"
      actions={
        <Button size="sm" onClick={() => setStatus(status === "open" ? "all" : "open")}>
          {status === "open" ? "Show resolved too" : "Show open only"}
        </Button>
      }
    >
      <div className={styles.stack}>
        {failure ? (
          <Alert tone="danger" title="Could not resolve the conflict" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {state.error ? (
          <Alert tone="danger" title="Could not load the conflicts" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Sync conflicts"
            columns={conflictColumns}
            rows={paged.rows}
            getRowKey={(row) => row.id}
            loading={state.loading}
            empty={{ title: status === "open" ? "No open conflicts" : "No conflicts", description: "A conflict appears when Passage is the source of truth and the legacy row differs from the loaded record." }}
          />
        )}
        <LoadMore paged={paged} noun="conflicts" />
      </div>
      {pending ? (
        <ConfirmDialog
          title={pending.resolution === "keep_passage" ? "Keep the Passage record?" : "Write the legacy values to Passage?"}
          description={
            pending.resolution === "keep_passage"
              ? `The Passage record for ${pending.conflict.sourceKey} stays as it is. The same legacy row is not reported again unless the legacy system changes it again.`
              : `The legacy values for ${pending.conflict.sourceKey} are written into the Passage record. This changes Passage data and is refused if the record was edited in Passage after the last load.`
          }
          confirmLabel={pending.resolution === "keep_passage" ? "Keep Passage" : "Accept legacy"}
          cancelLabel="Cancel"
          onConfirm={() => resolve(pending.conflict, pending.resolution)}
          onCancel={() => setPending(null)}
        />
      ) : null}
    </Card>
  );
}

/** A short one-line view of a conflict's legacy values. */
export function summarize(fields: Record<string, unknown>): string {
  const text = Object.entries(fields)
    .map(([k, v]) => `${k}=${typeof v === "string" ? v : JSON.stringify(v)}`)
    .join(", ");
  return text.length > 120 ? `${text.slice(0, 117)}...` : text;
}
