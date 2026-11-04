// Client for the sales service (services/sales/api/openapi.yaml).
// Browser calls use /api/v1/sales/..., which the web BFF rewrites to /v1/sales/....

export const base = "/api/v1/sales";

export const ORDER_TYPES = ["ORDER", "JOURNAL", "SUBSCRIPTION", "RETURNED"] as const;
export type OrderType = (typeof ORDER_TYPES)[number];

/** The order types a legal entity may set as its default_order_type (PRD §5.27.13); RETURNED is not one. */
export const DEFAULT_ORDER_TYPES = ["ORDER", "JOURNAL", "SUBSCRIPTION"] as const;
export type DefaultOrderType = (typeof DEFAULT_ORDER_TYPES)[number];

/** How a line gets its confirmed ship date (FR-27.3, BR-27.2). ATP and CTP are PRD values the service refuses until their sources exist. */
export const DELIVERY_DATE_CONTROLS = ["NONE", "SALES_LEAD_TIME"] as const;
export type DeliveryDateControl = (typeof DELIVERY_DATE_CONTROLS)[number];

/** Where the credit rules run (FR-27.7). confirm is required: it is the only point that places a CREDIT hold. */
export const CREDIT_CHECK_POINTS = ["entry", "confirm", "release"] as const;
export type CreditCheckPoint = (typeof CREDIT_CHECK_POINTS)[number];
/** PRD check points the service refuses until fulfillment (packing slip) and billing (invoice) run them. */
export const INACTIVE_CREDIT_CHECK_POINTS = ["packing_slip", "invoice"] as const;

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

export const AGREEMENT_STATUSES = ["ACTIVE", "EXHAUSTED", "CLOSED"] as const;
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
  /** Confirmed ship date plus the customer's transport days, rolled to its next receiving day (BR-27.2). */
  confirmedReceiptDate?: string;
  /** The control the line was entered with; absent when the legal entity default applies. */
  deliveryDateControl?: DeliveryDateControl;
  lineStatus: string;
  /** Quantity covered by delivery reports; the line is PICKED while it is below quantity (BR-27.5). */
  deliveredQty?: Decimal;
  /** Quantity covered by invoice reports, never above deliveredQty. */
  invoicedQty?: Decimal;
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
  status: "ACTIVE" | "RELEASED" | "CLEARED" | "REJECTED";
  orderAmount: Decimal;
  appliedBy: string;
  releasedBy?: string;
  releasedUntil?: string;
  appliedAt: string;
  /** Set when a credit manager rejected the hold (the order was cancelled with it). */
  rejectedBy?: string;
  rejectedAt?: string;
  rejectionComment?: string;
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

/** The walk-in buyer of a one-time customer order (FR-27.22): kept on the order header for the invoice. */
export interface OneTimeParty {
  name: string;
  line1: string;
  line2?: string;
  city: string;
  region?: string;
  postalCode: string;
  /** ISO 3166 alpha-2. */
  country: string;
  taxId?: string;
}

export interface Order {
  /** Set only on the 202 result of a change that was stored for approval instead of applied. */
  pendingChange?: ChangeRequest;
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
  /** Set only on a one-time customer order (FR-27.22). */
  oneTime?: OneTimeParty;
  /** The customer's delivery terms copied at confirm (BR-27.2); they date every line's confirmedReceiptDate. Empty receivingDays: every day. */
  confirmedDeliveryTerms?: { transportDays: number; receivingDays: number[] };
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
  /** Omitted: the legal entity's delivery date control default applies at confirm. */
  deliveryDateControl?: DeliveryDateControl;
}

export interface NewOrder {
  legalEntityId: string;
  customerId: string;
  invoiceAccountId: string;
  /** Omitted: the legal entity's default order type applies. */
  orderType?: OrderType;
  currency: string;
  requestedShipDate: string;
  channel?: string;
  externalRef?: string;
  customerGroup?: string;
  listingOverrideReason?: string;
  /** Makes the order a one-time customer order held to the one-time order limit (SLS-0016). */
  oneTime?: OneTimeParty;
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
  /** How far a scheduling release's cumulative received quantity may differ from the shipped quantity (default 0). */
  cumTolerance?: Decimal;
}

export interface AgreementRelease {
  id: string;
  quantity: Decimal;
  salesOrderId: string;
  orderNumber: string;
  actor: string;
  releasedAt: string;
  /** Set when the order was cancelled and the quantity went back to the commitment. */
  returnedAt?: string;
}

