// Pricing service (/api/v1/pricing) for the demo: trade agreements (condition records), the customer price list
// and price simulation over the same records the sales order lines were priced from (see model.ts).
import type { AppliedCondition, ConditionRecord, ConditionRecordInput, NetPriceRow, PriceListItem, ResolveRequest, ResolveResponse, ResolvedLine, TraceStep } from "../../../modules/pricing/api/client";
import { reply, route } from "../../backend";
import { day, dec, uid } from "../../store";
import { K, conditions, customerByKey, itemByKey, priceFor, r2 } from "./model";

const BASE = "/api/v1/pricing";
const OVERLAP = "PRC-0002";

/** What a record's customer filter matches: its key plus the customer's number and name. */
const customerText = (r: ConditionRecord): string => {
  const c = customerByKey(r.customerKey);
  return [r.customerKey, c?.number, c?.name].filter(Boolean).join(" ").toLowerCase();
};
const productText = (r: ConditionRecord): string => [r.productKey, itemByKey(r.productKey)?.name].filter(Boolean).join(" ").toLowerCase();
const csvCell = (v: string | number | boolean): string => (/[",\n]/.test(String(v)) ? `"${String(v).replace(/"/g, '""')}"` : String(v));

function filtered(query: URLSearchParams): ConditionRecord[] {
  const customer = query.get("customer")?.toLowerCase();
  const product = query.get("product")?.toLowerCase();
  const asOf = query.get("asOf");
  const endsFrom = query.get("validToFrom");
  const endsTo = query.get("validToTo");
  const rows = conditions
    .all()
    .filter(
      (r) =>
        (!query.get("status") || r.status === query.get("status")) &&
        (!query.get("type") || r.type === query.get("type")) &&
        (!query.get("legalEntityId") || r.legalEntityId === query.get("legalEntityId")) &&
        (!customer || customerText(r).includes(customer)) &&
        (!product || productText(r).includes(product)) &&
        (!asOf || (r.validFrom <= asOf && r.validTo >= asOf)) &&
        (!endsFrom || r.validTo >= endsFrom) &&
        (!endsTo || r.validTo <= endsTo),
    );
  // Products together, customer-specific records after the list prices: how a pricing analyst reads the grid.
  rows.sort((a, b) => a.productKey.localeCompare(b.productKey) || a.type.localeCompare(b.type) || a.customerKey.localeCompare(b.customerKey) || Number(a.fromQuantity) - Number(b.fromQuantity));
  return rows;
}

route("GET", `${BASE}/condition-records`, ({ query }) => ({ items: filtered(query).slice(0, Number(query.get("limit") ?? 100)) }));

// The export is registered before {id} reads so the colon action is not taken for an id.
route("GET", `${BASE}/condition-records:export`, ({ query }) => {
  const header = ["type", "status", "customer_number", "customer", "product_number", "product", "unit", "currency", "from_quantity", "amount", "discount_pct_1", "discount_pct_2", "valid_from", "valid_to"];
  const lines = filtered(query).map((r) => {
    const c = r.customerKind === "customer" ? customerByKey(r.customerKey) : undefined;
    const i = r.productKind === "product" ? itemByKey(r.productKey) : undefined;
    return [r.type, r.status, c?.number ?? (r.customerKind === "all" ? "ALL" : r.customerKey), c?.name ?? "All customers", i?.number ?? (r.productKind === "all" ? "ALL" : r.productKey), i?.name ?? "All products", r.unit, r.currency, r.fromQuantity, r.amount, r.discPct1, r.discPct2, r.validFrom, r.validTo].map(csvCell).join(",");
  });
  return reply.text([header.join(","), ...lines].join("\n") + "\n", "text/csv; charset=utf-8", "trade-agreements.csv");
});

route("POST", `${BASE}/condition-records`, ({ body }) => {
  const input = body as ConditionRecordInput;
  const n = conditions.all().length + 1;
  const created = conditions.insert({ ...input, id: `PRC-${String(100 + n).padStart(4, "0")}`, status: "DRAFT", version: 1 });
  return reply.created(created);
});
route("POST", `${BASE}/condition-records/{id}:activate`, ({ params }) => {
  const r = conditions.get(params.id);
  if (!r) return reply.problem(404, "PRC-0404", "Condition record not found");
  const clash = conditions.all().find((o) => o.id !== r.id && o.status === "ACTIVE" && o.type === r.type && o.customerKey === r.customerKey && o.productKey === r.productKey && o.fromQuantity === r.fromQuantity && o.validFrom <= r.validTo && o.validTo >= r.validFrom);
  if (clash) return reply.problem(409, OVERLAP, `The record overlaps the active record ${clash.id.slice(0, 8)} for the same customer, product and quantity break.`);
  return conditions.update(r.id, { status: "ACTIVE", version: r.version + 1 });
});
route("POST", `${BASE}/condition-records/{id}:withdraw`, ({ params }) => {
  const r = conditions.get(params.id);
  if (!r) return reply.problem(404, "PRC-0404", "Condition record not found");
  return conditions.update(r.id, { status: "WITHDRAWN", version: r.version + 1 });
});
route("GET", `${BASE}/condition-records/{id}`, ({ params }) => conditions.get(params.id) ?? reply.problem(404, "PRC-0404", "Condition record not found"));

// ---- Price list ----

function applicable(customerKey: string, asOf: string): { record: ConditionRecord; scope: PriceListItem["scope"] }[] {
  const customer = customerByKey(customerKey);
  return conditions
    .all()
    .filter((r) => r.status === "ACTIVE" && r.validFrom <= asOf && r.validTo >= asOf)
    .flatMap((r): { record: ConditionRecord; scope: PriceListItem["scope"] }[] => {
      if (r.customerKind === "all") return [{ record: r, scope: "ALL" as const }];
      if (r.customerKind === "customer" && customer && (r.customerKey === customer.number || r.customerKey === customer.id)) return [{ record: r, scope: "CUSTOMER" as const }];
      return [];
    });
}
const rank = { CUSTOMER: 0, GROUP: 1, ALL: 2 } as const;

route("GET", `${BASE}/price-list/net-prices`, ({ query }) => {
  const asOf = query.get("asOf") ?? day(0);
  const customerKey = query.get("customerId") ?? "";
  const customer = customerByKey(customerKey);
  const wanted = query.get("productId") ? itemByKey(query.get("productId")!) : undefined;
  const prices = applicable(customerKey, asOf).filter((a) => a.record.type === "PRICE" && (!wanted || a.record.productKey === wanted.number));
  const byProduct = new Map<string, Set<number>>();
  for (const { record } of prices) byProduct.set(record.productKey, (byProduct.get(record.productKey) ?? new Set<number>()).add(Number(record.fromQuantity)));
  const rows: NetPriceRow[] = [...byProduct.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([productKey, quantities]) => {
      const item = itemByKey(productKey);
      return {
        productKind: "product",
        productKey,
        variant: "",
        unit: item?.unit ?? "ea",
        breaks: [...quantities]
          .sort((a, b) => a - b)
          .map((qty) => {
            const p = customer ? priceFor(customer, productKey, qty, asOf) : { unitPrice: 0, discPct: 0, priceRecord: undefined, discRecord: undefined };
            const net = r2(p.unitPrice * (1 - p.discPct / 100));
            return {
              quantity: dec(qty, 0),
              unitPrice: dec(p.unitPrice),
              netUnitPrice: dec(net),
              lineNet: dec(r2(net * qty)),
              priceRecordId: p.priceRecord?.id,
              applied: [...(p.priceRecord ? [{ type: "PRICE" as const, recordId: p.priceRecord.id }] : []), ...(p.discRecord ? [{ type: "LINE_DISC" as const, recordId: p.discRecord.id }] : [])],
            };
          }),
      };
    });
  return { rows, truncated: false };
});

route("GET", `${BASE}/price-list`, ({ query }) => {
  const asOf = query.get("asOf") ?? day(0);
  const type = query.get("type");
  const wanted = query.get("productId") ? itemByKey(query.get("productId")!) : undefined;
  const items: PriceListItem[] = applicable(query.get("customerId") ?? "", asOf)
    .filter((a) => (!type || a.record.type === type) && (!wanted || a.record.productKey === wanted.number || a.record.productKind === "all"))
    .sort((a, b) => rank[a.scope] - rank[b.scope] || a.record.productKey.localeCompare(b.record.productKey) || Number(a.record.fromQuantity) - Number(b.record.fromQuantity))
    .map(({ record, scope }) => ({ ...record, scope }));
  return { items, truncated: false, total: items.length };
});

// ---- Simulation ----

route("POST", `${BASE}/resolve`, ({ body, query }) => {
  const input = body as ResolveRequest;
  const customer = customerByKey(input.customerId);
  const explain = query.get("explain") === "true";
  const applied: AppliedCondition[] = [];
  const trace: TraceStep[] = [];
  const lines: ResolvedLine[] = input.lines.map((l, i) => {
    const item = itemByKey(l.productId);
    const qty = Number(l.quantity);
    if (!item || !customer) {
      return { lineNo: i + 1, unit: l.unit, quantity: l.quantity, unitPrice: "0", netUnitPrice: "0", lineNet: "0", applied: [], errorCode: "PRC-0003", errorDetail: !customer ? "The customer is not known to the demo company" : "No active price record for this product" };
    }
    const p = priceFor(customer, item.number, qty, input.pricingDate);
    if (!p.priceRecord) return { lineNo: i + 1, unit: l.unit, quantity: l.quantity, unitPrice: "0", netUnitPrice: "0", lineNet: "0", applied: [], errorCode: "PRC-0003", errorDetail: "No active price record for this product" };
    const net = r2(p.unitPrice * (1 - p.discPct / 100));
    const lineApplied: AppliedCondition[] = [{ type: "PRICE", recordId: p.priceRecord.id, access: p.priceRecord.customerKind === "all" ? 3 : 1 }];
    if (p.discRecord) lineApplied.push({ type: "LINE_DISC", recordId: p.discRecord.id, access: 1 });
    applied.push(...lineApplied);
    const steps: TraceStep[] = [
      {
        condition: "PRICE",
        selected: p.priceRecord.id,
        accesses: [
          { seq: 1, access: "Customer and product", evaluated: true, candidates: conditions.all().filter((r) => r.type === "PRICE" && r.status === "ACTIVE" && r.customerKind === "customer" && r.customerKey === customer.number && r.productKey === item.number).map((r) => r.id), winner: p.priceRecord.customerKind === "customer" ? p.priceRecord.id : undefined, reason: p.priceRecord.customerKind === "customer" ? "Customer record found" : "No customer record for this quantity" },
          { seq: 3, access: "All customers and product", evaluated: p.priceRecord.customerKind === "all", candidates: conditions.all().filter((r) => r.type === "PRICE" && r.status === "ACTIVE" && r.customerKind === "all" && r.productKey === item.number).map((r) => r.id), winner: p.priceRecord.customerKind === "all" ? p.priceRecord.id : undefined, reason: p.priceRecord.customerKind === "all" ? `Highest quantity break at or below ${qty}` : "Skipped, a customer record won" },
        ],
      },
      { condition: "LINE_DISC", selected: p.discRecord?.id, accesses: [{ seq: 1, access: "Customer and product", evaluated: true, candidates: p.discRecord ? [p.discRecord.id] : [], winner: p.discRecord?.id, reason: p.discRecord ? `${p.discRecord.discPct1}% from ${p.discRecord.fromQuantity} units` : "No line discount applies" }] },
    ];
    if (explain) trace.push(...steps);
    return { lineNo: i + 1, unit: l.unit, quantity: l.quantity, unitPrice: dec(p.unitPrice), netUnitPrice: dec(net), lineNet: dec(r2(net * qty)), applied: lineApplied, trace: explain ? steps : undefined };
  });
  const subtotal = lines.reduce((s, l, i) => s + Number(l.unitPrice) * Number(input.lines[i].quantity), 0);
  const orderNet = lines.reduce((s, l) => s + Number(l.lineNet), 0);
  const response: ResolveResponse = {
    currency: input.currency,
    lines,
    subtotal: dec(subtotal),
    totalDiscount: dec(subtotal - orderNet),
    surcharge: "0",
    orderNet: dec(orderNet),
    applied,
    trace: explain ? trace : undefined,
    explainRef: `explain-${uid(K.condition, 900).slice(-8)}`,
    freeGoods: [],
  };
  return response;
});
