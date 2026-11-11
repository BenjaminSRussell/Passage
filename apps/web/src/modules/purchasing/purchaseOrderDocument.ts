import { counterparty, entityLabel, legalEntityParty, orgUnitParty, pdfMultilineText, pdfText, type DocumentRequest } from "../../shell/documents/pdfDocument";
import type { PurchaseOrder } from "./api/client";

/**
 * The purchase order as the PDF shows it: the vendor's name and address and the item names instead of ids, and the
 * warehouse (or site) the order delivers to.
 */
export async function purchaseOrderDocument(po: PurchaseOrder): Promise<DocumentRequest> {
  const le = po.legalEntityId;
  const [legalEntity, vendor, deliverTo, ...names] = await Promise.all([
    legalEntityParty(le),
    counterparty("vendor", po.vendorId, { label: "Vendor", fallbackName: "Vendor", purposes: ["business", "remit_to"], legalEntityId: le }),
    orgUnitParty("warehouses", po.warehouseId, "Deliver to", le).then((w) => w ?? orgUnitParty("sites", po.siteId, "Deliver to", le)),
    ...po.lines.map((l) => entityLabel("product", l.productId)),
  ]);
  return {
    documentType: "PURCHASE_ORDER",
    number: pdfText(po.number),
    legalEntity,
    counterparty: vendor.party,
    ...(deliverTo ? { additionalParties: [deliverTo] } : {}),
    fields: [
      ...(vendor.accountNumber ? [{ label: "Vendor number", value: vendor.accountNumber }] : []),
      { label: "Currency", value: po.currency },
      { label: "Version", value: String(po.versionNo) },
    ],
    columns: [
      { header: "Item" },
      { header: "Delivery" },
      { header: "Qty", align: "R" },
      { header: "Unit", align: "L" },
      { header: "Unit price", align: "R" },
      { header: "Amount", align: "R" },
    ],
    lines: po.lines.map((l, i) => [pdfMultilineText(l.description) || pdfText(names[i]) || `Line ${l.lineNo}`, l.deliveryDate, l.qty, pdfText(l.unit), l.unitPrice, l.lineAmount]),
    totals: [
      { label: "Subtotal", value: po.subtotal },
      ...(Number(po.discountTotal) !== 0 ? [{ label: "Discount", value: po.discountTotal }] : []),
      ...(Number(po.chargesTotal) !== 0 ? [{ label: "Charges", value: po.chargesTotal }] : []),
      { label: "Tax", value: po.taxTotal },
      { label: `Total ${po.currency}`, value: po.total },
    ],
  };
}
