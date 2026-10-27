import type { AckDocument, AckStatus, AsnDocument, InvoiceDocument, MessageStatus, OutboundSet } from "./api/client";
import type { BadgeTone } from "../../shell/ui";

export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

/** CCYYMMDD as YYYY-MM-DD; anything else unchanged. */
export function formatEdiDate(value?: string): string {
  if (!value) return dash;
  return /^\d{8}$/.test(value) ? `${value.slice(0, 4)}-${value.slice(4, 6)}-${value.slice(6, 8)}` : value;
}

export const ACK_LABELS: Record<AckStatus, { label: string; tone: BadgeTone }> = {
  PENDING: { label: "Pending", tone: "neutral" },
  ACCEPTED: { label: "Accepted", tone: "success" },
  ACCEPTED_WITH_ERRORS: { label: "Accepted with errors", tone: "warning" },
  REJECTED: { label: "Rejected", tone: "danger" },
  TIMEOUT: { label: "Timed out", tone: "danger" },
};

export const MESSAGE_LABELS: Record<MessageStatus, { label: string; tone: BadgeTone }> = {
  RECEIVED: { label: "Received", tone: "neutral" },
  MAPPED: { label: "Mapped", tone: "success" },
  POSTED: { label: "Posted", tone: "success" },
  FAILED: { label: "Failed", tone: "danger" },
  SENT: { label: "Sent", tone: "info" },
  ACKED: { label: "Acknowledged", tone: "success" },
};

/** BEG01 purpose codes (BR-35.1). */
export const PURPOSES: Record<string, string> = { "00": "Original", "01": "Cancellation", "04": "Change" };

/** Outbound transaction set names. */
export const OUTBOUND_LABELS: Record<OutboundSet, string> = {
  "856": "856 advance ship notice",
  "810": "810 invoice",
  "855": "855 purchase order acknowledgment",
};

/** AK5/AK9 codes of a 997. */
export const AK_CODES: Record<string, string> = { A: "Accepted", E: "Accepted with errors", P: "Partially accepted", R: "Rejected" };

/** BAK02 acknowledgment types of an 855. */
export const ACK_TYPES: Record<string, string> = {
  AD: "Accepted, no change",
  AC: "Accepted with change",
  RD: "Rejected with detail",
  AK: "Accepted, no detail",
  RJ: "Rejected, no detail",
};

const asnExample: AsnDocument = {
  shipmentId: "LOAD42-SHIP7",
  shipDate: "20261021",
  shipTime: "0930",
  bol: "BOL9001",
  scac: "FDEG",
  grossWeight: "812.5",
  weightUnit: "KG",
  shipFrom: { name: "Plant", gln: "0614141000012" },
  shipTo: { name: "Distributor DC", gln: "0614141000029" },
  orders: [
    {
      poNumber: "PO1234",
      poDate: "20261015",
      packs: [{ sscc: "376104250021234569", items: [{ lineNo: "1", quantity: "10", uom: "EA", itemRefs: [{ qualifier: "BP", value: "ABC-1" }] }] }],
    },
  ],
};

const invoiceExample: InvoiceDocument = {
  number: "INV-7001",
  date: "20261101",
  poNumber: "PO1234",
  poDate: "20261015",
  shipDate: "20261021",
  terms: { code: "05", discountPct: "2", discountDays: "10", netDays: "30" },
  remitTo: { name: "Manufacturer", gln: "0614141000012" },
  billTo: { name: "Distributor", gln: "0614141000029" },
  shipTo: { name: "Distributor DC", gln: "0614141000029" },
  lines: [{ lineNo: "1", quantity: "10", uom: "EA", unitPrice: "12.50", itemRefs: [{ qualifier: "BP", value: "ABC-1" }] }],
  taxes: [{ type: "ST", amount: "10.00", percent: "8" }],
  charges: [{ indicator: "C", code: "D240", amount: "5.00" }],
  total: "140.00",
};

const ackExample: AckDocument = {
  poNumber: "PO1234",
  poDate: "20261015",
  ackDate: "20261016",
  ackType: "AC",
  lines: [
    { lineNo: "1", orderedQty: "10", uom: "EA", status: "IA", ackQty: "10" },
    { lineNo: "2", orderedQty: "5", uom: "EA", status: "IQ", ackQty: "4" },
  ],
};

/** A valid example document per outbound set, used to prefill the form. */
export const OUTBOUND_EXAMPLES: Record<OutboundSet, string> = {
  "856": JSON.stringify(asnExample, null, 2),
  "810": JSON.stringify(invoiceExample, null, 2),
  "855": JSON.stringify(ackExample, null, 2),
};
