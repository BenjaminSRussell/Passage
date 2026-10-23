export interface Attachment {
  id: string;
  tenantId: string;
  version: number;
  ext: Record<string, unknown>;
  ownerId: string;
  fileName: string;
  sizeBytes: number;
  contentType: string;
}

export type AttachmentInput = Omit<Attachment, "id" | "tenantId" | "version" | "ext">;

const base = "/api/v1/platform/attachments";

async function call<T>(path: string, method: string, headers: Record<string, string> = {}, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await res.json();
  return (res.status === 204 ? undefined : await res.json()) as T;
}

export const listAttachment = () => call<Attachment[]>("", "GET");
export const getAttachment = (id: string) => call<Attachment>(`/${id}`, "GET");
export const createAttachment = (body: AttachmentInput, idempotencyKey: string) => call<Attachment>("", "POST", { "Idempotency-Key": idempotencyKey }, body);
export const updateAttachment = (id: string, body: Partial<AttachmentInput>, ifMatch: string) => call<Attachment>(`/${id}`, "PATCH", { "If-Match": ifMatch }, body);
export const removeAttachment = (id: string, ifMatch: string) => call<void>(`/${id}`, "DELETE", { "If-Match": ifMatch });
