// The invoice and statement PDFs (#30, FR-30.26). The BFF reads the document the way the app does (billing's or receivables'
// customer-scoped read) and turns it into print's document request; print renders it (POST /print/v1/documents:render) and stores
// nothing. These builders are pure: what the customer sees in the PDF is exactly what the read returned.

/** The invoice as billing's customer-portal read returns it. */
export interface PortalInvoice {
  id: string;
  type: string;
  status: string;
  number: string;
  currency: string;
  invoiceDate: string;
  dueDate: string;
  paymentTermDays: number;
  totals: { net: string; charges: string; tax: string; gross: string };
  originalNumber?: string;
  lines?: Array<{ lineNo: number; description: string; qty: string; unitPrice: string; discountPct: string; net: string; tax: string }>;
}

/** The statement as receivables' customer-portal read returns it. */
export interface PortalStatement {
  asOf: string;
  sections: Array<{
    currency: string;
    closingBalance: string;
    lines: Array<{ sourceType: string; documentNumber: string; documentDate: string; dueDate?: string; amount: string; openAmount: string; runningBalance: string }>;
  }>;
}

/** Print's DocumentRequest (services/print/api/openapi.yaml), the part these documents use. */
export interface DocumentRequest {
  documentType: "CUSTOMER_INVOICE" | "PERIODIC_STATEMENT";
  number: string;
  legalEntity: { name: string };
  title?: string;
  fields?: Array<{ label: string; value: string }>;
  columns: Array<{ header: string; align?: "L" | "R" }>;
  lines: string[][];
  totals?: Array<{ label: string; value: string }>;
  notes?: string;
  body?: string;
}

/** Print refuses a document of more than 500 lines; a statement longer than that is cut and says so. */
export const MAX_LINES = 500;

const TITLES: Record<string, string> = { INVOICE: "Invoice", CREDIT_NOTE: "Credit note", DEBIT_NOTE: "Debit note" };

const cut = (text: string, max: number): string => (text.length > max ? `${text.slice(0, max - 1)}…` : text);

/** The seller printed in the letterhead: the legal entity's customer-facing name, or a plain word when identity had none. */
export function sellerName(name: string | null): string {
  return name && name.trim() !== "" ? cut(name.trim(), 200) : "Your supplier";
}

export function invoiceDocument(invoice: PortalInvoice, seller: string | null): DocumentRequest {
  const lines = (invoice.lines ?? []).slice(0, MAX_LINES);
  const fields = [
    { label: "Invoice date", value: invoice.invoiceDate },
    { label: "Due date", value: invoice.dueDate },
    { label: "Payment terms", value: `${invoice.paymentTermDays} days` },
    { label: "Currency", value: invoice.currency },
    ...(invoice.originalNumber ? [{ label: "Credits invoice", value: invoice.originalNumber }] : []),
  ];
  const totals = [
    { label: "Net", value: invoice.totals.net },
    ...(Number(invoice.totals.charges) !== 0 ? [{ label: "Charges", value: invoice.totals.charges }] : []),
    { label: "Tax", value: invoice.totals.tax },
    { label: `Total ${invoice.currency}`, value: invoice.totals.gross },
  ];
  return {
    documentType: "CUSTOMER_INVOICE",
    number: cut(invoice.number, 200),
    legalEntity: { name: sellerName(seller) },
    title: TITLES[invoice.type] ?? "Invoice",
    fields,
    columns: [{ header: "Line", align: "R" }, { header: "Description" }, { header: "Quantity", align: "R" }, { header: "Unit price", align: "R" }, { header: "Discount %", align: "R" }, { header: "Net", align: "R" }, { header: "Tax", align: "R" }],
    lines: lines.map((l) => [String(l.lineNo), cut(l.description, 500), l.qty, l.unitPrice, l.discountPct, l.net, l.tax]),
    totals,
    ...((invoice.lines ?? []).length > MAX_LINES ? { notes: `The first ${MAX_LINES} of ${(invoice.lines ?? []).length} lines are shown.` } : {}),
  };
}

const SOURCE_LABELS: Record<string, string> = {
  INVOICE: "Invoice",
  FEE: "Dunning fee",
  INTEREST: "Interest note",
  DEDUCTION: "Deduction",
  PAYMENT: "Unapplied payment",
  ADJUSTMENT: "Adjustment",
  REBATE: "Rebate",
  CREDIT_NOTE: "Credit note",
};

export function statementDocument(statement: PortalStatement, seller: string | null): DocumentRequest {
  const all = statement.sections.flatMap((s) => s.lines.map((l) => ({ currency: s.currency, ...l })));
  const lines = all.slice(0, MAX_LINES);
  const balances = statement.sections.map((s) => `${s.closingBalance} ${s.currency}`).join(", ");
  return {
    documentType: "PERIODIC_STATEMENT",
    number: `Statement ${statement.asOf}`,
    legalEntity: { name: sellerName(seller) },
    fields: [{ label: "As of", value: statement.asOf }],
    body:
      statement.sections.length === 0
        ? `Statement of account as of ${statement.asOf}. Nothing is open on your account.`
        : `Statement of account as of ${statement.asOf}. The table lists every open item and the running balance in each currency. Closing balance: ${balances}.`,
    columns: [{ header: "Currency" }, { header: "Document" }, { header: "Kind" }, { header: "Date" }, { header: "Due" }, { header: "Amount", align: "R" }, { header: "Open", align: "R" }, { header: "Balance", align: "R" }],
    lines: lines.map((l) => [l.currency, cut(l.documentNumber, 500), SOURCE_LABELS[l.sourceType] ?? l.sourceType, l.documentDate, l.dueDate ?? "", l.amount, l.openAmount, l.runningBalance]),
    totals: statement.sections.slice(0, 20).map((s) => ({ label: `Closing balance ${s.currency}`, value: s.closingBalance })),
    ...(all.length > MAX_LINES ? { notes: `The first ${MAX_LINES} of ${all.length} lines are shown. Ask us for the full statement.` } : {}),
  };
}
