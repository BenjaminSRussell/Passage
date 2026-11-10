import { useState } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import { DUTY_HUB_WRITE, listDeadLetters, replayDelivery, type Delivery } from "./api/hubClient";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { DEAD_LETTER_REASONS, NOT_REPLAYABLE_CODES } from "./syncFormat";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

/** Dead letters: events the ERP (or the mapping) refused for good, with the answer kept, and a replay once the cause is fixed. */
export function SyncDeadLettersPage() {
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_HUB_WRITE);
  const state = useLoad((signal) => listDeadLetters(undefined, signal), "sync-dead-letters");
  const [replaying, setReplaying] = useState<string | null>(null);
  const [replayed, setReplayed] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);

  async function replay(row: Delivery) {
    setReplaying(row.id);
    setFailure(null);
    setReplayed(null);
    try {
      await replayDelivery(row.id);
      setReplayed(row.eventType);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setReplaying(null);
    }
  }

  const columns: DataTableColumn<Delivery>[] = [
    { key: "eventType", header: "Event", mono: true },
    { key: "aggregateId", header: "Subject", mono: true },
    { key: "deadLetterCode", header: "Reason", render: (row) => (row.deadLetterCode ? `${DEAD_LETTER_REASONS[row.deadLetterCode] ?? "Failed"} (${row.deadLetterCode})` : dash) },
    { key: "lastStatus", header: "Last HTTP status", align: "end", render: (row) => (row.lastStatus === null ? dash : String(row.lastStatus)) },
    { key: "responseExcerpt", header: "The ERP's answer", render: (row) => <span className={styles.mono}>{row.responseExcerpt ?? dash}</span> },
    { key: "createdAt", header: "Received", render: (row) => formatDateTime(row.createdAt) },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.deadLetterCode !== null && NOT_REPLAYABLE_CODES.has(row.deadLetterCode) ? (
          // The ERP already accepted this record (its key is in the answer); a replay would push a duplicate.
          <span>Reconcile in the ERP (legacy key {row.legacyKey ?? dash})</span>
        ) : (
          <Button size="sm" disabled={!canWrite || replaying !== null} loading={replaying === row.id} onClick={() => void replay(row)} aria-label={`Replay ${row.eventType} ${row.aggregateId}`}>
            Replay
          </Button>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title="Sync dead letters" description="Fix the cause first (a missing customer in the ERP, a wrong key path, the ERP's data), then replay: the event is sent again with the same idempotency key." />
      {replayed ? (
        <Alert tone="success" title="Queued for delivery" live="status">
          {replayed} will be sent on the next poll.
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not replay" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load dead letters" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Sync dead letters"
          columns={columns}
          rows={state.data?.items ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No dead letters", description: "Events the ERP refused for good would appear here." }}
        />
      )}
    </div>
  );
}
