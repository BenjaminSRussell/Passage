export type GuideFile = "ap-clerk.md" | "buyer.md" | "ar-clerk.md" | "order-desk.md" | "tenant-admin.md";

export interface RouteHelp {
  patterns: readonly string[];
  title: string;
  purpose: string;
  howTo: readonly string[];
  guide: `${GuideFile}#${string}`;
}

export const routeHelp: readonly RouteHelp[] = [
  {
    patterns: ["/purchasing"],
    title: "Purchasing",
    purpose: "Your starting point for buying: requests, orders and what is still on its way.",
    howTo: ["Open Requisitions to ask for something.", "Open Purchase orders to buy from a vendor.", "Use Approvals to decide requests waiting for you."],
    guide: "buyer.md#raise-a-purchase-order",
  },
  {
    patterns: ["/purchasing/requisitions", "/purchasing/requisitions/:id", "/purchasing/requisitions/:id/edit"],
    title: "Requisitions",
    purpose: "Internal requests to buy something. An approver decides each one before it can become an order.",
    howTo: ["Select New requisition to start one.", "Submit a draft for approval.", "Withdraw or reopen a request you sent by mistake.", "Approvers choose Approve or Send back."],
    guide: "buyer.md#ask-for-what-you-need",
  },
  {
    patterns: ["/purchasing/requisitions/new"],
    title: "New requisition",
    purpose: "Ask for goods or services. Add a line for each thing you need.",
    howTo: ["Choose the legal entity and currency.", "Select Add line and fill in the product, quantity and estimated unit price.", "Select Save requisition, then submit it for approval."],
    guide: "buyer.md#ask-for-what-you-need",
  },
  {
    patterns: ["/purchasing/approvals"],
    title: "Requisition approvals",
    purpose: "Requests waiting for a decision.",
    howTo: ["Filter by Waiting for to see yours.", "Open a request to read its lines.", "Choose Approve, or Send back with a comment."],
    guide: "buyer.md#ask-for-what-you-need",
  },
  {
    patterns: ["/purchasing/consolidation"],
    title: "Consolidation",
    purpose: "Approved request lines waiting to be ordered. Group them by vendor into purchase orders or ask vendors for prices.",
    howTo: ["Enter a vendor on each line.", "Select lines, then Create purchase orders.", "Or select Start RFQ for selected lines.", "Use Split line to divide a line."],
    guide: "buyer.md#ask-for-what-you-need",
  },
  {
    patterns: ["/purchasing/purchase-orders", "/purchasing/purchase-orders/:id", "/purchasing/purchase-orders/:id/edit"],
    title: "Purchase orders",
    purpose: "Orders placed with vendors. The buttons on an order depend on its status.",
    howTo: ["Draft: Submit for approval.", "In review: Approve or Reject.", "Approved: Confirm and send.", "Confirmed: Change order or Update prices.", "Any open order: Cancel order."],
    guide: "buyer.md#raise-a-purchase-order",
  },
  {
    patterns: ["/purchasing/purchase-orders/new"],
    title: "New purchase order",
    purpose: "Order goods or services from a vendor.",
    howTo: ["Choose the legal entity, vendor and currency.", "Select Add line and fill in product, quantity and delivery date.", "Leave Unit price blank to use the agreed price.", "Select Save purchase order."],
    guide: "buyer.md#raise-a-purchase-order",
  },
  {
    patterns: ["/purchasing/purchase-orders/:id/*", "/purchasing/reprice"],
    title: "Purchase order line details",
    purpose: "Invoicing plans, delivery schedules, service details and price updates for one order or line.",
    howTo: ["Change the schedule or plan, then save.", "Use Update prices to reprice the order."],
    guide: "buyer.md#change-a-confirmed-order",
  },
  {
    patterns: ["/purchasing/rfqs/*", "/purchasing/rfq-types/*"],
    title: "Requests for quotation",
    purpose: "Ask several vendors for a price before you buy.",
    howTo: ["Start an RFQ from Consolidation or New.", "Compare replies before you choose a vendor."],
    guide: "buyer.md#ask-for-what-you-need",
  },
  {
    patterns: [
      "/purchasing/agreements/*",
      "/purchasing/scheduling-agreements/*",
      "/purchasing/trade-agreements/*",
      "/purchasing/pricing",
      "/purchasing/releases/*",
      "/purchasing/catalogs/*",
      "/purchasing/catalog-imports/*",
    ],
    title: "Agreements, prices and catalogs",
    purpose: "Agreed prices and quantities with vendors. Orders pick up these prices when you leave Unit price blank.",
    howTo: ["Open an agreement to see its lines and what has been ordered.", "Use New to add one.", "Missing price? Add a trade agreement for the product and vendor."],
    guide: "buyer.md#raise-a-purchase-order",
  },
  {
    patterns: ["/purchasing/inquiries/*", "/purchasing/open-supply"],
    title: "Purchasing reports",
    purpose: "Lists of what is overdue, backordered, expiring or changed, to show where buying needs attention.",
    howTo: ["Open a report from the Inquiries list.", "Open a row to see the order or agreement behind it."],
    guide: "buyer.md#raise-a-purchase-order",
  },
  {
    patterns: ["/purchasing/inbound-changes/*"],
    title: "Changes from vendors",
    purpose: "Changes vendors have asked for on orders you sent them.",
    howTo: ["Open a change to see what the vendor wants.", "Accept or decline it after you have checked the order."],
    guide: "buyer.md#change-a-confirmed-order",
  },
  {
    patterns: ["/purchasing/charge-codes/*", "/purchasing/auto-charges/*"],
    title: "Charges",
    purpose: "Codes for freight and other extra costs, and rules that add them to orders by themselves.",
    howTo: ["Use New to add a code or a rule.", "Open a row to change it."],
    guide: "buyer.md#raise-a-purchase-order",
  },
  {
    patterns: ["/purchasing/signing-limits/*", "/purchasing/signing-limit-requests/*"],
    title: "Signing limits",
    purpose: "How much each person may approve, and requests to raise a limit.",
    howTo: ["Open a limit to see who holds it.", "Use New to ask for a higher limit."],
    guide: "buyer.md#ask-for-what-you-need",
  },
  {
    patterns: ["/purchasing/category-vendors/*", "/purchasing/category-managers", "/purchasing/model-service-specs/*", "/purchasing/policies/*"],
    title: "Vendors, categories and buying rules",
    purpose: "Which vendors may supply each category, who manages it, and the rules that apply to each legal entity.",
    howTo: ["Open a row to see its details.", "Use New to add one where the page allows it."],
    guide: "buyer.md#raise-a-purchase-order",
  },
  {
    patterns: ["/purchasing/*"],
    title: "Purchasing set-up and reports",
    purpose: "Rules, limits and reports that support buying, such as signing limits, charge codes and overdue orders.",
    howTo: ["Open a row to see its details.", "Use New to add one where the page allows it."],
    guide: "buyer.md#raise-a-purchase-order",
  },
  {
    patterns: ["/receiving/receipts"],
    title: "Receipts",
    purpose: "Records of goods arriving from vendors.",
    howTo: ["Select Record arrival when a delivery comes in.", "Open a receipt to register and post it."],
    guide: "buyer.md#receive-the-goods",
  },
  {
    patterns: ["/receiving/receipts/new"],
    title: "Record arrival",
    purpose: "Record a delivery against a purchase order.",
    howTo: ["Enter the vendor, purchase order and legal entity.", "Enter the vendor's packing slip number.", "Choose the order line and quantity for each line.", "Select Record arrival."],
    guide: "ap-clerk.md#record-the-goods-arriving",
  },
  {
    patterns: ["/receiving/receipts/:id"],
    title: "Receipt",
    purpose: "One delivery. Register what you counted, then post it to put the goods in stock.",
    howTo: ["Fill in quantity received, location and stock status.", "Select Post receipt, then Confirm post.", "Use Correct quantities or Cancel receipt after posting."],
    guide: "ap-clerk.md#record-the-goods-arriving",
  },
  {
    patterns: ["/ap-invoices"],
    title: "Vendor invoices",
    purpose: "Invoices from vendors that have been posted.",
    howTo: ["Filter by vendor or status.", "Select Post vendor invoice to add one.", "Open an invoice to see its ledger entry."],
    guide: "ap-clerk.md#enter-a-vendor-invoice",
  },
  {
    patterns: ["/ap-invoices/grir"],
    title: "Goods received, not invoiced",
    purpose: "Goods that arrived and are waiting for a vendor invoice.",
    howTo: ["Filter by vendor, status or age.", "Tick lines from one vendor and one currency.", "Select Invoice selected items."],
    guide: "ap-clerk.md#enter-a-vendor-invoice",
  },
  {
    patterns: ["/ap-invoices/post"],
    title: "Post vendor invoice",
    purpose: "Book a vendor invoice against goods you received.",
    howTo: ["Enter the invoice number exactly as printed.", "Check the invoice and posting dates.", "Check each line's quantity and unit price.", "Select Post invoice."],
    guide: "ap-clerk.md#enter-a-vendor-invoice",
  },
  {
    patterns: ["/ap-invoices/:id"],
    title: "Vendor invoice",
    purpose: "One posted vendor invoice, with links to its receipt and purchase order.",
    howTo: ["Check the total and the ledger entry.", "Follow the links to the receipt and purchase order.", "A held invoice waits for an auditor."],
    guide: "ap-clerk.md#what-the-statuses-mean",
  },
  {
    patterns: ["/payables"],
    title: "Payment proposals",
    purpose: "Lists of vendor invoices chosen for payment. A second person approves each list before it is paid.",
    howTo: ["Select New payment proposal to start one.", "Filter by status.", "Open a proposal to check or approve it.", "Open Vendor open items to see what is owed."],
    guide: "ap-clerk.md#create-a-payment-proposal",
  },
  {
    patterns: ["/payables/open-items"],
    title: "Vendor open items",
    purpose: "Invoices you owe vendors. Put one on hold to keep it out of payment.",
    howTo: ["Filter by vendor, due date or status.", "Select Hold payment of an invoice and give a reason.", "Select Release hold on an invoice when it can be paid."],
    guide: "ap-clerk.md#check-what-is-due",
  },
  {
    patterns: ["/payables/proposals/new"],
    title: "New payment proposal",
    purpose: "Pick the payment date and method. Every invoice that is due is saved as a draft for approval.",
    howTo: ["Choose the company currency and payment date.", "Set Include invoices due by to look further ahead.", "Use Pay a vendor a different way for exceptions.", "Select Create proposal."],
    guide: "ap-clerk.md#create-a-payment-proposal",
  },
  {
    patterns: ["/payables/proposals/:id"],
    title: "Payment proposal",
    purpose: "One list of invoices to pay, grouped by vendor, with totals.",
    howTo: ["Check the totals and any invoices left out.", "Select Show invoices on a row for the detail.", "A different person from the creator selects Approve proposal.", "Select Cancel proposal to send the invoices back."],
    guide: "ap-clerk.md#approve-a-payment-proposal",
  },
  {
    patterns: ["/sales/workspace"],
    title: "Sales workspace",
    purpose: "Your starting point for customer orders, holds and quotations.",
    howTo: ["Open Orders to see what is in progress.", "Open Credit release queue to see held orders."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/sales", "/sales/orders"],
    title: "Sales orders",
    purpose: "Customer orders and how far each has got.",
    howTo: ["Select New sales order to enter one.", "Open an order to confirm, change or cancel it."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/sales/orders/new"],
    title: "New sales order",
    purpose: "Enter a customer's order. It is saved as a draft until you confirm it.",
    howTo: ["Fill in the legal entity, customer and currency.", "Add a line for each product.", "Select Save draft order."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/sales/orders/:id", "/sales/orders/:id/change", "/sales/orders/:id/promise"],
    title: "Sales order",
    purpose: "One customer order, with its holds, credit check and deliveries.",
    howTo: ["Select Confirm order to run the credit check.", "Release or apply holds in the Holds card.", "Select Change or cancel to edit.", "Check Deliveries and invoices for progress."],
    guide: "order-desk.md#confirm-the-order",
  },
  {
    patterns: ["/sales/orders/:id/progress/new", "/sales/order-progress", "/sales/order-auto-close"],
    title: "Order progress",
    purpose: "What has been delivered and invoiced on an order. Both are recorded automatically when a packing slip or invoice posts.",
    howTo: ["Check the delivered and invoiced quantities.", "Use Close invoiced orders to tidy up finished orders."],
    guide: "order-desk.md#invoice-it",
  },
  {
    patterns: ["/sales/credit-holds", "/sales/credit-exposure", "/sales/credit-profiles/*"],
    title: "Credit control",
    purpose: "Orders held for credit and each customer's credit limit.",
    howTo: ["Open a held order to release or reject it.", "A comment is needed to reject.", "Check a customer's exposure before you release."],
    guide: "order-desk.md#confirm-the-order",
  },
  {
    patterns: ["/sales/quotations"],
    title: "Quotations",
    purpose: "Prices offered to customers before they order.",
    howTo: ["Select New to write a quotation.", "Open a quotation to see its lines and status."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/sales/quotations/new"],
    title: "New quotation",
    purpose: "Offer a customer a price for products.",
    howTo: ["Choose the customer and add a line for each product.", "Save it, then open it to follow its status."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/sales/quotations/:id"],
    title: "Quotation",
    purpose: "One quotation with its lines and status.",
    howTo: ["Check the lines and prices.", "Follow the status to see whether the customer accepted."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/sales/change-requests", "/sales/change-policy"],
    title: "Order change requests",
    purpose: "Changes asked for on orders that are already confirmed, and the rules for when a change needs a decision.",
    howTo: ["Open a request to see what changed.", "Approve or reject it."],
    guide: "order-desk.md#confirm-the-order",
  },
  {
    patterns: ["/sales/change-requests/:id"],
    title: "Order change request",
    purpose: "One requested change to a confirmed order.",
    howTo: ["Compare the old and new values.", "Approve or reject it, with a comment."],
    guide: "order-desk.md#confirm-the-order",
  },
  {
    patterns: ["/sales/backorders", "/sales/backorder-runs", "/sales/backorder-processing/*"],
    title: "Backorders",
    purpose: "Order lines that could not be shipped yet, and the runs that try again when stock arrives.",
    howTo: ["Open a row to see the order line.", "Use New to start a processing run."],
    guide: "order-desk.md#ship-it",
  },
  {
    patterns: ["/sales/atp", "/sales/allocation-quotas/*"],
    title: "Availability",
    purpose: "What you can promise customers, and quotas that reserve stock for them.",
    howTo: ["Look up a product to see what is available.", "Open a quota to change it."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/sales/product-listings/*", "/sales/supplementary-items/*", "/sales/product-proposals/*", "/sales/alternative-items/*"],
    title: "Product listings and suggestions",
    purpose: "Which products each customer may order, and extra or alternative items offered with them.",
    howTo: ["Open a row to see its details.", "Use New to add one."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/sales/agreements/*", "/sales/schedule-releases"],
    title: "Sales agreements",
    purpose: "Agreed prices and quantities with a customer, and the releases that turn them into orders.",
    howTo: ["Open an agreement to see its lines and releases.", "Use New to add an agreement or a release."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/sales/intake-errors/*"],
    title: "Order intake errors",
    purpose: "Orders that arrived from outside but could not be entered.",
    howTo: ["Open an error to see what was wrong.", "Fix the cause, then retry."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/sales/delivery-terms/*", "/sales/delivery-tolerances/*", "/sales/parameters"],
    title: "Sales rules",
    purpose: "Delivery terms, how much over or under delivery is allowed, and sales settings.",
    howTo: ["Open a row to change it.", "Use New to add one."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/sales/*"],
    title: "Sales set-up and reports",
    purpose: "Quotations, agreements, listings, backorders and rules that support selling.",
    howTo: ["Open a row to see its details.", "Use New to add one where the page allows it."],
    guide: "order-desk.md#enter-the-order",
  },
  {
    patterns: ["/fulfillment", "/fulfillment/release"],
    title: "Release lines",
    purpose: "Send order lines to the warehouse so they can be picked.",
    howTo: ["Enter the warehouse.", "Enter each order line to release.", "Choose whether to require full availability."],
    guide: "order-desk.md#ship-it",
  },
  {
    patterns: ["/fulfillment/workbench", "/fulfillment/waves", "/fulfillment/waves/:id"],
    title: "Picking",
    purpose: "Group released lines into pick waves and confirm what was picked.",
    howTo: ["Choose how to group lines and preview.", "Create the waves.", "Assign a picker, print the pick list, confirm picks."],
    guide: "order-desk.md#ship-it",
  },
  {
    patterns: ["/fulfillment/shipments", "/fulfillment/shipments/:id"],
    title: "Shipments",
    purpose: "Goods being picked, packed and shipped for customers.",
    howTo: ["Open a shipment.", "Select Confirm pick for each line.", "Pack with Auto-pack by packing rule or Pack a container.", "Select Post packing slip to ship."],
    guide: "order-desk.md#ship-it",
  },
  {
    patterns: ["/fulfillment/packing-slips", "/fulfillment/packing-slips/:id", "/fulfillment/delivered-not-invoiced"],
    title: "Packing slips",
    purpose: "Shipping documents posted when goods leave. Each one is later invoiced.",
    howTo: ["Open a slip to see its lines.", "Slips shipped but not yet billed are listed under Delivered, not invoiced."],
    guide: "order-desk.md#invoice-it",
  },
  {
    patterns: ["/fulfillment/manifests/*", "/fulfillment/drop-ships/*"],
    title: "Manifests and direct deliveries",
    purpose: "Carrier manifests for a day's shipments, and orders the vendor ships straight to the customer.",
    howTo: ["Open a row to see its shipments.", "Follow the status until it is delivered."],
    guide: "order-desk.md#ship-it",
  },
  {
    patterns: ["/fulfillment/exceptions/*", "/fulfillment/backorders"],
    title: "Shipping problems",
    purpose: "Shipments that need attention, and lines that could not ship yet.",
    howTo: ["Open a row to see what went wrong.", "Fix the cause, then continue the shipment."],
    guide: "order-desk.md#ship-it",
  },
  {
    patterns: ["/fulfillment/scan", "/fulfillment/sscc-register"],
    title: "Scanning and labels",
    purpose: "Scan goods as you pick and pack, and see the labels issued to containers.",
    howTo: ["Scan an item or container.", "Check the register for a label."],
    guide: "order-desk.md#ship-it",
  },
  {
    patterns: ["/fulfillment/container-types", "/fulfillment/packing-rules", "/fulfillment/packaging-materials", "/fulfillment/packaging-fees", "/fulfillment/returnable-packaging", "/fulfillment/returnable-movements", "/fulfillment/returnable-aging"],
    title: "Packaging",
    purpose: "Container types, packing rules, packaging materials and fees, and returnable packaging held by customers.",
    howTo: ["Open a row to change it.", "Use Auto-pack by packing rule on a shipment to apply the rules."],
    guide: "order-desk.md#ship-it",
  },
  {
    patterns: ["/fulfillment/warehouse-settings", "/fulfillment/operation-rules", "/fulfillment/release-rules", "/fulfillment/delivery-tolerances", "/fulfillment/legal-entity-settings"],
    title: "Warehouse rules",
    purpose: "Settings that decide how lines are released, picked and shipped.",
    howTo: ["Open a row to change it.", "Changes apply to new work, not to shipments already started."],
    guide: "order-desk.md#ship-it",
  },
  {
    patterns: ["/fulfillment/*"],
    title: "Fulfillment set-up and reports",
    purpose: "Manifests, packaging, rules and exceptions that support shipping.",
    howTo: ["Open a row to see its details.", "Check Exceptions for shipments that need attention."],
    guide: "order-desk.md#ship-it",
  },
  {
    patterns: ["/billing"],
    title: "Billing",
    purpose: "Invoices to customers, credit notes and revenue.",
    howTo: ["Select Invoice shipments to bill shipped goods.", "Open an invoice to post or credit it."],
    guide: "order-desk.md#invoice-it",
  },
  {
    patterns: ["/billing/proposals/new"],
    title: "Invoice shipments",
    purpose: "Draft invoices from shipped packing slips.",
    howTo: ["Enter the legal entity, customer, currency and invoice date.", "Add each packing slip and its lines.", "Select Create proposals."],
    guide: "order-desk.md#invoice-it",
  },
  {
    patterns: ["/billing/invoices/:id", "/billing/invoices/:id/trace"],
    title: "Invoice",
    purpose: "One customer invoice. A proposal is a draft until you post it.",
    howTo: ["Select Post invoice to number it and send it on.", "Select Credit invoice to reduce what is owed.", "Review it again if the tax changed."],
    guide: "order-desk.md#invoice-it",
  },
  {
    patterns: ["/billing/free-text-invoices/new"],
    title: "New free-text invoice",
    purpose: "Invoice something that did not ship, such as a service or a fee.",
    howTo: ["Choose the customer and legal entity.", "Add a line with a description, amount and revenue account.", "Save it, then post it."],
    guide: "order-desk.md#invoice-it",
  },
  {
    patterns: ["/billing/credit-notes", "/billing/credit-notes/new"],
    title: "Credit notes",
    purpose: "Documents that reduce what a customer owes.",
    howTo: ["Select New to write a credit note.", "Or open an invoice and select Credit invoice."],
    guide: "order-desk.md#invoice-it",
  },
  {
    patterns: ["/billing/revenue-allocation", "/billing/revenue-contracts/*", "/billing/deferred-revenue"],
    title: "Revenue",
    purpose: "How revenue from contracts is spread over time.",
    howTo: ["Open a contract to see its schedule.", "Use New to add a contract."],
    guide: "order-desk.md#invoice-it",
  },
  {
    patterns: ["/billing/*"],
    title: "Billing documents and revenue",
    purpose: "Free-text invoices, credit notes and revenue schedules.",
    howTo: ["Use New to add one.", "Open a row to see its details."],
    guide: "order-desk.md#invoice-it",
  },
  {
    patterns: ["/receivables"],
    title: "AR workspace",
    purpose: "Your starting point for money owed by customers.",
    howTo: ["Open Open items to see what is unpaid.", "Open Payments to record money received."],
    guide: "ar-clerk.md#look-at-what-is-owed",
  },
  {
    patterns: ["/receivables/open-items"],
    title: "Open items",
    purpose: "Customer invoices and debits that are not yet paid.",
    howTo: ["Filter by customer.", "Open Payments to record money received."],
    guide: "ar-clerk.md#look-at-what-is-owed",
  },
  {
    patterns: ["/receivables/payments"],
    title: "Payments",
    purpose: "Money received from customers and its status.",
    howTo: ["Filter by status.", "Select Record payment to add one.", "Open a payment to settle it."],
    guide: "ar-clerk.md#record-the-payment",
  },
  {
    patterns: ["/receivables/payments/new"],
    title: "Record payment",
    purpose: "Enter money you received from a customer.",
    howTo: ["Choose the legal entity, customer and payment mode.", "Enter the reference, amount and dates.", "Give a short-pay reason if it is less than billed.", "Select Record payment."],
    guide: "ar-clerk.md#record-the-payment",
  },
  {
    patterns: ["/receivables/payments/:id"],
    title: "Payment",
    purpose: "One payment. Match it to open items, then apply it.",
    howTo: ["Select Find matches and use a suggestion.", "Set Apply, Cash discount and Write-off per row.", "Select Apply payment. Anything left stays on account."],
    guide: "ar-clerk.md#settle-it-against-invoices",
  },
  {
    patterns: ["/receivables/opening-items"],
    title: "Imported opening items",
    purpose: "Customer balances brought in when you started using the system.",
    howTo: ["Check the imported items against your old records."],
    guide: "ar-clerk.md#look-at-what-is-owed",
  },
  {
    patterns: ["/receivables/lockbox/*", "/receivables/bank-statements/*"],
    title: "Bank files",
    purpose: "Payments and statements loaded from the bank.",
    howTo: ["Select New to load a file.", "Open a row to check its payments."],
    guide: "ar-clerk.md#record-the-payment",
  },
  {
    patterns: ["/receivables/disputes/*", "/receivables/deductions/*"],
    title: "Disputes and deductions",
    purpose: "Invoices a customer disagrees with, and amounts they took off a payment.",
    howTo: ["Open a row to see the reason.", "Use New to record one."],
    guide: "ar-clerk.md#settle-it-against-invoices",
  },
  {
    patterns: ["/receivables/down-payments/*"],
    title: "Down payments",
    purpose: "Money received before an invoice, held until it is used.",
    howTo: ["Open a down payment to see what is left.", "Use New to record one."],
    guide: "ar-clerk.md#record-the-payment",
  },
  {
    patterns: ["/receivables/worklist", "/receivables/promises/*", "/receivables/dunning-runs/*", "/receivables/interest-notes/*", "/receivables/cases/*", "/receivables/statements", "/receivables/balance-confirmations/*", "/receivables/balance-snapshots"],
    title: "Collections",
    purpose: "Chasing late payers: reminders, promises to pay, interest and statements.",
    howTo: ["Start from the worklist.", "Open a row to see the customer's history.", "Use New to add a reminder run or a case."],
    guide: "ar-clerk.md#look-at-what-is-owed",
  },
  {
    patterns: ["/receivables/*"],
    title: "Collections and bank files",
    purpose: "Disputes, reminders, bank statements and other work that supports getting paid.",
    howTo: ["Open a row to see its details.", "Use New to add one where the page allows it."],
    guide: "ar-clerk.md#look-at-what-is-owed",
  },
];

const segments = (path: string) => path.split("/").filter(Boolean);

function score(pattern: string, path: readonly string[]): number | undefined {
  const parts = segments(pattern);
  const wildcard = parts.at(-1) === "*";
  const fixed = wildcard ? parts.slice(0, -1) : parts;
  if (wildcard ? path.length < fixed.length : path.length !== fixed.length) return undefined;
  let total = wildcard ? -1 : 0;
  for (const [index, part] of fixed.entries()) {
    if (part.startsWith(":")) total += 2;
    else if (part === path[index]) total += 3;
    else return undefined;
  }
  return total;
}

export function resolveHelp(pathname: string): { entry: RouteHelp; pattern: string } | undefined {
  const path = segments(pathname.split(/[?#]/)[0] ?? "");
  let best: { entry: RouteHelp; pattern: string; value: number } | undefined;
  for (const entry of routeHelp) {
    for (const pattern of entry.patterns) {
      const value = score(pattern, path);
      if (value !== undefined && (!best || value > best.value)) best = { entry, pattern, value };
    }
  }
  return best && { entry: best.entry, pattern: best.pattern };
}

export const helpForRoute = (pathname: string): RouteHelp | undefined => resolveHelp(pathname)?.entry;
