import { describe, expect, it } from "vitest";
import { KeycloakProvisioner, KeycloakProvisionError, type NewPortalUser } from "../src/provision.ts";
import { provisionerFromEnv } from "../src/main.ts";

const TENANT = "11111111-1111-4111-8111-111111111111";
const VENDOR = "0b1c2d3e-0000-4000-8000-0000000000aa";
const LE = "00000000-0000-4000-8000-0000000000a1";
const BASE = "https://id.example.invalid";
const ADMIN = `${BASE}/admin/realms/passage`;

const user: NewPortalUser = { email: "Sam@Vendor.example", displayName: "Sam Supplier", tenantId: TENANT, legalEntityId: LE, vendorId: VENDOR, role: "supplier-user" };

interface Call {
  method: string;
  url: string;
  body: unknown;
}

/** A fake Keycloak: the token endpoint, the users it holds, the supplier-user role and the role mappings it was asked for. */
function fakeKeycloak(existing: Record<string, unknown> | null, existingRoles: { name: string }[] = [], opts: { credentials?: unknown[]; conflictOnCreate?: boolean } = {}) {
  const calls: Call[] = [];
  let users = existing ? [existing] : [];
  const fetcher = async (url: string, init?: RequestInit): Promise<Response> => {
    const method = init?.method ?? "GET";
    const body = typeof init?.body === "string" && init.body.startsWith("[") || typeof init?.body === "string" && init.body.startsWith("{") ? JSON.parse(init.body as string) : init?.body;
    calls.push({ method, url, body });
    if (url.endsWith("/protocol/openid-connect/token")) return Response.json({ access_token: "admin-token", expires_in: 300 });
    if (method === "GET" && url.startsWith(`${ADMIN}/users?`)) {
      const q = new URL(url).searchParams;
      const [field, value] = q.has("email") ? ["email", q.get("email")] : ["username", q.get("username")];
      return Response.json(users.filter((u) => String(u[field] ?? "").toLowerCase() === value));
    }
    if (method === "GET" && url.endsWith("/credentials")) return Response.json(opts.credentials ?? []);
    if (method === "POST" && url === `${ADMIN}/users` && opts.conflictOnCreate) return new Response(null, { status: 409 });
    if (method === "POST" && url === `${ADMIN}/users`) {
      users = [{ id: "kc-new", ...(body as object) }];
      return new Response(null, { status: 201 });
    }
    if (method === "GET" && url.endsWith("/role-mappings/realm")) return Response.json(existingRoles);
    if (method === "GET" && url === `${ADMIN}/roles/supplier-user`) return Response.json({ id: "role-1", name: "supplier-user" });
    if (method === "POST" && url.endsWith("/role-mappings/realm")) return new Response(null, { status: 204 });
    if (method === "PUT") return new Response(null, { status: 204 });
    return new Response(null, { status: 404 });
  };
  const provisioner = new KeycloakProvisioner({ baseUrl: `${BASE}/`, realm: "passage", clientId: "portal-admin", clientSecret: "s", portalClientId: "passage-portal-supplier", redirectUri: "https://supplier.example.invalid/", fetcher });
  return { calls, provisioner };
}

