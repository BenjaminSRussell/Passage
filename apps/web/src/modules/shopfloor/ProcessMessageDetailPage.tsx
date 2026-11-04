import { useRef, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, PageHeader, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  deleteProcessMessage,
  describeProblem,
  getProcessMessage,
  newKey,
  reprocessProcessMessage,
  type Characteristic,
  type ProcessDelivery,
  type ProcessMessage,
  type Problem,
} from "./api/client";
import { dash, formatDateTime } from "./format";
import styles from "./pages.module.css";
import { categoryLabel, DESTINATION_LABELS, DeliveryStatus, MessageStatus, limits } from "./processLabels";
import { useLoad } from "./useLoad";

const valueColumns: DataTableColumn<Characteristic>[] = [
  { key: "name", header: "Characteristic", mono: true },
  { key: "type", header: "Type" },
  { key: "value", header: "Value", align: "end", mono: true, render: (row) => (row.unit ? `${row.value} ${row.unit}` : row.value) },
  { key: "limits", header: "Limits", render: (row) => limits(row) },
  { key: "outOfLimit", header: "Check", render: (row) => (row.outOfLimit ? <Badge tone="warning">Out of limit</Badge> : dash) },
];

/**
 * One process message with its values and per-destination deliveries. A destination in
 * error can be reprocessed, or the message deleted with a reason (FR-25.17).
 */
