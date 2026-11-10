// Procure-to-pay document flow (issue #502): purchase order -> receipt -> vendor invoice -> payment.
// Receipts and invoices come from the receiving service's related-documents query; the purchase order from
// purchasing and the payment state from payables. A stage the user may not read is shown as not available.
import { z } from "zod";
import { DocumentFlow, type FlowDocument, type FlowNextStep, type FlowStage } from "../../shell/documents/DocumentFlow";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { useFlow } from "../../shell/documents/useFlow";
import { defaultFetch, readProblem, type FetchLike } from "../../shell/api/http";
import { fetchOpenItemsForInvoices, type OpenItem } from "../../shell/api/openItems";
import { getPurchaseOrder, type PurchaseOrder } from "../purchasing/api/client";

const relatedSchema = z.object({
  purchaseOrderId: z.string().optional(),
  receipts: z.array(z.object({ id: z.string(), receiptNumber: z.string(), state: z.string(), receiptDate: z.string(), ledgerEntryId: z.string().optional() })),
  invoices: z.array(z.object({ id: z.string(), invoiceNumber: z.string(), status: z.string(), invoiceDate: z.string(), total: z.string(), currency: z.string(), ledgerEntryId: z.string().optional() })),
  truncated: z.boolean(),
});
export type RelatedDocuments = z.infer<typeof relatedSchema>;

export type PurchaseFlowAnchor = { kind: "po" | "receipt" | "invoice"; id: string };

/** GET /v1/receiving/related-documents: the receipts and vendor invoices of one purchase order. */
export async function fetchRelatedDocuments(anchor: PurchaseFlowAnchor, fetchImpl: FetchLike = defaultFetch): Promise<RelatedDocuments> {
  const param = { po: "purchaseOrderId", receipt: "receiptId", invoice: "invoiceId" }[anchor.kind];
  const res = await fetchImpl(`/api/v1/receiving/related-documents?${param}=${encodeURIComponent(anchor.id)}`, { method: "GET", headers: { Accept: "application/json" } });
  if (!res.ok) throw await readProblem(res);
  return relatedSchema.parse(await res.json());
}

export interface PurchaseFlow {
  related: RelatedDocuments;
  /** Null when the purchase order cannot be read or the documents name none. */
  order: PurchaseOrder | null;
  /** The payables open items of the invoices; null when the user may not read them. */
  openItems: OpenItem[] | null;
}

export async function loadPurchaseFlow(anchor: PurchaseFlowAnchor): Promise<PurchaseFlow> {
  const related = await fetchRelatedDocuments(anchor);
  const poId = anchor.kind === "po" ? anchor.id : related.purchaseOrderId;
  let order: PurchaseOrder | null = null;
  if (poId) {
    try {
      order = (await getPurchaseOrder(poId)).body;
    } catch {
      order = null;
    }
  }
  let openItems: OpenItem[] | null = [];
  if (related.invoices.length > 0) {
    const entity = order?.legalEntityId;
    try {
      openItems = entity ? await fetchOpenItemsForInvoices(entity, related.invoices.map((i) => i.id)) : null;
    } catch {
      openItems = null;
    }
  }
  return { related, order, openItems };
}

const PO_STATUS: Record<string, string> = { DRAFT: "Draft", IN_REVIEW: "In review", APPROVED: "Approved", REJECTED: "Rejected", CONFIRMED: "Confirmed", FINALIZED: "Finalized", CANCELLED: "Cancelled" };
const RECEIPT_STATE: Record<string, string> = { ARRIVED: "Arrived", REGISTERED: "Registered", POSTED: "Posted", CANCELLED: "Cancelled" };
const INVOICE_STATUS: Record<string, string> = { POSTED: "Posted", AUDIT_BLOCKED: "Held for audit", AUDIT_REJECTED: "Rejected after audit" };

const itemOf = (invoiceId: string, items: OpenItem[] | null) => items?.find((o) => o.sourceInvoiceId === invoiceId);

