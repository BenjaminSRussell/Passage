// Client for the assets service (services/assets/api/openapi.yaml).
// Browser calls use /api/v1/..., which the web BFF rewrites to /v1/....

export const base = "/api/v1";

/** Decimal values travel as strings (never floats). */
export type Decimal = string;

export const ASSET_STATUSES = ["NotYetAcquired", "Open", "Sold", "Scrapped", "Transferred"] as const;
export type AssetStatus = (typeof ASSET_STATUSES)[number];

export const METHODS = ["StraightLine", "DecliningBalance"] as const;
export type Method = (typeof METHODS)[number];

/** Conventions each method accepts (services/assets/internal/domain/depreciation.go). */
export const CONVENTIONS: Record<Method, readonly string[]> = {
  StraightLine: ["None", "FullMonth", "MidMonth15"],
  DecliningBalance: ["None", "HalfYear"],
};

export const PAYMENTS_PER_YEAR = [1, 2, 4, 12] as const;

export interface Asset {
  id: string;
  legalEntityId: string;
  assetNumber: string;
  name: string;
  classCode: string;
  barcode: string | null;
  quantity: Decimal;
  status: AssetStatus;
  acquiredDate: string | null;
  bookCount: number;
  version: number;
  createdAt: string;
}

export interface Book {
  id: string;
  assetId: string;
  legalEntityId: string;
  bookCode: string;
  currency: string;
  method: Method;
  factor: Decimal;
  switchToStraightLine: boolean;
  serviceLifePeriods: number;
  salvageValue: Decimal;
  convention: string;
  inServiceDate: string;
  cost: Decimal;
  accumulatedDepreciation: Decimal;
  nbv: Decimal;
  status: "Open" | "Sold" | "Scrapped";
}

export interface AssetDetail extends Asset {
  books: Book[];
}

export interface Page<T> {
  items: T[];
  nextCursor?: string;
}

export interface ScheduleLine {
  index: number;
  period: string;
  amount: Decimal;
  accumulated: Decimal;
  nbv: Decimal;
}

export interface Schedule {
  bookId: string;
  bookCode: string;
  currency: string;
  total: Decimal;
  lines: ScheduleLine[];
}

export interface Transaction {
  id: string;
  assetBookId: string;
  bookCode: string;
  currency: string;
  type: "Acquisition" | "DisposalSale" | "DisposalScrap";
  transactionDate: string;
  costAmount: Decimal;
  accumulatedAmount: Decimal;
  proceedsAmount: Decimal | null;
  gainLoss: Decimal | null;
  sourceType: string;
  createdAt: string;
}

export interface Disposal {
  asset: Asset;
  books: Book[];
  transactions: Transaction[];
}

export interface LeaseLine {
  periodNo: number;
  opening: Decimal;
  interest: Decimal;
  payment: Decimal;
  closing: Decimal;
  rouDepreciation: Decimal;
}

export interface Lease {
  id: string;
  legalEntityId: string;
  leaseNumber: string;
  commencementDate: string;
  termMonths: number;
  paymentsPerYear: number;
  paymentAmount: Decimal;
  paymentTiming: "arrears" | "advance";
  ibrRate: Decimal;
  currency: string;
  liability: Decimal;
  rouAsset: Decimal;
  status: "Draft" | "Active" | "Modified" | "Terminated";
  version: number;
  createdAt: string;
  schedule?: LeaseLine[];
}

export interface NewAsset {
  legalEntityId: string;
  assetNumber: string;
  name: string;
  classCode: string;
  barcode?: string;
  quantity?: Decimal;
}

export interface NewBook {
  bookCode: string;
  currency: string;
  method: Method;
  factor?: Decimal;
  switchToStraightLine: boolean;
  serviceLifePeriods: number;
  salvageValue?: Decimal;
  convention: string;
  inServiceDate: string;
  cost: Decimal;
}

export interface DisposeInput {
  kind: "sale" | "scrap";
  percentage: Decimal;
  proceeds?: Decimal;
  transactionDate?: string;
}

export interface NewLease {
  legalEntityId: string;
  leaseNumber: string;
  commencementDate: string;
  termMonths: number;
  paymentsPerYear: number;
  paymentAmount: Decimal;
  paymentTiming: "arrears" | "advance";
  ibrRate: Decimal;
  currency: string;
}

/** A value read together with the ETag that If-Match must echo back. */
export interface Versioned<T> {
  value: T;
  etag: string;
}

/** A failed call, carrying the RFC 9457 code and any field errors. */
export class AssetsApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "AssetsApiError";
  }
}

export interface Problem {
  status: number;
  code: string;
  message: string;
  fields: Record<string, string>;
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof AssetsApiError) {
    return { status: error.status, code: error.code, message: error.message, fields: error.fields };
  }
  return { status: 0, code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

export function newKey(): string {
  return crypto.randomUUID();
}

async function toError(res: Response): Promise<AssetsApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message =
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new AssetsApiError(res.status, code, message, fields);
}

async function send(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return res;
}

async function json<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  return (await (await send(path, init, signal)).json()) as T;
}

function query(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

function post<T>(path: string, body: unknown, key: string, extra: Record<string, string> = {}): Promise<T> {
  return json<T>(path, { method: "POST", body: JSON.stringify(body), headers: { "Idempotency-Key": key, ...extra } });
}

const id = encodeURIComponent;

export interface AssetFilter {
  status?: AssetStatus | "";
  after?: string;
}

export function listAssets(filter: AssetFilter = {}, signal?: AbortSignal): Promise<Page<Asset>> {
  return json(`/assets${query({ status: filter.status, after: filter.after })}`, {}, signal);
}

export async function getAsset(assetId: string, signal?: AbortSignal): Promise<Versioned<AssetDetail>> {
  const res = await send(`/assets/${id(assetId)}`, {}, signal);
  return { value: (await res.json()) as AssetDetail, etag: res.headers.get("ETag") ?? "" };
}

export function createAsset(input: NewAsset, key: string = newKey()): Promise<Asset> {
  return post(`/assets`, input, key);
}

export function addBook(assetId: string, input: NewBook, key: string = newKey()): Promise<Book> {
  return post(`/assets/${id(assetId)}/books`, input, key);
}

export function schedulePath(assetId: string, bookId: string): string {
  return `${base}/assets/${id(assetId)}/schedule?book=${id(bookId)}`;
}

export function getSchedule(assetId: string, bookId: string, signal?: AbortSignal): Promise<Schedule> {
  return json(`/assets/${id(assetId)}/schedule${query({ book: bookId })}`, {}, signal);
}

export async function listTransactions(assetId: string, signal?: AbortSignal): Promise<Transaction[]> {
  return (await json<{ items: Transaction[] }>(`/assets/${id(assetId)}/transactions`, {}, signal)).items;
}

export function disposeAsset(assetId: string, etag: string, input: DisposeInput, key: string = newKey()): Promise<Disposal> {
  return post(`/assets/${id(assetId)}/dispose`, input, key, { "If-Match": etag });
}

export function listLeases(after?: string, signal?: AbortSignal): Promise<Page<Lease>> {
  return json(`/leases${query({ after })}`, {}, signal);
}

export function getLease(leaseId: string, signal?: AbortSignal): Promise<Lease> {
  return json(`/leases/${id(leaseId)}`, {}, signal);
}

export function createLease(input: NewLease, key: string = newKey()): Promise<Lease> {
  return post(`/leases`, input, key);
}
