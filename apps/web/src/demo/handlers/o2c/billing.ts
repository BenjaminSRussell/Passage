// Billing service (/api/v1/billing) for the demo: the invoices behind the invoiced sales orders in model.ts, and
// the shipped order SO-024186 as a proposal that can be posted.
import type { Invoice, InvoiceSummary } from "../../../modules/billing/api/client";
import { reply, route } from "../../backend";
import { day } from "../../store";
import { invoices, slips, type InvoiceRow } from "./model";

const BASE = "/api/v1/billing";

function strip({ settled: _settled, orderId: _orderId, ...invoice }: InvoiceRow): Invoice {
  return invoice;
}
function header({ lines: _lines, installments: _installments, credited: _credited, reason: _reason, ...rest }: Invoice): InvoiceSummary {
  return rest;
}

route("GET", `${BASE}/invoices`, ({ query }) => {
  const rows = invoices
    .all()
    .filter((i) => (!query.get("status") || i.status === query.get("status")) && (!query.get("type") || i.type === query.get("type")) && (!query.get("customerId") || i.customerId === query.get("customerId")) && !query.get("originalInvoiceId"))
    .filter(() => query.get("type") !== "CREDIT_NOTE")
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

route("GET", `${BASE}/invoices/{id}`, ({ params }) => {
  const inv = invoices.get(params.id);
  return inv ? reply.ok(strip(inv), { ETag: `"${inv.version}"` }) : reply.problem(404, "BIL-0404", "Invoice not found");
});
