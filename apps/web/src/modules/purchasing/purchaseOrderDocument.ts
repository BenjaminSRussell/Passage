import { entityLabel, legalEntityParty, pdfText, type DocumentRequest } from "../../shell/documents/pdfDocument";
import type { PurchaseOrder } from "./api/client";

/** The purchase order as the PDF shows it: vendor and item names instead of ids. */
export async function purchaseOrderDocument(po: PurchaseOrder): Promise<DocumentRequest> {
  const [legalEntity, vendor, ...names] = await Promise.all([
    legalEntityParty(po.legalEntityId),
    entityLabel("vendor", po.vendorId),
    ...po.lines.map((l) => entityLabel("product", l.productId)),
  ]);
  return {
    documentType: "PURCHASE_ORDER",
    number: pdfText(po.number),
    legalEntity: { name: pdfText(legalEntity.name), address: (legalEntity.address ?? []).map(pdfText) },
    counterparty: { label: "Vendor", name: pdfText(vendor || "Vendor"), address: [] },
    fields: [
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
    lines: po.lines.map((l, i) => [pdfText(l.description || names[i] || `Line ${l.lineNo}`), l.deliveryDate, l.qty, pdfText(l.unit), l.unitPrice, l.lineAmount]),
    totals: [
      { label: "Subtotal", value: po.subtotal },
      ...(Number(po.discountTotal) !== 0 ? [{ label: "Discount", value: po.discountTotal }] : []),
      ...(Number(po.chargesTotal) !== 0 ? [{ label: "Charges", value: po.chargesTotal }] : []),
      { label: "Tax", value: po.taxTotal },
      { label: `Total ${po.currency}`, value: po.total },
    ],
  };
}
