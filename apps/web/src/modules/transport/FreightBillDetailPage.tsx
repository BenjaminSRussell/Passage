import { useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, PageHeader, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import { decideFreightBill, describeProblem, getFreightBill, newKey, type Decision, type DecisionAction, type FreightBillDetail, type Line, type MatchLine, type Problem } from "./api/client";
import { dash, fixed, formatDateTime, matchTone, money, statusLabel } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const expectedColumns = (currency: string): DataTableColumn<Line>[] => [
  { key: "chargeCode", header: "Charge", mono: true },
  { key: "amount", header: "Expected", align: "end", render: (row) => money(row.amount, currency) },
];

/** The reconciliation grid: expected vs invoiced per charge, lines outside tolerance highlighted (PRD 6.34.6). */
const matchColumns: DataTableColumn<MatchLine>[] = [
  { key: "chargeCode", header: "Charge", mono: true },
  { key: "expected", header: "Expected", align: "end", render: (row) => fixed(row.expected, 2) },
  { key: "invoiced", header: "Invoiced", align: "end", render: (row) => fixed(row.invoiced, 2) },
  {
    key: "variance",
    header: "Variance",
    align: "end",
    render: (row) => <span className={row.within ? undefined : styles.outside}>{fixed(row.variance, 2)}</span>,
  },
  { key: "allowed", header: "Allowed", align: "end", render: (row) => fixed(row.allowed, 2) },
  {
    key: "within",
    header: "Tolerance",
    render: (row) => <Badge tone={row.within ? "success" : "warning"}>{row.within ? "Within" : "Outside"}</Badge>,
  },
];

const decisionColumns: DataTableColumn<Decision>[] = [
  { key: "decidedAt", header: "When", render: (row) => formatDateTime(row.decidedAt) },
  { key: "action", header: "Decision", render: (row) => statusLabel(row.action) },
  {
    key: "change",
    header: "State",
    render: (row) => `${statusLabel(row.fromStatus)} → ${statusLabel(row.toStatus)}`,
  },
  { key: "reason", header: "Reason", render: (row) => row.reason || dash },
  { key: "decidedBy", header: "By" },
];

/**
 * Approve or dispute a matched bill (BR-34.4). MATCHED approves as is; a
 * VARIANCE can be disputed with the carrier or approved with a reason; a
 * DISPUTED bill is closed by approving it with a reason. Approval releases the
 * bill to payables.
 */
function DecisionCard({ bill, onDone }: { bill: FreightBillDetail; onDone(): void }) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState<DecisionAction | null>(null);
  const key = useRef(newKey());
  const needsReason = bill.matchStatus !== "MATCHED";

  async function decide(action: DecisionAction) {
    const text = reason.trim();
    setFailure(null);
    if ((action === "DISPUTE" || needsReason) && text === "") {
      setError(action === "DISPUTE" ? "Tell the carrier what is disputed." : "Give the reason for approving a bill with a variance or dispute.");
      return;
    }
    if (text.length > 500) {
      setError("Keep the reason to 500 characters.");
      return;
    }
    setError(undefined);
    setBusy(action);
    try {
      await decideFreightBill(bill.id, action, bill.version, text, key.current);
      key.current = newKey();
      onDone();
    } catch (e) {
      const problem = describeProblem(e);
      if (problem.fields.reason) setError(problem.fields.reason);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card title="Decision" headingLevel={3}>
      <div className={styles.stack}>
        <p className={styles.note}>
          {bill.matchStatus === "MATCHED"
            ? "Every charge is within tolerance. Approving releases the invoiced amount to payables and reverses the accrual."
            : bill.matchStatus === "VARIANCE"
              ? "Dispute the variance with the carrier, or approve the invoiced amount with a reason. Payables gets nothing until the bill is approved."
              : "The variance is in dispute. Approve the bill with the outcome once the carrier has answered."}
        </p>
        <FormField id="decision-reason" label="Reason" hint={needsReason ? "Required; at most 500 characters." : "Optional for a matched bill."} error={error}>
          {(control) => <Textarea {...control} rows={3} value={reason} onChange={(e) => { setReason(e.target.value); key.current = newKey(); }} invalid={Boolean(error)} />}
        </FormField>
        <div className={styles.actions}>
          <Button variant="primary" loading={busy === "APPROVE"} onClick={() => decide("APPROVE")}>
            Approve for payment
          </Button>
          {bill.matchStatus === "VARIANCE" ? (
            <Button loading={busy === "DISPUTE"} onClick={() => decide("DISPUTE")}>
              Dispute with carrier
            </Button>
          ) : null}
        </div>
        {failure ? (
          <Alert
            tone="danger"
            title={failure.code === "conflict_version" ? "The bill changed since you opened it" : "The decision was not saved"}
            live="alert"
            action={failure.code === "conflict_version" ? <Button size="sm" onClick={onDone}>Reload</Button> : undefined}
          >
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
      </div>
    </Card>
  );
}

/** One freight bill with its matched carrier invoice (BR-34.4). */
export function FreightBillDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getFreightBill(id, signal), `freight-bill:${id}`);
  const bill = state.data;

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Freight bill" />
        <Alert tone="danger" title="Could not load the freight bill" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
        <Link to="/transport/freight-bills">Back to freight bills</Link>
      </div>
    );
  }
  if (!bill) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Freight bill" />
        <Card>
          <p className={styles.note}>Loading the freight bill…</p>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }

  const entries: [string, string, boolean?][] = [
    ["Freight bill", bill.id, true],
    ["Load", bill.loadId, true],
    ["Carrier", bill.carrierId, true],
    ["Expected total", money(bill.expectedTotal, bill.currency)],
    ["Variance", money(bill.varianceTotal, bill.currency)],
    ["Created", formatDateTime(bill.createdAt)],
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Freight bill ${bill.id.slice(0, 8)}`}
        meta={<Badge tone={matchTone(bill.matchStatus)}>{statusLabel(bill.matchStatus)}</Badge>}
        actions={
          bill.carrierInvoiceId ? null : (
            <Link className={buttonClassName("primary")} to={`/transport/carrier-invoices/new?freightBillId=${bill.id}&carrierId=${bill.carrierId}&currency=${bill.currency}`}>
              Register carrier invoice
            </Link>
          )
        }
      />
      <Card>
        <dl className={styles.details}>
          {entries.map(([term, value, mono]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={`${styles.value} ${mono ? styles.mono : ""}`}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      {bill.match && bill.carrierInvoice ? (
        <Card title={`Matched to invoice ${bill.carrierInvoice.invoiceNo}`}>
          <div className={styles.stack}>
            <p className={styles.note}>
              {bill.match.status === "MATCHED"
                ? "Every charge is within tolerance."
                : "At least one charge is outside the tolerance of $5.00 or 2 % of the expected amount."}
            </p>
            <DataTable label="Expected and invoiced charges" columns={matchColumns} rows={bill.match.lines} getRowKey={(row) => row.chargeCode} />
          </div>
        </Card>
      ) : (
        <Card title="Expected charges">
          <div className={styles.stack}>
            <DataTable label="Expected charges" columns={expectedColumns(bill.currency)} rows={bill.expectedLines} getRowKey={(row, i) => `${row.chargeCode}-${i}`} />
            <EmptyState compact headingLevel={3} title="No carrier invoice yet" description="Register the carrier's invoice to match it against these charges." />
          </div>
        </Card>
      )}
      {bill.matchStatus === "MATCHED" || bill.matchStatus === "VARIANCE" || bill.matchStatus === "DISPUTED" ? (
        <DecisionCard key={bill.version} bill={bill} onDone={state.reload} />
      ) : null}
      {bill.decisions && bill.decisions.length > 0 ? (
        <Card title="Decisions" headingLevel={3}>
          <DataTable label="Approvals and disputes" columns={decisionColumns} rows={bill.decisions} getRowKey={(row) => row.id} />
        </Card>
      ) : null}
      <Link to="/transport/freight-bills">Back to freight bills</Link>
    </div>
  );
}
