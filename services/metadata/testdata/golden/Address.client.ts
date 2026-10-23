export interface Address {
  id: string;
  tenantId: string;
  version: number;
  ext: Record<string, unknown>;
  line1: string;
  line2?: string;
  city?: string;
  postalCode?: string;
  country: string;
}

export type AddressInput = Omit<Address, "id" | "tenantId" | "version" | "ext">;

const base = "/api/v1/platform/addresses";

async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const listAddress = () => call<Address[]>("", "GET");
export const getAddress = (id: string) => call<Address>(`/${id}`, "GET");
export const createAddress = (body: AddressInput, idempotencyKey: string) => call<Address>("", "POST", { "Idempotency-Key": idempotencyKey }, body);
export const updateAddress = (id: string, body: Partial<AddressInput>, ifMatch: string) => call<Address>(`/${id}`, "PATCH", { "If-Match": ifMatch }, body);
export const removeAddress = (id: string, ifMatch: string) => call<void>(`/${id}`, "DELETE", { "If-Match": ifMatch });
