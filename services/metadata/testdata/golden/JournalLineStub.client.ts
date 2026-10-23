export interface JournalLineStub {
  id: string;
  tenantId: string;
  legalEntityId: string;
  version: number;
  ext: Record<string, unknown>;
  journalEntryId: string;
  account: string;
  amount: string;
  postingDate: string;
  memo?: string;
}

export type JournalLineStubInput = Omit<JournalLineStub, "id" | "tenantId" | "version" | "ext" | "legalEntityId">;

const base = "/api/v1/ledger/journal-line-stubs";

async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const listJournalLineStub = () => call<JournalLineStub[]>("", "GET");
export const getJournalLineStub = (id: string) => call<JournalLineStub>(`/${id}`, "GET");
export const createJournalLineStub = (body: JournalLineStubInput, idempotencyKey: string) => call<JournalLineStub>("", "POST", { "Idempotency-Key": idempotencyKey }, body);
export const updateJournalLineStub = (id: string, body: Partial<JournalLineStubInput>, ifMatch: string) => call<JournalLineStub>(`/${id}`, "PATCH", { "If-Match": ifMatch }, body);
export const removeJournalLineStub = (id: string, ifMatch: string) => call<void>(`/${id}`, "DELETE", { "If-Match": ifMatch });
