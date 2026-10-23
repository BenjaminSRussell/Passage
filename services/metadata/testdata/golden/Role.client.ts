export interface Role {
  id: string;
  tenantId: string;
  version: number;
  ext: Record<string, unknown>;
  code: string;
  name: string;
}

export type RoleInput = Omit<Role, "id" | "tenantId" | "version" | "ext">;

const base = "/api/v1/platform/roles";

async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const listRole = () => call<Role[]>("", "GET");
export const getRole = (id: string) => call<Role>(`/${id}`, "GET");
export const createRole = (body: RoleInput, idempotencyKey: string) => call<Role>("", "POST", { "Idempotency-Key": idempotencyKey }, body);
export const updateRole = (id: string, body: Partial<RoleInput>, ifMatch: string) => call<Role>(`/${id}`, "PATCH", { "If-Match": ifMatch }, body);
export const removeRole = (id: string, ifMatch: string) => call<void>(`/${id}`, "DELETE", { "If-Match": ifMatch });
