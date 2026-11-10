import { z } from "zod";
import { defaultFetch, requestUrl, type FetchLike } from "../api/http";

const inboxItem = z.object({
  id: z.string(),
  source: z.string(),
  kind: z.string(),
  reason: z.string(),
  number: z.string(),
  link: z.string().startsWith("/"),
  actions: z.array(z.string()),
  createdAt: z.string(),
  dueDate: z.string().optional(),
});

const inboxPage = z.object({
  items: z.array(inboxItem),
  nextCursor: z.string().optional(),
  unavailable: z.array(z.object({ source: z.string(), reason: z.string() })),
  truncated: z.array(z.string()),
});

export type InboxItem = z.infer<typeof inboxItem>;
export type InboxPage = z.infer<typeof inboxPage>;

export const INBOX_DUTY = "inbox.read";
export const INBOX_PAGE_SIZE = 50;

const sourceNames: Record<string, string> = {
  purchasing: "requisition approvals",
  party: "customer changes",
  ledger: "journal batches",
  payables: "payment holds",
};

export function sourceName(source: string): string {
  return sourceNames[source] ?? "other work";
}

export function sourceList(sources: string[]): string {
  return [...new Set(sources.map(sourceName))].join(", ");
}

export function fetchInbox(
  query: { legalEntityId: string; cursor?: string; limit?: number },
  signal?: AbortSignal,
  fetchImpl: FetchLike = defaultFetch,
): Promise<InboxPage> {
  const params = new URLSearchParams({ limit: String(query.limit ?? INBOX_PAGE_SIZE) });
  if (query.legalEntityId !== "") params.set("legalEntityId", query.legalEntityId);
  if (query.cursor) params.set("cursor", query.cursor);
  return requestUrl("GET", `/api/v1/inbox?${params.toString()}`, inboxPage, fetchImpl, {}, undefined, signal);
}

export type InboxDecision = "approve" | "reject";

const decisionDuties: Record<string, string> = { party: "party.approve" };

export function decisionDuty(item: InboxItem): string | undefined {
  return item.actions.includes("approve") ? decisionDuties[item.source] : undefined;
}

export async function decideInboxItem(item: InboxItem, decision: InboxDecision, fetchImpl: FetchLike = defaultFetch): Promise<void> {
  await requestUrl("POST", `/api/v1/party/pending-changes/${encodeURIComponent(item.id)}:${decision}`, z.unknown(), fetchImpl);
}