describe("Keycloak provisioner", () => {
  it("creates the user with the portal claims, the supplier-user role and the invitation email", async () => {
    const kc = fakeKeycloak(null);
    expect(await kc.provisioner.createUser(user)).toEqual({ created: true });
    const create = kc.calls.find((c) => c.method === "POST" && c.url === `${ADMIN}/users`);
    expect(create?.body).toMatchObject({
      username: "sam@vendor.example",
      email: "sam@vendor.example",
      enabled: true,
      requiredActions: ["VERIFY_EMAIL", "UPDATE_PASSWORD", "CONFIGURE_TOTP"],
      attributes: { tenant_id: [TENANT], legal_entity_id: [LE], vendor_id: [VENDOR], portal_contact: ["supplier"] },
    });
    const role = kc.calls.find((c) => c.method === "POST" && c.url === `${ADMIN}/users/kc-new/role-mappings/realm`);
    expect(role?.body).toEqual([{ id: "role-1", name: "supplier-user" }]);
    const email = kc.calls.find((c) => c.method === "PUT" && c.url.includes("/execute-actions-email"));
    expect(new URL(email!.url).searchParams.get("client_id")).toBe("passage-portal-supplier");
    expect(kc.calls.every((c) => c.url.endsWith("/token") || c.url.startsWith(ADMIN))).toBe(true);
  });

  it("adopts its own leftover of an earlier attempt, and nothing else", async () => {
    // Mid-onboarding: the required actions are still pending. Its legal-entity list is kept, never replaced.
    const leftover = { id: "kc-old", username: "sam@vendor.example", email: "sam@vendor.example", enabled: true, requiredActions: ["UPDATE_PASSWORD"], attributes: { tenant_id: [TENANT], vendor_id: [VENDOR], legal_entity_id: ["le-earlier"], portal_contact: ["supplier"] } };
    const kc = fakeKeycloak(leftover, [{ name: "default-roles-passage" }]);
    expect(await kc.provisioner.createUser(user)).toEqual({ created: true });
    expect(kc.calls.some((c) => c.method === "PUT" && c.url === `${ADMIN}/users/kc-old`)).toBe(false);
    expect(kc.calls.some((c) => c.method === "POST" && c.url === `${ADMIN}/users`)).toBe(false);
    // Unverified with no credential is mid-onboarding too; a missing attribute is added, the others stay.
    const unverified = { id: "kc-u", username: "sam@vendor.example", email: "sam@vendor.example", enabled: true, emailVerified: false, requiredActions: [], attributes: { tenant_id: [TENANT], vendor_id: [VENDOR], portal_contact: ["supplier"] } };
    const kc2 = fakeKeycloak(unverified, [{ name: "supplier-user" }]);
    expect(await kc2.provisioner.createUser(user)).toEqual({ created: true });
    const put = kc2.calls.find((c) => c.method === "PUT" && c.url === `${ADMIN}/users/kc-u`);
    expect((put?.body as { attributes: Record<string, string[]> }).attributes).toEqual({ tenant_id: [TENANT], vendor_id: [VENDOR], portal_contact: ["supplier"], legal_entity_id: [LE] });
  });

  it("leaves a staff, disabled or other vendor's account untouched and answers pending", async () => {
    const staff = { id: "kc-staff", username: "sam@vendor.example", enabled: true, attributes: { tenant_id: [TENANT], department: ["finance"] } };
    const otherVendor = { id: "kc-v", username: "sam@vendor.example", enabled: true, attributes: { tenant_id: [TENANT], vendor_id: ["0b1c2d3e-0000-4000-8000-0000000000bb"], portal_contact: ["supplier"] } };
    const disabled = { id: "kc-d", username: "sam@vendor.example", enabled: false, attributes: { tenant_id: [TENANT], vendor_id: [VENDOR], portal_contact: ["supplier"] } };
    for (const existing of [staff, otherVendor, disabled]) {
      const kc = fakeKeycloak(existing);
      const out = await kc.provisioner.createUser(user);
      expect(out.created).toBe(false);
      expect(kc.calls.filter((c) => c.method !== "GET" && !c.url.endsWith("/token"))).toEqual([]);
    }
    const admin = { id: "kc-a", username: "sam@vendor.example", enabled: true, attributes: { tenant_id: [TENANT], vendor_id: [VENDOR], portal_contact: ["supplier"] } };
    const kc = fakeKeycloak(admin, [{ name: "supplier-admin" }]);
    expect((await kc.provisioner.createUser(user)).created).toBe(false);
  });

  it("never adopts an active colleague: onboarding done means pending, with Keycloak untouched", async () => {
    const attrs = { tenant_id: [TENANT], vendor_id: [VENDOR], legal_entity_id: [LE], portal_contact: ["supplier"] };
    const active = { id: "kc-act", username: "sam@vendor.example", email: "sam@vendor.example", enabled: true, emailVerified: true, requiredActions: [], attributes: attrs };
    const withPassword = { ...active, id: "kc-pw", emailVerified: false };
    for (const [existing, credentials] of [[active, []], [withPassword, [{ type: "password" }]]] as const) {
      const kc = fakeKeycloak(existing, [{ name: "supplier-user" }], { credentials: [...credentials] });
      expect((await kc.provisioner.createUser(user)).created).toBe(false);
      expect(kc.calls.filter((c) => c.method !== "GET" && !c.url.endsWith("/token"))).toEqual([]);
    }
  });

  it("answers pending for an address held under another username, or a create Keycloak refuses with 409", async () => {
    const other = { id: "kc-x", username: "sam.s", email: "sam@vendor.example", enabled: true, attributes: {} };
    const kc = fakeKeycloak(other);
    expect((await kc.provisioner.createUser(user)).created).toBe(false);
    expect(kc.calls.some((c) => c.method === "POST" && c.url === `${ADMIN}/users`)).toBe(false);
    const raced = fakeKeycloak(null, [], { conflictOnCreate: true });
    expect((await raced.provisioner.createUser(user)).created).toBe(false);
    expect(raced.calls.some((c) => c.url.includes("/execute-actions-email"))).toBe(false);
  });

  it("fails when Keycloak refuses, so the invitation is not accepted", async () => {
    const provisioner = new KeycloakProvisioner({ baseUrl: BASE, realm: "passage", clientId: "c", clientSecret: "s", portalClientId: "p", redirectUri: "/", fetcher: async () => new Response(null, { status: 401 }) });
    await expect(provisioner.createUser(user)).rejects.toBeInstanceOf(KeycloakProvisionError);
  });

  it("is used only when the admin URL is configured", () => {
    const log = () => {};
    expect(provisionerFromEnv({}, () => "", log).constructor.name).toBe("PendingProvisioner");
    const env = { KEYCLOAK_ADMIN_URL: BASE, KEYCLOAK_ADMIN_CLIENT_ID: "portal-admin", KEYCLOAK_ADMIN_CLIENT_SECRET_FILE: "/run/secret" };
    expect(provisionerFromEnv(env, () => "s\n", log)).toBeInstanceOf(KeycloakProvisioner);
  });
});
