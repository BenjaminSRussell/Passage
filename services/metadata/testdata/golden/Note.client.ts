export interface Note {
  id: string;
  tenantId: string;
  version: number;
  ext: Record<string, unknown>;
  body: string;
  authorId: string;
}

export type NoteInput = Omit<Note, "id" | "tenantId" | "version" | "ext">;

const base = "/api/v1/platform/notes";

async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const listNote = () => call<Note[]>("", "GET");
export const getNote = (id: string) => call<Note>(`/${id}`, "GET");
export const createNote = (body: NoteInput, idempotencyKey: string) => call<Note>("", "POST", { "Idempotency-Key": idempotencyKey }, body);
export const updateNote = (id: string, body: Partial<NoteInput>, ifMatch: string) => call<Note>(`/${id}`, "PATCH", { "If-Match": ifMatch }, body);
export const removeNote = (id: string, ifMatch: string) => call<void>(`/${id}`, "DELETE", { "If-Match": ifMatch });
