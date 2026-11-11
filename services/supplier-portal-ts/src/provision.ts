// Creating the identity-provider user of an accepted invitation (#511). Keycloak user administration is live infrastructure and is
// out of scope here; the portal depends on this interface, and the adapter that ships records that the user still has to be created.

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
