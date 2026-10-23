export interface VendorStub {
  id: string;
  tenantId: string;
  legalEntityId: string;
  version: number;
  ext: Record<string, unknown>;
  account: string;
  name: string;
  status: "active" | "blocked" | "preferred";
  creditLimit?: string;
  creditLimitCurrency?: string;
  taxId?: string;
}

export type VendorStubInput = Omit<VendorStub, "id" | "tenantId" | "version" | "ext" | "legalEntityId">;

const base = "/api/v1/vendor/vendor-stubs";

async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const listVendorStub = () => call<VendorStub[]>("", "GET");
export const getVendorStub = (id: string) => call<VendorStub>(`/${id}`, "GET");
export const createVendorStub = (body: VendorStubInput, idempotencyKey: string) => call<VendorStub>("", "POST", { "Idempotency-Key": idempotencyKey }, body);
export const updateVendorStub = (id: string, body: Partial<VendorStubInput>, ifMatch: string) => call<VendorStub>(`/${id}`, "PATCH", { "If-Match": ifMatch }, body);
export const removeVendorStub = (id: string, ifMatch: string) => call<void>(`/${id}`, "DELETE", { "If-Match": ifMatch });
