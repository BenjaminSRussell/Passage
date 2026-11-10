import { useRef, useState } from "react";
import { Alert, Button, Card, DataTable, type DataTableColumn } from "../../shell/ui";
import { describeProblem, newKey, reprocessInterchange, type Problem, type ReprocessedMessage, type ReprocessResult } from "./api/client";
import { dash } from "./format";
import { MessagePill } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const columns: DataTableColumn<ReprocessedMessage>[] = [
  { key: "transactionSet", header: "Set", mono: true },
  { key: "stControl", header: "ST02", mono: true },
  { key: "previousError", header: "Failed with", render: (row) => row.previousError || dash },
  { key: "status", header: "Outcome", render: (row) => <MessagePill status={row.status} /> },
  { key: "error", header: "New finding", render: (row) => row.error || dash },
  { key: "changed", header: "Changes", render: (row) => (row.changed ? "Yes" : "No") },
];

/**
 * Reprocess the failed transactions of an inbound interchange from its stored
 * raw payload (FR-35.10). Preview runs the service's dry run; Reprocess writes
 * with one Idempotency-Key per attempt, so a double click runs once.
 */
export function ReprocessCard({ interchangeId, failedCount, onDone }: { interchangeId: string; failedCount: number; onDone(): void }) {
  const [busy, setBusy] = useState<"preview" | "apply" | null>(null);
  const [result, setResult] = useState<ReprocessResult | null>(null);
  const [error, setError] = useState<Problem | null>(null);
  const key = useRef<string | null>(null);

  async function run(dryRun: boolean) {
    setBusy(dryRun ? "preview" : "apply");
    setError(null);
    if (!dryRun && key.current === null) key.current = newKey();
    try {
      const res = await reprocessInterchange(interchangeId, { dryRun, key: key.current ?? undefined });
      setResult(res);
      if (!dryRun) {
        key.current = null;
        onDone();
      }
    } catch (failure) {
      const problem = describeProblem(failure);
      // A reused key cannot succeed again; the next attempt gets a fresh one.
      if (problem.code === "idempotency_key_reuse") key.current = null;
      setError(problem);
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card
      title="Reprocess failed transactions"
      actions={
        <>
          <Button size="sm" onClick={() => run(true)} loading={busy === "preview"} disabled={busy === "apply"}>
            Preview
          </Button>
          <Button size="sm" variant="primary" onClick={() => run(false)} loading={busy === "apply"} disabled={busy === "preview"}>
            Reprocess
          </Button>
        </>
      }
    >
      <div className={styles.stack}>
        <p className={styles.note}>
          {`${failedCount} transaction${failedCount === 1 ? "" : "s"} failed. Reprocessing maps them again from the interchange as received, after a map or master-data fix. Transactions that now map are passed on again; the 997 already sent is not changed. Preview shows the outcome without saving it.`}
        </p>
        {error ? (
          <Alert tone="danger" title="Reprocess failed" live="alert">
            <ProblemMessage problem={error} />
          </Alert>
        ) : null}
        {result ? (
          <>
            <Alert tone={result.changed > 0 ? "success" : "info"} title={result.dryRun ? "Preview" : "Reprocessed"} live="status">
              {result.dryRun
                ? `${result.changed} of ${result.messages.length} would change. Nothing was saved.`
                : `${result.changed} of ${result.messages.length} changed.`}
            </Alert>
            <DataTable
              label={result.dryRun ? "Preview of reprocess" : "Reprocess outcome"}
              columns={columns}
              rows={result.messages}
              getRowKey={(row) => row.messageId}
              empty={{ title: "Nothing to reprocess", description: "No transaction of this interchange is failed any more." }}
            />
          </>
        ) : null}
      </div>
    </Card>
  );
}
