export interface ProductStub {
  id: string;
  tenantId: string;
  version: number;
  ext: Record<string, unknown>;
  sku: string;
  description?: string;
  uom: string;
  active: boolean;
}

export type ProductStubInput = Omit<ProductStub, "id" | "tenantId" | "version" | "ext">;

const base = "/api/v1/product/product-stubs";

async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const listProductStub = () => call<ProductStub[]>("", "GET");
export const getProductStub = (id: string) => call<ProductStub>(`/${id}`, "GET");
export const createProductStub = (body: ProductStubInput, idempotencyKey: string) => call<ProductStub>("", "POST", { "Idempotency-Key": idempotencyKey }, body);
export const updateProductStub = (id: string, body: Partial<ProductStubInput>, ifMatch: string) => call<ProductStub>(`/${id}`, "PATCH", { "If-Match": ifMatch }, body);
export const removeProductStub = (id: string, ifMatch: string) => call<void>(`/${id}`, "DELETE", { "If-Match": ifMatch });
