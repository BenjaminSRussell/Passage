import {
  counterparty,
  entityLabel,
  legalEntityParty,
  orgUnitParty,
  pdfText,
  type Counterparty,
  type DocumentRequest,
} from "../../shell/documents/pdfDocument";
import { getOrder, type OneTimeParty } from "../sales/api/client";
import type { PackingSlip } from "./api/client";

/** A one-time customer's delivery address, which the order itself carries. */
function oneTimeShipTo(p: OneTimeParty): Counterparty {
  const address = [p.line1, p.line2, [p.postalCode, p.city].filter(Boolean).join(" "), p.region, p.country].map(pdfText).filter((l) => l !== "");
  return { party: { label: "Ship to", name: pdfText(p.name) || "Customer", address }, accountNumber: "" };
}

/**
 * The packing slip as the PDF shows it: the shipped quantities with the sales order's item names, the customer's
 * delivery address and the warehouse the goods left from. No prices or costs.
 */
export async function packingSlipDocument(slip: PackingSlip): Promise<DocumentRequest> {
  const { value: order } = await getOrder(slip.orderId);
  const le = order.legalEntityId;
  const orderLines = slip.lines.map((l) => order.lines.find((o) => o.id === l.orderLineId));
  const warehouses = [...new Set(orderLines.map((l) => l?.warehouseId).filter((id): id is string => Boolean(id)))];
  const [legalEntity, customer, shipFrom, ...names] = await Promise.all([
    legalEntityParty(le),
    order.oneTime
      ? oneTimeShipTo(order.oneTime)
      : counterparty("customer", order.customerId, { label: "Ship to", fallbackName: "Customer", purposes: ["delivery", "business"], legalEntityId: le }),
    // One warehouse ships the whole slip, or the slip names none.
    warehouses.length === 1 ? orgUnitParty("warehouses", warehouses[0], "Ship from", le) : null,
    ...orderLines.map((l) => entityLabel("product", l?.productId)),
  ]);
  return {
    documentType: "PACKING_SLIP",
    number: pdfText(slip.packingSlipNumber),
    legalEntity,
    counterparty: customer.party,
    ...(shipFrom ? { additionalParties: [shipFrom] } : {}),
    fields: [
      ...(customer.accountNumber ? [{ label: "Customer number", value: customer.accountNumber }] : []),
      { label: "Sales order", value: pdfText(order.orderNumber) },
      { label: "Shipped on", value: slip.postingDate },
    ],
    columns: [{ header: "Item" }, { header: "Order line", align: "R" }, { header: "Qty shipped", align: "R" }, { header: "Unit" }],
    lines: slip.lines.map((l, i) => {
      const line = orderLines[i];
      return [pdfText(names[i]) || "Item", line ? String(line.lineNo) : "", l.qty, pdfText(line?.unit)];
    }),
  };
}
