import { useQuery, useQueries } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { fetchReceipt, type Receipt } from "../receiving/api/client";
import { ApiError, fetchInvoice, type Invoice } from "./api/client";
import { invoiceStatusLabel, invoiceStatusTone } from "./parts";

type InvoiceLine = Invoice["lines"][number];

function Detail({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

const when = (stamp?: string) => (stamp ? stamp.slice(0, 10) : "");

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
        {invoice.rejectedBy ? `Rejected by ${invoice.rejectedBy} on ${when(invoice.rejectedAt)}.` : ""}
      </Alert>
    );
  }
  return invoice.releasedBy ? (
    <Alert tone="success" title="Released from audit hold">
      Released by {invoice.releasedBy} on {when(invoice.releasedAt)}.
    </Alert>
  ) : null;
}

export function InvoiceDetailPage() {
  const { id = "" } = useParams();
  const result = useQuery({ queryKey: ["ap-invoices", "invoice", id], queryFn: () => fetchInvoice(id) });
  const invoice = result.data;
  const receiptIds = [...new Set(invoice?.lines.map((l) => l.receiptId) ?? [])];
  const receipts = useQueries({ queries: receiptIds.map((receiptId) => ({ queryKey: ["receiving", "receipt", receiptId], queryFn: () => fetchReceipt(receiptId) })) });
  const receiptById = new Map<string, Receipt>(receipts.flatMap((r) => (r.data ? [[r.data.id, r.data] as [string, Receipt]] : [])));
  const orderIds = [...new Set([...receiptById.values()].map((r) => r.purchaseOrderId))];

  const receiptLabel = (receiptId: string) => receiptById.get(receiptId)?.receiptNumber ?? "Receipt";
  const columns: DataTableColumn<InvoiceLine>[] = [
    { key: "lineNo", header: "Line", align: "end" },
    { key: "receiptId", header: "Receipt", render: (l) => <Link to={`/receiving/receipts/${encodeURIComponent(l.receiptId)}`}>{receiptLabel(l.receiptId)}</Link> },
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
              <Detail term="Ledger entry">{invoice.ledgerEntryId || "None yet; it is created when the invoice is released"}</Detail>
              {orderIds.map((orderId) => (
                <Detail key={orderId} term="Purchase order">
                  <Link to={`/purchasing/purchase-orders/${encodeURIComponent(orderId)}`}>View the purchase order</Link>
                </Detail>
              ))}
            </dl>
          </Card>
          <DataTable label="Invoice lines" columns={columns} rows={invoice.lines} getRowKey={(l) => String(l.lineNo)} />
        </>
      )}
    </div>
  );
}
