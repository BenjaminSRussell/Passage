import type { StatTone } from "../ui";

/** What a tile needs to build its request: the person's working legal entity and today's date (YYYY-MM-DD). */
export interface KpiContext {
  legalEntityId: string;
  today: string;
}

/**
 * One number on a workspace home. It counts the rows of a list the owning service already serves, read with the
 * person's own permissions, and links to the screen that shows that list.
 */
export interface KpiDefinition {
  id: string;
  label: string;
  /** Duty the list needs. Without it the tile is left out and nothing is requested. */
  duty: string;
  /** The list to read, with its filters. Tiles that share a path share one request. */
  path(ctx: KpiContext): string;
  /** Which rows count; every row when left out. */
  counts?(row: Record<string, unknown>, ctx: KpiContext): boolean;
  /** The screen that lists what the tile counts. */
  to: string;
  tone?: StatTone;
}

/** How many rows one request asks for. A list with more shows as "100+". */
export const KPI_PAGE_SIZE = 100;
const DUE_SOON_DAYS = 7;

export function addDays(day: string, days: number): string {
  const date = new Date(`${day}T00:00:00Z`);
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

function query(params: Record<string, string | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value) search.set(key, value);
  }
  return `?${search.toString()}`;
}

const size = String(KPI_PAGE_SIZE);
const text = (row: Record<string, unknown>, key: string) => (typeof row[key] === "string" ? (row[key] as string) : "");

const payablesOpenItems = (ctx: KpiContext, filter: Record<string, string>) => `/api/v1/payables/open-items${query({ legal_entity: ctx.legalEntityId, ...filter, limit: size })}`;
const purchaseOrders = (approvalStatus: string) => `/api/v1/procurement/purchase-orders${query({ approvalStatus, limit: size })}`;
const journalBatches = (status: string) => `/api/v1/ledger/journal-batches${query({ status })}`;
const salesOrders = (filter: Record<string, string>) => `/api/v1/sales/orders${query({ ...filter, limit: size })}`;
const receivablesOpenItems = () => `/api/v1/receivables/open-items${query({ status: "open", limit: "500" })}`;
const unsettled = (row: Record<string, unknown>) => text(row, "status") === "OPEN" || text(row, "status") === "PARTIALLY_SETTLED";

/** The tiles of each shipped workspace, by workspace id. A workspace a tenant adds has none. */
export const workspaceKpis: Record<string, KpiDefinition[]> = {
  "ap-clerk": [
    { id: "ap-on-hold", label: "Invoices on payment hold", duty: "open-item.read", path: (ctx) => payablesOpenItems(ctx, { held: "true" }), to: "/payables/open-items", tone: "warning" },
    {
      id: "ap-due-soon",
      label: `Invoices due in the next ${DUE_SOON_DAYS} days`,
      duty: "open-item.read",
      path: (ctx) => payablesOpenItems(ctx, { due_from: ctx.today, due_to: addDays(ctx.today, DUE_SOON_DAYS - 1) }),
      counts: unsettled,
      to: "/payables/open-items",
    },
    { id: "ap-proposals", label: "Payment proposals to approve", duty: "payment-proposal.read", path: (ctx) => `/api/v1/payables/payment-proposals${query({ legal_entity: ctx.legalEntityId, status: "DRAFT", limit: size })}`, to: "/payables" },
  ],
  buyer: [
    { id: "po-in-review", label: "Purchase orders waiting for approval", duty: "procurement.purchase-order.inquire", path: () => purchaseOrders("IN_REVIEW"), to: "/purchasing/purchase-orders" },
    { id: "po-open", label: "Purchase orders confirmed and still open", duty: "procurement.purchase-order.inquire", path: () => purchaseOrders("CONFIRMED"), to: "/purchasing/purchase-orders" },
    {
      id: "po-overdue",
      label: "Purchase order lines overdue",
      duty: "procurement.purchase-order.inquire",
      path: (ctx) => `/api/v1/procurement/inquiries/overdue-lines${query({ legalEntityId: ctx.legalEntityId, limit: size })}`,
      to: "/purchasing/inquiries/overdue",
      tone: "danger",
    },
  ],
  controller: [
    { id: "journals-draft", label: "Journals in draft", duty: "ledger.journal.read", path: () => journalBatches("draft"), to: "/ledger/journal-batches?status=draft" },
    { id: "journals-in-approval", label: "Journals waiting for approval", duty: "ledger.journal.read", path: () => journalBatches("in_approval"), to: "/ledger/journal-batches?status=in_approval" },
    { id: "journals-approved", label: "Journals approved, not posted", duty: "ledger.journal.read", path: () => journalBatches("approved"), to: "/ledger/journal-batches?status=approved", tone: "warning" },
  ],
  "sales-rep": [
    { id: "so-open", label: "Open sales orders", duty: "sales.order.read", path: () => salesOrders({ status: "OPEN" }), to: "/sales/orders" },
    { id: "so-confirmed", label: "Confirmed orders to ship", duty: "sales.order.read", path: () => salesOrders({ status: "CONFIRMED" }), to: "/sales/orders" },
    { id: "so-credit-hold", label: "Orders on credit hold", duty: "sales.credit.release", path: () => salesOrders({ hold: "CREDIT" }), to: "/sales/credit-holds", tone: "warning" },
  ],
  "ar-clerk": [
    { id: "ar-open", label: "Open customer items", duty: "receivables.view", path: receivablesOpenItems, to: "/receivables/open-items" },
    {
      id: "ar-overdue",
      label: "Overdue customer invoices",
      duty: "receivables.view",
      path: receivablesOpenItems,
      counts: (row, ctx) => text(row, "side") !== "CREDIT" && text(row, "dueDate") !== "" && text(row, "dueDate") < ctx.today,
      to: "/receivables/open-items",
      tone: "danger",
    },
  ],
};

export interface KpiCount {
  value: number;
  /** True when the list has more rows than were read, so the value is a floor. */
  more: boolean;
}

/** Counts the rows of a list response, which the services send as { items, nextCursor } or as a bare array. */
export function countRows(body: unknown, definition: KpiDefinition, ctx: KpiContext): KpiCount {
  const page = (body ?? {}) as { items?: unknown; nextCursor?: unknown };
  const rows = (Array.isArray(body) ? body : Array.isArray(page.items) ? page.items : []) as Record<string, unknown>[];
  const counted = definition.counts ? rows.filter((row) => row !== null && typeof row === "object" && definition.counts?.(row, ctx)) : rows;
  return { value: counted.length, more: typeof page.nextCursor === "string" && page.nextCursor !== "" };
}
