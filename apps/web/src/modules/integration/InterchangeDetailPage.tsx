import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, PageHeader, Skeleton } from "../../shell/ui";
import { getInterchange, type InterchangeDetail } from "./api/client";
import { formatDateTime } from "./format";
import { AckPill, MessagePill, OrderView } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** One interchange: envelope, each transaction with its canonical document, and the 997. */
export function InterchangeDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<InterchangeDetail>((signal) => getInterchange(id, signal), `interchange:${id}`);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Interchange" />
        <Alert tone="danger" title="Could not load the interchange" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Interchange" />
        <span className="visually-hidden">Loading the interchange…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const ic = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Interchange ${ic.controlNumber}`}
        description={`${ic.direction === "IN" ? "Received from" : "Sent to"} ${ic.partnerName} on ${formatDateTime(ic.receivedAt)}`}
        meta={<AckPill status={ic.ackStatus} />}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to={`/integration/partners/${ic.partnerId}`}>
              Partner
            </Link>
            <Link className={buttonClassName("ghost")} to="/integration/interchanges">
              All interchanges
            </Link>
          </>
        }
      />
      <Card title="Envelope">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Standard</dt>
            <dd className={styles.value}>{`${ic.standard} ${ic.version}`}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>ISA13 control number</dt>
            <dd className={`${styles.value} ${styles.mono}`}>{ic.controlNumber}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Transactions</dt>
            <dd className={styles.value}>{`${ic.messageCount} (${ic.failedCount} failed)`}</dd>
          </div>
        </dl>
      </Card>
      <ul className={styles.results} aria-label="Transactions">
        {ic.messages.map((m) => (
          <li key={m.id}>
            <Card title={`${m.transactionSet} · ST02 ${m.stControl}`} headingLevel={3} actions={<MessagePill status={m.status} />}>
              <div className={styles.stack}>
                {m.error ? (
                  <Alert tone={m.status === "FAILED" ? "danger" : "warning"} title="Mapping findings">
                    {m.error}
                  </Alert>
                ) : null}
                {m.canonical ? (
                  <OrderView order={m.canonical} label={`Lines of ${m.transactionSet} ${m.stControl}`} />
                ) : (
                  <p className={styles.note}>No canonical document: this transaction set is not mapped yet.</p>
                )}
              </div>
            </Card>
          </li>
        ))}
      </ul>
      <Card title="997 sent to the partner">
        {ic.ack997 ? (
          <pre className={styles.payload} aria-label="997 functional acknowledgment">
            {ic.ack997}
          </pre>
        ) : (
          <p className={styles.note}>No 997 was recorded for this interchange.</p>
        )}
      </Card>
      <p className={styles.note}>The raw interchange is not shown here: it carries partner address data that only the message monitor shows, masked.</p>
    </div>
  );
}
