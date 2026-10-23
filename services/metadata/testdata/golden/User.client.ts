export interface User {
  id: string;
  tenantId: string;
  version: number;
  ext: Record<string, unknown>;
  subject: string;
  displayName: string;
  email?: string;
  principalType: "human" | "service" | "agent";
  active: boolean;
}

export type UserInput = Omit<User, "id" | "tenantId" | "version" | "ext">;

const base = "/api/v1/platform/users";

async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const listUser = () => call<User[]>("", "GET");
export const getUser = (id: string) => call<User>(`/${id}`, "GET");
export const createUser = (body: UserInput, idempotencyKey: string) => call<User>("", "POST", { "Idempotency-Key": idempotencyKey }, body);
export const updateUser = (id: string, body: Partial<UserInput>, ifMatch: string) => call<User>(`/${id}`, "PATCH", { "If-Match": ifMatch }, body);
export const removeUser = (id: string, ifMatch: string) => call<void>(`/${id}`, "DELETE", { "If-Match": ifMatch });
