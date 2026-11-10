import { GuidedFlow, type GuidedStep } from "../../shell/documents/GuidedFlow";
import { useStageCount } from "../../shell/documents/useStageCount";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { PageHeader } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { listInvoicePage } from "../billing/api/client";
import { listPackingSlips } from "../fulfillment/api/client";
import { listOpenItems } from "../receivables/api/client";
import { listOrders } from "./api/client";

const LIMIT = 100;

/** The guided order-to-cash page: the standard case from order to payment, with what waits at each step. */
export function OrderToCashPage() {
  const duties = usePrincipalDuties();
  const toConfirm = useStageCount("o2c-confirm", "orders to confirm", "sales.order.read", async () => {
    const page = await listOrders({ status: "OPEN", limit: LIMIT });
    return { count: page.items.length, more: page.nextCursor !== undefined };
  });
  const toShip = useStageCount("o2c-ship", "orders to ship", "sales.order.read", async () => {
    const page = await listOrders({ status: "CONFIRMED", limit: LIMIT });
    return { count: page.items.length, more: page.nextCursor !== undefined };
  });
  const toInvoice = useStageCount("o2c-invoice", "shipments not invoiced", "fulfillment.warehouse.view", async () => {
    const slips = await listPackingSlips({ status: "POSTED", limit: LIMIT });
    return { count: slips.length, more: slips.length === LIMIT };
  });
  const toPost = useStageCount("o2c-post", "invoice proposals to post", "billing.invoice.view", async () => {
    const page = await listInvoicePage({ status: "PROPOSAL", limit: LIMIT });
    return { count: page.items.length, more: page.nextCursor !== undefined };
  });
  const toCollect = useStageCount("o2c-collect", "open receivables", "receivables.view", async () => {
    const items = await listOpenItems({ status: "open", limit: 500 });
    return { count: items.length, more: items.length === 500 };
  });
  const can = (duty: string) => holdsDuty(duties, duty);

  const steps: GuidedStep[] = [
    {
      key: "order",
      title: "Take and confirm the sales order",
      description: "Enter what the customer ordered, then confirm it. Confirming checks credit and reserves stock.",
      to: "/sales/orders/new",
      action: "New sales order",
      fullScreen: { to: "/sales/orders", label: "All sales orders" },
      count: toConfirm,
      locked: !can("sales.order.write"),
    },
    {
      key: "ship",
      title: "Pick, pack and ship",
      description: "Release the order to the warehouse, pick and pack it, then post the packing slip.",
      to: "/fulfillment/shipments",
      action: "Open shipments",
      fullScreen: { to: "/fulfillment/workbench", label: "Picking workbench" },
      count: toShip,
      locked: !can("fulfillment.warehouse.view"),
    },
    {
      key: "invoice",
      title: "Invoice what shipped",
      description: "Turn posted packing slips into a customer invoice proposal and review the price and tax.",
      to: "/billing/proposals/new",
      action: "Invoice shipments",
      fullScreen: { to: "/billing", label: "All invoices" },
      count: toInvoice,
      locked: !can("billing.invoice.maintain"),
    },
    {
      key: "post",
      title: "Post the invoice",
      description: "Posting gives the invoice its number and opens the receivable. After that it cannot change.",
      to: "/billing",
      action: "Open invoices",
      count: toPost,
      locked: !can("billing.invoice.view"),
    },
    {
      key: "payment",
      title: "Record the payment",
      description: "Record the money you received and apply it to the invoices it pays.",
      to: "/receivables/payments/new",
      action: "Record a payment",
      fullScreen: { to: "/receivables/open-items", label: "Open receivables" },
      count: toCollect,
      locked: !can("receivables.view"),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title="Order to cash" description="Follow a customer order from the sales order to the payment. Each step opens the screen that does it." />
      <GuidedFlow label="Order to cash steps" steps={steps} />
    </div>
  );
}
