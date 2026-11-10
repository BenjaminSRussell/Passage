// Services the shell itself calls on every page: the working-context legal entity picker, the My work inbox and
// the entity lookups that turn ids into names (shell/lookup/entities.ts). Imported after the area handlers, so an
// area file that serves one of these paths more specifically wins.
import type { InboxPage } from "../../shell/inbox/inbox";
import { CUSTOMERS, ITEMS, LEGAL_ENTITY, SITES, VENDORS, WAREHOUSES, WORKERS, type Item, type Party } from "../company";
import { reply, route } from "../backend";
import { at, day } from "../store";

const ORG = "/api/v1/org";

const legalEntities = [{ id: LEGAL_ENTITY.id, code: LEGAL_ENTITY.code, name: LEGAL_ENTITY.name, currency: LEGAL_ENTITY.currency, country: LEGAL_ENTITY.country, status: "active" }];
const sites = SITES.map((s) => ({ ...s, legalEntityId: LEGAL_ENTITY.id, status: "active" }));
const warehouses = WAREHOUSES.map((w) => ({ ...w, legalEntityId: LEGAL_ENTITY.id, status: "active" }));
const currencies = [
  { code: "USD", name: "US dollar", status: "active" },
  { code: "CAD", name: "Canadian dollar", status: "active" },
  { code: "EUR", name: "Euro", status: "active" },
  { code: "MXN", name: "Mexican peso", status: "active" },
];

/** Case-insensitive match of ?q against any of the given fields; an empty q matches everything. */
function matches(q: string | null, ...fields: (string | undefined)[]): boolean {
  const needle = (q ?? "").trim().toLowerCase();
  return needle === "" || fields.some((f) => (f ?? "").toLowerCase().includes(needle));
}

function limit<T>(rows: T[], query: URLSearchParams): T[] {
  const n = Number(query.get("limit") ?? "50");
  return rows.slice(0, Number.isFinite(n) && n > 0 ? n : 50);
}

function orgList<T extends { id: string; code: string; name: string }>(path: string, rows: T[], scoped: (row: T, query: URLSearchParams) => boolean = () => true) {
  route("GET", `${ORG}/${path}`, ({ query }) => ({ items: limit(rows.filter((r) => matches(query.get("q"), r.name, r.code) && scoped(r, query)), query) }));
  route("GET", `${ORG}/${path}/{id}`, ({ params }) => rows.find((r) => r.id === params.id || r.code === params.id) ?? reply.problem(404, "ORG-0404", `No ${path.replace(/-/g, " ").replace(/s$/, "")} with that id`));
}

orgList("legal-entities", legalEntities);
orgList("sites", sites);
orgList("warehouses", warehouses, (w, query) => !query.get("siteId") || w.siteId === query.get("siteId"));
route("GET", `${ORG}/currencies`, () => ({ items: currencies }));

// Customer and vendor roles share the party's id in the demo, so a role id resolves straight to its party.
function role(p: Party, kind: "customer" | "vendor") {
  return { id: p.id, partyId: p.id, legalEntityId: LEGAL_ENTITY.id, accountNumber: p.number, name: p.name, paymentTerms: p.paymentTerms, kind };
}
for (const [kind, rows] of [
  ["customers", CUSTOMERS],
  ["vendors", VENDORS],
] as const) {
  const one = kind === "customers" ? "customer" : "vendor";
  route("GET", `/api/v1/party/${kind}:search`, ({ query }) => ({ items: limit(rows.filter((p) => matches(query.get("q"), p.name, p.number)).map((p) => role(p, one)), query) }));
  route("GET", `/api/v1/party/${kind}/{id}`, ({ params }) => {
    const p = rows.find((r) => r.id === params.id);
    return p ? role(p, one) : reply.problem(404, "PTY-0404", `No ${one} with that id`);
  });
}
route("GET", "/api/v1/party/parties:search", ({ query }) => ({
  items: limit(
    [...CUSTOMERS, ...VENDORS].filter((p) => matches(query.get("q"), p.name, p.number)).map((p) => ({ id: p.id, name: p.name, partyNumber: p.number })),
    query,
  ),
}));

// Products resolve by id or by item number, since some areas use readable item numbers as product ids.
const ITEM_GROUPS = [
  { id: "finished", name: "Finished pumps" },
  { id: "subassembly", name: "Subassemblies" },
  { id: "component", name: "Purchased components" },
  { id: "raw", name: "Raw materials" },
];
function product(i: Item) {
  return { id: i.id, productNumber: i.number, name: i.name, baseUom: i.unit, itemGroupId: i.kind, batchTracked: i.kind === "raw", serialTracked: i.kind === "finished", catchWeight: false };
}
route("GET", "/api/v1/product/setup/item-groups", () => ({ items: ITEM_GROUPS }));
route("GET", "/api/v1/product/products/{id}", ({ params }) => {
  const i = ITEMS.find((it) => it.id === params.id || it.number === params.id);
  return i ? product(i) : reply.problem(404, "PRD-0404", "No product with that id");
});