export function purchaseStages(flow: PurchaseFlow, anchor: PurchaseFlowAnchor): FlowStage[] {
  const { order, related } = flow;
  const orderDocs: FlowDocument[] = order ? [{ id: order.id, label: `Purchase order ${order.number}`, to: `/purchasing/purchase-orders/${order.id}`, status: PO_STATUS[order.approvalStatus] ?? order.approvalStatus, current: anchor.kind === "po" }] : [];
  const receiptDocs = related.receipts.map((r) => ({ id: r.id, label: `Receipt ${r.receiptNumber}`, to: `/receiving/receipts/${r.id}`, status: RECEIPT_STATE[r.state] ?? r.state, current: anchor.kind === "receipt" && anchor.id === r.id }));
  const invoiceDocs = related.invoices.map((i) => ({ id: i.id, label: `Invoice ${i.invoiceNumber}`, to: `/ap-invoices/${i.id}`, status: INVOICE_STATUS[i.status] ?? i.status, current: anchor.kind === "invoice" && anchor.id === i.id }));
  const paymentDocs: FlowDocument[] = flow.openItems
    ? related.invoices.flatMap((i) => {
        const item = itemOf(i.id, flow.openItems);
        if (!item) return [];
        const paid = item.status === "SETTLED" || Number(item.openAmount) === 0;
        const status = paid ? "Paid" : item.paymentHold ? "On hold" : item.proposalId ? "In a payment proposal" : "Due";
        return [{ id: `pay-${i.id}`, label: `Invoice ${i.invoiceNumber}`, to: item.proposalId ? `/payables/proposals/${item.proposalId}` : "/payables/open-items", status }];
      })
    : [];
  return [
    { key: "po", title: "Purchase order", documents: orderDocs, emptyText: "Not available" },
    { key: "receipt", title: "Receipt", documents: receiptDocs, emptyText: "Nothing received yet" },
    { key: "invoice", title: "Vendor invoice", documents: invoiceDocs, emptyText: "Not invoiced yet" },
    { key: "payment", title: "Payment", documents: paymentDocs, unavailable: flow.openItems === null, emptyText: "Nothing to pay yet" },
  ];
}

/** The next thing to do for the purchase order, with the link that does it; null when the flow is finished. */
export function purchaseNextStep(flow: PurchaseFlow): FlowNextStep | null {
  const { order, related } = flow;
  if (order && order.approvalStatus === "CANCELLED") return null;
  if (order && !["CONFIRMED", "FINALIZED"].includes(order.approvalStatus)) {
    return { title: "approve and confirm the purchase order", description: "The order is not confirmed yet, so the vendor has not been told to deliver.", to: `/purchasing/purchase-orders/${order.id}`, action: "Open the order" };
  }
  const open = related.receipts.find((r) => r.state === "ARRIVED" || r.state === "REGISTERED");
  if (open) {
    return open.state === "ARRIVED"
      ? { title: "register the goods received", description: `Receipt ${open.receiptNumber} has arrived at the dock. Register what was counted, then post it.`, to: `/receiving/receipts/${open.id}`, action: "Open the receipt" }
      : { title: "post the receipt", description: `Receipt ${open.receiptNumber} is registered. Posting books the goods to the ledger.`, to: `/receiving/receipts/${open.id}`, action: "Open the receipt" };
  }
  const posted = related.receipts.filter((r) => r.state === "POSTED");
  if (posted.length === 0) {
    const carry = order ? `?purchaseOrderId=${order.id}&vendorId=${order.vendorId}` : "";
    return { title: "receive the goods", description: "Record the delivery when it arrives at the dock. The purchase order is carried over.", to: `/receiving/receipts/new${carry}`, action: "Record arrival" };
  }
  if (related.invoices.length === 0) {
    return { title: "enter the vendor invoice", description: "The goods are received and posted. Enter the invoice so it can be matched to the receipt.", to: "/ap-invoices/post", action: "Enter vendor invoice" };
  }
  const due = related.invoices.map((i) => ({ i, item: itemOf(i.id, flow.openItems) })).find((x) => x.item && x.item.status !== "SETTLED" && Number(x.item.openAmount) > 0 && !x.item.proposalId);
  if (due) {
    return { title: "pay the vendor", description: `Invoice ${due.i.invoiceNumber} is posted and not yet in a payment.`, to: "/payables/proposals/new", action: "Create a payment proposal" };
  }
  return null;
}

/** The document flow panel and next-step offer on a purchase order, receipt or vendor invoice page. */
export function PurchaseFlowPanel(props: { anchor: PurchaseFlowAnchor; refreshKey?: string }) {
  return holdsDuty(usePrincipalDuties(), "receiving.document-flow.read") ? <PurchaseFlow {...props} /> : null;
}

function PurchaseFlow({ anchor, refreshKey = "" }: { anchor: PurchaseFlowAnchor; refreshKey?: string }) {
  const flow = useFlow(() => loadPurchaseFlow(anchor), `${anchor.kind}:${anchor.id}:${refreshKey}`);
  const data = flow.data;
  const label = `Document flow of this ${{ po: "purchase order", receipt: "receipt", invoice: "vendor invoice" }[anchor.kind]}`;
  return (
    <DocumentFlow
      label={label}
      loading={flow.loading && !data}
      error={flow.error ? "The related documents could not be read. The page itself is not affected." : undefined}
      onRetry={flow.reload}
      stages={data ? purchaseStages(data, anchor) : []}
      nextStep={data ? purchaseNextStep(data) : null}
      complete="This order has been received, invoiced and paid."
    />
  );
}