export interface Agreement extends Required<Omit<AgreementInput, "validFrom" | "validTo" | "cumTolerance">> {
  id: string;
  agreementNumber: string;
  releasedQty: Decimal;
  remainingQty: Decimal;
  status: AgreementStatus;
  validFrom?: string;
  validTo?: string;
  /** Cumulative settings of scheduling releases (BR-27.12); absent on older responses. */
  cumTolerance?: Decimal;
  cumShipped?: Decimal;
  cumResetDate?: string;
  /** Set when the agreement was closed early. */
  closeReason?: string;
  closedBy?: string;
  closedAt?: string;
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
  /** Computed by the service from each line's delivered less invoiced quantity in the profile currency; a value sent is ignored. */
  deliveredNotInvoiced?: Decimal;
  /** Read-only: delivered, not invoiced goods in other currencies, not counted for want of an FX rate. */
  deliveredNotInvoicedOtherCurrencies?: { currency: string; amount: Decimal }[];
  /** Computed by the service from the confirmed orders in the profile currency; a value sent is ignored. */
  openOrders?: Decimal;
  /** Read-only: open orders in other currencies, not counted for want of an FX rate. */
  openOrdersOtherCurrencies?: { currency: string; amount: Decimal }[];
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

/**
 * A PATCH body: send lines, addLines, removeLines, requestedShipDate or any of
 * them; absent fields keep their value. A removed line is cancelled and keeps
 * its number; an added line is numbered after the order's highest.
 */
export interface ChangeOrderInput {
  reason?: string;
  requestedShipDate?: string;
  lines?: LineChangeInput[];
  addLines?: NewOrderLine[];
  removeLines?: number[];
  /** Accepts an added line the product listing denies; only a caller with sales.order.approve. */
  listingOverrideReason?: string;
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

/** Rejects an ACTIVE CREDIT hold of an unconfirmed order with a comment; the order is cancelled with it. */
export function rejectHold(orderId: string, holdId: string, etag: string, comment: string): Promise<Versioned<Order>> {
  return versioned<Order>(`/orders/${encodeURIComponent(orderId)}/holds/${encodeURIComponent(holdId)}:reject`, {
    method: "POST",
    body: JSON.stringify({ comment }),
    headers: { "If-Match": etag },
  });
}

/** reviewDueBy (YYYY-MM-DD) lists only profiles whose review date is on or before it, oldest first. */
export async function listCreditProfiles(legalEntityId = "", signal?: AbortSignal, reviewDueBy = ""): Promise<CreditProfile[]> {
  const body = await json<{ items?: CreditProfile[] }>(`/credit-profiles${query({ legalEntityId, reviewDueBy, limit: 200 })}`, {}, signal);
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

/** The per legal entity sales parameters (PRD §5.27.13), named as in the PRD. */
export interface SalesParametersInput {
  quotationExpiryDays: number;
  quotationProbabilityDefault: number;
  creditReleaseValidityDays: number;
  creditHoldSlaBusinessDays: number;
  defaultOrderType: DefaultOrderType;
  /** The most a one-time order may be worth, in oneTimeOrderLimitCurrency; both absent: 5,000 USD and 4,500 EUR. */
  oneTimeOrderLimit?: Decimal;
  oneTimeOrderLimitCurrency?: string;
  /** Where the credit rules run; the service defaults to confirm and release when absent. */
  creditCheckPoints?: CreditCheckPoint[];
  /** Days a (channel, externalRef) pair identifies one order, 1 to 90; the service defaults to 30 when absent. */
  intakeDedupWindowDays?: number;
  /** The control of a line without its own (FR-27.3); the service defaults to NONE when absent. */
  deliveryDateControlDefault?: DeliveryDateControl;
  /** Days SALES_LEAD_TIME adds to today, 0 to 365; the service defaults to 0 when absent. */
  salesLeadTimeDays?: number;
}

/** The parameters of a legal entity; version 0 means none were saved and these are the defaults. */
export interface SalesParameters extends SalesParametersInput {
  legalEntityId: string;
  version: number;
  updatedBy?: string;
  updatedAt?: string;
}

export function getSalesParameters(legalEntityId: string, signal?: AbortSignal): Promise<Versioned<SalesParameters>> {
  return versioned<SalesParameters>(`/parameters${query({ legalEntityId })}`, {}, signal);
}

/** Creates (etag "0") or replaces the parameters at the given ETag. */
export function putSalesParameters(legalEntityId: string, input: SalesParametersInput, etag: string): Promise<Versioned<SalesParameters>> {
  return versioned<SalesParameters>(`/parameters${query({ legalEntityId })}`, {
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

/** Closes what a partly shipped order, or one of its lines when lineNo is given, has not delivered (BR-27.5). */
export function cancelOrderRemainder(id: string, etag: string, reason: string, lineNo?: number): Promise<Versioned<Order>> {
  const path = lineNo ? `/orders/${encodeURIComponent(id)}/lines/${lineNo}:cancel-remainder` : `/orders/${encodeURIComponent(id)}:cancel-remainder`;
  return versioned<Order>(path, { method: "POST", body: JSON.stringify({ reason }), headers: { "If-Match": etag } });
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

/** A partial change of an agreement; an empty validFrom or validTo clears that bound. */
export interface AgreementRevisionInput {
  commitmentQty?: Decimal;
  unitPrice?: Decimal;
  cumTolerance?: Decimal;
  validFrom?: string;
  validTo?: string;
  reason?: string;
}

/** One entry of an agreement's history: a revision, the close, or quantity returned by a cancelled order. */
export interface AgreementVersion {
  version: number;
  kind: "REVISED" | "CLOSED" | "RETURNED";
  actor: string;
  reason: string;
  diff: FieldChange[];
  changedAt: string;
}

/** Revises the commitment, price, validity or tolerance at the agreement's ETag (FR-27.10). */
export function reviseAgreement(id: string, etag: string, input: AgreementRevisionInput): Promise<Versioned<Agreement>> {
  return versioned<Agreement>(`/agreements/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(input), headers: { "If-Match": etag } });
}

/** Closes the agreement early at its ETag; it takes no more releases. */
export function closeAgreement(id: string, etag: string, reason: string): Promise<Versioned<Agreement>> {
  return versioned<Agreement>(`/agreements/${encodeURIComponent(id)}:close`, { method: "POST", body: JSON.stringify({ reason }), headers: { "If-Match": etag } });
}

export async function listAgreementVersions(id: string, signal?: AbortSignal): Promise<AgreementVersion[]> {
  const body = await json<{ items?: AgreementVersion[] }>(`/agreements/${encodeURIComponent(id)}/versions`, {}, signal);
  return body.items ?? [];
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
  /** The order's status: only a CREDIT hold of a DRAFT or OPEN order can be rejected. */
  orderStatus: string;
  customerId: string;
  holdType: HoldType;
  reasonCode: string;
  amount: Decimal;
  currency: string;
  appliedBy: string;
  appliedAt: string;
  /** When the hold is due for release: appliedAt plus the legal entity's SLA in business days. */
  slaDueAt: string;
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

export interface AlternativeRuleInput {
  legalEntityId: string;
  orderedProductId: string;
  alternativeProductId: string;
  /** Absent for every customer of the legal entity. */
  customerId?: string;
  /** 1 to 99; 1 is offered first. */
  priority?: number;
  /** First and last requested date the rule applies to (YYYY-MM-DD). */
  validFrom?: string;
  validTo?: string;
}

/** An alternative item rule (FR-27.13). */
export interface AlternativeRule extends AlternativeRuleInput {
  id: string;
  priority: number;
}

export interface AlternativeRuleFilter {
  legalEntityId?: string;
  orderedProductId?: string;
}

export interface AlternativePromise {
  promisedDate?: string;
  complete: boolean;
  backorder: Decimal;
  source: string;
  schedule: AtpScheduleLine[];
}

export interface AlternativeEvaluationInput {
  legalEntityId: string;
  customerId?: string;
  productId: string;
  quantity: Decimal;
  requested: string;
  /** ATP buckets of the ordered product. */
  periods: AtpPeriod[];
  /** ATP buckets of the alternatives to check. */
  alternatives?: { productId: string; periods: AtpPeriod[] }[];
}

export interface AlternativeOffer {
  ruleId: string;
  alternativeProductId: string;
  priority: number;
  /** Quantity, at most the requested one, that ships by the requested date. */
  availableOnDate: Decimal;
  coversQuantity: boolean;
  promise: AlternativePromise;
}

export interface AlternativeEvaluation {
  ordered: { productId: string; availableOnDate: Decimal; coversQuantity: boolean; promise: AlternativePromise };
  /** True when the ordered product cannot ship the whole quantity by the requested date. */
  needed: boolean;
  offers: AlternativeOffer[];
  /** Applicable alternatives that came without ATP buckets. */
  unchecked: { ruleId: string; alternativeProductId: string; priority: number }[];
}

export async function listAlternativeRules(filter: AlternativeRuleFilter = {}, signal?: AbortSignal): Promise<AlternativeRule[]> {
  const body = await json<{ items?: AlternativeRule[] }>(
    `/alternative-item-rules${query({ legalEntityId: filter.legalEntityId, orderedProductId: filter.orderedProductId, limit: 200 })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export function createAlternativeRule(input: AlternativeRuleInput): Promise<AlternativeRule> {
  return json<AlternativeRule>("/alternative-item-rules", { method: "POST", body: JSON.stringify(input) });
}

export async function deleteAlternativeRule(id: string): Promise<void> {
  await send(`/alternative-item-rules/${encodeURIComponent(id)}`, { method: "DELETE" });
}

/** The alternatives that can ship what the ordered product cannot by the requested date (FR-27.13); nothing is stored. */
export function evaluateAlternatives(input: AlternativeEvaluationInput, signal?: AbortSignal): Promise<AlternativeEvaluation> {
  return json<AlternativeEvaluation>("/alternative-items:evaluate", { method: "POST", body: JSON.stringify(input) }, signal);
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

/** A customer's transport days and receiving weekdays (FR-27.3, BR-27.2). Weekdays are ISO: 1 Monday to 7 Sunday. */
export interface DeliveryTermsInput {
  legalEntityId: string;
  customerId: string;
  transportDays: number;
  receivingDays: number[];
}

export interface DeliveryTerms extends DeliveryTermsInput {
  id: string;
  version: number;
  updatedBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface DeliveryTermsFilter {
  legalEntityId?: string;
  customerId?: string;
}

export async function listDeliveryTerms(filter: DeliveryTermsFilter = {}, signal?: AbortSignal): Promise<DeliveryTerms[]> {
  const body = await json<{ items?: DeliveryTerms[] }>(
    `/delivery-terms${query({ legalEntityId: filter.legalEntityId, customerId: filter.customerId, limit: 200 })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export function getDeliveryTerms(id: string, signal?: AbortSignal): Promise<Versioned<DeliveryTerms>> {
  return versioned<DeliveryTerms>(`/delivery-terms/${encodeURIComponent(id)}`, {}, signal);
}

export function createDeliveryTerms(input: DeliveryTermsInput, key: string = newKey()): Promise<Versioned<DeliveryTerms>> {
  return versioned<DeliveryTerms>("/delivery-terms", { method: "POST", body: JSON.stringify(input), headers: { "Idempotency-Key": key } });
}

/** Replaces the transport days and weekdays at the given ETag. */
export function replaceDeliveryTerms(id: string, etag: string, input: Pick<DeliveryTermsInput, "transportDays" | "receivingDays">): Promise<Versioned<DeliveryTerms>> {
  return versioned<DeliveryTerms>(`/delivery-terms/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(input), headers: { "If-Match": etag } });
}

export const SCHEDULE_STATUSES = ["CONFIRMED", "BACKORDER", "UNPROMISED"] as const;
export type ScheduleStatus = (typeof SCHEDULE_STATUSES)[number];

/** One delivery slice of an order line (FR-27.6); no date while UNPROMISED. */
export interface ScheduleLine {
  id: string;
  lineNo: number;
  seq: number;
  quantity: Decimal;
  deliveryDate?: string;
  status: ScheduleStatus;
  /** LINE copies the line's confirmed ship date; ATP was placed by a promise. */
  source: "LINE" | "ATP";
  /** The ATP bucket a dated ATP slice drew on. */
  bucketDate?: string;
}

export interface PromiseLineInput {
  completeOnly: boolean;
  periods: AtpPeriod[];
}

export interface PromiseLineResult {
  order: Order;
  scheduleLines: ScheduleLine[];
}

export interface BackorderItem {
  scheduleLineId: string;
  legalEntityId: string;
  orderId: string;
  orderNumber: string;
  orderVersion: number;
  customerId: string;
  lineId: string;
  lineNo: number;
  productId: string;
  siteId: string;
  warehouseId: string;
  quantity: Decimal;
  requestedShipDate: string;
  promisedDate?: string;
  daysLate?: number;
  status: "BACKORDER" | "UNPROMISED";
}

export interface BackorderFilter {
  legalEntityId?: string;
  productId?: string;
  customerId?: string;
  promisedTo?: string;
}

/** Places a line of a confirmed order against ATP buckets and stores its schedule lines (FR-27.6). */
export function promiseLine(orderId: string, lineNo: number, etag: string, input: PromiseLineInput): Promise<Versioned<PromiseLineResult>> {
  return versioned<PromiseLineResult>(`/orders/${encodeURIComponent(orderId)}/lines/${lineNo}:promise`, {
    method: "POST",
    body: JSON.stringify(input),
    headers: { "If-Match": etag },
  });
}

/** The order's active schedule lines by line and sequence. */
export async function listScheduleLines(orderId: string, signal?: AbortSignal): Promise<ScheduleLine[]> {
  const body = await json<{ items?: ScheduleLine[] }>(`/orders/${encodeURIComponent(orderId)}/schedule-lines`, {}, signal);
  return body.items ?? [];
}

/** Lines promised after the requested date or not at all, by promised date (AC-27.8). */
export async function listBackorders(filter: BackorderFilter = {}, signal?: AbortSignal): Promise<BackorderItem[]> {
  const body = await json<{ items?: BackorderItem[] }>(
    `/backorders${query({ legalEntityId: filter.legalEntityId, productId: filter.productId, customerId: filter.customerId, promisedTo: filter.promisedTo, limit: 200 })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export const RELEASE_TYPES = ["FORECAST", "JIT"] as const;
export type ReleaseType = (typeof RELEASE_TYPES)[number];
export type ScheduleReleaseStatus = "ACCEPTED" | "HELD" | "REJECTED" | "SUPERSEDED";

export interface ScheduleReleaseLine {
  id: string;
  dueDate: string;
  quantity: Decimal;
  status: "OPEN" | "SUPERSEDED";
}

/** A stored scheduling-agreement release (FR-27.20); lines are present on a single read and after a write. */
export interface ScheduleRelease {
  id: string;
  agreementId: string;
  agreementNumber: string;
  releaseNo: string;
  releaseType: ReleaseType;
  status: ScheduleReleaseStatus;
  cumReceived: Decimal;
  cumShipped: Decimal;
  /** Shipped quantity of cumulative periods closed by a later reset date that still counts against this release. */
  shippedOffset?: Decimal;
  tolerance: Decimal;
  difference: Decimal;
  openQuantity: Decimal;
  code?: string;
  serviceCode?: string;
  buckets: ReleaseBucket[];
  lines?: ScheduleReleaseLine[];
  receivedBy: string;
  receivedAt: string;
  resolvedBy?: string;
  resolvedAt?: string;
  resolutionReason?: string;
  version: number;
}

export interface ScheduleReleaseInput {
  releaseNo: string;
  releaseType: ReleaseType;
  cumReceived: Decimal;
  buckets: ReleaseBucket[];
}

export async function listScheduleReleases(agreementId: string, signal?: AbortSignal): Promise<ScheduleRelease[]> {
  const body = await json<{ items?: ScheduleRelease[] }>(`/agreements/${encodeURIComponent(agreementId)}/schedule-releases?limit=200`, {}, signal);
  return body.items ?? [];
}

export function getScheduleRelease(agreementId: string, id: string, signal?: AbortSignal): Promise<Versioned<ScheduleRelease>> {
  return versioned<ScheduleRelease>(`/agreements/${encodeURIComponent(agreementId)}/schedule-releases/${encodeURIComponent(id)}`, {}, signal);
}

/** Stores a release; a cumulative mismatch answers 201 with status HELD, not an error (BR-27.12). */
export function createScheduleRelease(agreementId: string, input: ScheduleReleaseInput, key: string = newKey()): Promise<Versioned<ScheduleRelease>> {
  return versioned<ScheduleRelease>(`/agreements/${encodeURIComponent(agreementId)}/schedule-releases`, {
    method: "POST",
    body: JSON.stringify(input),
    headers: { "Idempotency-Key": key },
  });
}

/** Accepts (override) or rejects a HELD release at its ETag. */
export function resolveScheduleRelease(agreementId: string, id: string, etag: string, decision: "ACCEPT" | "REJECT", reason: string): Promise<Versioned<ScheduleRelease>> {
  return versioned<ScheduleRelease>(`/agreements/${encodeURIComponent(agreementId)}/schedule-releases/${encodeURIComponent(id)}:resolve`, {
    method: "POST",
    body: JSON.stringify({ decision, reason }),
    headers: { "If-Match": etag },
  });
}

/** Sets the agreement's cumulative shipped quantity at its ETag and recalculates the release in effect. */
export function recordAgreementShipped(agreementId: string, etag: string, cumShipped: Decimal, cumResetDate?: string): Promise<Versioned<Agreement>> {
  return versioned<Agreement>(`/agreements/${encodeURIComponent(agreementId)}:record-shipped`, {
    method: "POST",
    body: JSON.stringify({ cumShipped, ...(cumResetDate ? { cumResetDate } : {}) }),
    headers: { "If-Match": etag },
  });
}

export const BACKORDER_RUN_STATUSES = ["PREVIEW", "COMMITTED"] as const;
export type BackorderRunStatus = (typeof BACKORDER_RUN_STATUSES)[number];

export interface BackorderRunInput {
  legalEntityId: string;
  productId: string;
  siteId: string;
  warehouseId: string;
  strategy: BackorderStrategy;
  supply: Decimal;
  supplyDate: string;
  loseThreshold?: number;
  defaultPriorityClass?: number;
  priorityClasses?: Record<string, number>;
}

/** One open order line of a stored run: the quantity with a promised date now and after the run (FR-27.17). */
export interface BackorderRunLine {
  seq: number;
  orderId: string;
  orderNumber: string;
  orderVersion: number;
  lineId: string;
  lineNo: number;
  customerId: string;
  priorityClass: number;
  requestedDate: string;
  orderValue: Decimal;
  demand: Decimal;
  before: Decimal;
  after: Decimal;
  changed: boolean;
  deconfirmed: boolean;
  gainDate?: string;
}

export interface BackorderRun {
  id: string;
  legalEntityId: string;
  productId: string;
  siteId: string;
  warehouseId: string;
  strategy: BackorderStrategy;
  supply: Decimal;
  supplyDate: string;
  loseThreshold: number;
  defaultPriorityClass: number;
  status: BackorderRunStatus;
  allocated: Decimal;
  deconfirmedCount: number;
  changedCount: number;
  version: number;
  createdBy: string;
  createdAt: string;
  committedBy?: string;
  committedAt?: string;
  warning?: string;
  warningCode?: string;
  lines?: BackorderRunLine[];
}

export interface BackorderRunFilter {
  legalEntityId?: string;
  productId?: string;
  status?: BackorderRunStatus | "";
}

/** Stored backorder runs, newest first, without their lines. */
export async function listBackorderRuns(filter: BackorderRunFilter = {}, signal?: AbortSignal): Promise<BackorderRun[]> {
  const body = await json<{ items?: BackorderRun[] }>(
    `/backorder-runs${query({ legalEntityId: filter.legalEntityId, productId: filter.productId, status: filter.status, limit: 200 })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export function getBackorderRun(id: string, signal?: AbortSignal): Promise<Versioned<BackorderRun>> {
  return versioned<BackorderRun>(`/backorder-runs/${encodeURIComponent(id)}`, {}, signal);
}

/** Plans a run over the open lines held by the service and stores it as a PREVIEW; nothing changes until commit. */
export function createBackorderRun(input: BackorderRunInput, key: string = newKey()): Promise<Versioned<BackorderRun>> {
  return versioned<BackorderRun>("/backorder-runs", { method: "POST", body: JSON.stringify(input), headers: { "Idempotency-Key": key } });
}

/** Applies a PREVIEW run at its ETag in one transaction; an order changed since the preview is refused (SLS-0042). */
export function commitBackorderRun(id: string, etag: string): Promise<Versioned<BackorderRun>> {
  return versioned<BackorderRun>(`/backorder-runs/${encodeURIComponent(id)}:commit`, { method: "POST", headers: { "If-Match": etag } });
}

/** One quotation moved to EXPIRED by an expiry run. */
export interface ExpiredQuotation {
  id: string;
  quotationNumber: string;
  legalEntityId: string;
  customerId: string;
  validUntil: string;
  previousStatus: "DRAFT" | "SENT";
  version: number;
}

export interface ExpireResult {
  expired: ExpiredQuotation[];
  count: number;
  /** True when the 500-quotation batch limit stopped the run; run it again. */
  more: boolean;
}

/** Moves every DRAFT or SENT quotation past its validity to EXPIRED (FR-27.1). Repeating it is harmless. */
export async function expireQuotations(legalEntityId = "", signal?: AbortSignal): Promise<ExpireResult> {
  return json<ExpireResult>(`/quotations:expire${query({ legalEntityId })}`, { method: "POST" }, signal);
}

export interface PipelineCurrency {
  currency: string;
  openCount: number;
  openNet: Decimal;
  /** Sum of net total times win probability. */
  weightedNet: Decimal;
}

/** Workspace tiles and quotation pipeline KPIs (PRD §5.27.14). */
export interface SalesSummary {
  activeHolds: number;
  backorderLines: number;
  quotationsExpiring: number;
  quotationsLapsed: number;
  /** OPEN intake errors: channel orders refused at intake that nobody has closed. */
  openIntakeErrors: number;
  pipeline: PipelineCurrency[];
  windowDays: number;
  won: number;
  lost: number;
  expired: number;
  /** Percent at one place; absent when nothing closed in the window. */
  conversionRatePct?: Decimal;
}

/** Workspace scope and the pipeline KPI filters (status, expiry window, probability; PRD §5.27.14). */
export interface SalesSummaryFilter {
  legalEntityId?: string;
  windowDays?: number;
  /** Pipeline only: DRAFT or SENT; both when absent. */
  status?: "DRAFT" | "SENT";
  /** Pipeline only: the lowest win probability included. */
  minProbabilityPct?: number;
  /** Pipeline only: last valid day within this many days from today. */
  expiringWithinDays?: number;
}

export function getSalesSummary(filter: SalesSummaryFilter = {}, signal?: AbortSignal): Promise<SalesSummary> {
  const { legalEntityId, windowDays, status, minProbabilityPct, expiringWithinDays } = filter;
  return json<SalesSummary>(`/summary${query({ legalEntityId, windowDays, status, minProbabilityPct, expiringWithinDays })}`, {}, signal);
}

// ---- Change approval (FR-27.9) ----

export const CHANGE_STATUSES = ["PENDING", "APPROVED", "REJECTED", "WITHDRAWN"] as const;
export type ChangeStatus = (typeof CHANGE_STATUSES)[number];

/** A change of a confirmed order that waits for, or has had, a decision. */
export interface ChangeRequest {
  id: string;
  legalEntityId: string;
  orderId: string;
  orderNumber: string;
  customerId: string;
  /** CANCEL is the cancellation of the whole order (its change is empty); CHANGE a PATCH. */
  kind: "CHANGE" | "CANCEL";
  status: ChangeStatus;
  /** The order version the change was made against; approval needs the order still at it. */
  baseVersion: number;
  reason: string;
  change: ChangeOrderInput;
  diff: FieldChange[];
  requestedBy: string;
  requestedAt: string;
  decidedBy?: string;
  decidedAt?: string;
  decisionComment: string;
  appliedVersion?: number;
  version: number;
}

export interface ChangeRequestFilter {
  legalEntityId?: string;
  status?: ChangeStatus | "";
  orderId?: string;
}

export async function listChangeRequests(filter: ChangeRequestFilter = {}, signal?: AbortSignal): Promise<ChangeRequest[]> {
  const body = await json<{ items?: ChangeRequest[] }>(
    `/change-requests${query({ legalEntityId: filter.legalEntityId, status: filter.status, orderId: filter.orderId })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export function getChangeRequest(id: string, signal?: AbortSignal): Promise<Versioned<ChangeRequest>> {
  return versioned<ChangeRequest>(`/change-requests/${encodeURIComponent(id)}`, {}, signal);
}

/** Approves the request at its ETag; the change is applied to the order in the same call. */
export function approveChangeRequest(id: string, etag: string, comment: string): Promise<Versioned<{ request: ChangeRequest; order: Order }>> {
  return versioned(`/change-requests/${encodeURIComponent(id)}:approve`, { method: "POST", body: JSON.stringify({ comment }), headers: { "If-Match": etag } });
}

export function rejectChangeRequest(id: string, etag: string, comment: string): Promise<Versioned<ChangeRequest>> {
  return versioned<ChangeRequest>(`/change-requests/${encodeURIComponent(id)}:reject`, { method: "POST", body: JSON.stringify({ comment }), headers: { "If-Match": etag } });
}

/** The requester takes their own PENDING request back; anyone else is refused with SLS-0114. */
export function withdrawChangeRequest(id: string, etag: string, comment: string): Promise<Versioned<ChangeRequest>> {
  return versioned<ChangeRequest>(`/change-requests/${encodeURIComponent(id)}:withdraw`, { method: "POST", body: JSON.stringify({ comment }), headers: { "If-Match": etag } });
}

export interface ChangePolicy {
  legalEntityId: string;
  customerId: string;
  approvalRequired: boolean;
  /** A change moving the order total by less applies at once; absent when every change needs approval. */
  approvalThreshold?: string;
  thresholdCurrency?: string;
  /** The only users who may approve or reject the customer's requests; empty when any approver may. */
  approverIds?: string[];
  version: number;
  updatedBy?: string;
  updatedAt?: string;
}

/** What a PUT replaces: a member it names (null / [] clears); a member left out keeps its stored value. */
export interface ChangePolicyInput {
  approvalRequired: boolean;
  approvalThreshold?: string | null;
  thresholdCurrency?: string | null;
  approverIds?: string[];
}

export function getChangePolicy(legalEntityId: string, customerId: string, signal?: AbortSignal): Promise<Versioned<ChangePolicy>> {
  return versioned<ChangePolicy>(`/customers/${encodeURIComponent(customerId)}/change-policy${query({ legalEntityId })}`, {}, signal);
}

/** Creates (etag "0") or replaces the policy at the given ETag. */
export function putChangePolicy(legalEntityId: string, customerId: string, policy: ChangePolicyInput, etag: string): Promise<Versioned<ChangePolicy>> {
  return versioned<ChangePolicy>(`/customers/${encodeURIComponent(customerId)}/change-policy${query({ legalEntityId })}`, {
    method: "PUT",
    body: JSON.stringify(policy),
    headers: { "If-Match": etag },
  });
}

export const INTAKE_ERROR_STATUSES = ["OPEN", "RESOLVED"] as const;
export type IntakeErrorStatus = (typeof INTAKE_ERROR_STATUSES)[number];

/** A channel order refused at intake (PRD §5.27.14). The payload is not kept, only its hash. */
export interface IntakeError {
  id: string;
  legalEntityId: string;
  channel: string;
  externalRef?: string;
  requestHash: string;
  errorCode: string;
  message: string;
  status: IntakeErrorStatus;
  attempts: number;
  firstSeenAt: string;
  lastSeenAt: string;
  resolvedBy?: string;
  resolvedAt?: string;
  resolutionNote?: string;
  /** True while the refused request is kept so the error can be replayed (never returned). */
  replayable: boolean;
  replayCount: number;
  /** The order a successful replay stored or found. */
  replayedOrderId?: string;
  version: number;
}

export interface IntakeErrorFilter {
  legalEntityId?: string;
  channel?: string;
  status?: IntakeErrorStatus;
  errorCode?: string;
}

export async function listIntakeErrors(filter: IntakeErrorFilter = {}, signal?: AbortSignal): Promise<IntakeError[]> {
  const body = await json<{ items?: IntakeError[] }>(
    `/intake-errors${query({ legalEntityId: filter.legalEntityId, channel: filter.channel, status: filter.status, errorCode: filter.errorCode })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

export function getIntakeError(id: string, signal?: AbortSignal): Promise<Versioned<IntakeError>> {
  return versioned<IntakeError>(`/intake-errors/${encodeURIComponent(id)}`, {}, signal);
}

/** Closes an OPEN intake error at its ETag with a note (1 to 500 characters). */
export function resolveIntakeError(id: string, etag: string, note: string): Promise<Versioned<IntakeError>> {
  return versioned<IntakeError>(`/intake-errors/${encodeURIComponent(id)}:resolve`, { method: "POST", body: JSON.stringify({ note }), headers: { "If-Match": etag } });
}

/** The outcome of replaying one refused order (PRD §5.27.10). */
export interface IntakeReplayResult {
  intakeError: IntakeError;
  /** The order was stored and the intake error closed. */
  replayed: boolean;
  orderId?: string;
  orderNumber?: string;
  /** Why intake refused the order again; present when replayed is false. */
  failure?: { code: string; message: string };
}

/** Replays one OPEN intake error through order intake again. Intake refusing it again is still a result, not an error. */
export async function replayIntakeError(id: string): Promise<IntakeReplayResult> {
  return (await versioned<IntakeReplayResult>(`/intake-errors/${encodeURIComponent(id)}:replay`, { method: "POST" })).value;
}

export interface IntakeReplayBatch {
  attempted: number;
  replayed: number;
  failed: number;
  /** Dead letters (10 or more attempts) left for an operator. */
  skipped: number;
  items: IntakeReplayResult[];
}

/** Replays the open intake errors of the filter, oldest first. */
export function replayIntakeErrors(filter: Pick<IntakeErrorFilter, "legalEntityId" | "channel" | "errorCode"> = {}): Promise<IntakeReplayBatch> {
  return json<IntakeReplayBatch>(
    `/intake-errors:replay${query({ legalEntityId: filter.legalEntityId, channel: filter.channel, errorCode: filter.errorCode })}`,
    { method: "POST" },
  );
}

export type AutoCloseResultKind = "CLOSED" | "CANDIDATE" | "SKIPPED";

/** An order the auto-close looked at. */
export interface AutoCloseItem {
  orderId: string;
  orderNumber: string;
  legalEntityId: string;
  /** When the order last changed (the invoice that completed it, or a later change); the grace period runs from here. */
  lastChangedAt: string;
  version: number;
  result: AutoCloseResultKind;
}

export interface AutoCloseRun {
  closed: number;
  /** Orders that changed between the listing and the close and were left alone. */
  skipped: number;
  /** More candidates are left than the limit allowed. A scheduler repeats while this is true and the run closed something. */
  more: boolean;
  items: AutoCloseItem[];
}

export interface AutoCloseRequest {
  legalEntityId?: string;
  graceDays: number;
  limit: number;
  dryRun: boolean;
}

/** Closes the INVOICED orders unchanged for graceDays (BR-27.5), or lists them with dryRun. Repeating it is harmless. */
export function autoCloseOrders(request: AutoCloseRequest, signal?: AbortSignal): Promise<AutoCloseRun> {
  return json<AutoCloseRun>(
    `/orders:auto-close${query({ legalEntityId: request.legalEntityId, graceDays: String(request.graceDays), limit: String(request.limit), dryRun: String(request.dryRun) })}`,
    { method: "POST" },
    signal,
  );
}

export const PROGRESS_KINDS = ["DELIVERED", "INVOICED"] as const;
export type ProgressKind = (typeof PROGRESS_KINDS)[number];

/** One line of a stored delivery or invoice report: what it covered and the line after it. */
export interface OrderProgressLine {
  lineId: string;
  lineNo: number;
  quantity: Decimal;
  deliveredQty: Decimal;
  invoicedQty: Decimal;
  lineStatus: string;
}

/** A delivery or invoice recorded on an order (BR-27.5). */
export interface OrderProgress {
  id: string;
  legalEntityId: string;
  orderId: string;
  orderNumber: string;
  kind: ProgressKind;
  sourceRef: string;
  lines: OrderProgressLine[];
  orderStatus: string;
  orderVersion: number;
  recordedBy: string;
  recordedAt: string;
}

export interface OrderProgressFilter {
  legalEntityId?: string;
  orderId?: string;
  kind?: ProgressKind;
}

export interface RecordProgressInput {
  kind: ProgressKind;
  sourceRef: string;
  lines: { lineNo: number; quantity: Decimal }[];
}

export async function listOrderProgress(filter: OrderProgressFilter = {}, signal?: AbortSignal): Promise<OrderProgress[]> {
  const body = await json<{ items?: OrderProgress[] }>(
    `/order-progress${query({ legalEntityId: filter.legalEntityId, orderId: filter.orderId, kind: filter.kind })}`,
    {},
    signal,
  );
  return body.items ?? [];
}

/** Records a delivery or an invoice; the quantities are what this report covers, not cumulative. */
export function recordOrderProgress(orderId: string, input: RecordProgressInput, key: string = newKey()): Promise<OrderProgress> {
  return json<OrderProgress>(`/orders/${encodeURIComponent(orderId)}:record-progress`, {
    method: "POST",
    body: JSON.stringify(input),
    headers: { "Idempotency-Key": key },
  });
}
