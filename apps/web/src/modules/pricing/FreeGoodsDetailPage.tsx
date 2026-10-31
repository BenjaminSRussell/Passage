import { useRef, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, PageHeader, Skeleton } from "../../shell/ui";
import { describeProblem, getFreeGoodsRule, newKey, PricingApiError, withdrawFreeGoodsRule, type FreeGoodsRule, type Problem } from "./api/client";
import { CALC_LABELS, describeOffer, describeWho, FREE_GOODS_STATES, freeGoodsState, KIND_LABELS } from "./freeGoodsFormat";
import { today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

function Detail({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/** One free goods rule and its withdraw action (FR-26.22). */
export function FreeGoodsDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getFreeGoodsRule(id, signal), id);
  const [changed, setChanged] = useState<FreeGoodsRule | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);
  const commandKey = useRef(newKey());

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Free goods rule" />
        <Alert tone="danger" title="Could not load the rule" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Free goods rule" />
        <span className="visually-hidden">Loading the rule…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const rule = changed && changed.id === id && changed.version >= state.data.version ? changed : state.data;
  const status = FREE_GOODS_STATES[freeGoodsState(rule, today())];

  async function withdraw() {
    setFailure(null);
    setDone("");
    setBusy(true);
    try {
      setChanged(await withdrawFreeGoodsRule(rule.id, rule.version, commandKey.current));
      setDone("The rule is withdrawn. It leaves the next price resolution.");
      commandKey.current = newKey();
    } catch (error) {
      setFailure(describeProblem(error));
      // A refused command is final; only a network or server failure may be retried with the same key.
      if (!(error instanceof PricingApiError) || error.status < 500) commandKey.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${rule.productId}: ${describeOffer(rule)}`}
        description={`${rule.validFrom} to ${rule.validTo} · version ${rule.version}`}
        meta={<Badge tone={status.tone}>{status.label}</Badge>}
        actions={
          <Link className={buttonClassName("ghost")} to="/pricing/free-goods">
            All free goods rules
          </Link>
        }
      />
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="Could not withdraw the rule" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card title="Rule">
        <dl className={styles.details}>
          <Detail term="Customers">{describeWho(rule)}</Detail>
          <Detail term="Product" mono>
            {rule.productId}
          </Detail>
          <Detail term="Unit" mono>
            {rule.unit}
          </Detail>
          <Detail term="Variant">{KIND_LABELS[rule.kind]}</Detail>
          <Detail term="Calculation">{CALC_LABELS[rule.calcRule]}</Detail>
          <Detail term="Offer">{describeOffer(rule)}</Detail>
          <Detail term="Legal entity" mono>
            {rule.legalEntityId}
          </Detail>
          <Detail term="Rule id" mono>
            {rule.id}
          </Detail>
        </dl>
      </Card>
      <Card title="Withdraw">
        <div className={styles.stack}>
          <p className={styles.note}>
            {rule.withdrawn
              ? "This rule is withdrawn. Create a new rule to offer free goods again."
              : "Withdrawing the rule removes it from the next price resolution at once. A withdrawn rule cannot be restored; create a new one instead."}
          </p>
          {rule.withdrawn ? null : (
            <div className={styles.actions}>
              <Button variant="danger" loading={busy} onClick={withdraw}>
                Withdraw rule
              </Button>
            </div>
          )}
        </div>
      </Card>
    </div>
  );
}
