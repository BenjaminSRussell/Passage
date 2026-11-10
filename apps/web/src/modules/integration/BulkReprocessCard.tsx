import { useEffect, useRef, useState } from "react";
import { Alert, Badge, Button, Card, DataTable, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import {
  bulkReprocess,
  describeProblem,
  MAX_BULK_REPROCESS,
  newKey,
  type BulkOutcome,
  type BulkReprocessItem,
  type BulkReprocessResult,
  type MonitorMessage,
  type Problem,
} from "./api/client";
import { dash } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const OUTCOMES: Record<BulkOutcome, { label: string; tone: BadgeTone }> = {
  REPROCESSED: { label: "Reprocessed", tone: "success" },
  NOT_FOUND: { label: "Not found", tone: "warning" },
  NOT_INBOUND: { label: "Outbound", tone: "warning" },
  UNREADABLE: { label: "Unreadable", tone: "danger" },
};

interface Target {
  interchangeId: string;
  controlNumber: string;
  partnerName: string;
}

/** The distinct inbound interchanges behind the failed, non-997 rows, in list order. */
export function reprocessTargets(rows: MonitorMessage[]): Target[] {
  const seen = new Set<string>();
  const out: Target[] = [];
  for (const row of rows) {
    if (row.direction !== "IN" || row.status !== "FAILED" || row.transactionSet === "997" || seen.has(row.interchangeId)) continue;
    seen.add(row.interchangeId);
    out.push({ interchangeId: row.interchangeId, controlNumber: row.controlNumber, partnerName: row.partnerName });
  }
  return out;
}

/**
 * Bulk reprocess from the message monitor (PRD §6.35.6: at most 200, with a
 * dry run). It takes the failed inbound interchanges in the current list, so
 * the filters choose what is reprocessed. Preview runs the dry run; Reprocess
 * writes with one Idempotency-Key per attempt.
 */
export function BulkReprocessCard({ rows, onDone }: { rows: MonitorMessage[]; onDone(): void }) {
  const all = reprocessTargets(rows);
  const targets = all.slice(0, MAX_BULK_REPROCESS);
  const byId = new Map(targets.map((t) => [t.interchangeId, t]));
  const ids = targets.map((t) => t.interchangeId);
  const idsKey = ids.join(",");
  const [busy, setBusy] = useState<"preview" | "apply" | null>(null);
  // The outcome keeps the interchanges it ran on: the list reloads after a run and they leave it.
  const [result, setResult] = useState<{ res: BulkReprocessResult; ran: Map<string, Target> } | null>(null);
  const [error, setError] = useState<Problem | null>(null);
  const key = useRef<string | null>(null);

  // Another selection is another request: forget the key, keep the last outcome until the next run.
  useEffect(() => {
    key.current = null;
  }, [idsKey]);

  async function run(dryRun: boolean) {
    setBusy(dryRun ? "preview" : "apply");
    setError(null);
    if (!dryRun && key.current === null) key.current = newKey();
    try {
      const ran = byId;
      const res = await bulkReprocess(ids, { dryRun, key: key.current ?? undefined });
      setResult({ res, ran });
      if (!dryRun) {
        key.current = null;
        onDone();
      }
    } catch (failure) {
      const problem = describeProblem(failure);
      if (problem.code === "idempotency_key_reuse") key.current = null;
      setError(problem);
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<BulkReprocessItem>[] = [
    { key: "controlNumber", header: "ISA13", mono: true, render: (row) => result?.ran.get(row.interchangeId)?.controlNumber ?? row.interchangeId },
    { key: "partnerName", header: "Partner", render: (row) => result?.ran.get(row.interchangeId)?.partnerName ?? dash },
    { key: "outcome", header: "Outcome", render: (row) => <Badge tone={OUTCOMES[row.outcome].tone}>{OUTCOMES[row.outcome].label}</Badge> },
    { key: "changed", header: "Changes", align: "end", render: (row) => `${row.changed} of ${row.messages.length}` },
    { key: "error", header: "Note", render: (row) => row.error || dash },
  ];

  if (targets.length === 0 && !result) return null;
  return (
    <Card
      title="Bulk reprocess"
      actions={
        <>
          <Button size="sm" onClick={() => run(true)} loading={busy === "preview"} disabled={busy === "apply" || targets.length === 0}>
            Preview
          </Button>
          <Button size="sm" variant="primary" onClick={() => run(false)} loading={busy === "apply"} disabled={busy === "preview" || targets.length === 0}>
            {`Reprocess ${targets.length}`}
          </Button>
        </>
      }
    >
      <div className={styles.stack}>
        <p className={styles.note}>
          {targets.length === 0
            ? "No failed inbound transaction is left in this list."
            : `${targets.length} inbound interchange${targets.length === 1 ? "" : "s"} in this list ha${targets.length === 1 ? "s" : "ve"} failed transactions. Reprocessing maps them again from the interchange as received; use the filters to choose which. Preview shows the outcome without saving it.`}
          {all.length > MAX_BULK_REPROCESS ? ` Only the first ${MAX_BULK_REPROCESS} are taken in one run.` : ""}
        </p>
        {error ? (
          <Alert tone="danger" title="Bulk reprocess failed" live="alert">
            <ProblemMessage problem={error} />
          </Alert>
        ) : null}
        {result ? (
          <>
            <Alert tone={result.res.changed > 0 ? "success" : "info"} title={result.res.dryRun ? "Preview" : "Reprocessed"} live="status">
              {`${result.res.changed} transaction${result.res.changed === 1 ? "" : "s"} in ${result.res.requested} interchange${result.res.requested === 1 ? "" : "s"} ${
                result.res.dryRun ? "would change. Nothing was saved." : "changed."
              }`}
            </Alert>
            <DataTable
              label={result.res.dryRun ? "Preview of bulk reprocess" : "Bulk reprocess outcome"}
              columns={columns}
              rows={result.res.items}
              getRowKey={(row) => row.interchangeId}
            />
          </>
        ) : null}
      </div>
    </Card>
  );
}
