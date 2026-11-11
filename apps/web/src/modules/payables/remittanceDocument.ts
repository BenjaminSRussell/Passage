import { entityLabel, legalEntityParty, pdfText, type DocumentRequest } from "../../shell/documents/pdfDocument";
import type { RemittanceAdvice, RemittancePayment } from "./api/client";
import { formatDecimal, METHOD_LABELS, sumDecimals } from "./format";

const nonZero = (value: string) => Number(value) !== 0;

/**
 * One payment of a remittance advice as the PDF shows it: the vendor by name, the invoices the payment settled
 * and what was paid. The discount and tax withheld columns appear only when the payment has such amounts, so
 * the usual advice stays four columns wide.
 */
export async function remittanceDocument(advice: RemittanceAdvice, payment: RemittancePayment): Promise<DocumentRequest> {
  const [legalEntity, vendor] = await Promise.all([legalEntityParty(advice.legalEntityId), entityLabel("vendor", advice.vendorId)]);
  const amount = (value: string) => formatDecimal(value, payment.currency) ?? "";
  const discount = payment.lines.some((l) => nonZero(l.discount));
  const withheld = payment.lines.some((l) => nonZero(l.withheld));
  return {
    documentType: "REMITTANCE_ADVICE",
    number: pdfText(payment.endToEndId),
    legalEntity: { name: pdfText(legalEntity.name), address: (legalEntity.address ?? []).map(pdfText) },
    counterparty: { label: "Vendor", name: pdfText(vendor || "Vendor"), address: [] },
    fields: [
      { label: "Payment date", value: advice.paymentDate },
      { label: "Payment method", value: METHOD_LABELS[payment.paymentMethod] ?? payment.paymentMethod },
      { label: "Currency", value: payment.currency },
    ],
    columns: [
      { header: "Invoice" },
      { header: "Invoice date" },
      { header: "Amount open", align: "R" },
      ...(discount ? [{ header: "Discount", align: "R" as const }] : []),
      ...(withheld ? [{ header: "Tax withheld", align: "R" as const }] : []),
      { header: "Paid", align: "R" },
    ],
    lines: payment.lines.map((l) => [
      pdfText(l.invoiceNumber),
      l.invoiceDate,
      amount(l.openAmount),
      ...(discount ? [amount(l.discount)] : []),
      ...(withheld ? [amount(l.withheld)] : []),
      amount(l.paid),
    ]),
    totals: [
      ...(discount ? [{ label: "Discount taken", value: amount(sumDecimals(payment.lines.map((l) => l.discount))) }] : []),
      ...(withheld ? [{ label: "Tax withheld", value: amount(sumDecimals(payment.lines.map((l) => l.withheld))) }] : []),
      { label: `Total paid ${payment.currency}`, value: amount(payment.amount) },
    ],
  };
}
