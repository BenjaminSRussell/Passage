import { z } from "zod";
import { readProblem } from "./http";

const BASE = "/api/v1/payables";

export const OPEN_ITEM_STATUSES = ["OPEN", "PARTIALLY_SETTLED", "SETTLED", "REVERSED"] as const;
export type OpenItemStatus = (typeof OPEN_ITEM_STATUSES)[number];

const openItemSchema = z.object({
  id: z.string(),
  legalEntityId: z.string(),
  vendorId: z.string(),
  sourceInvoiceId: z.string().optional(),
  invoiceNumber: z.string(),
  invoiceDate: z.string(),
  dueDate: z.string(),
  dueDateSource: z.enum(["EVENT", "DEFAULTED"]).optional(),
  currency: z.string(),
  amount: z.string(),
  openAmount: z.string(),
  status: z.enum(OPEN_ITEM_STATUSES),
  paymentHold: z.boolean(),
  paymentHoldReason: z.string().optional(),
  proposalId: z.string().optional(),
});
export type OpenItem = z.infer<typeof openItemSchema>;

export const OPEN_ITEMS_BY_INVOICE_LIMIT = 50;

/** Open items payables created from the given receiving invoices (one entity, at most 50 invoices). */
export async function fetchOpenItemsForInvoices(legalEntityId: string, invoiceIds: readonly string[]): Promise<OpenItem[]> {
  const params = new URLSearchParams({ legal_entity: legalEntityId, source_invoices: invoiceIds.join(","), limit: String(OPEN_ITEMS_BY_INVOICE_LIMIT) });
  const res = await fetch(`${BASE}/open-items?${params}`, { headers: { Accept: "application/json" } });
  if (!res.ok) throw await readProblem(res);
  return z.object({ items: z.array(openItemSchema) }).parse(await res.json()).items;
}
