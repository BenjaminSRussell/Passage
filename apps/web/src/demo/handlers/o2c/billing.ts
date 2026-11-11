// Billing service (/api/v1/billing) for the demo: the invoices behind the invoiced sales orders in model.ts, and
// the shipped order SO-024186 as a proposal that can be posted, and two credit notes against posted invoices.
import type { Invoice, InvoiceSummary } from "../../../modules/billing/api/client";
import { reply, route } from "../../backend";
import { day, table, uid } from "../../store";
import { invoices, K, r2, slips, type InvoiceRow } from "./model";

const BASE = "/api/v1/billing";

function strip({ settled: _settled, orderId: _orderId, ...invoice }: InvoiceRow): Invoice {
  return invoice;
}
function header({ lines: _lines, installments: _installments, credited: _credited, reason: _reason, ...rest }: Invoice): InvoiceSummary {
  return rest;
}

// Credit notes live apart from the invoices table, so the receivables and credit exposure built from it stay as seeded.
const dec = (n: number): string => n.toFixed(2);
const creditNotes = table<Invoice>([]);
function seedCreditNote(n: number, originalId: string, spec: { status: "PROPOSAL" | "POSTED"; ago: number; lineNo: number; qty: number; reason: string }): void {
  const original = invoices.get(originalId);
  const line = original?.lines.find((l) => l.lineNo === spec.lineNo);
  if (!original || !line || original.status === "PROPOSAL") return;
  const share = Math.min(1, spec.qty / Number(line.qty));
  const net = r2(Number(line.net) * share);
  const tax = r2(Number(line.tax) * share);
  const gross = r2(net + tax);
  const date = day(-spec.ago);
  creditNotes.insert({
    id: uid(K.invoice, 900 + n),
    legalEntityId: original.legalEntityId,
    customerId: original.customerId,
    invoiceAccountId: original.invoiceAccountId,
    type: "CREDIT_NOTE",
    status: spec.status,
    number: spec.status === "POSTED" ? `CN-${date.slice(2, 4)}-${String(310 + n * 3).padStart(5, "0")}` : undefined,
    currency: original.currency,
    invoiceDate: date,
    dueDate: date,
    postingDate: spec.status === "POSTED" ? date : undefined,
    paymentTermDays: 0,
    totals: { net: dec(net), charges: "0.00", tax: dec(tax), gross: dec(gross) },
    originalInvoiceId: original.id,
    originalNumber: original.number,
    reason: spec.reason,
    lines: [{ ...line, lineNo: 1, qty: String(spec.qty), net: dec(net), tax: dec(tax), originalLineNo: line.lineNo }],
    installments: [],
    version: spec.status === "POSTED" ? 2 : 1,
  });
  if (spec.status === "POSTED" && original.credited) {
    invoices.update(original.id, {
      credited: { gross: dec(Number(original.credited.gross) + gross), remaining: dec(Number(original.credited.remaining) - gross) },
      lines: original.lines.map((l) => (l.lineNo === line.lineNo ? { ...l, creditedQty: String(spec.qty) } : l)),
    });
  }
}
seedCreditNote(1, uid(K.invoice, 12), { status: "POSTED", ago: 3, lineNo: 1, qty: 1, reason: "One pump arrived with a cracked volute; the customer kept the rest of the shipment." });
seedCreditNote(2, uid(K.invoice, 40), { status: "PROPOSAL", ago: 1, lineNo: 1, qty: 1, reason: "Commissioning took one technician day, not two." });

route("GET", `${BASE}/invoices`, ({ query }) => {
  if (query.get("type") === "CREDIT_NOTE") {
    const items = creditNotes
      .all()
      .filter((c) => (!query.get("status") || c.status === query.get("status")) && (!query.get("originalInvoiceId") || c.originalInvoiceId === query.get("originalInvoiceId")) && (!query.get("customerId") || c.customerId === query.get("customerId")))
      .sort((a, b) => b.invoiceDate.localeCompare(a.invoiceDate));
    return { items: items.map(header) };
  }
  const rows = invoices
    .all()
    .filter((i) => (!query.get("status") || i.status === query.get("status")) && (!query.get("type") || i.type === query.get("type")) && (!query.get("customerId") || i.customerId === query.get("customerId")) && !query.get("originalInvoiceId") && (!query.get("orderId") || i.orderId === query.get("orderId")) && (!query.get("packingSlipId") || i.lines.some((l) => l.packingSlipId === query.get("packingSlipId"))))
    .sort((a, b) => b.invoiceDate.localeCompare(a.invoiceDate) || (b.number ?? "~").localeCompare(a.number ?? "~"));
  const limit = Number(query.get("limit") ?? 100);
  return { items: rows.slice(0, limit).map((r) => header(strip(r))) };
});

route("POST", `${BASE}/invoices/{id}:post`, ({ params, headers }) => {
  const inv = invoices.get(params.id);
  if (!inv) return reply.problem(404, "BIL-0404", "Invoice not found");
  const v = Number(/\d+/.exec(headers.get("If-Match") ?? "")?.[0]);
  if (v && v !== inv.version) return reply.problem(412, "BIL-0103", "The invoice changed since you opened it.");
  if (inv.status !== "PROPOSAL") return reply.problem(409, "BIL-0101", "Only a proposal can be posted.");
  const numbers = invoices.all().map((i) => Number(/(\d+)$/.exec(i.number ?? "")?.[1] ?? 0));
  const next = invoices.update(inv.id, {
    status: "POSTED",
    number: `INV-${day(0).slice(2, 4)}-${String(Math.max(...numbers) + 3).padStart(5, "0")}`,
    postingDate: day(0),
    version: inv.version + 1,
    credited: { gross: "0.00", remaining: inv.totals.gross },
  })!;
  // The packing slip of the order is now invoiced.
  for (const l of inv.lines) {
    const slip = l.packingSlipId ? slips.get(l.packingSlipId) : undefined;
    if (slip) slips.update(slip.id, { status: "INVOICED" });
  }
  return reply.ok(strip(next), { ETag: `"${next.version}"` });
});

route("POST", `${BASE}/credit-notes/{id}:post`, ({ params }) => {
  const cn = creditNotes.get(params.id);
  if (!cn) return reply.problem(404, "BIL-0404", "Credit note not found");
  if (cn.status !== "PROPOSAL") return reply.problem(409, "BIL-0101", "Only a proposal can be posted.");
  const next = creditNotes.update(cn.id, { status: "POSTED", number: `CN-${day(0).slice(2, 4)}-${String(330 + creditNotes.all().length * 3).padStart(5, "0")}`, postingDate: day(0), version: cn.version + 1 })!;
  return reply.ok(next, { ETag: `"${next.version}"` });
});

route("GET", `${BASE}/invoices/{id}`, ({ params }) => {
  const cn = creditNotes.get(params.id);
  if (cn) return reply.ok(cn, { ETag: `"${cn.version}"` });
  const inv = invoices.get(params.id);
  return inv ? reply.ok(strip(inv), { ETag: `"${inv.version}"` }) : reply.problem(404, "BIL-0404", "Invoice not found");
});
