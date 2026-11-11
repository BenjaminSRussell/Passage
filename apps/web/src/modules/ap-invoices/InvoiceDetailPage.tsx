import { useQuery, useQueries } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { holdsDuty } from "../../shell/dutyGate";
import { EntityName } from "../../shell/lookup/EntityName";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { fetchReceipt, type Receipt } from "../receiving/api/client";
import { PurchaseFlowPanel } from "../receiving/documentFlow";
import { ApiError, fetchInvoice, type Invoice } from "./api/client";
import { invoiceStatusLabel, invoiceStatusTone, matchStatusLabel, matchStatusTone, priceDifferenceText, UUID } from "./parts";
import { PaymentCard, useOpenItemsForInvoices } from "./payment";

type InvoiceLine = Invoice["lines"][number];
type MatchLine = NonNullable<Invoice["match"]>["lines"][number];

function Detail({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

const when = (stamp?: string) => (stamp ? stamp.slice(0, 10) : "");
const shortRef = (id: string) => id.slice(0, 8);

const RECEIPT_DUTY = "receiving.receipt.maintain";
const ORDER_DUTY = "procurement.purchase-order.inquire";
const LEDGER_DUTY = "ledger.journal.read";

function Reference({ to, allowed, children }: { to: string; allowed: boolean; children: ReactNode }) {
  return allowed ? <Link to={to}>{children}</Link> : <span>{children}</span>;
}

function HoldNotice({ invoice }: { invoice: Invoice }) {
  if (invoice.status === "AUDIT_BLOCKED") {
    return (
      <Alert tone="warning" title="Held for audit">
        {invoice.auditReason ? `Reason: ${invoice.auditReason}. ` : ""}Nothing is posted to the ledger until an auditor releases it.
      </Alert>
    );
  }
  if (invoice.status === "AUDIT_REJECTED") {
    return (
      <Alert tone="danger" title="Rejected after audit">
        {invoice.rejectReason ? `${invoice.rejectReason}. ` : ""}
        {invoice.rejectedBy ? (
          <>
            Rejected by <EntityName entity="user" id={invoice.rejectedBy} fallback="a user who is no longer listed" /> on {when(invoice.rejectedAt)}.
          </>
        ) : null}
      </Alert>
    );
  }
  return invoice.releasedBy ? (
    <Alert tone="success" title="Released from audit hold">
      Released by <EntityName entity="user" id={invoice.releasedBy} fallback="a user who is no longer listed" /> on {when(invoice.releasedAt)}.
    </Alert>
  ) : null;
}

function MatchCard({ invoice }: { invoice: Invoice }) {
  const match = invoice.match;
  if (!match) return null;
  const quantityOf = new Map(invoice.lines.map((l) => [l.lineNo, l.qty]));
  const columns: DataTableColumn<MatchLine>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "receivedQty", header: "Received", align: "end", render: (l) => l.receivedQty ?? "None" },
    { key: "invoicedQty", header: "Invoiced", align: "end", render: (l) => quantityOf.get(l.lineNo) ?? "" },
    { key: "priceVariance", header: "Price difference", render: (l) => (l.currencyMismatch ? "Different currency from the receipt" : priceDifferenceText(l.priceVariance.amount, l.priceVariance.currency)) },
    { key: "status", header: "Result", render: (l) => <Badge tone={matchStatusTone[l.status]}>{matchStatusLabel[l.status]}</Badge> },
  ];
  return (
    <Card title="Match with the receipt">
      <p>
        <Badge tone={matchStatusTone[match.status]}>{matchStatusLabel[match.status]}</Badge>{" "}
        {match.withinTolerance ? "Every line is within the standard tolerances." : "At least one line is outside the standard tolerances."}
      </p>
      <DataTable label="Match per invoice line" columns={columns} rows={match.lines} getRowKey={(l) => String(l.lineNo)} />
      <p>Checked against the receipts today using the standard tolerances; it is not a saved decision.</p>
    </Card>
  );
}

