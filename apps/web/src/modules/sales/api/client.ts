// Client for the sales service (services/sales/api/openapi.yaml).
// Browser calls use /api/v1/sales/..., which the web BFF rewrites to /v1/sales/....

export const base = "/api/v1/sales";

export const ORDER_TYPES = ["ORDER", "JOURNAL", "SUBSCRIPTION", "RETURNED"] as const;
export type OrderType = (typeof ORDER_TYPES)[number];

export const ORDER_STATUSES = ["DRAFT", "OPEN", "CONFIRMED", "PARTIALLY_SHIPPED", "SHIPPED", "INVOICED", "CLOSED", "CANCELLED"] as const;
export type OrderStatus = (typeof ORDER_STATUSES)[number];

export const HOLD_TYPES = ["CREDIT", "FRAUD", "MANUAL", "PRICE_REVIEW"] as const;
export type HoldType = (typeof HOLD_TYPES)[number];

/** Hold types a sales manager or fraud analyst applies by hand; CREDIT comes only from the credit check. */
export const MANUAL_HOLD_TYPES = ["MANUAL", "FRAUD", "PRICE_REVIEW"] as const;
export type ManualHoldType = (typeof MANUAL_HOLD_TYPES)[number];

export const LISTING_SCOPES = ["CUSTOMER", "GROUP"] as const;
export type ListingScope = (typeof LISTING_SCOPES)[number];

export const LISTING_TYPES = ["DENY", "ALLOW"] as const;
export type ListingType = (typeof LISTING_TYPES)[number];

export const AGREEMENT_STATUSES = ["ACTIVE", "EXHAUSTED"] as const;
export type AgreementStatus = (typeof AGREEMENT_STATUSES)[number];

export const LIMIT_TYPES = ["NONE", "BALANCE", "BALANCE_PACKING_SLIP", "BALANCE_ALL"] as const;
export type LimitType = (typeof LIMIT_TYPES)[number];

/** Decimal values travel as strings (never floats). */
export type Decimal = string;

export interface OrderSummary {
  id: string;
  legalEntityId: string;
  orderNumber: string;
  customerId: string;
  orderType: OrderType;
  status: OrderStatus;
  currency: string;
  requestedShipDate: string;
  channel: string;
  externalRef?: string;
  netTotal: Decimal;
  lineCount: number;
  activeHolds: HoldType[];
  version: number;
  createdAt: string;
}

export interface OrderPage {
  items: OrderSummary[];
  nextCursor?: string;
}

export interface OrderLine {
  id: string;
  lineNo: number;
  productId: string;
  quantity: Decimal;
  unit: string;
  siteId: string;
  warehouseId: string;
  unitPrice: Decimal;
  discPct: Decimal;
  lineNet: Decimal;
  requestedShipDate: string;
  confirmedShipDate?: string;
  lineStatus: string;
  /** Parent line id of a MANDATORY supplementary line added at commit (FR-27.14). */
  supplementaryOf?: string;
  /** Line number of that parent; the line follows it (BR-27.14). */
  parentLineNo?: number;
  supplementaryRuleId?: string;
}

/** A non-blocking message, such as SLS-0012 for each supplementary line an order received. */
export interface Notice {
  code: string;
  message: string;
  lineNo?: number;
}

export interface Hold {
  id: string;
  holdType: HoldType;
  reasonCode: string;
  status: "ACTIVE" | "RELEASED" | "CLEARED";
  orderAmount: Decimal;
  appliedBy: string;
  releasedBy?: string;
  releasedUntil?: string;
  appliedAt: string;
}

export interface CreditRule {
  id: string;
  result: "PASS" | "WARN" | "FAIL";
  value: string;
  threshold: string;
  note?: string;
}

export interface CreditEvaluation {
  outcome: "PASS" | "WARN" | "FAIL";
  rules: CreditRule[];
}

export interface Order {
  id: string;
  legalEntityId: string;
  orderNumber: string;
  customerId: string;
  invoiceAccountId: string;
  orderType: OrderType;
  status: OrderStatus;
  currency: string;
  requestedShipDate: string;
  channel: string;
  externalRef?: string;
  customerGroup?: string;
  netTotal: Decimal;
  version: number;
  creditCheck?: CreditEvaluation;
  lines: OrderLine[];
  holds: Hold[];
  /** Denied lines accepted with sales.order.approve and a reason (FR-27.15). */
  listingOverrides?: ListingOverride[];
  /** Set only on the result of a create, a quotation conversion or an agreement release. */
  warnings?: Notice[];
  createdAt: string;
  updatedAt: string;
}

