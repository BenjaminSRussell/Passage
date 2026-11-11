// Master data lookups other screens use: parties (customers and vendors) and products. Ids come from company.ts.
import type { Party as PartyRecord, Vendor } from "../../../modules/party/api/client";
import type { Product } from "../../../modules/product/api/client";
import { CUSTOMERS, ITEMS, LEGAL_ENTITY, SERVICES, USER, VENDORS, type Item, type Party, type ServiceProduct } from "../../company";
import { reply, route } from "../../backend";
import { at, uid } from "../../store";
import { JORDAN, personById } from "../hr/people";
import { workerDetail } from "../hr/self";

const PARTY = "/api/v1/party";
const PRODUCT = "/api/v1/product";

const parties = [...CUSTOMERS, ...VENDORS];

function toParty(p: Party, n: number): PartyRecord {
  return {
    id: p.id,
    partyNumber: p.number,
    partyType: "organization",
    name: p.name,
    searchName: p.name.toUpperCase(),
    status: "active",
    legalName: p.name,
    tradeName: p.name.replace(/,? (Inc\.|Co\.|Co-op)$/, ""),
    registrationNumber: `${p.state}-${String(4_100_000 + n * 1731)}`,
    languageCode: "en",
    timeZone: "America/Chicago",
    version: 1 + (n % 3),
    createdAt: at(-420 + n * 9),
    updatedAt: at(-30 - n),
  };
}

route("GET", `${PARTY}/parties/{id}`, ({ params }) => {
  const i = parties.findIndex((p) => p.id === params.id);
  if (i < 0) return reply.problem(404, "PTY-0404", "Party not found");
  const rec = toParty(parties[i], i);
  return reply.versioned(rec, rec.version);
});

route("GET", `${PARTY}/parties/{id}/vendors`, ({ params }) => {
  const v = VENDORS.find((x) => x.id === params.id);
  if (!v) return { vendors: [] };
  const role: Vendor = {
    id: uid(330, VENDORS.indexOf(v) + 1),
    partyId: v.id,
    legalEntityId: LEGAL_ENTITY.id,
    accountNumber: v.number,
    vendorGroupId: "TRADE",
    currencyCode: "USD",
    blocked: "none",
    version: 1,
  };
  return { vendors: [role] };
});

// Products: the catalog list, one product by id or item number, the released list, and the lookup search. Item groups
// are the four item kinds (handlers/shell.ts serves their names).
const KIND_DESCRIPTION = { finished: "Finished pump, built to order and stocked", subassembly: "Manufactured subassembly", component: "Purchased component", raw: "Raw material" } as const;

function product(i: Item): Product {
  return {
    id: i.id,
    productNumber: i.number,
    name: i.name,
    description: `${KIND_DESCRIPTION[i.kind]}. Lead time ${i.leadTimeDays} days.`,
    searchName: i.name.toUpperCase(),
    subtype: "product",
    productType: "item",
    baseUom: i.unit,
    itemGroupId: i.kind,
    batchTracked: i.kind === "raw",
    serialTracked: i.kind === "finished",
    catchWeight: false,
    lifecycleState: "active",
    version: 1,
  };
}

/** A service sold with the pumps: a product that is not stocked (company.ts SERVICES). */
function serviceProduct(s: ServiceProduct): Product {
  return { ...product({ ...ITEMS[0], id: s.id, number: s.number, name: s.name }), description: "Service, not stocked.", productType: "service", baseUom: "ea", itemGroupId: "", serialTracked: false };
}

const findItems = (q: string | null) => {
  const needle = (q ?? "").trim().toLowerCase();
  return ITEMS.filter((i) => !needle || i.number.toLowerCase().includes(needle) || i.name.toLowerCase().includes(needle));
};

route("GET", `${PRODUCT}/products`, ({ query }) => {
  const limit = Number(query.get("limit") ?? "50") || 50;
  const start = Number(query.get("cursor") ?? "0") || 0;
  const rows = findItems(query.get("q")).sort((a, b) => a.number.localeCompare(b.number));
  const page = rows.slice(start, start + limit);
  return { items: page.map(product), ...(start + limit < rows.length ? { nextCursor: String(start + limit) } : {}) };
});

route("GET", `${PRODUCT}/products/{id}`, ({ params }) => {
  const i = ITEMS.find((it) => it.id === params.id || it.number === params.id);
  if (i) return reply.versioned(product(i), 1);
  const s = SERVICES.find((it) => it.id === params.id || it.number === params.id);
  return s ? reply.versioned(serviceProduct(s), 1) : reply.problem(404, "PRD-0404", "Product not found");
});

route("GET", `${PRODUCT}/released-products`, () => ({ items: ITEMS.map((i) => ({ id: i.id })), next: "" }));

route("GET", `${PRODUCT}/products:search`, ({ query }) => ({ items: findItems(query.get("q")).slice(0, 10).map(product) }));

// The signed-in user can appear as an actor on documents the demo changes; resolve that id to their name.
route("GET", "/api/v1/hr/workers/{id}", ({ params }) => {
  const p = personById(params.id === USER.id ? JORDAN : params.id);
  return p ? workerDetail(p) : reply.problem(404, "HR-0404", "No worker with that id");
});
