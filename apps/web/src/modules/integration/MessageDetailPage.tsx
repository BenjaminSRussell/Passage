import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, PageHeader, Skeleton } from "../../shell/ui";
import { getMessage, type MonitorMessageDetail } from "./api/client";
import { formatDateTime } from "./format";
import { AckPill, CanonicalView, MessagePill } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function Entry({ term, value, mono }: { term: string; value: string; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
    </div>
  );
}

/** One monitored message: envelope, failing rule and canonical document (FR-35.10). */
export function MessageDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<MonitorMessageDetail>((signal) => getMessage(id, signal), `message:${id}`);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Message" />
        <Alert tone="danger" title="Could not load the message" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Message" />
        <span className="visually-hidden">Loading the message…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const m = state.data;
  const reprocessable = m.direction === "IN" && m.status === "FAILED" && m.transactionSet !== "997";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${m.transactionSet} · ST02 ${m.stControl}`}
        description={`${m.direction === "IN" ? "Received from" : "Sent to"} ${m.partnerName} on ${formatDateTime(m.createdAt)}`}
        meta={<MessagePill status={m.status} />}
        actions={
          <>
            <Link className={buttonClassName(reprocessable ? "primary" : "secondary")} to={`/integration/interchanges/${m.interchangeId}`}>
              {reprocessable ? "Reprocess from interchange" : "Interchange"}
            </Link>
            <Link className={buttonClassName("ghost")} to="/integration/messages">
              Message monitor
            </Link>
          </>
        }
      />
      {m.error ? (
        <Alert tone={m.status === "FAILED" ? "danger" : "warning"} title={m.status === "FAILED" ? "Failing rule" : "Findings"}>
          {m.error}
        </Alert>
      ) : null}
      <Card title="Envelope">
        <dl className={styles.details}>
          <Entry term="Partner" value={m.partnerName} />
          <Entry term="Direction" value={m.direction === "IN" ? "Inbound" : "Outbound"} />
          <Entry term="ISA13 control number" value={m.controlNumber} mono />
          <Entry term="Standard" value={m.standard} />
          <div className={styles.entry}>
            <dt className={styles.term}>Interchange 997</dt>
            <dd className={styles.value}>
              <AckPill status={m.ackStatus} />
            </dd>
          </div>
        </dl>
      </Card>
      <Card title="Canonical document" actions={m.piiMasked ? <Badge tone="neutral">Personal data masked</Badge> : null}>
        {m.canonical ? (
          <CanonicalView transactionSet={m.transactionSet} document={m.canonical} label={`Lines of ${m.transactionSet} ${m.stControl}`} />
        ) : (
          <p className={styles.note}>No canonical document: the transaction failed before it could be mapped, or its set is not mapped yet.</p>
        )}
      </Card>
      <p className={styles.note}>
        {m.piiMasked
          ? "Party names are shown as *** because you do not hold EDI_PII_VIEW. The raw payload, masked the same way, is on the interchange."
          : "The raw payload is on the interchange."}
      </p>
    </div>
  );
}