export function InvoiceDetailPage() {
  const { id = "" } = useParams();
  const duties = usePrincipalDuties();
  const canReceipt = holdsDuty(duties, RECEIPT_DUTY);
  const canOrder = holdsDuty(duties, ORDER_DUTY);
  const canLedger = holdsDuty(duties, LEDGER_DUTY);
  const result = useQuery({ queryKey: ["ap-invoices", "invoice", id], queryFn: () => fetchInvoice(id) });
  const invoice = result.data;
  const payment = useOpenItemsForInvoices(invoice ? [invoice] : []);
  const receiptIds = [...new Set(invoice?.lines.map((l) => l.receiptId) ?? [])];
  const receipts = useQueries({ queries: receiptIds.map((receiptId) => ({ queryKey: ["receiving", "receipt", receiptId], queryFn: () => fetchReceipt(receiptId), enabled: canReceipt })) });
  const receiptById = new Map<string, Receipt>(receipts.flatMap((r) => (r.data ? [[r.data.id, r.data] as [string, Receipt]] : [])));
  const orderIds = [...new Set([...receiptById.values()].map((r) => r.purchaseOrderId))];

  const receiptLabel = (receiptId: string) => receiptById.get(receiptId)?.receiptNumber ?? `Receipt ${shortRef(receiptId)}`;
  const columns: DataTableColumn<InvoiceLine>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "receiptId", header: "Receipt", render: (l) => (
        <Reference to={`/receiving/receipts/${encodeURIComponent(l.receiptId)}`} allowed={canReceipt}>
          {receiptLabel(l.receiptId)}
        </Reference>
      ),
    },
    { key: "receiptLineNo", header: "Receipt line", align: "end" },
    { key: "qty", header: "Quantity", align: "end" },
    { key: "unitPrice", header: "Unit price", align: "end" },
    { key: "amount", header: "Amount", align: "end", render: (l) => `${l.amount} ${invoice?.currency ?? ""}` },
    { key: "standardCost", header: "Price difference goes to", render: (l) => (l.standardCost ? "Price variance" : "Inventory") },
  ];

  const missing = result.error instanceof ApiError && result.error.status === 404;
  const forbidden = result.error instanceof ApiError && result.error.status === 403;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={invoice ? `Invoice ${invoice.invoiceNumber}` : "Vendor invoice"}
        description="What was invoiced, which receipts it was matched to and where it landed in the ledger."
        meta={invoice ? <Badge tone={invoiceStatusTone[invoice.status]}>{invoiceStatusLabel[invoice.status]}</Badge> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/ap-invoices">
            Back to vendor invoices
          </Link>
        }
      />
      {invoice ? <PurchaseFlowPanel anchor={{ kind: "invoice", id: invoice.id }} refreshKey={invoice.status} /> : null}
      {result.isError ? (
        <Alert
          tone="danger"
          title={forbidden ? "You can't view vendor invoices" : missing ? "Invoice not found" : "Could not load the invoice"}
          live="alert"
          action={forbidden || missing ? undefined : <Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}
        >
          <ProblemMessage problem={result.error} />
        </Alert>
      ) : !invoice ? (
        <Card>
          <div aria-busy="true">
            <span className="visually-hidden">Loading the invoice…</span>
            <Skeleton height="control" />
            <Skeleton height="control" width="1/2" />
          </div>
        </Card>
      ) : (
        <>
          <HoldNotice invoice={invoice} />
          <Card title="Details">
            <dl className={styles.details}>
              <Detail term="Vendor">
                <EntityName entity="vendor" id={invoice.vendorId} fallback="Unknown vendor" />
              </Detail>
              <Detail term="Legal entity">
                <EntityName entity="legalEntity" id={invoice.legalEntityId} fallback="Unknown legal entity" />
              </Detail>
              <Detail term="Invoice date">{invoice.invoiceDate}</Detail>
              <Detail term="Posting date">{invoice.postingDate}</Detail>
              <Detail term="Total">{`${invoice.total} ${invoice.currency}`}</Detail>
              <Detail term="Entered">{invoice.source === "ERS" ? "Self-billed by us" : "By hand"}</Detail>
              <Detail term="Ledger entry">
                {invoice.ledgerEntryId ? (
                  <Reference to={`/ledger/entries/${encodeURIComponent(invoice.ledgerEntryId)}`} allowed={canLedger && UUID.test(invoice.ledgerEntryId)}>
                    {`Journal entry ${shortRef(invoice.ledgerEntryId)}`}
                  </Reference>
                ) : (
                  "None yet; it is created when the invoice is released"
                )}
              </Detail>
              {orderIds.map((orderId) => (
                <Detail key={orderId} term="Purchase order">
                  <Reference to={`/purchasing/purchase-orders/${encodeURIComponent(orderId)}`} allowed={canOrder}>
                    {canOrder ? "View the purchase order" : `Purchase order ${shortRef(orderId)}`}
                  </Reference>
                </Detail>
              ))}
            </dl>
          </Card>
          <DataTable label="Invoice lines" columns={columns} rows={invoice.lines} getRowKey={(l) => String(l.lineNo)} />
          <MatchCard invoice={invoice} />
          <PaymentCard invoice={invoice} lookup={payment} />
        </>
      )}
    </div>
  );
}
