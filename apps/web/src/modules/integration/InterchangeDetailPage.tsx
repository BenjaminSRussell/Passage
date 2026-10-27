import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, PageHeader, Skeleton } from "../../shell/ui";
import { getInterchange, type InterchangeDetail } from "./api/client";
import { formatDateTime } from "./format";
import { AckPill, CanonicalView, MessagePill } from "./parts";
import styles from "./pages.module.css";
import { ReprocessCard } from "./ReprocessCard";
import { useLoad } from "./useLoad";

/** One interchange: envelope, each transaction with its canonical document, and the 997 either way. */
export function InterchangeDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad<InterchangeDetail>((signal) => getInterchange(id, signal), `interchange:${id}`);
  // Keeps the reprocess outcome on screen after the reload clears the failures.
  const [reprocessed, setReprocessed] = useState(false);

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
      {ic.direction === "IN" && (reprocessed || ic.messages.some((m) => m.status === "FAILED" && m.transactionSet !== "997")) ? (
        <ReprocessCard
          interchangeId={ic.id}
          failedCount={ic.messages.filter((m) => m.status === "FAILED" && m.transactionSet !== "997").length}
          onDone={() => {
            setReprocessed(true);
            state.reload();
          }}
        />
      ) : null}
      <ul className={styles.results} aria-label="Transactions">
        {ic.messages.map((m) => (
          <li key={m.id}>
            <Card title={`${m.transactionSet} · ST02 ${m.stControl}`} headingLevel={3} actions={<MessagePill status={m.status} />}>
              <div className={styles.stack}>
                {m.error ? (
                  <Alert tone={m.status === "FAILED" ? "danger" : "warning"} title={ic.direction === "OUT" ? "Partner acknowledgment" : "Findings"}>
                    {m.error}
                  </Alert>
                ) : null}
                {m.canonical ? (
                  <CanonicalView transactionSet={m.transactionSet} document={m.canonical} label={`Lines of ${m.transactionSet} ${m.stControl}`} />
                ) : (
                  <p className={styles.note}>No canonical document: this transaction set is not mapped yet.</p>
                )}
              </div>
            </Card>
          </li>
        ))}
      </ul>
      {ic.direction === "OUT" ? (
        <Card title="Partner 997">
          <p className={styles.note}>
            {ic.ackStatus === "PENDING"
              ? "Waiting for the partner's 997. It is matched to this interchange by its control number when it is received."
              : "The partner's 997 has been matched; each transaction above shows its result. The 997 itself is listed as an inbound interchange."}
          </p>
        </Card>
      ) : (
        <Card title="997 sent to the partner">
          {ic.ack997 ? (
            <pre className={styles.payload} aria-label="997 functional acknowledgment">
              {ic.ack997}
            </pre>
          ) : (
            <p className={styles.note}>No 997 was sent for this interchange. A 997 received from the partner is never acknowledged.</p>
          )}
        </Card>
      )}
      <p className={styles.note}>The raw interchange is not shown here: it carries partner address data, and the masked raw viewer is not built yet.</p>
    </div>
  );
}
