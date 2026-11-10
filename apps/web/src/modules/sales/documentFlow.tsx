// Order-to-cash document flow (issue #534): sales order -> packing slip -> customer invoice -> payment.
// The flow is read from the services that own each document (fulfillment, billing, receivables); a
// stage the user may not read is shown as not available instead of failing the panel.
import { useFlow } from "../../shell/documents/useFlow";
import { DocumentFlow, type FlowDocument, type FlowNextStep, type FlowStage } from "../../shell/documents/DocumentFlow";
import { getInvoice, listInvoices, type InvoiceSummary } from "../billing/api/client";
import { STATUS_LABELS as INVOICE_STATUS_LABELS } from "../billing/format";
import { getPackingSlip, listPackingSlips, type PackingSlipSummary } from "../fulfillment/api/client";
import { getPayment, listOpenItems, type OpenItem } from "../receivables/api/client";
import { getOrder, type Order } from "./api/client";

export type OrderFlowAnchor = { kind: "order" | "slip" | "invoice" | "payment"; id: string };

export interface OrderFlow {
  order?: Order;
  slips: PackingSlipSummary[] | null;
  invoices: InvoiceSummary[] | null;
  /** The receivables open items of the order's customer, by document number. */
  openItems: OpenItem[] | null;
}

const ORDER_STATUS_LABELS: Record<string, string> = {
  DRAFT: "Draft",
  OPEN: "Open",
  CONFIRMED: "Confirmed",
  PARTIALLY_SHIPPED: "Partly shipped",
  SHIPPED: "Shipped",
  INVOICED: "Invoiced",
  CLOSED: "Closed",
  CANCELLED: "Cancelled",
};
const SLIP_STATUS_LABELS: Record<string, string> = { PENDING: "Pending", POSTED: "Posted", CANCELLED: "Cancelled", INVOICED: "Invoiced" };

const settle = async <T,>(p: Promise<T>): Promise<T | null> => {
  try {
    return await p;
  } catch {
    return null;
  }
};

/** The sales order an anchor document belongs to; undefined when it has none (a free-text invoice). */
async function orderIdOf(anchor: OrderFlowAnchor): Promise<string | undefined> {
  switch (anchor.kind) {
    case "order":
      return anchor.id;
    case "slip":
      return (await getPackingSlip(anchor.id)).orderId;
    case "invoice": {
      const invoice = await getInvoice(anchor.id);
      return invoice.lines.find((l) => l.orderId)?.orderId;
    }
    case "payment": {
      const payment = await getPayment(anchor.id);
      if (!payment.customerId) return undefined;
      const references = new Set(payment.remittance.map((r) => r.reference.trim()));
      const invoices = await listInvoices({ customerId: payment.customerId, limit: 200 });
      const paid = invoices.find((i) => i.number && references.has(i.number));
      return paid ? (await getInvoice(paid.id)).lines.find((l) => l.orderId)?.orderId : undefined;
    }
  }
}

export async function loadOrderFlow(anchor: OrderFlowAnchor): Promise<OrderFlow> {
  const orderId = await orderIdOf(anchor);
  if (!orderId) return { slips: [], invoices: [], openItems: [] };
  const { value: order } = await getOrder(orderId);
  const [slips, invoices, openItems] = await Promise.all([
    settle(listPackingSlips({ orderId, limit: 100 })),
    settle(listInvoices({ orderId, limit: 100 })),
    settle(listOpenItems({ customer: order.customerId, status: "all", limit: 500 })),
  ]);
  return { order, slips, invoices, openItems };
}

const paidState = (invoice: InvoiceSummary, openItems: OpenItem[] | null): { label: string; open: boolean } | null => {
  if (!invoice.number || invoice.status === "PROPOSAL" || openItems === null) return null;
  const item = openItems.find((o) => o.documentNumber === invoice.number);
  if (!item) return { label: "Settled", open: false };
  return Number(item.openAmount) > 0 ? { label: `${item.openAmount} ${item.currency} open`, open: true } : { label: "Settled", open: false };
};