export interface ListingOverride {
  lineNo: number;
  productId: string;
  decision: string;
  reason: string;
  actor: string;
}

/** Confirm and release answer with the order and whether it is now confirmed. */
export interface ConfirmResult extends Order {
  confirmed: boolean;
}

export interface NewOrderLine {
  productId: string;
  quantity: Decimal;
  unit: string;
  siteId: string;
  warehouseId: string;
  unitPrice: Decimal;
  discPct?: Decimal;
  requestedShipDate: string;
}

export interface NewOrder {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  orderType: OrderType;
  currency: string;
  requestedShipDate: string;
  channel?: string;
  externalRef?: string;
  customerGroup?: string;
  listingOverrideReason?: string;
  lines: NewOrderLine[];
}

export interface ProductListingInput {
  legalEntityId: string;
  scopeType: ListingScope;
  scopeKey: string;
  productId: string;
  listingType: ListingType;
  validFrom?: string;
  validTo?: string;
}

export interface ProductListing extends ProductListingInput {
  id: string;
  createdBy: string;
  createdAt: string;
}

export interface AgreementInput {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId?: string;
  productId: string;
  unit: string;
  unitPrice: Decimal;
  currency: string;
  commitmentQty: Decimal;
  validFrom?: string;
  validTo?: string;
}

export interface AgreementRelease {
  id: string;
  quantity: Decimal;
  salesOrderId: string;
  orderNumber: string;
  actor: string;
  releasedAt: string;
}

export interface Agreement extends Required<Omit<AgreementInput, "validFrom" | "validTo">> {
  id: string;
  agreementNumber: string;
  releasedQty: Decimal;
  remainingQty: Decimal;
  status: AgreementStatus;
  validFrom?: string;
  validTo?: string;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  /** Present on a single read, newest first. */
  releases?: AgreementRelease[];
}

export interface ReleaseOrderInput {
  quantity: Decimal;
  siteId: string;
  warehouseId: string;
  requestedShipDate: string;
}

export interface ReleaseOrderResult {
  agreement: Agreement;
  order: Order;
}

export interface Exposure {
  openAr?: Decimal;
  unapplied?: Decimal;
  deliveredNotInvoiced?: Decimal;
  openOrders?: Decimal;
  overdueAr?: Decimal;
  oldestItemDays?: number;
  dunningLevel?: number;
}

export interface CreditProfileInput {
  limit: Decimal;
  currency: string;
  limitType: LimitType;
  tolerancePct: Decimal;
  toleranceAmount: Decimal;
  oldestItemDaysMax: number;
  maxDunningLevel: number;
  reviewDate?: string;
  customerSince?: string;
  mandatoryCheck: boolean;
  exposure: Exposure;
}

export interface CreditProfile extends CreditProfileInput {
  legalEntityId: string;
  customerId: string;
  version: number;
  updatedAt: string;
}

export interface AtpPeriod {
  date: string;
  supply: Decimal;
  demand: Decimal;
}

export interface AtpCheckInput {
  requested: string;
  quantity: Decimal;
  completeOnly: boolean;
  periods: AtpPeriod[];
}

export interface AtpScheduleLine {
  date: string;
  quantity: Decimal;
  period: number;
}

export interface AtpCheckResult {
  promise: {
    promisedDate?: string;
    complete: boolean;
    backorder: Decimal;
    source: string;
    schedule: AtpScheduleLine[];
  };
  periods: AtpPeriod[];
  cumulative: Decimal[];
}

/** Statuses from which an order can still be changed or cancelled (BR-27.5). */
export const CHANGEABLE_STATUSES: readonly OrderStatus[] = ["DRAFT", "OPEN", "CONFIRMED"];

/** Line statuses that can still change or be cancelled (BR-27.5). */
export const CHANGEABLE_LINE_STATUSES = ["OPEN", "RESERVED"];

export interface LineChangeInput {
  lineNo: number;
  quantity?: Decimal;
  unitPrice?: Decimal;
  discPct?: Decimal;
  requestedShipDate?: string;
}

/** A PATCH body: send lines, requestedShipDate or both; absent fields keep their value. */
export interface ChangeOrderInput {
  reason?: string;
  requestedShipDate?: string;
  lines?: LineChangeInput[];
}

