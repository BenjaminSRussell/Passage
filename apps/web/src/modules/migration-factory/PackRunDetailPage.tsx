import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  StatGroup,
  StatTile,
  StatusPill,
  Textarea,
  type DataTableColumn,
} from "../../shell/ui";
import { describeProblem, getRun, newKey, releaseQuarantined, type Problem, type QuarantinedRecord } from "./api/client";
import { canReconcile, formatDateTime, formatKey, money, projectsOf, projectStatus, SOURCE_LABELS, splitList, type ControlTotal } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const totalColumns: DataTableColumn<ControlTotal>[] = [
  { key: "name", header: "Control total", mono: true },
  { key: "key", header: "Key", render: (row) => formatKey(row.key) },
  { key: "source", header: "Legacy", align: "end", mono: true, render: (row) => money(row.source) },
  { key: "target", header: "Passage", align: "end", mono: true, render: (row) => money(row.target) },
  { key: "delta", header: "Delta", align: "end", mono: true, render: (row) => money(row.delta) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

interface ProjectRow {
  projectId: string;
  status: "RECONCILED" | "OPENING_MISMATCH";
}

interface Correction {
  party: string;
  account: string;
  parties: string;
}

/** One pack run: its counts, the control-total board, the quarantine with release, and its projects (9.49.6). */
export function PackRunDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getRun(id, signal), id);
  const run = state.data;
  const [selected, setSelected] = useState<QuarantinedRecord | null>(null);
  const [fix, setFix] = useState<Correction>({ party: "", account: "", parties: "" });
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const releaseKey = useRef(newKey());
  const mismatches = run ? run.controlTotals.filter((t) => t.status === "MISMATCH").length : 0;
  const releasable = Boolean(run && run.pack === "open_items" && run.status === "LOADED");

  const quarantineColumns: DataTableColumn<QuarantinedRecord>[] = [
    { key: "seq", header: "Seq", mono: true },
    { key: "legacyId", header: "Record", mono: true },
    { key: "rule", header: "Rule failed" },
    { key: "reason", header: "Reason" },
    { key: "amount", header: "Amount", align: "end", mono: true, render: (row) => `${money(row.amount)} ${row.currency ?? ""}`.trim() },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    {
      key: "action",
      header: "Action",
      render: (row) =>
        releasable && row.status === "OPEN" ? (
          <Button size="sm" onClick={() => choose(row)} aria-label={`Release record ${row.legacyId}`}>
            Release
          </Button>
        ) : row.status === "RELEASED" && row.releasedBy ? (
          `By ${row.releasedBy}`
        ) : null,
    },
  ];

  const projectColumns: DataTableColumn<ProjectRow>[] = [
    { key: "projectId", header: "Project", mono: true },
    { key: "status", header: "Opening balance", render: (row) => <StatusPill status={row.status === "RECONCILED" ? "Reconciled" : "Opening mismatch"} /> },
  ];

  function choose(record: QuarantinedRecord) {
    setSelected(record);
    setFix({ party: record.party ?? "", account: record.account ?? "", parties: "" });
    setFailure(null);
    setNotice(null);
    releaseKey.current = newKey();
  }

  async function submitRelease(event: FormEvent) {
    event.preventDefault();
    if (!run || !selected) return;
    setFailure(null);
    setBusy(true);
    try {
      const parties = splitList(fix.parties);
      const next = await releaseQuarantined(
        run.runId,
        selected.seq,
        {
          ...(fix.party.trim() !== (selected.party ?? "") ? { party: fix.party.trim() } : {}),
          ...(fix.account.trim() !== (selected.account ?? "") ? { account: fix.account.trim() } : {}),
          ...(parties.length > 0 ? { parties } : {}),
          version: run.version,
        },
        releaseKey.current,
      );
      state.set(next);
      setNotice(`Released ${selected.legacyId}. ${next.status === "RECONCILED" ? "Every control total now matches." : `${next.recordsQuarantined} record(s) still quarantined.`}`);
      setSelected(null);
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(
        problem.code === "MIG-0006" ? { ...problem, message: "Someone changed this run since you opened it. The run has been reloaded; check it and try again." } : problem,
      );
      if (problem.code === "MIG-0006") state.reload();
      releaseKey.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  const projects: ProjectRow[] = run && run.pack === "project_wip" ? projectsOf(run.controlTotals).map((p) => ({ projectId: p, status: projectStatus(run.controlTotals, p) })) : [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={run ? `Pack run ${run.pack}` : "Pack run"}
        description={
          run
            ? canReconcile(run.controlTotals)
              ? "Every control total matches the legacy extract."
              : `${mismatches} control total(s) do not match; the run can't reconcile until they do.`
            : undefined
        }
        meta={run ? <StatusPill status={run.status} /> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/migration-factory">
            All runs
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load the pack run" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="The record was not released" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      {notice ? (
        <Alert tone="success" title="Record released" live="status">
          {notice}
        </Alert>
      ) : null}
      {run ? (
        <>
          {run.message ? (
            <Alert tone="info" title="Nothing new to load">
              {run.message}
            </Alert>
          ) : null}
          <StatGroup label="Records">
            <StatTile label="Records" value={run.recordsIn} />
            <StatTile label="Loaded" value={run.recordsLoaded} />
            <StatTile label="Already present" value={run.alreadyPresent} />
            <StatTile label="Quarantined" value={run.recordsQuarantined} tone={run.recordsQuarantined > 0 ? "warning" : "neutral"} />
          </StatGroup>
          <Card title="Run">
            <dl className={styles.details}>
              <div className={styles.entry}>
                <dt className={styles.term}>Source</dt>
                <dd className={styles.value}>
                  {SOURCE_LABELS[run.sourceSystem] ?? run.sourceSystem} {run.sourceCompany}
                </dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Cutoff</dt>
                <dd className={styles.value}>{formatDateTime(run.cutoffTs)}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Started</dt>
                <dd className={styles.value}>{formatDateTime(run.createdAt)}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Input hash</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{run.inputHash}</dd>
              </div>
            </dl>
          </Card>
        </>
      ) : null}
      {!state.error ? (
        <DataTable
          label="Control totals"
          columns={totalColumns}
          rows={run?.controlTotals ?? []}
          getRowKey={(row) => `${row.name}|${formatKey(row.key)}`}
          loading={state.loading}
          empty={{ title: "No totals yet", description: "Totals appear once the run has loaded." }}
        />
      ) : null}
      {projects.length > 0 && run ? (
        <Card title="Projects">
          <p className={styles.note}>Opening lines never post to the GL. A project that does not reconcile can't be invoiced or estimated (BR-49.12).</p>
          <DataTable
            label="Projects"
            columns={projectColumns}
            rows={projects}
            getRowKey={(row) => row.projectId}
            onRowOpen={(row) => navigate(`/migration-factory/runs/${run.runId}/projects/${encodeURIComponent(row.projectId)}`)}
          />
        </Card>
      ) : null}
      {selected ? (
        <Card title={`Release ${selected.legacyId}`}>
          <form className={styles.form} onSubmit={submitRelease} noValidate>
            <p className={`${styles.note} ${styles.wide}`}>
              {selected.rule}: {selected.reason}. Correct the party or control account; amount and currency come from the legacy extract and change only by
              re-extract.
            </p>
            <FormField id="release-party" label="Party">
              {(control) => <Input {...control} value={fix.party} onChange={(event) => setFix({ ...fix, party: event.target.value })} />}
            </FormField>
            <FormField id="release-account" label="Control account">
              {(control) => <Input {...control} value={fix.account} onChange={(event) => setFix({ ...fix, account: event.target.value })} />}
            </FormField>
            <div className={styles.wide}>
              <FormField id="release-parties" label="Parties added to master data" hint="Ids created since the run, separated by commas." optional>
                {(control) => <Textarea {...control} mono rows={2} value={fix.parties} onChange={(event) => setFix({ ...fix, parties: event.target.value })} />}
              </FormField>
            </div>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Release record
              </Button>
              <Button variant="ghost" onClick={() => setSelected(null)}>
                Cancel
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      {run && run.quarantined.length > 0 ? (
        <DataTable label="Quarantined records" columns={quarantineColumns} rows={run.quarantined} getRowKey={(row) => String(row.seq)} />
      ) : null}
    </div>
  );
}
