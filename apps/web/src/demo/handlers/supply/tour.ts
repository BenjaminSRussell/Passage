import { VENDORS, WH_RAW } from "../../company";
import type { TourStop } from "../../tour";
import { uid } from "../../store";

// Supply chain screens for the home page demo tour, in walkthrough order.
export const TOUR: TourStop[] = [
  { area: "Supply chain", label: "Procurement workspace", to: "/purchasing", blurb: "Requisitions in review, orders to approve and confirm, and agreements nearing their limits at a glance." },
  { area: "Supply chain", label: "Requisitions", to: "/purchasing/requisitions", blurb: "Seven requisitions from draft to closed. Open one in review and approve it through its two-step chain." },
  { area: "Supply chain", label: "Purchase orders", to: "/purchasing/purchase-orders", blurb: "Twelve orders to six vendors, from draft and awaiting approval to confirmed, partly received and finalized." },
  { area: "Supply chain", label: "Late purchase order", to: `/purchasing/purchase-orders/${uid(302, 1)}`, blurb: "Ironbridge Foundry's impeller and volute castings, six days past due, with freight, totals and version history." },
  { area: "Supply chain", label: "Receipts", to: "/receiving/receipts", blurb: "Recent dock arrivals against those orders; register and post one to update the order and on-hand stock." },
  { area: "Supply chain", label: "On-hand inventory", to: "/inventory/onhand", blurb: "Stock by item and site with reservations and quarantine; the shortage items sit near or below safety stock." },
  { area: "Supply chain", label: "Inventory workspace", to: "/inventory/workspace", blurb: "Open journals, count sheets, reservations and transfer orders as tiles." },
  { area: "Supply chain", label: "Warehouse work", to: `/warehouse/work?warehouse=${WH_RAW.id}`, blurb: "Put-away, production picks and replenishment for the raw materials store; assign the next task to yourself." },
  { area: "Supply chain", label: "Procure to pay", to: "/purchasing/procure-to-pay", blurb: "The five steps from purchase order to vendor payment with what waits at each; open a purchase order to see its document flow and next step." },
  { area: "Supply chain", label: "Pay vendors", to: "/payables", blurb: "Payment proposals: approve a draft, start its payment run and step through the bank file, ledger posting and settlement." },
  { area: "Supply chain", label: "Vendor open items", to: "/payables/open-items", blurb: "What is owed to each vendor, with payment holds and their reasons." },
  { area: "Supply chain", label: "Vendor record", to: `/party/${VENDORS[0].id}`, blurb: "Ironbridge Foundry's party record with its vendor role in Cedar Ridge Pumps." },
];
