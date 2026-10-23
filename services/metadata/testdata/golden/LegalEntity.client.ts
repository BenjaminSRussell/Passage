export interface LegalEntity {
  id: string;
  tenantId: string;
  version: number;
  ext: Record<string, unknown>;
  code: string;
  name: string;
  baseCurrency: string;
  country: string;
}

export type LegalEntityInput = Omit<LegalEntity, "id" | "tenantId" | "version" | "ext">;

const base = "/api/v1/platform/legal-entities";

async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const listLegalEntity = () => call<LegalEntity[]>("", "GET");
export const getLegalEntity = (id: string) => call<LegalEntity>(`/${id}`, "GET");
export const createLegalEntity = (body: LegalEntityInput, idempotencyKey: string) => call<LegalEntity>("", "POST", { "Idempotency-Key": idempotencyKey }, body);
export const updateLegalEntity = (id: string, body: Partial<LegalEntityInput>, ifMatch: string) => call<LegalEntity>(`/${id}`, "PATCH", { "If-Match": ifMatch }, body);
export const removeLegalEntity = (id: string, ifMatch: string) => call<void>(`/${id}`, "DELETE", { "If-Match": ifMatch });
