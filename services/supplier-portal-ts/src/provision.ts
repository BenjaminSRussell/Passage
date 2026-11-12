// Creating the identity-provider user of an accepted invitation (#511). KeycloakProvisioner creates it through the Keycloak Admin
// API; PendingProvisioner, the default without KEYCLOAK_ADMIN_URL, records that an administrator still has to create it.

export interface NewPortalUser {
  email: string;
  displayName: string;
  tenantId: string;
  legalEntityId: string;
  vendorId: string;
  /** Always supplier-user: a vendor administrator is made in the identity provider, never by an invitation. */
  role: "supplier-user";
}

export type ProvisionResult = { created: true } | { created: false; pending: string };

export interface UserProvisioner {
  /** Creates the user, or says why it was not created. Rejects when the identity provider could not be reached. */
  createUser(user: NewPortalUser): Promise<ProvisionResult>;
}

/** The default until a Keycloak admin client is wired: the invitation is accepted and logged, and an administrator creates the user. */
export class PendingProvisioner implements UserProvisioner {
  private readonly log: (entry: Record<string, unknown>) => void;
  constructor(log: (entry: Record<string, unknown>) => void = () => {}) {
    this.log = log;
  }
  async createUser(user: NewPortalUser): Promise<ProvisionResult> {
    this.log({ msg: "invitation accepted; the user must be created in the identity provider", vendorId: user.vendorId, role: user.role });
    return { created: false, pending: "the identity provider user is created by an administrator" };
  }
}

/** For tests: remembers the users it was asked to create. */
export class FakeProvisioner implements UserProvisioner {
  readonly users: NewPortalUser[] = [];
  private readonly fail: Error | undefined;
  constructor(fail?: Error) {
    this.fail = fail;
  }
  async createUser(user: NewPortalUser): Promise<ProvisionResult> {
    if (this.fail) throw this.fail;
    this.users.push(user);
    return { created: true };
  }
}

export interface KeycloakProvisionerConfig {
  /** Keycloak's base URL, such as https://id.example.com. */
  baseUrl: string;
  realm: string;
  /** A confidential client with the realm-management roles manage-users and view-realm (client credentials). */
  clientId: string;
  clientSecret: string;
  /** The supplier portal's OIDC client: the invitation email's link returns there. */
  portalClientId: string;
  redirectUri: string;
  fetcher: (url: string, init?: RequestInit) => Promise<Response>;
  log?: (entry: Record<string, unknown>) => void;
}

/** The role every invited user gets; supplier-admin is granted by an administrator in Keycloak, never by an invitation. */
export const SUPPLIER_USER_ROLE = "supplier-user";
/** Set on every user this flow creates, so a retry can tell its own leftover from any other account. */
const MARKER = "portal_contact";
const PORTAL_ATTRIBUTES = new Set(["tenant_id", "legal_entity_id", "vendor_id", MARKER]);
const REQUIRED_ACTIONS = ["VERIFY_EMAIL", "UPDATE_PASSWORD", "CONFIGURE_TOTP"];
/** How long the invitation email's link works, in seconds (three days). */
const INVITATION_LIFESPAN = 3 * 24 * 3600;
const KEYCLOAK_DEFAULT_ROLES = (name: string) => name === "offline_access" || name === "uma_authorization" || name.startsWith("default-roles-");

/** Keycloak refused or answered in a way the flow cannot use; the invitation is not accepted. */
export class KeycloakProvisionError extends Error {}

/**
 * Creates the Keycloak user of an accepted invitation, the way identity's customer contacts are made (keycloak_contacts.go): an
 * enabled user with the supplier-user role, the portal's claims as attributes and the three required actions, then the email that
 * completes them. An existing user with the address is adopted only when it is provably a leftover of this flow (the marker, only
 * portal attributes, the same tenant and vendor, enabled, no realm role beyond supplier-user and Keycloak's defaults); any other
 * existing account is left untouched and the invitation answers pending, so a staff or another vendor's account is never taken over.
 */
export class KeycloakProvisioner implements UserProvisioner {
  private readonly cfg: KeycloakProvisionerConfig;
  private token: { value: string; expiresAt: number } | null = null;

  constructor(cfg: KeycloakProvisionerConfig) {
    this.cfg = { ...cfg, baseUrl: cfg.baseUrl.replace(/\/$/, "") };
  }

