import { useRef, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Skeleton, StatusPill } from "../../shell/ui";
import {
  activateConditionRecord,
  describeProblem,
  getConditionRecord,
  newKey,
  PricingApiError,
  withdrawConditionRecord,
  type ConditionRecord,
  type Problem,
} from "./api/client";
import { amountLabel, describeCustomer, describePercent, describeProduct, TYPE_LABELS } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** One trade agreement with its lifecycle actions: activate a draft, withdraw an active record (BR-26.9). */
export function AgreementDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getConditionRecord(id, signal), id);
  const [record, setRecord] = useState<ConditionRecord | null>(null);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState<"activate" | "withdraw" | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const key = useRef(newKey());
  // A record returned by an action belongs to this id only; another id shows its own load.
  const current = record && record.id === id ? record : state.data;

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Trade agreement" />
        <Alert tone="danger" title="Could not load the agreement" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Trade agreement" />
        <span className="visually-hidden">Loading the agreement…</span>
        <Skeleton height="tile" />
      </div>
    );
  }

  async function act(action: "activate" | "withdraw", r: ConditionRecord) {
    setBusy(action);
    setFailure(null);
    setDone("");
    try {
      const next = action === "activate" ? await activateConditionRecord(r.id, r.version, key.current) : await withdrawConditionRecord(r.id, r.version, reason.trim(), key.current);
      setRecord(next);
      setDone(action === "activate" ? "The agreement is active and prices documents from its first valid day." : "The agreement is withdrawn and no longer prices documents.");
      key.current = newKey();
    } catch (error) {
      setFailure(describeProblem(error));
      // A refused command is final; only a network or server failure may be retried with the same key.
      if (error instanceof PricingApiError && error.status < 500) key.current = newKey();
    } finally {
      setBusy(null);
    }
  }

  const percent = describePercent(current);
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${TYPE_LABELS[current.type]}: ${describeProduct(current)}`}
        description={`${describeCustomer(current)} · ${current.currency} · version ${current.version}`}
        meta={<StatusPill status={current.status.toLowerCase()} />}
        actions={
          <div className={styles.actions}>
            <Link className={buttonClassName("secondary")} to={`/pricing/agreements/new?copy=${current.id}`}>
              Copy
            </Link>
            <Link className={buttonClassName("ghost")} to="/pricing/agreements">
              All agreements
            </Link>
          </div>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failure.code === "PRC-0002" ? "Another active agreement overlaps this one" : "The action failed"} live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Card title="Agreement">
        <dl className={styles.details}>
          <Detail term="Customer">{describeCustomer(current)}</Detail>
          <Detail term="Product" mono>
            {describeProduct(current)}
          </Detail>
          <Detail term="Currency" mono>
            {current.currency}
          </Detail>
          <Detail term="Unit" mono>
            {current.unit || "Any unit"}
          </Detail>
          <Detail term="From quantity" mono>
            {current.fromQuantity}
          </Detail>
          <Detail term={amountLabel(current.type)} mono>
            {current.amount}
          </Detail>
          <Detail term="Percentages" mono>
            {percent || "None"}
          </Detail>
          <Detail term="Valid" mono>
            {`${current.validFrom} – ${current.validTo}`}
          </Detail>
          <Detail term="Search next">{current.searchNext ? "Yes" : "No"}</Detail>
          <Detail term="Exclusive">{current.exclusive ? "Yes" : "No"}</Detail>
          <Detail term="Campaign" mono>
            {current.campaignId ? <Link to={`/pricing/campaigns/${current.campaignId}`}>{current.campaignId}</Link> : "None"}
          </Detail>
          <Detail term="Legal entity" mono>
            {current.legalEntityId}
          </Detail>
          <Detail term="Record id" mono>
            {current.id}
          </Detail>
        </dl>
      </Card>
      {current.status === "DRAFT" ? (
        <Card title="Activate">
          <div className={styles.stack}>
            <p className={styles.note}>Activation is refused when an active agreement with the same key, currency, unit and from quantity overlaps these dates.</p>
            <div className={styles.actions}>
              <Button variant="primary" loading={busy === "activate"} onClick={() => act("activate", current)}>
                Activate
              </Button>
            </div>
          </div>
        </Card>
      ) : null}
      {current.status === "ACTIVE" ? (
        <Card title="Withdraw">
          <div className={styles.toolbar}>
            <FormField id="ta-withdraw-reason" label="Reason" optional hint="Sent with the withdrawal event.">
              {(c) => <Input {...c} value={reason} maxLength={200} onChange={(e) => setReason(e.target.value)} />}
            </FormField>
            <Button variant="danger" loading={busy === "withdraw"} onClick={() => act("withdraw", current)}>
              Withdraw
            </Button>
          </div>
        </Card>
      ) : null}
    </div>
  );
}
