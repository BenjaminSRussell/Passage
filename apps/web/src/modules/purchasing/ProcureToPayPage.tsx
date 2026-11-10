import { GuidedFlow, type GuidedStep } from "../../shell/documents/GuidedFlow";
import { useStageCount } from "../../shell/documents/useStageCount";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { PageHeader } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { fetchGrir } from "../ap-invoices/api/client";
import { listOpenItems } from "../payables/api/client";
import { fetchReceipts } from "../receiving/api/client";
import { listPurchaseOrders } from "./api/client";

const LIMIT = 100;

/** The guided procure-to-pay page: the standard case from purchase order to vendor payment, with what waits at each step. */
export function ProcureToPayPage() {
  const duties = usePrincipalDuties();
  const can = (duty: string) => holdsDuty(duties, duty);
  const toConfirm = useStageCount("p2p-confirm", "purchase orders to confirm", "procurement.purchase-order.inquire", async () => {
    const page = await listPurchaseOrders({ approvalStatus: "APPROVED" }, { limit: LIMIT });
    return { count: page.items.length, more: page.nextCursor !== undefined };
  });
  const toReceive = useStageCount("p2p-receive", "purchase orders awaiting receipt", "procurement.purchase-order.inquire", async () => {
    const page = await listPurchaseOrders({ approvalStatus: "CONFIRMED" }, { limit: LIMIT });
    return { count: page.items.length, more: page.nextCursor !== undefined };
  });
  const toPost = useStageCount("p2p-post", "receipts to register or post", "receiving.receipt.maintain", async () => {
    const [arrived, registered] = await Promise.all([fetchReceipts({ state: "ARRIVED", limit: LIMIT }), fetchReceipts({ state: "REGISTERED", limit: LIMIT })]);
    return { count: arrived.items.length + registered.items.length, more: !!arrived.nextCursor || !!registered.nextCursor };
  });
  const toInvoice = useStageCount("p2p-invoice", "received lines not invoiced", "receiving.ap.inquire", async () => {
    const page = await fetchGrir({ status: "OPEN", limit: LIMIT });
    return { count: page.items.length, more: !!page.nextCursor };
  });
  const toPay = useStageCount("p2p-pay", "vendor invoices to pay", "open-item.read", async () => {
    const page = await listOpenItems({ status: "OPEN" });
    return { count: page.items.length, more: !!page.nextCursor };
  });

  const steps: GuidedStep[] = [
    {
      key: "order",
      title: "Raise and confirm the purchase order",
      description: "Order what you need from the vendor, get it approved, then confirm it to send it.",
      to: "/purchasing/purchase-orders/new",
      action: "New purchase order",
      fullScreen: { to: "/purchasing/purchase-orders", label: "All purchase orders" },
      count: toConfirm,
      locked: !can("procurement.purchase-order.maintain"),
    },
    {
      key: "receive",
      title: "Receive the goods",
      description: "Record the delivery when it arrives at the dock.",
      to: "/receiving/receipts/new",
      action: "Record arrival",
      fullScreen: { to: "/purchasing/open-supply", label: "Open supply" },
      count: toReceive,
      locked: !can("receiving.receipt.maintain"),
    },
    {
      key: "post",
      title: "Register and post the receipt",
      description: "Register what was counted, then post it so the goods are booked to the ledger.",
      to: "/receiving/receipts",
      action: "Open receipts",
      count: toPost,
      locked: !can("receiving.receipt.maintain"),
    },
    {
      key: "invoice",
      title: "Enter the vendor invoice",
      description: "Post the vendor's invoice against the goods received. It is matched to the receipt.",
      to: "/ap-invoices/post",
      action: "Enter vendor invoice",
      fullScreen: { to: "/ap-invoices/grir", label: "Goods received, not invoiced" },
      count: toInvoice,
      locked: !can("receiving.vendor-invoice.post"),
    },
    {
      key: "pay",
      title: "Pay the vendor",
      description: "Choose the invoices that are due, have a second person approve them, then run the payment.",
      to: "/payables/proposals/new",
      action: "New payment proposal",
      fullScreen: { to: "/payables", label: "Payment proposals" },
      count: toPay,
      locked: !can("payment-proposal.write"),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title="Procure to pay" description="Follow a purchase from the order to the vendor's payment. Each step opens the screen that does it." />
      <GuidedFlow label="Procure to pay steps" steps={steps} />
    </div>
  );
}