  async createUser(user: NewPortalUser): Promise<ProvisionResult> {
    const email = user.email.trim().toLowerCase();
    const attributes = {
      tenant_id: [user.tenantId.toLowerCase()],
      legal_entity_id: [user.legalEntityId.toLowerCase()],
      vendor_id: [user.vendorId.toLowerCase()],
      [MARKER]: ["supplier"],
    };
    const pending = (): ProvisionResult => {
      this.cfg.log?.({ msg: "invitation accepted; an account with this address already exists and is not this flow's", vendorId: user.vendorId });
      return { created: false, pending: "an account with this address already exists; an administrator links it" };
    };
    const byName = await this.findUser("username", email);
    const byEmail = await this.findUser("email", email);
    // An account that holds the address under another username is someone else's: it is never adopted.
    if (byEmail && (!byName || String(byEmail["id"] ?? "") !== String(byName["id"] ?? ""))) return pending();
    const existing = byName;
    let id: string;
    if (existing) {
      id = String(existing["id"] ?? "");
      if (!(await this.adoptable(existing, user))) return pending();
      // Only the attributes the leftover lacks are added: an existing legal-entity list (or any other value) is never replaced.
      const current = (existing["attributes"] as Record<string, unknown[]> | undefined) ?? {};
      const missing = Object.fromEntries(Object.entries(attributes).filter(([name]) => !(Array.isArray(current[name]) && current[name].length > 0 && String(current[name][0]) !== "")));
      if (Object.keys(missing).length > 0) {
        await this.expect("update user", await this.call("PUT", `/users/${encodeURIComponent(id)}`, { ...existing, attributes: { ...current, ...missing } }), [200, 204]);
      }
    } else {
      const rep = { username: email, email, firstName: user.displayName, enabled: true, emailVerified: false, requiredActions: REQUIRED_ACTIONS, attributes };
      const res = await this.call("POST", "/users", rep);
      // 409: Keycloak holds the username or the address already (created meanwhile, or under another username).
      if (res.status === 409) return pending();
      await this.expect("create user", res, [200, 201]);
      const created = await this.findUser("username", email);
      id = String(created?.["id"] ?? "");
      if (!id) throw new KeycloakProvisionError("create user: the created user cannot be found");
    }
    const role = (await this.json("get role", await this.call("GET", `/roles/${SUPPLIER_USER_ROLE}`))) as Record<string, unknown>;
    await this.expect("assign role", await this.call("POST", `/users/${encodeURIComponent(id)}/role-mappings/realm`, [role]), [200, 204]);
    const query = new URLSearchParams({ lifespan: String(INVITATION_LIFESPAN), client_id: this.cfg.portalClientId, redirect_uri: this.cfg.redirectUri });
    await this.expect("send invitation", await this.call("PUT", `/users/${encodeURIComponent(id)}/execute-actions-email?${query}`, REQUIRED_ACTIONS), [200, 204]);
    this.cfg.log?.({ msg: existing ? "invitation accepted; the earlier account was adopted" : "invitation accepted; the user was created", vendorId: user.vendorId });
    return { created: true };
  }

  /**
   * A leftover of this flow that is still mid-onboarding: enabled, the marker and only portal attributes, the same tenant and
   * vendor, no realm role beyond supplier-user and Keycloak's defaults, and onboarding not finished (required actions still
   * pending, or the address unverified and no credential set). An active colleague's account is never adopted.
   */
  private async adoptable(existing: Record<string, unknown>, user: NewPortalUser): Promise<boolean> {
    if (existing["enabled"] !== true) return false;
    const attrs = (existing["attributes"] as Record<string, unknown[]> | undefined) ?? {};
    const names = Object.keys(attrs);
    if (names.length === 0 || !names.every((n) => PORTAL_ATTRIBUTES.has(n)) || !(MARKER in attrs)) return false;
    const first = (n: string) => String(attrs[n]?.[0] ?? "").toLowerCase();
    if (first("tenant_id") !== user.tenantId.toLowerCase() || first("vendor_id") !== user.vendorId.toLowerCase()) return false;
    const id = encodeURIComponent(String(existing["id"] ?? ""));
    const roles = (await this.json("get user roles", await this.call("GET", `/users/${id}/role-mappings/realm`))) as { name?: string }[];
    if (!roles.every((r) => r.name === SUPPLIER_USER_ROLE || KEYCLOAK_DEFAULT_ROLES(r.name ?? ""))) return false;
    const actions = existing["requiredActions"];
    if (Array.isArray(actions) && actions.length > 0) return true;
    if (existing["emailVerified"] === true) return false;
    const credentials = (await this.json("get user credentials", await this.call("GET", `/users/${id}/credentials`))) as unknown[];
    return Array.isArray(credentials) && credentials.length === 0;
  }

  private async findUser(by: "username" | "email", email: string): Promise<Record<string, unknown> | undefined> {
    const found = (await this.json("find user", await this.call("GET", `/users?${new URLSearchParams({ [by]: email, exact: "true" })}`))) as Record<string, unknown>[];
    return Array.isArray(found) ? found.find((u) => String(u[by] ?? "").toLowerCase() === email) : undefined;
  }

  private async accessToken(): Promise<string> {
    if (this.token && this.token.expiresAt > Date.now() + 10_000) return this.token.value;
    const res = await this.cfg.fetcher(`${this.cfg.baseUrl}/realms/${encodeURIComponent(this.cfg.realm)}/protocol/openid-connect/token`, {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded", accept: "application/json" },
      body: new URLSearchParams({ grant_type: "client_credentials", client_id: this.cfg.clientId, client_secret: this.cfg.clientSecret }),
    });
    if (!res.ok) throw new KeycloakProvisionError(`admin token: Keycloak answered ${res.status}`);
    const body = (await res.json()) as { access_token?: string; expires_in?: number };
    if (!body.access_token) throw new KeycloakProvisionError("admin token: no access token");
    this.token = { value: body.access_token, expiresAt: Date.now() + (body.expires_in ?? 60) * 1000 };
    return this.token.value;
  }

  private async call(method: string, path: string, body?: unknown): Promise<Response> {
    const headers: Record<string, string> = { authorization: `Bearer ${await this.accessToken()}`, accept: "application/json" };
    if (body !== undefined) headers["content-type"] = "application/json";
    return this.cfg.fetcher(`${this.cfg.baseUrl}/admin/realms/${encodeURIComponent(this.cfg.realm)}${path}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  }

  private async expect(op: string, res: Response, ok: number[]): Promise<void> {
    if (!ok.includes(res.status)) throw new KeycloakProvisionError(`${op}: Keycloak answered ${res.status}`);
  }

  private async json(op: string, res: Response): Promise<unknown> {
    if (!res.ok) throw new KeycloakProvisionError(`${op}: Keycloak answered ${res.status}`);
    return res.json();
  }
}
