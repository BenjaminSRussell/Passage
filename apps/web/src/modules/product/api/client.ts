const base = "/api/v1/product";

export type Lifecycle = "draft" | "active" | "phase_out" | "obsolete";
export type Subtype = "product" | "master" | "variant";
export type ProductType = "item" | "service";

export interface Product {
  id: string;
  productNumber: string;
  name: string;
  description: string;
  searchName: string;
  subtype: Subtype;
  productType: ProductType;
  baseUom: string;
  lifecycleState: Lifecycle;
  version: number;
}

export interface ProductPage {
  items: Product[];
  nextCursor?: string;
}

export interface NewProduct {
  productNumber: string;
  name: string;
  description?: string;
  baseUom: string;
  subtype: "product" | "master";
  productType: ProductType;
}

export interface ProductPatch {
  name?: string;
  description?: string;
  lifecycleState?: Lifecycle;
}

export class ProductApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string | undefined,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "ProductApiError";
  }
}

type Raw = Record<string, unknown>;

function pick(row: Raw, ...keys: string[]): string {
  for (const key of keys) {
    const value = row[key];
    if (typeof value === "string") return value;
  }
  return "";
}

export function toProduct(row: Raw): Product {
  return {
    id: pick(row, "id", "ID"),
    productNumber: pick(row, "productNumber", "ProductNumber"),
    name: pick(row, "name", "Name"),
    description: pick(row, "description", "Description"),
    searchName: pick(row, "searchName", "SearchName"),
    subtype: (pick(row, "subtype", "Subtype") || "product") as Subtype,
    productType: (pick(row, "productType", "ProductType") || "item") as ProductType,
    baseUom: pick(row, "baseUom", "BaseUOM"),
    lifecycleState: (pick(row, "lifecycleState", "LifecycleState") || "draft") as Lifecycle,
    version: Number(row.version ?? row.Version ?? 1),
  };
}

async function toError(res: Response): Promise<ProductApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const code = typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`;
  const message = typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`;
  const fields = body?.errors && typeof body.errors === "object" ? (body.errors as Record<string, string>) : {};
  return new ProductApiError(res.status, code, message, fields);
}

async function send<T>(path: string, init: RequestInit = {}, signal?: AbortSignal): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined) headers.set("Content-Type", "application/json");
  const res = await fetch(`${base}${path}`, { ...init, headers, credentials: "same-origin", signal });
  if (!res.ok) throw await toError(res);
  return (await res.json()) as T;
}

export async function listProducts(params: { q: string; cursor?: string; limit?: number }, signal?: AbortSignal): Promise<ProductPage> {
  const query = new URLSearchParams();
  if (params.q.trim() !== "") query.set("q", params.q.trim());
  if (params.cursor) query.set("cursor", params.cursor);
  if (params.limit) query.set("limit", String(params.limit));
  const text = query.toString();
  const suffix = text === "" ? "" : `?${text}`;
  const body = await send<{ items?: Raw[]; nextCursor?: string }>(`/products${suffix}`, {}, signal);
  return { items: (body.items ?? []).map(toProduct), nextCursor: body.nextCursor || undefined };
}

export async function getProduct(id: string, signal?: AbortSignal): Promise<Product> {
  return toProduct(await send<Raw>(`/products/${encodeURIComponent(id)}`, {}, signal));
}

export async function createProduct(input: NewProduct, key: string): Promise<Product> {
  const body = { ...input, description: input.description || undefined };
  return toProduct(await send<Raw>("/products", { method: "POST", body: JSON.stringify(body), headers: { "Idempotency-Key": key } }));
}

export async function updateProduct(id: string, version: number, patch: ProductPatch): Promise<Product> {
  return toProduct(
    await send<Raw>(`/products/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
      headers: { "If-Match": `"${version}"` },
    }),
  );
}
