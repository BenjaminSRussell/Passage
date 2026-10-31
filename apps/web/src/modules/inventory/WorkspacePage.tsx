import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Button, buttonClassName, Card, EmptyState, FormField, Input, PageHeader, Skeleton, StatGroup, StatTile } from "../../shell/ui";
import { getWorkspaceSummary, TRANSFER_STATUSES, type WorkspaceSummary } from "./api/client";
import { formatDateTime, sentence, UUID } from "./format";
import { ProblemAlert } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** True when no tile in "Needs attention" carries work. */
function nothingNeedsAttention(data: WorkspaceSummary): boolean {
  return data.journals.awaitingApproval + data.negativeOnHandRows + data.reconciliationDifferences + data.countSheetsCounted === 0;
}

/** Inventory management workspace: tiles for the open work, each linking to the page that clears it. */
export function WorkspacePage() {
  const [draft, setDraft] = useState("");
  const [entity, setEntity] = useState("");
  const [error, setError] = useState<string | undefined>();
  const load = useLoad((signal) => getWorkspaceSummary(entity, signal), entity);
  const data = load.data;

  function apply(event: FormEvent) {
    event.preventDefault();
    const value = draft.trim();
    if (value !== "" && !UUID.test(value)) {
      setError("Enter the legal entity id (a UUID).");
      return;
    }
    setError(undefined);
    setEntity(value);
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Inventory workspace" description="Open work across journals, counts, stock, reconciliation and transfers." />
      <Card title="Scope">
        <form onSubmit={apply} noValidate className={styles.form}>
          <FormField id="workspace-entity" label="Legal entity" optional error={error}>
            {(control) => <Input {...control} value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="All legal entities" invalid={Boolean(error)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply
            </Button>
          </div>
        </form>
      </Card>
      {load.error ? (
        <ProblemAlert title="Could not load the workspace" problem={load.error} onRetry={load.reload} />
      ) : load.loading ? (
        <div role="status" aria-label="Loading the workspace"><Skeleton height="tile" /></div>
      ) : data ? (
        <>
          <Card title="Needs attention">
            <StatGroup label="Needs attention">
              <StatTile label="Journals awaiting approval" value={data.journals.awaitingApproval} tone="warning" />
              <StatTile label="Negative on-hand rows" value={data.negativeOnHandRows} tone="danger" />
              <StatTile
                label="Reconciliation differences"
                value={data.reconciliationDifferences}
                tone="danger"
                hint={data.lastReconciliationAt ? `Last run ${formatDateTime(data.lastReconciliationAt)}` : "No reconciliation has run"}
              />
              <StatTile label="Count sheets counted" value={data.countSheetsCounted} tone="warning" hint="Waiting for approval or posting" />
            </StatGroup>
            {nothingNeedsAttention(data) ? <EmptyState compact title="Nothing needs attention" description="No journal waits for approval, no stock is negative, the latest reconciliation runs match and no counted sheet waits." /> : null}
            <div className={styles.formActions}>
              <Link className={buttonClassName("ghost")} to="/inventory/journals">
                Journals
              </Link>
              <Link className={buttonClassName("ghost")} to="/inventory/reports/negative-onhand">
                Negative on-hand
              </Link>
              <Link className={buttonClassName("ghost")} to="/inventory/reconciliations">
                Reconciliation
              </Link>
              <Link className={buttonClassName("ghost")} to="/inventory/count-sheets">
                Count sheets
              </Link>
            </div>
          </Card>
          <Card title="Open work">
            <StatGroup label="Open work">
              <StatTile label="Draft journals" value={data.journals.draft} />
              <StatTile label="Open count sheets" value={data.openCountSheets} />
              <StatTile label="Active reservations" value={data.activeReservations} />
            </StatGroup>
            <div className={styles.formActions}>
              <Link className={buttonClassName("ghost")} to="/inventory/reservations">
                Reservations
              </Link>
            </div>
          </Card>
          <Card title="Transfer orders by status">
            <StatGroup label="Transfer orders by status">
              {TRANSFER_STATUSES.map((status) => (
                <StatTile key={status} label={sentence(status)} value={data.transferOrdersByStatus[status] ?? 0} />
              ))}
            </StatGroup>
            <div className={styles.formActions}>
              <Link className={buttonClassName("ghost")} to="/inventory/transfer-orders">
                Transfer orders
              </Link>
            </div>
          </Card>
        </>
      ) : null}
    </div>
  );
}
