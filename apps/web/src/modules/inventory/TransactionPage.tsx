import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  applyNamedMove,
  describeProblem,
  getTransaction,
  NAMED_MOVES,
  newIdempotencyKey,
  type NamedCommand,
  type Problem,
  type StatusChange,
  type TransactionDetail,
  type TransactionStatus,
} from "./api/client";
import { dash, describeDimensions, formatDateTime, sentence, shortId, today } from "./format";
import { ALL_DIMENSIONS, Detail, money, ProblemAlert } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const commandLabels: Record<string, string> = {
  CREATE: "Registered",
  STEP: "Step forward",
  UNPICK: "Unpick",
  UNREGISTER: "Unregister",
  CANCEL_RESERVATION: "Cancel reservation",
};

const moveHints: Record<NamedCommand, string> = {
  UNPICK: "Return picked stock to its physical reservation.",
  UNREGISTER: "Withdraw a registered receipt back to arrived (expected) stock.",
  CANCEL_RESERVATION: "Release the reservation; the issue goes back to on order.",
};

export function statusOf(t: { issueStatus?: TransactionStatus; receiptStatus?: TransactionStatus }): TransactionStatus | undefined {
  return t.issueStatus ?? t.receiptStatus;
}

/** The named backward moves that apply to a transaction at status (BR-16.1). */
export function movesFor(status: TransactionStatus | undefined): { command: NamedCommand; to: TransactionStatus }[] {
  if (!status) return [];
  return (Object.keys(NAMED_MOVES) as NamedCommand[]).flatMap((command) => {
    const to = NAMED_MOVES[command].from[status];
    return to ? [{ command, to }] : [];
  });
}

const historyColumns: DataTableColumn<StatusChange>[] = [
  { key: "occurredAt", header: "When", mono: true, render: (row) => formatDateTime(row.occurredAt) },
  { key: "command", header: "Command", render: (row) => commandLabels[row.command] ?? sentence(row.command) },
  { key: "fromStatus", header: "From", render: (row) => (row.fromStatus ? sentence(row.fromStatus) : dash) },
  { key: "toStatus", header: "To", render: (row) => sentence(row.toStatus) },
  { key: "qty", header: "Quantity", align: "end", mono: true },
  { key: "date", header: "Date", mono: true },
];

/** The named moves available at the transaction's status, with the date they carry. */
function MovesCard({ txn, onMoved }: { txn: TransactionDetail; onMoved(change: StatusChange): void }) {
  const status = statusOf(txn);
  const moves = movesFor(status);
  const [date, setDate] = useState(today());
  const [busy, setBusy] = useState<NamedCommand | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  // One key per move and date until it succeeds: a retry after a lost response replays it.
  const keys = useRef<Record<string, string>>({});

  async function run(command: NamedCommand) {
    const slot = `${command}|${date}`;
    keys.current[slot] ??= newIdempotencyKey();
    setBusy(command);
    setFailure(null);
    try {
      const change = await applyNamedMove(txn.id, command, date, keys.current[slot]);
      delete keys.current[slot];
      onMoved(change);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card title="Move back">
      {moves.length === 0 ? (
        <p className={styles.muted}>
          {status
            ? `No backward move applies at ${sentence(status).toLowerCase()}. Unpick applies to picked issues, unregister to registered receipts and cancel reservation to reserved issues; forward moves come from the documents that own the transaction.`
            : "The transaction has no status."}
        </p>
      ) : (
        <form onSubmit={(event: FormEvent) => event.preventDefault()} noValidate className={styles.form}>
          <FormField id="txn-move-date" label="Date of the move">
            {(control) => <Input {...control} type="date" value={date} onChange={(event) => setDate(event.target.value)} />}
          </FormField>
          <div className={styles.formActions}>
            {moves.map((move) => (
              <Button key={move.command} loading={busy === move.command} disabled={busy !== null && busy !== move.command} onClick={() => void run(move.command)}>
                {`${commandLabels[move.command]} to ${sentence(move.to).toLowerCase()}`}
              </Button>
            ))}
          </div>
          <ul className={styles.hints}>
            {moves.map((move) => (
              <li key={move.command}>{moveHints[move.command]}</li>
            ))}
          </ul>
        </form>
      )}
      {failure ? (
        <Alert tone="danger" title="The status was not changed" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
    </Card>
  );
}

/** One inventory transaction (FR-16.13) with its status history and the named backward moves (FR-16.2). */
export function TransactionPage() {
  const { id = "" } = useParams();
  const [attempt, setAttempt] = useState(0);
  const state = useLoad((signal) => getTransaction(id, signal), `${id}|${attempt}`);
  const [notice, setNotice] = useState("");

  const back = (
    <Link className={buttonClassName("ghost")} to="/inventory/transactions">
      Back to transactions
    </Link>
  );

  if (state.error) {
    if (state.error.code === "INV-TXN-001") {
      return <EmptyState icon="xCircle" headingLevel={2} title="Transaction not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return <ProblemAlert title="Could not load the transaction" problem={state.error} onRetry={state.reload} />;
  }
  const txn = state.data;
  if (!txn) {
    return (
      <div className={styles.stack} aria-busy="true">
        <span className="visually-hidden">Loading transaction…</span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  const status = statusOf(txn);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Transaction ${shortId(txn.id)}`}
        meta={status ? <Badge tone={txn.issueStatus ? "warning" : "success"}>{`${txn.issueStatus ? "Issue" : "Receipt"} · ${sentence(status)}`}</Badge> : undefined}
        description={`${txn.qty} ${txn.enteredUnit} of product ${shortId(txn.productId)} · ${describeDimensions(txn.dimensions, ALL_DIMENSIONS)}`}
        actions={back}
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      <Card title="Details">
        <dl className={styles.details}>
          <Detail term="Transaction" mono>
            {txn.id}
          </Detail>
          <Detail term="Product" mono>
            {txn.productId}
          </Detail>
          <Detail term="Legal entity" mono>
            {txn.legalEntityId}
          </Detail>
          <Detail term="Origin">{`${sentence(txn.sourceType)}${txn.reference ? ` ${txn.reference}` : ""}`}</Detail>
          <Detail term="Physical date">{txn.physicalDate}</Detail>
          <Detail term="Financial date">{txn.financialDate ?? dash}</Detail>
          <Detail term="Physical cost" mono>
            {money(txn.costAmountPhysical, txn.currency)}
          </Detail>
          <Detail term="Posted cost" mono>
            {money(txn.costAmountPosted, txn.currency)}
          </Detail>
        </dl>
        {txn.journalId ? (
          <p className={styles.muted}>
            <Link to={`/inventory/journals/${txn.journalId}`}>Open journal {shortId(txn.journalId)}</Link>
          </p>
        ) : null}
      </Card>
      <MovesCard
        key={status}
        txn={txn}
        onMoved={(change) => {
          setNotice(`${commandLabels[change.command]}: ${sentence(change.fromStatus ?? "").toLowerCase()} to ${sentence(change.toStatus).toLowerCase()}.`);
          setAttempt((n) => n + 1);
        }}
      />
      <DataTable
        label="Status history"
        columns={historyColumns}
        rows={txn.statusChanges}
        getRowKey={(row) => row.id}
        empty={{ title: "No status changes", description: "Journal, blocking, transfer and consignment postings write the transaction at its final status, so they have no history." }}
      />
    </div>
  );
}