export interface FieldChange {
  /** 0 for a header field. */
  lineNo: number;
  field: string;
  old: string;
  new: string;
}

export interface OrderVersion {
  version: number;
  actor: string;
  reason: string;
  diff: FieldChange[];
  changedAt: string;
}

export interface ReleaseBucket {
  date: string;
  cumRequired: Decimal;
}

export interface ReleaseCheckInput {
  cumReceived: Decimal;
  cumShipped: Decimal;
  tolerance?: Decimal;
  buckets: ReleaseBucket[];
}

export interface ReleaseCheckResult {
  held: boolean;
  code?: string;
  serviceCode?: string;
  difference: Decimal;
  openQuantity: Decimal;
  lines: { date: string; quantity: Decimal }[];
}

export const BACKORDER_STRATEGIES = ["REDISTRIBUTE", "WIN", "GAIN", "FILL", "LOSE"] as const;
export type BackorderStrategy = (typeof BACKORDER_STRATEGIES)[number];

export interface BackorderLineInput {
  id: string;
  priorityClass: number;
  requestedDate: string;
  orderValue: Decimal;
  demand: Decimal;
  confirmed: Decimal;
}

export interface BackorderPreviewInput {
  strategy: BackorderStrategy;
  available: Decimal;
  loseThreshold?: number;
  lines: BackorderLineInput[];
}

export interface PlannedLine {
  id: string;
  before: Decimal;
  after: Decimal;
  changed: boolean;
  deconfirmed: boolean;
}

export interface BackorderPreviewResult {
  preview: boolean;
  strategy: BackorderStrategy;
  allocated: Decimal;
  deconfirmedCount: number;
  warning?: string;
  warningCode?: string;
  lines: PlannedLine[];
}

/** A value read together with the ETag that If-Match must echo back. */
export interface Versioned<T> {
  value: T;
  etag: string;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class SalesApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "SalesApiError";
  }
}

export interface Problem {
  status: number;
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof SalesApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function toError(res: Response): Promise<SalesApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new SalesApiError(res.status, code, message, fields);
}

async function send(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) {
    throw await toError(res);
  }
  return res;
}

async function json<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  return (await (await send(path, init, signal)).json()) as T;
}

