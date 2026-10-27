import { Link, useParams } from "react-router-dom";
import { Alert, Badge, buttonClassName, Card, EmptyState, PageHeader, Skeleton, StatGroup, StatTile, type BadgeTone } from "../../shell/ui";
import { getReconciliation, type Reconciliation, type ReconciliationStatus } from "./api/client";
import { formatDateTime, signed } from "./format";
import { Detail, ProblemAlert } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const reconciliationStatusLabels: Record<ReconciliationStatus, string> = {
  MATCHED: "Matched",
  DIFFERENCE: "Difference",
};

export const reconciliationTones: Record<ReconciliationStatus, BadgeTone> = {
  MATCHED: "success",
  DIFFERENCE: "danger",
};

const sourceLabels: Record<Reconciliation["ledgerSource"], string> = {
  GL_JOURNALS: "GL of the posted inventory journals",
  LEDGER_OVERRIDE: "Balance read from the ledger",
};

/** One reconciliation run: both sides, the difference and the negative-stock flag. */
export function ReconciliationPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getReconciliation(id, signal), id);
  const back = (
    <Link className={buttonClassName("ghost")} to="/inventory/reconciliations">
      Back to reconciliation
    </Link>
  );

  if (state.error) {
    if (state.error.code === "INV-REC-001") {
      return <EmptyState icon="xCircle" headingLevel={2} title="Reconciliation run not found" description="It may belong to another legal entity, or the link is wrong." action={back} />;
    }
    return <ProblemAlert title="Could not load the reconciliation run" problem={state.error} onRetry={state.reload} />;
  }
  const run = state.data;
  if (!run) {
    return (
      <div className={styles.stack} aria-busy="true">
        <span className="visually-hidden">Loading reconciliation run…</span>
        <Skeleton width="1/2" height="control" />
        <Skeleton height="tile" />
      </div>
    );
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Reconciliation ${run.runDate}`}
        description={`Inventory account ${run.account} · ${run.currency}`}
        actions={
          <>
            <Badge tone={reconciliationTones[run.status]}>{reconciliationStatusLabels[run.status]}</Badge>
            {back}
          </>
        }
      />
      {run.status === "DIFFERENCE" ? (
        <Alert tone="danger" title="Inventory and ledger disagree">
          {`The inventory value is ${signed(run.diff)} ${run.currency} against the ledger. Look for ledger postings to the inventory account without an inventory transaction, or stock valued without a ledger entry.`}
        </Alert>
      ) : (
        <Alert tone="success" title="Inventory matches the ledger">
          The on-hand value equals the ledger balance of the inventory account.
        </Alert>
      )}
      {run.negativeRows > 0 ? (
        <Alert tone="warning" title="Negative stock">
          {`${run.negativeRows} on-hand ${run.negativeRows === 1 ? "row has" : "rows have"} negative physical stock. Check the on-hand inquiry.`}
        </Alert>
      ) : null}
      <StatGroup label="Reconciliation result">
        <StatTile label="Inventory value" value={`${run.inventoryValue} ${run.currency}`} hint="Sum of on-hand posted value" />
        <StatTile label="Ledger balance" value={`${run.ledgerBalance} ${run.currency}`} hint={sourceLabels[run.ledgerSource]} />
        <StatTile label="Difference" value={`${signed(run.diff)} ${run.currency}`} hint="Inventory value minus ledger balance" />
        <StatTile label="Negative on-hand rows" value={run.negativeRows} tone="warning" />
      </StatGroup>
      <Card title="Run">
        <dl className={styles.details}>
          <Detail term="Legal entity" mono>
            {run.legalEntityId}
          </Detail>
          <Detail term="Run date" mono>
            {run.runDate}
          </Detail>
          <Detail term="Ledger side">{sourceLabels[run.ledgerSource]}</Detail>
          <Detail term="Computed">{formatDateTime(run.createdAt)}</Detail>
        </dl>
      </Card>
    </div>
  );
}
