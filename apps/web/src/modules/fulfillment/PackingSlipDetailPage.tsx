import { useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { cancelPackingSlip, describeProblem, getPackingSlip, newKey, type JournalEntry, type PackingSlip, type Problem } from "./api/client";
import { SlipStatusBadge } from "./badges";
import { fixed, formatDate, formatMoney, shortId } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

type SlipLine = PackingSlip["lines"][number];

const lineColumns: DataTableColumn<SlipLine>[] = [
  { key: "orderLineId", header: "Order line", mono: true, render: (row) => shortId(row.orderLineId) },
  { key: "qty", header: "Quantity", align: "end" },
  { key: "costAmount", header: "Cost", align: "end", render: (row) => fixed(row.costAmount, 2) },
  { key: "costSnapshotId", header: "Cost snapshot", mono: true },
];

const journalColumns: DataTableColumn<JournalEntry>[] = [
  { key: "account", header: "Account", mono: true },
  { key: "orderLineId", header: "Order line", mono: true, render: (row) => shortId(row.orderLineId) },
  { key: "debit", header: "Debit", align: "end", render: (row) => (row.amount.startsWith("-") ? "" : formatMoney(row.amount, row.currency)) },
  { key: "credit", header: "Credit", align: "end", render: (row) => (row.amount.startsWith("-") ? formatMoney(row.amount.slice(1), row.currency) : "") },
];

/** A packing slip with its lines and journal; a posted, uninvoiced slip can be cancelled (FR-28.5). */
export function PackingSlipDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getPackingSlip(id, signal), id);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const key = useRef(newKey());

  async function cancel() {
    setBusy(true);
    setFailure(null);
    try {
      const reversal = await cancelPackingSlip(id, key.current);
      navigate(`/fulfillment/packing-slips/${reversal.id}`);
    } catch (error) {
      setFailure(describeProblem(error));
      key.current = newKey();
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Packing slip" />
        <Alert tone="danger" title="Could not load the packing slip" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Packing slip" />
        <span className="visually-hidden">Loading the packing slip…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const slip = state.data;
  const reversal = slip.reversalOfId !== null;
  const cancellable = slip.status === "POSTED" && !reversal;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Packing slip ${slip.packingSlipNumber}`}
        description={reversal ? "Reversal of a cancelled packing slip: quantities and amounts mirror the original." : `Posted ${formatDate(slip.postingDate)}`}
        meta={<SlipStatusBadge status={slip.status} reversal={reversal} />}
        actions={
          <>
            {cancellable && !confirming ? (
              <Button variant="danger" onClick={() => setConfirming(true)}>
                Cancel packing slip
              </Button>
            ) : null}
            <Link className={buttonClassName("secondary")} to={`/fulfillment/shipments/${slip.shipmentId}`}>
              Open shipment
            </Link>
          </>
        }
      />
      {confirming ? (
        <Alert
          tone="warning"
          title="Cancel this packing slip?"
          action={
            <span className={styles.actions}>
              <Button variant="danger" size="sm" loading={busy} onClick={cancel}>
                Yes, cancel it
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setConfirming(false)}>
                Keep it
              </Button>
            </span>
          }
        >
          A reversing slip posts the mirror journal and returns the shipment to Packed. An invoiced slip cannot be cancelled.
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not cancel the packing slip" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <Card>
        <dl className={styles.details}>
          {[
            ["Order", slip.orderId],
            ["Shipment", slip.shipmentId],
            ["Posting date", formatDate(slip.postingDate)],
            ["Total cost", formatMoney(slip.totalCost, slip.currency)],
            ...(reversal ? [["Reverses", slip.reversalOfId ?? ""]] : []),
          ].map(([term, value]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={`${styles.value} ${styles.mono}`}>
                {term === "Reverses" ? <Link to={`/fulfillment/packing-slips/${value}`}>{value}</Link> : value}
              </dd>
            </div>
          ))}
        </dl>
      </Card>
      <DataTable label="Packing slip lines" columns={lineColumns} rows={slip.lines} getRowKey={(row) => row.shipmentLineId} />
      <Card title="Journal">
        <DataTable
          label="Journal"
          columns={journalColumns}
          rows={slip.journal}
          getRowKey={(row, i) => `${row.account}-${row.orderLineId}-${i}`}
          empty={{ title: "No journal lines", description: "Drop-ship and non-stocked lines post nothing at shipment." }}
        />
      </Card>
    </div>
  );
}
