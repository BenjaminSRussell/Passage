import type { TourStop } from "../../tour";
import { uid } from "../../store";
import { K } from "./model";

// Order to cash screens for the home page demo tour, in walkthrough order.
export const TOUR: TourStop[] = [
  { area: "Order to cash", label: "Sales orders", to: "/sales/orders", blurb: "Sixteen orders from six customers, from draft to invoiced, with two on credit hold; filter by status or hold." },
  { area: "Order to cash", label: "A partly shipped order", to: `/sales/orders/${uid(K.order, 3)}`, blurb: "SO-024183 for Summit Mining: lines and prices, schedule, deliveries, change history and the credit check." },
  { area: "Order to cash", label: "New sales order", to: "/sales/orders/new", blurb: "Enter an order, confirm it and watch the credit check run. Use the ids from an order page; the form tells you which ones it needs." },
  { area: "Order to cash", label: "Sales workspace", to: "/sales/workspace", blurb: "What needs attention in sales today: holds, backorders, expiring quotes and the quotation pipeline." },
  { area: "Order to cash", label: "Credit release queue", to: "/sales/credit-holds", blurb: "Orders held for credit, with the rule that failed. Release SO-024185 to confirm it." },
  { area: "Order to cash", label: "Credit exposure", to: "/sales/credit-exposure", blurb: "Each customer's open AR, goods delivered but not invoiced and open orders against their limit." },
  { area: "Order to cash", label: "Trade agreements", to: "/pricing/agreements", blurb: "List prices, quantity breaks, customer contracts and discounts that price every order line." },
  { area: "Order to cash", label: "Shipments", to: "/fulfillment/shipments", blurb: "Shipments from release to delivered, matching the shipped sales orders; open one for lines, pallets and tracking." },
  { area: "Order to cash", label: "Invoices", to: "/billing", blurb: "Posted, part-paid and settled invoices for the shipped orders, plus a proposal ready to post." },
];