export function orderStages(flow: OrderFlow, anchor: OrderFlowAnchor): FlowStage[] {
  const { order } = flow;
  const orderDocs: FlowDocument[] = order ? [{ id: order.id, label: `Sales order ${order.orderNumber}`, to: `/sales/orders/${order.id}`, status: ORDER_STATUS_LABELS[order.status] ?? order.status, current: anchor.kind === "order" }] : [];
  const slipDocs = (flow.slips ?? []).map((s) => ({
    id: s.id,
    label: `Packing slip ${s.packingSlipNumber}`,
    to: `/fulfillment/packing-slips/${s.id}`,
    status: s.reversalOfId ? "Reversal" : (SLIP_STATUS_LABELS[s.status] ?? s.status),
    current: anchor.kind === "slip" && anchor.id === s.id,
  }));
  const invoiceDocs = (flow.invoices ?? []).map((i) => ({
    id: i.id,
    label: i.number ? `Invoice ${i.number}` : "Invoice proposal",
    to: `/billing/invoices/${i.id}`,
    status: INVOICE_STATUS_LABELS[i.status],
    current: anchor.kind === "invoice" && anchor.id === i.id,
  }));
  const paymentDocs: FlowDocument[] = (flow.invoices ?? []).flatMap((i) => {
    const state = paidState(i, flow.openItems);
    return state ? [{ id: `pay-${i.id}`, label: `Invoice ${i.number}`, to: `/billing/invoices/${i.id}`, status: state.label }] : [];
  });
  if (anchor.kind === "payment") paymentDocs.unshift({ id: anchor.id, label: "This payment", current: true });
  return [
    { key: "order", title: "Sales order", documents: orderDocs },
    { key: "slip", title: "Packing slip", documents: slipDocs, unavailable: flow.slips === null, emptyText: "Not shipped yet" },
    { key: "invoice", title: "Customer invoice", documents: invoiceDocs, unavailable: flow.invoices === null, emptyText: "Not invoiced yet" },
    { key: "payment", title: "Payment", documents: paymentDocs, unavailable: flow.openItems === null && anchor.kind !== "payment", emptyText: "Nothing to collect yet" },
  ];
}

/** The next thing to do for the order, with the link that does it; null when the flow is finished. */
export function orderNextStep(flow: OrderFlow): FlowNextStep | null {
  const { order } = flow;
  if (!order || order.status === "CANCELLED" || order.status === "CLOSED") return null;
  if (order.status === "DRAFT" || order.status === "OPEN") {
    return { title: "confirm the order", description: "The order is not confirmed yet, so nothing can be shipped.", to: `/sales/orders/${order.id}`, action: "Open the order" };
  }
  const slips = flow.slips ?? [];
  const invoices = flow.invoices ?? [];
  const shipped = slips.filter((s) => s.status === "POSTED" || s.status === "INVOICED");
  if (flow.slips !== null && shipped.length === 0) {
    return { title: "ship the order", description: "Pick, pack and post a packing slip for this order.", to: `/fulfillment/shipments?orderId=${order.id}`, action: "Open its shipments" };
  }
  const proposal = invoices.find((i) => i.status === "PROPOSAL");
  if (proposal) {
    return { title: "post the invoice", description: "The invoice is a proposal. Review it and post it to get its number.", to: `/billing/invoices/${proposal.id}`, action: "Review the invoice" };
  }
  if (flow.invoices !== null && shipped.some((s) => s.status === "POSTED")) {
    return { title: "invoice the shipment", description: "A packing slip is posted and not yet billed.", to: `/billing/proposals/new?customerId=${order.customerId}`, action: "Invoice shipments" };
  }
  const unpaid = invoices.map((i) => ({ i, state: paidState(i, flow.openItems) })).find((x) => x.state?.open);
  if (unpaid) {
    return { title: "record the payment", description: `Invoice ${unpaid.i.number} still has an open amount.`, to: `/receivables/payments/new?customerId=${order.customerId}`, action: "Record a payment" };
  }
  return null;
}

/**
 * The document flow panel and next-step offer on a sales order, packing slip, customer invoice or payment page.
 * `refreshKey` should change when the page's own document does (its status or version), so the offer follows the step just done.
 */
export function OrderFlowPanel({ anchor, refreshKey = "" }: { anchor: OrderFlowAnchor; refreshKey?: string }) {
  const flow = useFlow(() => loadOrderFlow(anchor), `${anchor.kind}:${anchor.id}:${refreshKey}`);
  const data = flow.data;
  const label = `Document flow of this ${{ order: "sales order", slip: "packing slip", invoice: "invoice", payment: "payment" }[anchor.kind]}`;
  if (data && !data.order) return null;
  return (
    <DocumentFlow
      label={label}
      loading={flow.loading && !data}
      error={flow.error ? "The related documents could not be read. The page itself is not affected." : undefined}
      onRetry={flow.reload}
      stages={data ? orderStages(data, anchor) : []}
      nextStep={data ? orderNextStep(data) : null}
      complete="This order has been shipped, invoiced and paid."
    />
  );
}
