export interface Tenant {
  id: string;
  tenantId: string;
  version: number;
  ext: Record<string, unknown>;
  slug: string;
  region: string;
  tier: "starter" | "professional" | "enterprise";
}

export type TenantInput = Omit<Tenant, "id" | "tenantId" | "version" | "ext">;

const base = "/api/v1/platform/tenants";

async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const listTenant = () => call<Tenant[]>("", "GET");
export const getTenant = (id: string) => call<Tenant>(`/${id}`, "GET");
export const createTenant = (body: TenantInput, idempotencyKey: string) => call<Tenant>("", "POST", { "Idempotency-Key": idempotencyKey }, body);
export const updateTenant = (id: string, body: Partial<TenantInput>, ifMatch: string) => call<Tenant>(`/${id}`, "PATCH", { "If-Match": ifMatch }, body);
export const removeTenant = (id: string, ifMatch: string) => call<void>(`/${id}`, "DELETE", { "If-Match": ifMatch });
