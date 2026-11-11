import type { TourStop } from "../../tour";
import { uid } from "../../store";
import { KR } from "./arLedger";
import { K } from "./model";

// Order to cash screens for the home page demo tour, in walkthrough order.
export const TOUR: TourStop[] = [
  { area: "Order to cash", label: "Sales orders", to: "/sales/orders", blurb: "Sixteen orders from six customers, from draft to invoiced, with two on credit hold; filter by status or hold." },
  { area: "Order to cash", label: "New sales order", to: "/sales/orders/new", blurb: "Enter an order by picking the customer and products by name. Save it as a draft, then confirm it to run the credit check." },
  { area: "Order to cash", label: "A partly shipped order", to: `/sales/orders/${uid(K.order, 3)}`, blurb: "SO-024183 for Summit Mining Supply: lines and prices, schedule, deliveries, change history and the credit check." },
  { area: "Order to cash", label: "Quotations", to: "/sales/quotations", blurb: "Eight quotations from draft to converted or lost. Open one to send it, change it or convert it into a draft order." },
  { area: "Order to cash", label: "Sales workspace", to: "/sales/workspace", blurb: "What needs attention in sales today: holds, backorders, expiring quotes and the quotation pipeline." },
  { area: "Order to cash", label: "Credit release queue", to: "/sales/credit-holds", blurb: "Orders held for credit, with the rule that failed. Release SO-024185 to confirm it." },
  { area: "Order to cash", label: "Credit exposure", to: "/sales/credit-exposure", blurb: "Each customer's open AR, goods delivered but not invoiced and open orders against their limit." },
  { area: "Order to cash", label: "Trade agreements", to: "/pricing/agreements", blurb: "List prices, quantity breaks, customer contracts and discounts that price every order line. Export them to CSV." },
  { area: "Order to cash", label: "Price list", to: "/pricing/price-list", blurb: "Pick a customer to see every agreement that applies today and the net price at each quantity break." },
  { area: "Order to cash", label: "Price simulation", to: "/pricing/simulate", blurb: "Price a customer and product at a quantity, then read which agreements produced the net price." },
  { area: "Order to cash", label: "Shipments", to: "/fulfillment/shipments", blurb: "Shipments from release to delivered, matching the shipped sales orders; open one for lines, pallets and tracking." },
  { area: "Order to cash", label: "Order to cash", to: "/sales/order-to-cash", blurb: "The five steps from order to payment with what waits at each, then open an order to see its document flow and next step." },
  { area: "Order to cash", label: "Invoices", to: "/billing", blurb: "Posted, part-paid and settled invoices for the shipped orders, plus a proposal ready to post." },
  { area: "Order to cash", label: "AR workspace", to: "/receivables", blurb: "Open receivables aged by due date, the payments waiting to be applied and the disputes being worked." },
  { area: "Order to cash", label: "Apply a payment", to: `/receivables/payments/${uid(KR.payment, 10)}`, blurb: "Bluewater's ACH for its open invoice: Find matches scores it 100 on reference and amount, then apply it and the invoice is settled." },
  { area: "Order to cash", label: "Collections worklist", to: "/receivables/worklist", blurb: "Customers ranked by risk: Metro's broken promise, Summit's price dispute, Coastal's retention and Prairie's returned check." },
  { area: "Order to cash", label: "Dunning runs", to: "/receivables/dunning-runs", blurb: "Two issued runs and a cancelled one. Start a new run to propose a reminder for Prairie, with the disputed and blocked items left out." },
];
