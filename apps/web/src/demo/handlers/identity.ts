// Identity admin screens: the legal entities of the tenant and the customer portal contacts (apps/web/src/modules/admin/security).
// The contacts are people at the demo customers (company.ts) who can sign in to the customer portal.
import { CUSTOMERS, LEGAL_ENTITY } from "../company";
import { reply, route } from "../backend";
import { at, table, uid } from "../store";

const entities = [
  { legalEntityId: LEGAL_ENTITY.id, code: LEGAL_ENTITY.code, displayName: LEGAL_ENTITY.name, status: "active", createdAt: at(-400) },
];
route("GET", "/api/v1/legal-entities", () => ({ items: entities }));

interface Contact {
  id: string;
  contactId: string;
  email: string;
  displayName: string;
  customerId: string;
  legalEntityIds: string[];
  status: "enabled" | "disabled";
  invitationSent: boolean;
  invitedAt: string;
  lastInvitationSentAt?: string;
}

const contacts = table<Contact>([
  { id: uid(120, 1), contactId: uid(120, 1), email: "dana.ortiz@bluewater.example", displayName: "Dana Ortiz", customerId: CUSTOMERS[0]!.id, legalEntityIds: [LEGAL_ENTITY.id], status: "enabled", invitationSent: true, invitedAt: at(-60), lastInvitationSentAt: at(-60) },
  { id: uid(120, 2), contactId: uid(120, 2), email: "sam.keller@harlan.example", displayName: "Sam Keller", customerId: CUSTOMERS[1]!.id, legalEntityIds: [LEGAL_ENTITY.id], status: "enabled", invitationSent: true, invitedAt: at(-30), lastInvitationSentAt: at(-30) },
  { id: uid(120, 3), contactId: uid(120, 3), email: "former.buyer@summit.example", displayName: "Former Buyer", customerId: CUSTOMERS[2]!.id, legalEntityIds: [LEGAL_ENTITY.id], status: "disabled", invitationSent: true, invitedAt: at(-200), lastInvitationSentAt: at(-200) },
]);

const view = ({ id: _id, ...contact }: Contact) => contact;
const known = new Set(entities.map((e) => e.legalEntityId));
const mappingErrors = (body: { customerId?: string; legalEntityIds?: string[] }): Record<string, string> => {
  const errors: Record<string, string> = {};
  if (!CUSTOMERS.some((c) => c.id === body.customerId)) errors.customerId = "must be a customer of this tenant";
  if (!body.legalEntityIds?.length || body.legalEntityIds.some((id) => !known.has(id))) errors.legalEntityIds = "every legal entity must exist and be active in this tenant";
  return errors;
};

route("GET", "/api/v1/identity/customer-contacts", () => ({ items: contacts.all().map(view) }));
route("POST", "/api/v1/identity/customer-contacts", ({ body }) => {
  const b = (body ?? {}) as { email?: string; givenName?: string; familyName?: string; customerId?: string; legalEntityIds?: string[] };
  const email = (b.email ?? "").trim().toLowerCase();
  const errors = mappingErrors(b);
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) errors.email = "must be an email address";
  if (Object.keys(errors).length > 0) return reply.problem(400, "COM-0001", "invalid customer contact", errors);
  if (contacts.all().some((c) => c.email === email)) return reply.problem(409, "IDN-CONTACT-EXISTS", "a customer contact with this email address already exists");
  const id = crypto.randomUUID();
  const stored = contacts.insert({
    id, contactId: id, email, displayName: `${b.givenName ?? ""} ${b.familyName ?? ""}`.trim(), customerId: b.customerId as string, legalEntityIds: b.legalEntityIds as string[],
    status: "enabled", invitationSent: true, invitedAt: at(0), lastInvitationSentAt: at(0),
  });
  return reply.created(view(stored));
});
route("PATCH", "/api/v1/identity/customer-contacts/{id}", ({ params, body }) => {
  const current = contacts.get(params.id!);
  if (!current) return reply.problem(404, "IDN-CONTACT-NOT-FOUND", "customer contact not found");
  if (current.status === "disabled") return reply.problem(409, "IDN-CONTACT-DISABLED", "the contact is disabled");
  const b = (body ?? {}) as { customerId?: string; legalEntityIds?: string[] };
  const errors = mappingErrors(b);
  if (Object.keys(errors).length > 0) return reply.problem(400, "COM-0001", "invalid customer contact mapping", errors);
  return view(contacts.update(params.id!, { customerId: b.customerId as string, legalEntityIds: b.legalEntityIds as string[] }) as Contact);
});
route("POST", "/api/v1/identity/customer-contacts/{id}:resend", ({ params }) => {
  const current = contacts.get(params.id!);
  if (!current) return reply.problem(404, "IDN-CONTACT-NOT-FOUND", "customer contact not found");
  if (current.status === "disabled") return reply.problem(409, "IDN-CONTACT-DISABLED", "the contact is disabled");
  return view(contacts.update(params.id!, { invitationSent: true, lastInvitationSentAt: at(0) }) as Contact);
});
route("POST", "/api/v1/identity/customer-contacts/{id}:disable", ({ params }) => {
  if (!contacts.get(params.id!)) return reply.problem(404, "IDN-CONTACT-NOT-FOUND", "customer contact not found");
  return view(contacts.update(params.id!, { status: "disabled" }) as Contact);
});
