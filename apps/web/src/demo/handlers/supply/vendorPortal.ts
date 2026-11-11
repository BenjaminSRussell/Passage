// Buyer side of the supplier portal in the party service (#511): registrations from the public portal page waiting for
// review, the onboarding questionnaires a new vendor answers, and the vendor groups a buyer picks when accepting one.
// Typed against the module's client so drift fails tsc.
import type { Prospect, Questionnaire } from "../../../modules/party/api/client";
import { VENDORS } from "../../company";
import { reply, route } from "../../backend";
import { at, uid } from "../../store";

const PARTY = "/api/v1/party";
const K_PROSPECT = 760;
const K_QUESTIONNAIRE = 761;
const K_VENDOR_GROUP = 762;

const vendorGroups = [
  { id: uid(K_VENDOR_GROUP, 1), code: "TRADE", name: "Trade suppliers", status: "active" },
  { id: uid(K_VENDOR_GROUP, 2), code: "SERVICES", name: "Service providers", status: "active" },
  { id: uid(K_VENDOR_GROUP, 3), code: "FREIGHT", name: "Freight carriers", status: "active" },
];

route("GET", `${PARTY}/vendor-groups`, () => ({ vendorGroups }));
route("GET", `${PARTY}/vendor-groups/{id}`, ({ params }) => vendorGroups.find((g) => g.id === params.id) ?? reply.problem(404, "PTY-SUP-NOT-FOUND", "Vendor group not found"));

const questionnaires: Questionnaire[] = [
  { id: uid(K_QUESTIONNAIRE, 1), name: "New supplier onboarding", version: 3, createdAt: at(-200) },
  { id: uid(K_QUESTIONNAIRE, 2), name: "Castings and forgings quality survey", version: 1, createdAt: at(-90) },
];

route("GET", `${PARTY}/vendor-questionnaires`, () => ({ items: questionnaires }));

const accepted = VENDORS[4];

const prospects: Prospect[] = [
  {
    id: uid(K_PROSPECT, 1),
    companyName: "Lakeshore Machining LLC",
    contactName: "Dana Whitfield",
    email: "dana.whitfield@lakeshoremachining.example",
    phone: "+1 414 555 0137",
    country: "US",
    taxId: "39-2214876",
    website: "https://lakeshoremachining.example",
    message: "CNC turning and milling of pump shafts and impellers, ISO 9001 certified. We would like to quote on your shaft work.",
    status: "NEW",
    createdAt: at(-0.6),
  },
  {
    id: uid(K_PROSPECT, 2),
    companyName: "Northfield Coatings Inc.",
    contactName: "Ravi Menon",
    email: "ravi.menon@northfieldcoatings.example",
    country: "US",
    message: "Powder coating and epoxy lining for pump housings up to 2 m.",
    status: "NEW",
    createdAt: at(-2.3),
  },
  {
    id: uid(K_PROSPECT, 3),
    companyName: "Bayside Industrial Resale",
    contactName: "Chris Moreau",
    email: "sales@baysideresale.example",
    country: "US",
    status: "REJECTED",
    createdAt: at(-12),
    decidedAt: at(-11),
    decisionNote: "We only buy new components from manufacturers or authorised distributors.",
  },
  {
    id: uid(K_PROSPECT, 4),
    companyName: accepted.name,
    contactName: "Megan Ostrowski",
    email: "megan.ostrowski@riverbendsteel.example",
    country: "US",
    taxId: "42-1873390",
    status: "ACCEPTED",
    createdAt: at(-40),
    decidedAt: at(-38),
    partyId: accepted.id,
    vendorId: accepted.id,
    requestId: uid(K_PROSPECT, 104),
  },
];

route("GET", `${PARTY}/vendor-prospects`, ({ query }) => {
  const status = query.get("status");
  return { items: prospects.filter((p) => !status || p.status === status).sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
});

route("GET", `${PARTY}/vendor-prospects/{id}`, ({ params }) => prospects.find((p) => p.id === params.id) ?? reply.problem(404, "PTY-PROSPECT-NOT-FOUND", "not found"));

function decide(id: string, patch: Partial<Prospect>) {
  const at0 = prospects.findIndex((p) => p.id === id);
  if (at0 < 0) return reply.problem(404, "PTY-PROSPECT-NOT-FOUND", "not found");
  if (prospects[at0].status !== "NEW") return reply.problem(409, "PTY-PROSPECT-STATE", "the registration was already decided");
  prospects[at0] = { ...prospects[at0], ...patch, decidedAt: at(0) };
  return prospects[at0];
}

route("POST", `${PARTY}/vendor-prospects/{id}:reject`, ({ params, body }) => {
  const note = String((body as { note?: unknown } | undefined)?.note ?? "").trim();
  if (note === "") return reply.problem(422, "PTY-SUP-INVALID", "request failed validation", { note: "is required, at most 1000 characters" });
  return decide(params.id, { status: "REJECTED", decisionNote: note });
});

route("POST", `${PARTY}/vendor-prospects/{id}:accept`, ({ params, body }) => {
  const input = (body ?? {}) as { accountNumber?: string; vendorGroupId?: string; currencyCode?: string; questionnaireId?: string };
  const errors: Record<string, string> = {};
  if (!input.accountNumber) errors.accountNumber = "is required: 1 to 20 letters, digits, dot, dash or underscore";
  if (!vendorGroups.some((g) => g.id === input.vendorGroupId)) errors.vendorGroupId = "must be a UUID";
  if (!/^[A-Z]{3}$/.test(input.currencyCode ?? "")) errors.currencyCode = "must be a 3-letter ISO 4217 code";
  if (!questionnaires.some((q) => q.id === input.questionnaireId)) errors.questionnaireId = "is required";
  if (Object.keys(errors).length > 0) return reply.problem(422, "PTY-SUP-INVALID", "request failed validation", errors);
  const n = prospects.findIndex((p) => p.id === params.id) + 1;
  return decide(params.id, { status: "ACCEPTED", partyId: uid(K_PROSPECT, 200 + n), vendorId: uid(K_PROSPECT, 300 + n), requestId: uid(K_PROSPECT, 400 + n) });
});
