import { useRef, useState } from "react";
import { Alert, Button, Card, DataTable, FormField, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { describeConflictError, listConflicts, resolveConflict, type ConflictResolution, type OfflineConflict } from "./api/client";
import { newKey, type Problem } from "./api/print";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** MOB-0107 catalog text for one conflict (§6.32.18). */
export function conflictMessage(c: Pick<OfflineConflict, "idempotencyKey" | "conflictReason">): string {
  return `Transaction ${c.idempotencyKey} conflicts: ${c.conflictReason}.`;
}

const RESOLUTIONS: Record<ConflictResolution, { label: string; effect: string; done: string }> = {
  reapply: {
    label: "Apply as movement",
    effect: "The inventory moves as the worker scanned it and the link to the order line is dropped.",
    done: "applied as a movement",
  },
  reject: {
    label: "Reject",
    effect: "The transaction is discarded. Nothing moves; the worker's later transactions replay after it.",
    done: "rejected",
  },
};

const columns: DataTableColumn<OfflineConflict>[] = [
  { key: "idempotencyKey", header: "Transaction", mono: true },
  { key: "deviceId", header: "Device", mono: true },
  { key: "command", header: "Command", mono: true },
  { key: "capturedAt", header: "Captured", render: (row) => formatDateTime(row.capturedAt) },
  { key: "conflictReason", header: "Conflict", render: (row) => `MOB-0107 ${conflictMessage(row)}` },
];

/**
 * Supervisor queue of offline CONFLICT transactions (BR-32.4, AC-32.6, §6.32.14). Replay for the
 * device stays blocked until the supervisor applies the transaction as a movement or rejects it.
 */
export function OfflineConflictsPage() {
  const state = useLoad((signal) => listConflicts(signal), "conflicts", describeConflictError);
  const [selected, setSelected] = useState<OfflineConflict | null>(null);
  const [resolution, setResolution] = useState<ConflictResolution | "">("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<string | null>(null);
  // One key per (conflict, resolution) so a retried submit cannot resolve twice.
  const key = useRef(newKey());

  function open(row: OfflineConflict) {
    setSelected(row);
    setResolution("");
    setFailure(null);
    setDone(null);
    key.current = newKey();
  }

  function choose(value: ConflictResolution | "") {
    setResolution(value);
    setFailure(null);
    key.current = newKey();
  }

  async function confirm() {
    if (!selected || resolution === "") return;
    setBusy(true);
    setFailure(null);
    try {
      await resolveConflict(selected, resolution, key.current);
      setDone(`Transaction ${selected.idempotencyKey} was ${RESOLUTIONS[resolution].done}.`);
      setSelected(null);
      setResolution("");
      state.reload();
    } catch (error) {
      setFailure(describeConflictError(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Offline conflicts"
        description="Confirmations a handheld captured offline that the server could not apply, because the line was cancelled or completed by someone else meanwhile. The device replays nothing after a conflict until it is resolved here."
        actions={<Button onClick={state.reload}>Refresh</Button>}
      />
      {done ? (
        <Alert tone="success" live="status">
          {done}
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load offline conflicts" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Offline conflicts"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.idempotencyKey}
          loading={state.loading}
          onRowOpen={open}
          empty={{ title: "No offline conflicts", description: "Every offline confirmation has replayed or been resolved." }}
        />
      )}
      {selected ? (
        <Card
          title={`Resolve ${selected.idempotencyKey}`}
          headingLevel={2}
          actions={
            <Button variant="ghost" size="sm" onClick={() => setSelected(null)}>
              Close
            </Button>
          }
        >
          <div className={styles.stack}>
            <dl className={styles.details}>
              <div className={styles.entry}>
                <dt className={styles.term}>Device</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{selected.deviceId}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Command</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{selected.command}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Captured</dt>
                <dd className={styles.value}>{formatDateTime(selected.capturedAt)}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Conflict</dt>
                <dd className={styles.value}>{conflictMessage(selected)}</dd>
              </div>
            </dl>
            <FormField
              id="conflict-resolution"
              label="Resolution"
              hint={resolution ? RESOLUTIONS[resolution].effect : "Choose how to settle this transaction."}
            >
              {(control) => (
                <Select {...control} value={resolution} onChange={(event) => choose(event.target.value as ConflictResolution | "")}>
                  <option value="">Choose a resolution</option>
                  <option value="reapply">{RESOLUTIONS.reapply.label}</option>
                  <option value="reject">{RESOLUTIONS.reject.label}</option>
                </Select>
              )}
            </FormField>
            {failure ? (
              <Alert tone="danger" title="The conflict was not resolved" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            ) : null}
            <div className={styles.toolbar}>
              <Button
                variant={resolution === "reject" ? "danger" : "primary"}
                disabled={resolution === ""}
                loading={busy}
                onClick={confirm}
              >
                Confirm resolution
              </Button>
            </div>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
