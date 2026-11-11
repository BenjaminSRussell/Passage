// Vendor invoices in the receiving service (/api/v1/receiving/invoices) for the demo: invoices posted against the
// posted receipts of receiving.ts (matched, with a price variance, held for audit, self-billed and one rejected
// duplicate), and the captured documents not yet posted. Typed against the module's client so drift fails tsc.
import type { Capture, Invoice } from "../../../modules/ap-invoices/api/client";
import { LEGAL_ENTITY, VENDORS, itemByNumber } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, uid } from "../../store";
import { purchaseOrders } from "./purchasing";

const BASE = "/api/v1/receiving";
const K_INVOICE = 390;
const K_CAPTURE = 391;
const K_RECEIPT = 320;
const LEDGER = uid(363, 900);

/** Receipt n of receiving.ts, its purchase order and the item it received. */
const RECEIPTS: Record<number, { po: number; item: string; qty: number; ago: number }> = {
  3: { po: 4, item: "SEAL-MK-125", qty: 180, ago: 2 },
  4: { po: 5, item: "BRG-6206", qty: 1000, ago: 4 },
  5: { po: 3, item: "MTR-5HP-TEFC", qty: 24, ago: 10 },
  6: { po: 12, item: "FST-KIT-A", qty: 400, ago: 30 },
};
const vendorOf = (receipt: number): string => purchaseOrders.get(uid(302, RECEIPTS[receipt].po))?.vendorId ?? VENDORS[itemByNumber(RECEIPTS[receipt].item).vendor ?? 0].id;

interface Spec {
  number: string;
  receipt: number;
  unitPrice: number;
  invoiceAgo: number;
  status: Invoice["status"];
  source?: Invoice["source"];
  extra?: Partial<Invoice>;
}

function invoice(n: number, s: Spec): Invoice {
  const r = RECEIPTS[s.receipt];
  const cost = itemByNumber(r.item).cost;
  const amount = r2(s.unitPrice * r.qty);
  const variance = r2((s.unitPrice - cost) * r.qty);
  const within = Math.abs(s.unitPrice - cost) / cost <= 0.02;
  const lineStatus = variance === 0 ? "MATCHED" : "PRICE_VARIANCE";
  const posted = s.status === "POSTED";
  const postingDay = day(-s.invoiceAgo + 1);
  return {
    id: uid(K_INVOICE, n),
    vendorId: vendorOf(s.receipt),
    legalEntityId: LEGAL_ENTITY.id,
    invoiceNumber: s.number,
    invoiceDate: day(-s.invoiceAgo),
    postingDate: postingDay,
    currency: "USD",
    ledgerId: LEDGER,
    fiscalPeriodId: `FY${postingDay.slice(0, 4)}-P${postingDay.slice(5, 7)}`,
    total: dec(amount),
    ledgerEntryId: posted ? uid(364, 900 + n) : "",
    createdAt: at(-s.invoiceAgo + 1),
    status: s.status,
    source: s.source ?? "MANUAL",
    match: { status: lineStatus, withinTolerance: within, lines: [{ lineNo: 1, status: lineStatus, receivedQty: dec(r.qty, 0), priceVariance: { amount: dec(variance), currency: "USD" } }] },
    lines: [{ lineNo: 1, receiptId: uid(K_RECEIPT, s.receipt), receiptLineNo: 1, qty: dec(r.qty, 0), unitPrice: dec(s.unitPrice), amount: dec(amount), standardCost: true }],
    ...s.extra,
  };
}
const r2 = (n: number) => Math.round(n * 100) / 100;

const invoices: Invoice[] = [
  invoice(1, { number: "KSG-40391", receipt: 3, unitPrice: 52, invoiceAgo: 1, status: "AUDIT_BLOCKED", extra: { auditReason: "Unit price 7.2% above the purchase order, outside the 2% tolerance" } }),
  invoice(2, { number: "PBC-55120", receipt: 4, unitPrice: 7.4, invoiceAgo: 3, status: "POSTED", source: "ERS" }),
  invoice(3, { number: "VMW-121044", receipt: 5, unitPrice: 395, invoiceAgo: 8, status: "POSTED", extra: { auditReason: "Invoice over 5,000.00 needs a second approval", releasedBy: "marcus.lee", releasedAt: at(-6.8) } }),
  invoice(4, { number: "AFS-74402", receipt: 6, unitPrice: 12.8, invoiceAgo: 28, status: "POSTED" }),
  invoice(5, { number: "AFS-74402A", receipt: 6, unitPrice: 12.8, invoiceAgo: 21, status: "AUDIT_REJECTED", extra: { auditReason: "Possible duplicate", rejectedBy: "ana.ortiz", rejectedAt: at(-20), rejectReason: "Duplicate of AFS-74402, already posted" } }),
];

const captures: Capture[] = [
  { id: uid(K_CAPTURE, 1), legalEntityId: LEGAL_ENTITY.id, status: "CAPTURED", reviewReasons: [], vendorTaxId: "63-1187420", invoiceNumber: "IBF-88502", invoiceDate: day(-1), currency: "USD", total: "12096.00", createdAt: at(-0.4) },
  { id: uid(K_CAPTURE, 2), legalEntityId: LEGAL_ENTITY.id, status: "NEEDS_REVIEW", reviewReasons: ["LOW_CONFIDENCE", "TOTAL_MISMATCH"], vendorTaxId: "42-0987115", invoiceNumber: "RBS-90417", invoiceDate: day(-1), currency: "USD", total: "4550.00", createdAt: at(-0.7) },
  { id: uid(K_CAPTURE, 3), legalEntityId: LEGAL_ENTITY.id, status: "NEEDS_REVIEW", reviewReasons: ["NO_VENDOR_TAX_ID"], vendorTaxId: "", invoiceNumber: "100377", invoiceDate: day(-3), currency: "USD", total: "312.40", createdAt: at(-2.2) },
];

route("GET", `${BASE}/invoices`, ({ query }) => {
  const vendor = query.get("vendorId");
  const status = query.get("status");
  const items = invoices
    .filter((i) => (!vendor || i.vendorId === vendor) && (!status || i.status === status))
    .sort((a, b) => b.invoiceDate.localeCompare(a.invoiceDate) || b.invoiceNumber.localeCompare(a.invoiceNumber));
  return { items };
});

route("GET", `${BASE}/invoices/{id}`, ({ params }) => invoices.find((i) => i.id === params.id) ?? reply.problem(404, "RCV-0404", "Invoice not found"));

route("GET", `${BASE}/invoice-captures`, ({ query }) => {
  const status = query.get("status");
  return { items: captures.filter((c) => !status || c.status === status).sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
});