const workers = WORKERS.map((w) => ({ id: w.id, personnelNumber: w.number, legalName: w.name, preferredName: w.name, status: "active" }));
route("GET", "/api/v1/hr/workers", ({ query }) => ({ items: limit(workers.filter((w) => matches(query.get("q"), w.legalName, w.personnelNumber)), query) }));
route("GET", "/api/v1/hr/workers/{id}", ({ params }) => workers.find((w) => w.id === params.id) ?? reply.problem(404, "HR-0404", "No worker with that id"));

// My work: a short, believable inbox that points into the demo tour.
const inbox: InboxPage = {
  items: [
    { id: "inbox-1", source: "purchasing", kind: "requisition", reason: "Waiting for your approval", number: "REQ-007101", link: "/purchasing/requisitions", actions: ["open"], createdAt: at(-0.3), dueDate: day(1) },
    { id: "inbox-2", source: "sales", kind: "credit-hold", reason: "Order held for credit limit", number: "SO-024185", link: "/sales/credit-holds", actions: ["open"], createdAt: at(-0.5), dueDate: day(0) },
    { id: "inbox-3", source: "production", kind: "late-order", reason: "Running late behind the balancing bottleneck", number: "MO-24618", link: "/production/MO-24618", actions: ["open"], createdAt: at(-1), dueDate: day(-3) },
    { id: "inbox-4", source: "purchasing", kind: "purchase-order", reason: "Purchase order waiting for approval", number: "PO-048207", link: "/purchasing/purchase-orders", actions: ["open"], createdAt: at(-1.4), dueDate: day(2) },
    { id: "inbox-5", source: "quality", kind: "quality-order", reason: "Usage decision pending", number: "QO-24007", link: "/quality/orders/QO-24007", actions: ["open"], createdAt: at(-2) },
  ],
  unavailable: [],
  truncated: [],
};
route("GET", "/api/v1/inbox", () => inbox);

// Record search (GET /api/v1/search): customers, vendors and products from the company data, and the documents
// the other areas hold, read back through their own list routes so the numbers always agree with those screens.
interface SearchHit {
  type: string;
  id: string;
  number: string;
  title: string;
  subtitle?: string;
  status?: string;
  link: string;
}

async function listed(path: string): Promise<Record<string, unknown>[]> {
  try {
    const res = await fetch(path);
    const body = (await res.json()) as { items?: Record<string, unknown>[] };
    return res.ok && Array.isArray(body.items) ? body.items : [];
  } catch {
    return [];
  }
}

const str = (row: Record<string, unknown>, key: string): string => (typeof row[key] === "string" ? (row[key] as string) : "");

async function documents(): Promise<SearchHit[]> {
  const [pos, receipts, orders, slips, invoices] = await Promise.all([
    listed("/api/v1/procurement/purchase-orders"),
    listed("/api/v1/receiving/receipts"),
    listed("/api/v1/sales/orders"),
    listed("/api/v1/fulfillment/packing-slips"),
    listed("/api/v1/billing/invoices"),
  ]);
  const doc = (type: string, row: Record<string, unknown>, number: string, link: string, status: string, subtitle = ""): SearchHit => ({
    type,
    id: str(row, "id"),
    number,
    title: number,
    subtitle: subtitle || undefined,
    status: status || undefined,
    link: `${link}/${str(row, "id")}`,
  });
  return [
    ...pos.map((r) => doc("purchase_order", r, str(r, "number"), "/purchasing/purchase-orders", str(r, "documentStatus"), `${str(r, "currency")} ${str(r, "total")}`.trim())),
    ...receipts.map((r) => doc("receipt", r, str(r, "receiptNumber"), "/receiving/receipts", str(r, "state"))),
    ...orders.map((r) => doc("sales_order", r, str(r, "orderNumber"), "/sales/orders", str(r, "status"))),
    ...slips.map((r) => doc("packing_slip", r, str(r, "packingSlipNumber"), "/fulfillment/packing-slips", str(r, "status"), str(r, "postingDate"))),
    ...invoices.map((r) => doc("customer_invoice", r, str(r, "number"), "/billing/invoices", str(r, "status"))),
  ].filter((h) => h.number !== "" && h.id !== "");
}

route("GET", "/api/v1/search", async ({ query }) => {
  const needle = (query.get("q") ?? "").trim().toLowerCase();
  if (needle.length < 2) return reply.problem(400, "COM-0001", "request failed validation", { q: "must be 2 to 100 characters" });
  const wanted = new Set((query.get("types") ?? "").split(",").filter(Boolean));
  const masters: SearchHit[] = [
    ...[...CUSTOMERS, ...VENDORS].map((p) => ({ type: "party", id: p.id, number: p.number, title: p.name, subtitle: "organization", status: "active", link: `/party/${p.id}` })),
    ...ITEMS.map((i) => ({ type: "product", id: i.id, number: i.number, title: i.name, status: "active", link: `/product/${encodeURIComponent(i.number)}` })),
  ];
  const all = [...masters, ...(await documents())].filter((h) => wanted.size === 0 || wanted.has(h.type));
  const rank = (h: SearchHit) => (h.number.toLowerCase() === needle ? 0 : h.number.toLowerCase().startsWith(needle) ? 1 : 2);
  const results = all
    .filter((h) => h.number.toLowerCase().includes(needle) || h.title.toLowerCase().includes(needle))
    .sort((a, b) => rank(a) - rank(b) || a.number.localeCompare(b.number));
  return { results: limit(results, query) };
});
