// Client for the trading-partner routes (FR-40.1, FR-40.4, BR-40.3) in
// services/intercompany/api/openapi.yaml. Percentages and amounts are decimal
// strings; the browser never does arithmetic on a float.
import { request } from "./client";

export type PricingPolicy = "PriceList" | "CostPlus" | "TransferPriceTable";

export interface PartnerActions {
  autoCreate: boolean;
  autoConfirm: boolean;
  autoPostPackingSlip: boolean;
  autoPostInvoice: boolean;
}

export interface Partner {
  id: string;
  version: number;
  sellerEntity: string;
  buyerEntity: string;
  /** IC customer in the seller entity that stands for the buyer. */
  customerAccount: string;
  /** IC vendor in the buyer entity that stands for the seller. */
  vendorAccount: string;
  active: boolean;
  actions: PartnerActions;
  pricing: PricingPolicy;
  markupPct: string;
  tolerance: { pricePct: string; dateDays: number };
  createdAt: string;
  updatedAt: string;
}

export interface PartnerPolicyInput {
  customerAccount?: string;
  vendorAccount?: string;
  active?: boolean;
  actions?: PartnerActions;
  pricing?: PricingPolicy;
  markupPct?: string;
  tolerance?: { pricePct?: string; dateDays?: number };
}

export interface CreatePartnerInput extends PartnerPolicyInput {
  sellerEntity: string;
  buyerEntity: string;
  customerAccount: string;
  vendorAccount: string;
}

export interface PartnerPage {
  items: Partner[];
  nextCursor?: string;
}

export interface PartnerQuery {
  entity?: string;
  active?: boolean;
  after?: string;
  limit?: number;
}

/** One page of the tenant's pairings, ordered by id. */
export async function listPartners(query: PartnerQuery = {}, signal?: AbortSignal): Promise<PartnerPage> {
  const params = new URLSearchParams();
  if (query.entity) params.set("entity", query.entity);
  if (query.active !== undefined) params.set("active", String(query.active));
  if (query.after) params.set("after", query.after);
  if (query.limit) params.set("limit", String(query.limit));
  const qs = params.toString();
  return (await (await request("GET", `/partners${qs ? `?${qs}` : ""}`, { signal })).json()) as PartnerPage;
}

export async function getPartner(id: string, signal?: AbortSignal): Promise<Partner> {
  return (await (await request("GET", `/partners/${encodeURIComponent(id)}`, { signal })).json()) as Partner;
}

/** Store a pairing. Send the same key again to retry one submission safely. */
export async function createPartner(input: CreatePartnerInput, idempotencyKey: string): Promise<Partner> {
  const res = await request("POST", "/partners", { body: input, headers: { "Idempotency-Key": idempotencyKey } });
  return (await res.json()) as Partner;
}

/** Change the policy of the pairing at `version`; a stale version fails with IC-0843 (412). */
export async function updatePartner(id: string, version: number, patch: PartnerPolicyInput): Promise<Partner> {
  const res = await request("PATCH", `/partners/${encodeURIComponent(id)}`, { body: patch, headers: { "If-Match": `"${version}"` } });
  return (await res.json()) as Partner;
}

/** A fresh Idempotency-Key for one create submission. */
export function newIdempotencyKey(): string {
  return globalThis.crypto.randomUUID();
}

export interface LineTransferPrice {
  item: string;
  method: "Cost" | "CostPlus" | "Table";
  markupPct?: string;
  unitPrice?: string;
  effectiveFrom: string;
}

export interface PriceLineRequest {
  item: string;
  date: string;
  currency: string;
  quantity: string;
  listPrice?: string;
  unitCost?: string;
  transferPrices?: LineTransferPrice[];
}

export interface PricedLine {
  partnerId: string;
  partnerVersion: number;
  item: string;
  quantity: string;
  currency: string;
  unitPrice: string;
  extended: string;
  basis: { policy: PricingPolicy; method: string; markupPct?: string; source: string; effectiveFrom?: string };
}

/** Price one IC line from the stored pairing's policy (FR-40.4). Nothing is stored. */
export async function priceLine(id: string, req: PriceLineRequest, signal?: AbortSignal): Promise<PricedLine> {
  return (await (await request("POST", `/partners/${encodeURIComponent(id)}/lines:price`, { body: req, signal })).json()) as PricedLine;
}

export interface OrderChangeRequest {
  oldQuantity: string;
  newQuantity: string;
  oldPrice: string;
  newPrice: string;
  oldDate: string;
  newDate: string;
}

export interface ChangeDecision {
  partnerId: string;
  partnerVersion: number;
  action: "UpdateSilently" | "ReApprove";
  code?: string;
  reasons: string[];
  dateShiftDays: number;
  tolerance: { pricePct: string; dateDays: number };
}

/** Check a change to a linked sales order against the pairing's tolerance (BR-40.3). */
export async function evaluateChange(id: string, req: OrderChangeRequest, signal?: AbortSignal): Promise<ChangeDecision> {
  return (await (await request("POST", `/partners/${encodeURIComponent(id)}/changes:evaluate`, { body: req, signal })).json()) as ChangeDecision;
}
