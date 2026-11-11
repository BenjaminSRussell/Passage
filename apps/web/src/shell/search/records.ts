import { z } from "zod";
import { apiFetch } from "../api/http";

/** The record types the search service indexes (services/search). */
export const RECORD_TYPES = ["party", "product", "purchase_order", "receipt", "vendor_invoice", "sales_order", "packing_slip", "customer_invoice", "production_order", "worker"] as const;
export type RecordType = (typeof RECORD_TYPES)[number];

/** Group headings in the palette; plural, sentence case. */
export const RECORD_TYPE_LABEL: Record<RecordType, string> = {
  party: "Customers and vendors",
  product: "Products",
  purchase_order: "Purchase orders",
  receipt: "Receipts",
  vendor_invoice: "Vendor invoices",
  sales_order: "Sales orders",
  packing_slip: "Packing slips",
  customer_invoice: "Customer invoices",
  production_order: "Production orders",
  worker: "Workers",
};

/** Singular name, for screen readers. */
export const RECORD_TYPE_NOUN: Record<RecordType, string> = {
  party: "Party",
  product: "Product",
  purchase_order: "Purchase order",
  receipt: "Receipt",
  vendor_invoice: "Vendor invoice",
  sales_order: "Sales order",
  packing_slip: "Packing slip",
  customer_invoice: "Customer invoice",
  production_order: "Production order",
  worker: "Worker",
};

export interface RecordHit {
  type: RecordType;
  id: string;
  number: string;
  title: string;
  subtitle?: string;
  status?: string;
  /** Web route of the record, as the service built it. */
  link: string;
}

export type RecordSearch = (query: string, signal: AbortSignal) => Promise<RecordHit[]>;

export const MIN_RECORD_QUERY = 2;
const RESULT_LIMIT = 20;

const SearchResponse = z.object({
  results: z.array(
    z.object({
      type: z.string(),
      id: z.string(),
      number: z.string(),
      title: z.string().default(""),
      subtitle: z.string().optional(),
      status: z.string().optional(),
      link: z.string(),
    }),
  ),
});

const isRecordType = (value: string): value is RecordType => (RECORD_TYPES as readonly string[]).includes(value);

/** Only in-app paths are followed; an absolute or protocol-relative link is dropped. */
export const isAppPath = (link: string): boolean => link.startsWith("/") && !link.startsWith("//");

/** GET /api/v1/search: records of the caller's tenant and legal entities, ranked by the service. */
export const searchRecords: RecordSearch = async (query, signal) => {
  const params = new URLSearchParams({ q: query.trim(), limit: String(RESULT_LIMIT) });
  const res = await apiFetch(`/api/v1/search?${params.toString()}`, { headers: { Accept: "application/json" }, signal });
  if (!res.ok) {
    throw new Error(`record search failed with status ${res.status}`);
  }
  const body = SearchResponse.parse(await res.json());
  return body.results.flatMap((r) => (isRecordType(r.type) && isAppPath(r.link) ? [{ ...r, type: r.type }] : []));
};
