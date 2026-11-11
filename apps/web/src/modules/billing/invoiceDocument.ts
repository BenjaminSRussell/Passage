import { counterparty, legalEntityParty, pdfMultilineText, pdfText, type DocumentRequest } from "../../shell/documents/pdfDocument";
import type { Invoice } from "./api/client";

/** The customer invoice (or credit note) as the PDF shows it: the customer's name and invoice address instead of its id. */
export async function invoiceDocument(invoice: Invoice): Promise<DocumentRequest> {
  const le = invoice.legalEntityId;
  const [legalEntity, customer] = await Promise.all([
    legalEntityParty(le),
    counterparty("customer", invoice.customerId, { label: "Bill to", fallbackName: "Customer", purposes: ["invoice", "business"], legalEntityId: le }),
  ]);
  const credit = invoice.type === "CREDIT_NOTE";
  const notes = pdfMultilineText(invoice.reason);
  return {
    documentType: "CUSTOMER_INVOICE",
    number: pdfText(invoice.number) || "Proposal",
    legalEntity,
    counterparty: customer.party,
    fields: [
      ...(customer.accountNumber ? [{ label: "Customer number", value: customer.accountNumber }] : []),
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
    lines: invoice.lines.map((l) => [pdfMultilineText(l.description), l.qty, l.unitPrice, l.net, l.tax]),
    totals: [
      { label: "Net", value: invoice.totals.net },
      ...(Number(invoice.totals.charges) !== 0 ? [{ label: "Charges", value: invoice.totals.charges }] : []),
      { label: "Tax", value: invoice.totals.tax },
      { label: `Total ${invoice.currency}`, value: invoice.totals.gross },
    ],
    ...(notes ? { notes } : {}),
  };
}