export function ProcessMessageDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getProcessMessage(id, signal), id);
  const [fresh, setFresh] = useState<ProcessMessage | null>(null);
  const [reprocessing, setReprocessing] = useState<ProcessDelivery["destination"] | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [notice, setNotice] = useState<{ title: string; text: string } | null>(null);
  const message = fresh && fresh.id === id ? fresh : state.data;
  const deleted = Boolean(message?.deletedAt);
  const deletable = Boolean(message && !deleted && message.deliveries.some((d) => d.status === "error"));

  const deliveryColumns: DataTableColumn<ProcessDelivery>[] = [
    { key: "destination", header: "Destination", render: (row) => DESTINATION_LABELS[row.destination] ?? row.destination },
    { key: "status", header: "Status", render: (row) => <DeliveryStatus status={row.status} /> },
    { key: "attempts", header: "Failed attempts", align: "end", render: (row) => String(row.attempts) },
    { key: "lastError", header: "Last error", render: (row) => row.lastError ?? dash },
    {
      key: "when",
      header: "Next attempt or delivered",
      render: (row) => (row.deliveredAt ? `Delivered ${formatDateTime(row.deliveredAt)}` : row.nextAttemptAt ? formatDateTime(row.nextAttemptAt) : dash),
    },
    {
      key: "action",
      header: "Action",
      render: (row) =>
        row.status === "error" && !deleted ? (
          <Button size="sm" aria-label={`Reprocess ${DESTINATION_LABELS[row.destination] ?? row.destination}`} onClick={() => setReprocessing(row.destination)}>
            Reprocess
          </Button>
        ) : (
          dash
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Process message"
        description={<Link to="/shopfloor/process-messages">All process messages</Link>}
        actions={
          deletable && !deleting ? (
            <Button
              variant="danger"
              onClick={() => {
                setReprocessing(null);
                setDeleting(true);
              }}
            >
              Delete message
            </Button>
          ) : undefined
        }
      />
      {notice ? (
        <Alert tone="success" title={notice.title} live="status">
          {notice.text}
        </Alert>
      ) : null}
      {state.error && !message ? (
        <Alert tone="danger" title="Could not load the process message" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : !message ? (
        <Card>
          <Skeleton />
        </Card>
      ) : (
        <>
          {message.deletedAt ? (
            <Alert tone="warning" title="Deleted">
              {`Deleted ${formatDateTime(message.deletedAt)}${message.deletedBy ? ` by ${message.deletedBy}` : ""}: ${message.deleteReason ?? ""}. `}
              It no longer appears in the monitor, takes no delivery reports and is not retried.
            </Alert>
          ) : null}
          {deleting && deletable ? (
            <DeleteForm
              messageId={message.id}
              onDone={(saved) => {
                setFresh(saved);
                setDeleting(false);
                setNotice({ title: "Message deleted", text: "The reason is kept with the message, and its remaining retries have stopped." });
              }}
              onCancel={() => setDeleting(false)}
            />
          ) : null}
          <Card>
            <dl className={styles.details}>
              <div className={styles.entry}>
                <dt className={styles.term}>Status</dt>
                <dd className={`${styles.value} ${styles.flags}`}>
                  <MessageStatus status={message.status} />
                  {message.outOfLimit ? <Badge tone="warning">Out of limit</Badge> : null}
                </dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Category</dt>
                <dd className={styles.value}>{categoryLabel(message.categoryCode)}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Order</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{message.orderId}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Phase</dt>
                <dd className={`${styles.value} ${styles.mono}`}>{message.phasePath || dash}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Source</dt>
                <dd className={styles.value}>{`${message.source}, message ${message.sourceMessageId}`}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Occurred</dt>
                <dd className={styles.value}>{formatDateTime(message.occurredAt)}</dd>
              </div>
              <div className={styles.entry}>
                <dt className={styles.term}>Received</dt>
                <dd className={styles.value}>{formatDateTime(message.receivedAt)}</dd>
              </div>
            </dl>
          </Card>
          <Card title="Values" padded={false}>
            <DataTable
              label="Values"
              columns={valueColumns}
              rows={message.values}
              getRowKey={(row) => row.name}
              empty={{ title: "No values", description: "This message reports an event without characteristic values." }}
            />
          </Card>
          {reprocessing ? (
            <ReprocessForm
              key={reprocessing}
              messageId={message.id}
              destination={reprocessing}
              onDone={(saved) => {
                setFresh(saved);
                setReprocessing(null);
                setNotice({ title: "Sent again", text: `${DESTINATION_LABELS[reprocessing]} is pending again and the message was published to it once more.` });
              }}
              onCancel={() => setReprocessing(null)}
            />
          ) : null}
          <Card title="Deliveries" padded={false}>
            <DataTable
              label="Deliveries"
              columns={deliveryColumns}
              rows={message.deliveries}
              getRowKey={(row) => row.destination}
              empty={{ title: "No destinations", description: "The category routes this message nowhere." }}
            />
          </Card>
          <p className={styles.note}>
            A destination is retried after 1, 5, 15 and 60 minutes. After the fifth failed attempt it stays in error until it is reprocessed, or the
            message is deleted with a reason.
          </p>
        </>
      )}
    </div>
  );
}

interface ReprocessFormProps {
  messageId: string;
  destination: ProcessDelivery["destination"];
  onDone(saved: ProcessMessage): void;
  onCancel(): void;
}

function ReprocessForm({ messageId, destination, onDone, onCancel }: ReprocessFormProps) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const text = reason.trim();
    if (text.length < 1 || text.length > 500) {
      setError("Say why the destination should be sent again, up to 500 characters.");
      return;
    }
    setError(null);
    setFailure(null);
    setBusy(true);
    try {
      onDone(await reprocessProcessMessage(messageId, { destination, reason: text }, key.current));
    } catch (failed) {
      const problem = describeProblem(failed);
      if (problem.fields.reason) setError(problem.fields.reason);
      else setFailure(problem);
      // Keep the key so a retry after a lost response replays the stored outcome.
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title={`Reprocess ${DESTINATION_LABELS[destination]}`} headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <FormField id="reprocess-reason" label="Reason" hint="Kept on the delivery, e.g. the quality system is back online." error={error ?? undefined}>
          {(control) => (
            <Textarea
              {...control}
              value={reason}
              maxLength={500}
              rows={3}
              invalid={Boolean(error)}
              onChange={(event) => {
                setReason(event.target.value);
                key.current = newKey();
              }}
            />
          )}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not reprocess" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Send again
          </Button>
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      </form>
    </Card>
  );
}

interface DeleteFormProps {
  messageId: string;
  onDone(saved: ProcessMessage): void;
  onCancel(): void;
}

function DeleteForm({ messageId, onDone, onCancel }: DeleteFormProps) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const text = reason.trim();
    if (text.length < 1 || text.length > 500) {
      setError("Say why the message is deleted, up to 500 characters.");
      return;
    }
    setError(null);
    setFailure(null);
    setBusy(true);
    try {
      onDone(await deleteProcessMessage(messageId, { reason: text }, key.current));
    } catch (failed) {
      const problem = describeProblem(failed);
      if (problem.fields.reason) setError(problem.fields.reason);
      else setFailure(problem);
      // Keep the key so a retry after a lost response replays the stored outcome.
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Delete message" headingLevel={3}>
      <form className={styles.form} onSubmit={submit} noValidate>
        <p className={`${styles.note} ${styles.formActions}`}>
          The message leaves the monitor and its remaining retries stop. Destinations already delivered are not undone, and the message and your reason
          are kept for audit.
        </p>
        <FormField id="delete-reason" label="Reason for deleting" hint="For example, a duplicate of another message or a sample that was lost." error={error ?? undefined}>
          {(control) => (
            <Textarea
              {...control}
              value={reason}
              maxLength={500}
              rows={3}
              invalid={Boolean(error)}
              onChange={(event) => {
                setReason(event.target.value);
                key.current = newKey();
              }}
            />
          )}
        </FormField>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not delete" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="danger" loading={busy}>
            Delete message
          </Button>
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      </form>
    </Card>
  );
}
