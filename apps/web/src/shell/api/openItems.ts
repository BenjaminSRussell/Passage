import { readProblem } from "./http";

const BASE = "/api/v1/payables";

export const OPEN_ITEM_STATUSES = ["OPEN", "PARTIALLY_SETTLED", "SETTLED", "REVERSED"] as const;
export type OpenItemStatus = (typeof OPEN_ITEM_STATUSES)[number];

export interface OpenItem {
  id: string;
  legalEntityId: string;
  vendorId: string;
  sourceInvoiceId?: string;
  invoiceNumber: string;
  invoiceDate: string;
  dueDate: string;
  dueDateSource?: "EVENT" | "DEFAULTED";
  currency: string;
  amount: string;
  openAmount: string;
  status: OpenItemStatus;
  paymentHold: boolean;
  paymentHoldReason?: string;
  proposalId?: string;
}

export const OPEN_ITEMS_BY_INVOICE_LIMIT = 50;

/** Open items payables created from the given receiving invoices (one entity, at most 50 invoices). */
export async function fetchOpenItemsForInvoices(legalEntityId: string, invoiceIds: readonly string[]): Promise<OpenItem[]> {
  const params = new URLSearchParams({ legal_entity: legalEntityId, source_invoices: invoiceIds.join(","), limit: String(OPEN_ITEMS_BY_INVOICE_LIMIT) });
  const res = await fetch(`${BASE}/open-items?${params}`, { headers: { Accept: "application/json" } });
  if (!res.ok) throw await readProblem(res);
  return ((await res.json()) as { items: OpenItem[] }).items;
}
