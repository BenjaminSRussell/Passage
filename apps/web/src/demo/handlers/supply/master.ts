// Master data lookups other screens use: parties (customers and vendors) and products. Ids come from company.ts.
import type { Party as PartyRecord, Vendor } from "../../../modules/party/api/client";
import { CUSTOMERS, ITEMS, LEGAL_ENTITY, VENDORS, type Party } from "../../company";
import { reply, route } from "../../backend";
import { at, uid } from "../../store";

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

route("GET", `${PRODUCT}/products:search`, ({ query }) => {
  const q = (query.get("q") ?? "").toLowerCase();
  const items = ITEMS.filter((i) => !q || i.number.toLowerCase().includes(q) || i.name.toLowerCase().includes(q)).map((i) => ({ productNumber: i.number, name: i.name }));
  return { items };
});
