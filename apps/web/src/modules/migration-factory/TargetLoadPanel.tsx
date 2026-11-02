import { useState } from "react";
import { Alert, Badge, Button, Card, DataTable, StatGroup, StatTile, StatusPill, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { describeProblem, getTargetLoad, loadRunTarget, type Problem, type TargetLoad, type TargetLoadStatus, type TargetRow } from "./api/client";
import { dash, formatDateTime, formatKey, loadNeedsAttention, money, type ControlTotal } from "./domain";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const STATUS_LABELS: Record<TargetLoadStatus, { label: string; tone: BadgeTone }> = {
  STAGED: { label: "Ready to load", tone: "neutral" },
  LOADING: { label: "Loading", tone: "info" },
  PARTIAL: { label: "Partly loaded", tone: "warning" },
  UNRECONCILED: { label: "Totals not read", tone: "warning" },
  MISMATCH: { label: "Totals differ", tone: "danger" },
  RECONCILED: { label: "Reconciled", tone: "success" },
  FAILED: { label: "Failed", tone: "danger" },
};

const reconciliationColumns: DataTableColumn<ControlTotal>[] = [
  { key: "name", header: "Reconciliation", mono: true, render: (row) => row.name.replace("target.receivables.", "") },
  { key: "key", header: "Currency", render: (row) => row.key.currency ?? formatKey(row.key) },
  { key: "source", header: "Sent by the factory", align: "end", mono: true, render: (row) => money(row.source) },
  { key: "target", header: "Held by receivables", align: "end", mono: true, render: (row) => money(row.target) },
  { key: "delta", header: "Delta", align: "end", mono: true, render: (row) => money(row.delta) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

const rejectColumns: DataTableColumn<TargetRow>[] = [
  { key: "seq", header: "Seq", mono: true },
  { key: "legacyId", header: "Record", mono: true },
  { key: "ledger", header: "Ledger" },
  { key: "code", header: "Target code", mono: true, render: (row) => row.code ?? dash },
  { key: "detail", header: "Reason", render: (row) => row.detail ?? dash },
  { key: "amount", header: "Amount", align: "end", mono: true, render: (row) => (row.amount ? `${row.side === "CREDIT" ? "-" : ""}${money(row.amount)} ${row.currency ?? ""}`.trim() : dash) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

function summary(load: TargetLoad): string {
  switch (load.status) {
    case "STAGED":
      return `${load.pending} of ${load.staged} rows are ready to send to receivables.`;
    case "LOADING":
      return "Another request is loading this run.";
    case "PARTIAL":
      return `${load.pending} row(s) are still pending because receivables did not answer. Load again to resume; rows already stored are not sent twice.`;
    case "UNRECONCILED":
      return "Every row was sent, but receivables' totals could not be read. The load is not reconciled until they are.";
    case "MISMATCH":
      return "Every row was sent, but receivables holds different totals than the factory sent. Do not cut over until the lines below match.";
    case "FAILED":
      return "The load stopped: receivables refused the factory's service token or the batch request. The rows stay pending; fix the cause and load again.";
    default:
      return load.clean ? "Every row reached receivables and its totals match." : "Receivables' totals match what was accepted. Rows listed below were not loaded.";
  }
}

/**
 * The load of an open_items run into receivables: status, per-row results, the target rejects with the target's code,
 * and the reconciliation of the target's totals against what the factory sent (integration track S8).
 */
export function TargetLoadPanel({ initial, onChanged }: { initial: TargetLoad; onChanged?: () => void }) {
  const state = useLoad((signal) => getTargetLoad(initial.runId, signal), `${initial.runId}|${initial.version}`);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const detail = state.data;
  const load: TargetLoad = detail ?? initial;
  const status = STATUS_LABELS[load.status] ?? STATUS_LABELS.STAGED;

  async function run() {
    setBusy(true);
    setFailure(null);
    try {
      await loadRunTarget(initial.runId);
      if (onChanged) onChanged();
      else state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
      state.reload();
    } finally {
      setBusy(false);
    }
  }

  const canLoad = load.pending > 0 || load.status !== "RECONCILED";
  return (
    <Card title="Load into receivables">
      <div className={styles.stack}>
        <p className={styles.note}>
          Import source <span className={styles.mono}>{load.importSource}</span>, legal entity <span className={styles.mono}>{load.legalEntityId}</span>. Last pass{" "}
          {formatDateTime(load.finishedAt ?? load.startedAt)}; {load.attempts} pass(es).
        </p>
        <p>
          <Badge tone={status.tone}>{status.label}</Badge>
        </p>
        {failure ? (
          <Alert tone="danger" title="The load did not run" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {loadNeedsAttention(load.status) ? (
          <Alert tone={load.status === "MISMATCH" || load.status === "FAILED" ? "danger" : "warning"} title={status.label} live="status">
            {summary(load)}
            {load.lastError ? ` ${load.lastError}` : ""}
          </Alert>
        ) : (
          <p className={styles.note}>{summary(load)}</p>
        )}
        <StatGroup label="Load results">
          <StatTile label="Created" value={load.created} />
          <StatTile label="Updated" value={load.updated} />
          <StatTile label="Unchanged" value={load.unchanged + load.skipped} />
          <StatTile label="Pending" value={load.pending} tone={load.pending > 0 ? "warning" : "neutral"} />
          <StatTile label="Rejected" value={load.rejected} tone={load.rejected > 0 ? "danger" : "neutral"} />
          <StatTile label="Open AP, target not available" value={load.notAvailable} tone={load.notAvailable > 0 ? "warning" : "neutral"} />
        </StatGroup>
        <div className={styles.formActions}>
          <Button variant="primary" loading={busy} disabled={!canLoad} onClick={run}>
            {load.status === "STAGED" ? "Load into receivables" : "Load again"}
          </Button>
        </div>
        {state.error ? (
          <Alert tone="danger" title="Could not read the load" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : (
          <>
            <DataTable
              label="Reconciliation with receivables"
              columns={reconciliationColumns}
              rows={detail?.reconciliation ?? []}
              getRowKey={(row) => `${row.name}|${formatKey(row.key)}`}
              loading={state.loading && !detail}
              empty={{ title: "Not reconciled yet", description: "The comparison with receivables appears once every row has been sent." }}
            />
            <DataTable
              label="Rejected and unavailable rows"
              columns={rejectColumns}
              rows={detail?.rejects ?? []}
              getRowKey={(row) => String(row.seq)}
              loading={state.loading && !detail}
              empty={{ title: "No rejects", description: "Every row sent so far was accepted by receivables." }}
            />
          </>
        )}
      </div>
    </Card>
  );
}
