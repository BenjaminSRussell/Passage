// Receivables open items (/api/v1/receivables) for the demo: one item per posted invoice, so the document flow
// and the order-to-cash page can show what is still to collect.
import type { OpenItem } from "../../../modules/receivables/api/client";
import { route } from "../../backend";
import { invoices } from "./model";

route("GET", "/api/v1/receivables/open-items", ({ query }) => {
  const all = query.get("status") === "all";
  const items: OpenItem[] = invoices
    .all()
    .filter((i) => i.number && i.status !== "PROPOSAL" && (!query.get("customer") || i.customerId === query.get("customer")))
    .map((i) => {
      const open = i.status === "SETTLED" ? 0 : Number(i.totals.gross) * (1 - i.settled);
      return {
        id: i.id,
        legalEntityId: i.legalEntityId,
        customerId: i.customerId,
        sourceType: "INVOICE",
        documentNumber: i.number ?? "",
        documentDate: i.invoiceDate,
        dueDate: i.dueDate,
        currency: i.currency,
        amount: i.totals.gross,
        openAmount: open.toFixed(2),
        cashDiscountAmount: "0.00",
        dunningLevel: 0,
        dunningBlock: false,
        status: open > 0 ? "OPEN" : "CLEARED",
        version: 1,
        side: "DEBIT",
      } as OpenItem;
    })
    .filter((i) => all || Number(i.openAmount) > 0);
  return { items };
});
