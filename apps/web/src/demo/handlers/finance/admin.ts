// Security administration for the demo: identity users (/api/v1/identity/users), the identity service's legal
// entities (/api/v1/legal-entities), the role and duty catalogue and role assignments (/api/v1/authz), and the
// effective-permissions report. Users and their roles come from finance/people.ts, so the SoD conflicts name the
// same people and roles.
import type { DutyDetail, DutySummary, EffectivePermissions, LegalEntity, RoleAssignment, RoleSummary, User } from "../../../modules/admin/security/api";
import { LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";
import { at, newId, table, uid } from "../../store";
import { DUTIES } from "./duties";
import { byUsername, emailOf, ME, PEOPLE } from "./people";

const K_ASSIGNMENT = 621;
const K_ENTITY = 622;

// --- users -----------------------------------------------------------------------------------

const users = table<User & { id: string }>(
  PEOPLE.map((p) => ({
    id: p.id,
    userId: p.id,
    idpSubject: `keycloak:${p.username}`,
    email: emailOf(p),
    displayName: p.name,
    status: p.status,
    createdAt: at(-p.since),
  })),
);
const userOf = ({ id: _id, ...u }: User & { id: string }): User => u;

// --- legal entities (identity) ------------------------------------------------------------------

const entities = table<LegalEntity & { id: string }>([{ id: LEGAL_ENTITY.id, legalEntityId: LEGAL_ENTITY.id, code: LEGAL_ENTITY.code, displayName: LEGAL_ENTITY.name, status: "active", createdAt: at(-1500) }]);
const entityOf = ({ id: _id, ...e }: LegalEntity & { id: string }): LegalEntity => e;

// --- assignments ----------------------------------------------------------------------------------

let n = 0;
const assignment = (userId: string, role: string, validFromDaysAgo: number, extra: Partial<RoleAssignment> = {}): RoleAssignment & { id: string } => {
  const assignmentId = uid(K_ASSIGNMENT, ++n);
  return { id: assignmentId, assignmentId, userId, role, validFrom: at(-validFromDaysAgo), grantedBy: "tom.becker", createdAt: at(-validFromDaysAgo), ...extra };
};

const assignments = table<RoleAssignment & { id: string }>([
  ...PEOPLE.flatMap((p) =>
    p.roles.map((role, i) =>
      assignment(p.id, role, p.since - i * 37, {
        reason: i === 0 ? `Job role: ${p.title}` : undefined,
        ...(p.status === "deprovisioned" ? { validTo: at(-120), reason: "Left the company; ended by deprovisioning" } : {}),
      }),
    ),
  ),
  // History: temporary cover that has ended, and one that runs out at the end of the month.
  assignment(byUsername("maya.chen").id, "receiving.vendor-invoice.post", 95, { validTo: at(-60), reason: "Cover for Ana Ortiz during the system upgrade", grantedBy: ME.username }),
  assignment(byUsername("dana.kim").id, "reporting.statement.run", 12, { validTo: at(20), reason: "Quarter-end close support", grantedBy: ME.username }),
]);
const assignmentOf = ({ id: _id, ...a }: RoleAssignment & { id: string }): RoleAssignment => a;
const active = (a: RoleAssignment) => Date.parse(a.validFrom) <= Date.now() && (!a.validTo || Date.parse(a.validTo) > Date.now());

// --- routes -----------------------------------------------------------------------------------------

const matches = (q: string | null, ...fields: (string | undefined)[]) => {
  const needle = (q ?? "").trim().toLowerCase();
  return needle === "" || fields.some((f) => (f ?? "").toLowerCase().includes(needle));
};

route("GET", "/api/v1/identity/users", ({ query }) => {
  const status = query.get("status");
  const items = users
    .all()
    .filter((u) => (!status || u.status === status) && matches(query.get("q"), u.displayName, u.email))
    .sort((a, b) => a.displayName.localeCompare(b.displayName))
    .map(userOf);
  return { items };
});
route("GET", "/api/v1/identity/users/{id}/effective-permissions", ({ params, query }): EffectivePermissions | ReturnType<typeof reply.problem> => {
  const user = users.get(params.id);
  if (!user) return reply.problem(404, "IAM-0404", "user not found");
  const roles = assignments.all().filter((a) => a.userId === user.id && active(a)).map((a) => a.role);
  const duties = [...new Set(roles)];
  const permissions = duties.flatMap((d) =>
    (DUTIES.find((x) => x.name === d)?.permissions ?? []).flatMap((p) => p.methods.map((method) => ({ resource: p.resource, method, allow: true, duty: d }))),
  );
  return { userId: user.userId, ...(query.get("legalEntityId") ? { legalEntityId: query.get("legalEntityId")! } : {}), duties, ignoredRoles: [], permissions };
});
route("GET", "/api/v1/identity/users/{id}", ({ params }) => {
  const u = users.get(params.id);
  return u ? userOf(u) : reply.problem(404, "IAM-0404", "user not found");
});

route("GET", "/api/v1/legal-entities", () => ({ items: entities.all().map(entityOf) }));
route("POST", "/api/v1/legal-entities", ({ body }) => {
  const input = (body ?? {}) as { code?: string; displayName?: string };
  const code = (input.code ?? "").toUpperCase();
  if (entities.all().some((e) => e.code === code)) return reply.problem(409, "IDN-LEGAL-ENTITY-EXISTS", "a legal entity with this code exists", { code: "A legal entity with this code already exists." });
  const id = uid(K_ENTITY, entities.all().length + 1);
  return reply.created(entityOf(entities.insert({ id, legalEntityId: id, code, displayName: input.displayName ?? code, status: "active", createdAt: at(0) })));
});

const assignable = DUTIES.filter((d) => !d.serviceOnly);
route("GET", "/api/v1/authz/roles", ({ query }): { items: RoleSummary[] } => ({ items: assignable.filter((d) => matches(query.get("q"), d.name)).map((d) => ({ name: d.name, duties: [d.name] })) }));
route("GET", "/api/v1/authz/roles/{name}", ({ params }) => {
  const d = assignable.find((x) => x.name === params.name);
  return d ? { name: d.name, duties: [d satisfies DutyDetail] } : reply.problem(404, "IAM-ROLE-NOT-FOUND", "role not found");
});
route("GET", "/api/v1/authz/duties", ({ query }): { items: DutySummary[] } => {
  const serviceOnly = query.get("serviceOnly");
  return {
    items: DUTIES.filter((d) => matches(query.get("q"), d.name) && (serviceOnly === null || String(d.serviceOnly) === serviceOnly)).map((d) => ({
      name: d.name,
      serviceOnly: d.serviceOnly,
      permissionCount: d.permissions.reduce((s, p) => s + p.methods.length, 0),
    })),
  };
});

route("GET", "/api/v1/authz/assignments", ({ query }) => {
  const userId = query.get("userId");
  const role = query.get("role");
  const onlyActive = query.get("active") === "true";
  const items = assignments
    .all()
    .filter((a) => (!userId || a.userId === userId) && (!role || a.role === role) && (!onlyActive || active(a)))
    .sort((a, b) => b.validFrom.localeCompare(a.validFrom))
    .map(assignmentOf);
  return { items };
});
route("POST", "/api/v1/authz/assignments", ({ body }) => {
  const input = (body ?? {}) as { userId?: string; role?: string; legalEntityId?: string; validFrom?: string; validTo?: string; reason?: string };
  if (input.userId === ME.id) return reply.problem(403, "IAM-0416", "you cannot grant a role to yourself");
  if (!users.get(input.userId ?? "")) return reply.problem(422, "IAM-0422", "no user with that id", { userId: "No user has this id." });
  if (!assignable.some((d) => d.name === input.role)) return reply.problem(422, "IAM-0422", "no role with that name", { role: "No role has this name." });
  const overlap = assignments.all().some((a) => a.userId === input.userId && a.role === input.role && active(a));
  if (overlap) return reply.problem(409, "IDN-ASSIGNMENT-OVERLAP", "the user already holds this role for that time", { role: "The user already holds this role." });
  const id = newId();
  const row = { id, assignmentId: id, userId: input.userId!, role: input.role!, ...(input.legalEntityId ? { legalEntityId: input.legalEntityId } : {}), validFrom: input.validFrom ?? at(0), ...(input.validTo ? { validTo: input.validTo } : {}), ...(input.reason ? { reason: input.reason } : {}), grantedBy: ME.username, createdAt: at(0) };
  return reply.created(assignmentOf(assignments.insert(row)));
});
route("POST", "/api/v1/authz/assignments/{id}/end", ({ params }) => {
  const a = assignments.get(params.id);
  if (!a) return reply.problem(404, "IAM-0404", "assignment not found");
  if (!active(a)) return reply.problem(409, "IDN-ASSIGNMENT-ENDED", "the assignment has already ended");
  return assignmentOf(assignments.update(a.id, { validTo: at(0) })!);
});
