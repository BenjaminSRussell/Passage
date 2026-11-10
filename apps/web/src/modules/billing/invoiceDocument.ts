import { entityLabel, legalEntityParty, pdfText, type DocumentRequest } from "../../shell/documents/pdfDocument";
import type { Invoice } from "./api/client";

/** The customer invoice (or credit note) as the PDF shows it: customer name instead of id. */
export async function invoiceDocument(invoice: Invoice): Promise<DocumentRequest> {
  const [legalEntity, customer] = await Promise.all([legalEntityParty(invoice.legalEntityId), entityLabel("customer", invoice.customerId)]);
  const credit = invoice.type === "CREDIT_NOTE";
  return {
    documentType: "CUSTOMER_INVOICE",
    number: pdfText(invoice.number ?? "Proposal"),
    legalEntity: { name: pdfText(legalEntity.name), address: (legalEntity.address ?? []).map(pdfText) },
    counterparty: { label: "Bill to", name: pdfText(customer || "Customer"), address: [] },
    fields: [
      { label: credit ? "Credit date" : "Invoice date", value: invoice.invoiceDate },
      ...(credit ? [] : [{ label: "Due date", value: invoice.dueDate }]),
      ...(credit && invoice.originalNumber ? [{ label: "Credits invoice", value: pdfText(invoice.originalNumber) }] : []),
      { label: "Currency", value: invoice.currency },
    ],
    columns: [
      { header: "Description" },
      { header: "Qty", align: "R" },
      { header: "Unit price", align: "R" },
      { header: "Net", align: "R" },
      { header: "Tax", align: "R" },
    ],
    lines: invoice.lines.map((l) => [pdfText(l.description), l.qty, l.unitPrice, l.net, l.tax]),
    totals: [
      { label: "Net", value: invoice.totals.net },
      ...(Number(invoice.totals.charges) !== 0 ? [{ label: "Charges", value: invoice.totals.charges }] : []),
      { label: "Tax", value: invoice.totals.tax },
      { label: `Total ${invoice.currency}`, value: invoice.totals.gross },
    ],
    notes: invoice.reason ? pdfText(invoice.reason) : undefined,
  };
}