async function versioned<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Versioned<T>> {
  const res = await send(path, init, signal);
  return { value: (await res.json()) as T, etag: res.headers.get("ETag") ?? "" };
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

/** A fresh Idempotency-Key for one user intent. */
export function newKey(): string {
  return crypto.randomUUID();
}

/** The ETag of a version, as the service writes it ("3"). */
export function etagOf(version: number): string {
  return `"${version}"`;
}

export interface OrderFilter {
  status?: OrderStatus | "";
  hold?: HoldType | "";
  customerId?: string;
  cursor?: string;
  limit?: number;
}

export function listOrders(filter: OrderFilter = {}, signal?: AbortSignal): Promise<OrderPage> {
  return json<OrderPage>(
    `/orders${query({ status: filter.status, hold: filter.hold, customerId: filter.customerId, cursor: filter.cursor, limit: filter.limit })}`,
    {},
    signal,
  );
}

export function getOrder(id: string, signal?: AbortSignal): Promise<Versioned<Order>> {
  return versioned<Order>(`/orders/${encodeURIComponent(id)}`, {}, signal);
}

export function createOrder(input: NewOrder, key: string = newKey()): Promise<Versioned<Order>> {
  return versioned<Order>("/orders", { method: "POST", body: JSON.stringify(input), headers: { "Idempotency-Key": key } });
}

/** Confirms at the given ETag. A failing credit check answers confirmed=false with a CREDIT hold. */
export function confirmOrder(id: string, etag: string): Promise<Versioned<ConfirmResult>> {
  return versioned<ConfirmResult>(`/orders/${encodeURIComponent(id)}:confirm`, { method: "POST", headers: { "If-Match": etag } });
}

/** Releases one ACTIVE hold; releasing the CREDIT hold of an unconfirmed order confirms it. */
export function releaseHold(orderId: string, holdId: string, etag: string): Promise<Versioned<ConfirmResult>> {
  return versioned<ConfirmResult>(`/orders/${encodeURIComponent(orderId)}/holds/${encodeURIComponent(holdId)}:release`, {
    method: "POST",
    headers: { "If-Match": etag },
  });
}

export async function listCreditProfiles(legalEntityId = "", signal?: AbortSignal): Promise<CreditProfile[]> {
  const body = await json<{ items?: CreditProfile[] }>(`/credit-profiles${query({ legalEntityId, limit: 200 })}`, {}, signal);
  return body.items ?? [];
}

export function getCreditProfile(legalEntityId: string, customerId: string, signal?: AbortSignal): Promise<Versioned<CreditProfile>> {
  return versioned<CreditProfile>(`/credit-profiles/${encodeURIComponent(customerId)}${query({ legalEntityId })}`, {}, signal);
}

/** Creates (etag "0") or replaces the profile at the given ETag. */
export function putCreditProfile(legalEntityId: string, customerId: string, input: CreditProfileInput, etag: string): Promise<Versioned<CreditProfile>> {
  return versioned<CreditProfile>(`/credit-profiles/${encodeURIComponent(customerId)}${query({ legalEntityId })}`, {
    method: "PUT",
    body: JSON.stringify(input),
    headers: { "If-Match": etag },
  });
}

/** Stateless ATP promise check over entered buckets (BR-27.1). */
export function atpCheck(input: AtpCheckInput): Promise<AtpCheckResult> {
  return json<AtpCheckResult>("/atp:check", { method: "POST", body: JSON.stringify(input) });
}

/** Changes an order at the given ETag (FR-27.9); answers with the order at its new version. */
export function changeOrder(id: string, etag: string, input: ChangeOrderInput): Promise<Versioned<Order>> {
  return versioned<Order>(`/orders/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(input), headers: { "If-Match": etag } });
}

/** Cancels an order that has not shipped (BR-27.5). */
export function cancelOrder(id: string, etag: string, reason: string): Promise<Versioned<Order>> {
  return versioned<Order>(`/orders/${encodeURIComponent(id)}:cancel`, { method: "POST", body: JSON.stringify({ reason }), headers: { "If-Match": etag } });
}

/** The order's change history, newest first. */
export async function listOrderVersions(id: string, signal?: AbortSignal): Promise<OrderVersion[]> {
  const body = await json<{ items?: OrderVersion[] }>(`/orders/${encodeURIComponent(id)}/versions`, {}, signal);
  return body.items ?? [];
}

/** Reconciles a scheduling-agreement release without storing it (BR-27.12). */
export function checkScheduleRelease(input: ReleaseCheckInput): Promise<ReleaseCheckResult> {
  return json<ReleaseCheckResult>("/schedule-releases:check", { method: "POST", body: JSON.stringify(input) });
}

/** Previews a backorder run; nothing is committed (BR-27.11). */
export function previewBackorderRun(input: BackorderPreviewInput): Promise<BackorderPreviewResult> {
  return json<BackorderPreviewResult>("/backorder-runs:preview", { method: "POST", body: JSON.stringify(input) });
}

/** Applies a MANUAL, FRAUD or PRICE_REVIEW hold at the given ETag (FR-27.8). */
export function applyHold(orderId: string, etag: string, holdType: ManualHoldType, reasonCode: string): Promise<Versioned<Order>> {
  return versioned<Order>(`/orders/${encodeURIComponent(orderId)}/holds`, {
    method: "POST",
    body: JSON.stringify({ holdType, reasonCode }),
    headers: { "If-Match": etag },
  });
}

export interface ListingFilter {
  legalEntityId?: string;
  productId?: string;
  scopeKey?: string;
}

/** The listing inquiry (BR-27.13), newest first. */
export async function listProductListings(filter: ListingFilter = {}, signal?: AbortSignal): Promise<ProductListing[]> {
  const body = await json<{ items?: ProductListing[] }>(
    `/product-listings${query({ legalEntityId: filter.legalEntityId, productId: filter.productId, scopeKey: filter.scopeKey, limit: 200 })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export function createProductListing(input: ProductListingInput): Promise<ProductListing> {
  return json<ProductListing>("/product-listings", { method: "POST", body: JSON.stringify(input) });
}

export async function deleteProductListing(id: string): Promise<void> {
  await send(`/product-listings/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export interface AgreementFilter {
  legalEntityId?: string;
  customerId?: string;
  status?: AgreementStatus | "";
}

export async function listAgreements(filter: AgreementFilter = {}, signal?: AbortSignal): Promise<Agreement[]> {
  const body = await json<{ items?: Agreement[] }>(
    `/agreements${query({ legalEntityId: filter.legalEntityId, customerId: filter.customerId, status: filter.status, limit: 200 })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export function getAgreement(id: string, signal?: AbortSignal): Promise<Versioned<Agreement>> {
  return versioned<Agreement>(`/agreements/${encodeURIComponent(id)}`, {}, signal);
}

export function createAgreement(input: AgreementInput, key: string = newKey()): Promise<Versioned<Agreement>> {
  return versioned<Agreement>("/agreements", { method: "POST", body: JSON.stringify(input), headers: { "Idempotency-Key": key } });
}

/** Draws quantity against the agreement and creates a DRAFT order at its price (FR-27.10). */
export function releaseAgreementOrder(id: string, input: ReleaseOrderInput, key: string = newKey()): Promise<ReleaseOrderResult> {
  return json<ReleaseOrderResult>(`/agreements/${encodeURIComponent(id)}:release-order`, {
    method: "POST",
    body: JSON.stringify(input),
    headers: { "Idempotency-Key": key },
  });
}

export const QUOTATION_STATUSES = ["DRAFT", "SENT", "CONVERTED", "LOST", "EXPIRED", "CANCELLED"] as const;
export type QuotationStatus = (typeof QUOTATION_STATUSES)[number];

export interface QuotationLine extends OrderLine {
  lineStatus: "OPEN" | "CONVERTED";
  /** The order a CONVERTED line became. */
  convertedOrderId?: string;
}

export interface Quotation {
  id: string;
  legalEntityId: string;
  quotationNumber: string;
  customerId: string;
  invoiceAccountId: string;
  currency: string;
  status: QuotationStatus;
  /** The last day the quotation can be sent or converted. */
  validUntil: string;
  probabilityPct: number;
  netTotal: Decimal;
  lostReason?: string;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  /** Filled on a single read, empty in the list. */
  lines: QuotationLine[];
  lineCount: number;
  openLines: number;
}

export interface NewQuotation {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId?: string;
  currency: string;
  validUntil?: string;
  probabilityPct?: number;
  lines: NewOrderLine[];
}

export interface QuotationFilter {
  legalEntityId?: string;
  customerId?: string;
  status?: QuotationStatus | "";
}

export interface ConvertQuotationInput {
  /** Quotation line numbers; empty converts every open line. */
  lineNos?: number[];
  listingOverrideReason?: string;
}

export interface QuotationConversion {
  quotation: Quotation;
  order: Order;
}

/** Quotations in the caller's scope, newest first, without lines. */
export async function listQuotations(filter: QuotationFilter = {}, signal?: AbortSignal): Promise<Quotation[]> {
  const body = await json<{ items?: Quotation[] }>(
    `/quotations${query({ legalEntityId: filter.legalEntityId, customerId: filter.customerId, status: filter.status, limit: 200 })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export function getQuotation(id: string, signal?: AbortSignal): Promise<Versioned<Quotation>> {
  return versioned<Quotation>(`/quotations/${encodeURIComponent(id)}`, {}, signal);
}

/** Stores a DRAFT quotation numbered SQ- (FR-27.1). */
export function createQuotation(input: NewQuotation, key: string = newKey()): Promise<Versioned<Quotation>> {
  return versioned<Quotation>("/quotations", { method: "POST", body: JSON.stringify(input), headers: { "Idempotency-Key": key } });
}

/** Moves a DRAFT quotation that is still valid to SENT. */
export function sendQuotation(id: string, etag: string): Promise<Versioned<Quotation>> {
  return versioned<Quotation>(`/quotations/${encodeURIComponent(id)}:send`, { method: "POST", headers: { "If-Match": etag } });
}

/** Marks a DRAFT or SENT quotation LOST with a reason. */
export function loseQuotation(id: string, etag: string, reason: string): Promise<Versioned<Quotation>> {
  return versioned<Quotation>(`/quotations/${encodeURIComponent(id)}:lose`, { method: "POST", body: JSON.stringify({ reason }), headers: { "If-Match": etag } });
}

/** Converts the selected open lines (all when none are given) into one DRAFT order (AC-27.6). */
export function convertQuotation(id: string, etag: string, input: ConvertQuotationInput): Promise<Versioned<QuotationConversion>> {
  return versioned<QuotationConversion>(`/quotations/${encodeURIComponent(id)}:convert`, {
    method: "POST",
    body: JSON.stringify(input),
    headers: { "If-Match": etag },
  });
}

/** One ACTIVE hold in the release queue. */
export interface HoldQueueItem {
  holdId: string;
  legalEntityId: string;
  orderId: string;
  orderNumber: string;
  /** The order's version; its ETag is what the release call must echo. */
  orderVersion: number;
  customerId: string;
  holdType: HoldType;
  reasonCode: string;
  amount: Decimal;
  currency: string;
  appliedBy: string;
  appliedAt: string;
  /** The failed rule of the last credit check (CREDIT holds). */
  failedRule?: CreditRule;
  creditLimit?: Decimal;
  limitCurrency?: string;
}

export interface HoldQueueFilter {
  legalEntityId?: string;
  holdType?: HoldType | "";
}

/** ACTIVE holds, oldest first (PRD §5.27.6 credit release queue). */
export async function listHolds(filter: HoldQueueFilter = {}, signal?: AbortSignal): Promise<HoldQueueItem[]> {
  const body = await json<{ items?: HoldQueueItem[] }>(
    `/holds${query({ legalEntityId: filter.legalEntityId, holdType: filter.holdType, limit: 200 })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export const SUPPLEMENTARY_REQUIREMENTS = ["MANDATORY", "OPTIONAL"] as const;
export type SupplementaryRequirement = (typeof SUPPLEMENTARY_REQUIREMENTS)[number];

export const SUPPLEMENTARY_PRICING = ["FREE", "PRICED"] as const;
export type SupplementaryPricing = (typeof SUPPLEMENTARY_PRICING)[number];

export interface SupplementaryRuleInput {
  legalEntityId: string;
  parentProductId: string;
  /** Absent for every customer of the legal entity. */
  customerId?: string;
  supplementaryProductId: string;
  unit: string;
  requirement: SupplementaryRequirement;
  baseQuantity: Decimal;
  quantityPerBase: Decimal;
  pricing: SupplementaryPricing;
  unitPrice?: Decimal;
}

export interface SupplementaryRule extends SupplementaryRuleInput {
  id: string;
  customerId?: string;
  unitPrice: Decimal;
}

export interface SupplementaryLine {
  ruleId: string;
  productId: string;
  unit: string;
  requirement: SupplementaryRequirement;
  quantity: Decimal;
  unitPrice: Decimal;
  supplementaryOf: string;
}

export interface SupplementaryEvaluationInput {
  legalEntityId: string;
  productId: string;
  customerId?: string;
  quantity: Decimal;
}

export interface SupplementaryRuleFilter {
  legalEntityId?: string;
  parentProductId?: string;
}

export async function listSupplementaryRules(filter: SupplementaryRuleFilter = {}, signal?: AbortSignal): Promise<SupplementaryRule[]> {
  const body = await json<{ items?: SupplementaryRule[] }>(
    `/supplementary-item-rules${query({ legalEntityId: filter.legalEntityId, parentProductId: filter.parentProductId, limit: 200 })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export function createSupplementaryRule(input: SupplementaryRuleInput): Promise<SupplementaryRule> {
  return json<SupplementaryRule>("/supplementary-item-rules", { method: "POST", body: JSON.stringify(input) });
}

export async function deleteSupplementaryRule(id: string): Promise<void> {
  await send(`/supplementary-item-rules/${encodeURIComponent(id)}`, { method: "DELETE" });
}

/** The supplementary lines a parent line triggers (FR-27.14); nothing is stored. */
export async function evaluateSupplementary(input: SupplementaryEvaluationInput): Promise<SupplementaryLine[]> {
  const body = await json<{ items?: SupplementaryLine[] }>("/supplementary-items:evaluate", { method: "POST", body: JSON.stringify(input) });
  return body.items ?? [];
}

export const PROPOSAL_REASONS = ["CROSS_SELL", "UP_SELL", "ACCESSORY"] as const;
export type ProposalReason = (typeof PROPOSAL_REASONS)[number];

export interface ProposalRuleInput {
  legalEntityId: string;
  orderedProductId: string;
  proposedProductId: string;
  /** Absent for every customer of the legal entity. */
  customerId?: string;
  reason: ProposalReason;
}

/** A product proposal rule (FR-27.21) with the responses recorded against it. */
export interface ProposalRule extends ProposalRuleInput {
  id: string;
  acceptedCount: number;
  declinedCount: number;
}

/** One product offered at line commit; it creates no line unless accepted. */
export interface Proposal {
  ruleId: string;
  orderedProductId: string;
  proposedProductId: string;
  reason: ProposalReason;
}

export interface ProposalEvaluationInput {
  legalEntityId: string;
  customerId?: string;
  /** The products being committed. */
  productIds: string[];
  /** The products already on the order. */
  currentProductIds?: string[];
}

export interface ProposalResponseInput {
  legalEntityId: string;
  ruleId: string;
  customerId?: string;
  orderId?: string;
  accepted: boolean;
}

export interface ProposalResponse extends ProposalResponseInput {
  id: string;
  orderedProductId: string;
  proposedProductId: string;
  reason: ProposalReason;
  respondedBy: string;
  respondedAt: string;
}

export interface ProposalRuleFilter {
  legalEntityId?: string;
  orderedProductId?: string;
}

export async function listProposalRules(filter: ProposalRuleFilter = {}, signal?: AbortSignal): Promise<ProposalRule[]> {
  const body = await json<{ items?: ProposalRule[] }>(
    `/product-proposal-rules${query({ legalEntityId: filter.legalEntityId, orderedProductId: filter.orderedProductId, limit: 200 })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export function createProposalRule(input: ProposalRuleInput): Promise<ProposalRule> {
  return json<ProposalRule>("/product-proposal-rules", { method: "POST", body: JSON.stringify(input) });
}

export async function deleteProposalRule(id: string): Promise<void> {
  await send(`/product-proposal-rules/${encodeURIComponent(id)}`, { method: "DELETE" });
}

/** The products to propose for the products being committed (FR-27.21); nothing is stored. */
export async function evaluateProposals(input: ProposalEvaluationInput, signal?: AbortSignal): Promise<Proposal[]> {
  const body = await json<{ items?: Proposal[] }>("/product-proposals:evaluate", { method: "POST", body: JSON.stringify(input) }, signal);
  return body.items ?? [];
}

/** Records the user's accept or decline of one proposal; a retry with the same key is counted once. */
export function respondToProposal(input: ProposalResponseInput, key: string = newKey()): Promise<ProposalResponse> {
  return json<ProposalResponse>("/product-proposals:respond", { method: "POST", body: JSON.stringify(input), headers: { "Idempotency-Key": key } });
}

export interface AllocationQuotaInput {
  legalEntityId: string;
  productId: string;
  /** Absent means every customer of the legal entity. */
  customerId?: string;
  periodStart: string;
  periodEnd: string;
  quotaQty: Decimal;
}

export interface AllocationConsumption {
  id: string;
  orderId: string;
  lineId: string;
  lineNo: number;
  quantity: Decimal;
  consumedAt: string;
  /** Set when the order or line was cancelled or changed. */
  returnedAt?: string;
}

export interface AllocationQuota extends Omit<AllocationQuotaInput, "quotaQty"> {
  id: string;
  quotaQty: Decimal;
  consumedQty: Decimal;
  remainingQty: Decimal;
  version: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  /** Present on a single read, newest first. */
  consumptions?: AllocationConsumption[];
}

export interface AllocationFilter {
  legalEntityId?: string;
  productId?: string;
  customerId?: string;
  onDate?: string;
}

/** The quota inquiry per product and period (FR-27.16). */
export async function listAllocationQuotas(filter: AllocationFilter = {}, signal?: AbortSignal): Promise<AllocationQuota[]> {
  const body = await json<{ items?: AllocationQuota[] }>(
    `/allocation-quotas${query({ legalEntityId: filter.legalEntityId, productId: filter.productId, customerId: filter.customerId, onDate: filter.onDate, limit: 200 })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export function getAllocationQuota(id: string, signal?: AbortSignal): Promise<Versioned<AllocationQuota>> {
  return versioned<AllocationQuota>(`/allocation-quotas/${encodeURIComponent(id)}`, {}, signal);
}

export function createAllocationQuota(input: AllocationQuotaInput, key: string = newKey()): Promise<Versioned<AllocationQuota>> {
  return versioned<AllocationQuota>("/allocation-quotas", { method: "POST", body: JSON.stringify(input), headers: { "Idempotency-Key": key } });
}

/** Sets a new quota quantity at the given ETag; below the consumed quantity is refused (BR-27.15). */
export function changeAllocationQuota(id: string, etag: string, quotaQty: Decimal): Promise<Versioned<AllocationQuota>> {
  return versioned<AllocationQuota>(`/allocation-quotas/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: JSON.stringify({ quotaQty }),
    headers: { "If-Match": etag },
  });
}
