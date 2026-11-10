import { entityLabel, legalEntityParty, pdfText, type DocumentRequest } from "../../shell/documents/pdfDocument";
import { getOrder } from "../sales/api/client";
import type { PackingSlip } from "./api/client";

/** The packing slip as the PDF shows it: the shipped quantities with the sales order's item names. No prices or costs. */
export async function packingSlipDocument(slip: PackingSlip): Promise<DocumentRequest> {
  const { value: order } = await getOrder(slip.orderId);
  const [legalEntity, customer, ...names] = await Promise.all([
    legalEntityParty(order.legalEntityId),
    entityLabel("customer", order.customerId),
    ...slip.lines.map((l) => entityLabel("product", order.lines.find((o) => o.id === l.orderLineId)?.productId)),
  ]);
  return {
    documentType: "PACKING_SLIP",
    number: pdfText(slip.packingSlipNumber),
    legalEntity: { name: pdfText(legalEntity.name), address: (legalEntity.address ?? []).map(pdfText) },
    counterparty: { label: "Ship to", name: pdfText(customer || "Customer"), address: [] },
    fields: [
      { label: "Sales order", value: pdfText(order.orderNumber) },
      { label: "Shipped on", value: slip.postingDate },
    ],
    columns: [{ header: "Item" }, { header: "Order line", align: "R" }, { header: "Qty shipped", align: "R" }, { header: "Unit" }],
    lines: slip.lines.map((l, i) => {
      const line = order.lines.find((o) => o.id === l.orderLineId);
      return [pdfText(names[i] || "Item"), line ? String(line.lineNo) : "", l.qty, pdfText(line?.unit)];
    }),
  };
}
