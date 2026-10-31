# Identity authorization integration

This package implements a service-side JWT verifier and OPA decision client. Configure `JWTVerifier` with a tenant's trusted issuer, expected audience, tenant ID, and JWKS URL. Route requests through its middleware before `RequireOPA`; the verifier checks RS256 signature, issuer, audience, `sub`, `tid`, `iat`, `exp`, and `nbf`, then overwrites the untrusted gateway principal with the verified subject. `RequireOPA` rejects contexts without the verifier's private marker. Duties are looked up in OPA's tenant-scoped bundle assignments; callers cannot provide duties in a header or token. An unreachable or malformed policy response returns 503.

```go
policy, err := identity.NewOPAClient("http://127.0.0.1:8181", nil)
if err != nil {
	log.Fatal(err)
}
verifier, err := identity.NewJWTVerifier(issuer, "passage-api", tenantID, jwksURL, nil)
if err != nil {
	log.Fatal(err)
}
r.With(verifier.Middleware, identity.RequireOPA(policy, http.MethodPut, "/v1/vendors")).Put("/v1/vendors/{id}", handler)
```

Run `go test ./...` from this directory. The bundle is under `../../policies/authz`; `opa test --timeout 120s ../../policies/authz` runs its Rego contract tests. The policy bundle seeds every duty the Go services enforce and starts with no assignments; the policy compiler must populate assignments per tenant before access is granted. Each route passes its HTTP method and resource path to the policy middleware.

Tenant-level writes need the `tenant.administer` duty (a flag duty, no routes) with no legal-entity scope, on top of the specific admin duty. The platform operator (`Maintain tenants`, operator tenant) seeds the first holder: `POST /v1/tenants` takes optional `adminUserId` and `adminIdpSubject`, and `POST /v1/tenants/{id}/admins` (`userId`, `reason`) grants it in an existing tenant. Before deploying, add `tenant.administer` to the duties claim of existing tenant administrators in the Keycloak duties mapper; the last active tenant-wide `tenant.administer` assignment of a tenant cannot be ended (`IDN-LAST-TENANT-ADMIN`).

Stable problem codes in this slice: `AUTH-0001` unauthenticated or invalid token, `AUTH-0003` policy denial, `AUTH-0404` tenant or gateway identity mismatch, `AUTH-0502` JWKS unavailable, `AUTH-0503` OPA unavailable.

## Wave 2 status

This is the first isolated slice, not a complete Wave 2 implementation. The following remain: Keycloak realm and deployment, OIDC/SAML federation and MFA, gateway JWT/host validation, SCIM provisioning, persistent tenant/legal-entity and role-assignment APIs (the tenant, user, legal-entity and effective-dated role-assignment tables with forced RLS live in `migrations/`), policy compilation and bundle distribution/version checks, Envoy Gateway deployment and per-tenant rate limits, and the admin-security web module with effective-permission reporting. Those need follow-up owned-path PRs.

## Schema grants and scoping

`passage_app` has SELECT and INSERT only on `identity_tenants` (insert is limited by RLS to the tenant id the transaction is scoped to): status changes and renames belong to the migrator, so a tenant cannot reactivate itself, rename its realm, or probe other tenants' realm names (only the operator tenant can read other tenants). `identity_role_assignments` rejects overlapping grants of the same user, role and legal entity (an exclusion constraint, open-ended `valid_to` counts as infinity), so a revoke always takes effect. A caller scoped to specific legal entities does not see tenant-wide assignments (null `legal_entity_id`); only a `*` scope does.

## Effective permissions

`LoadCatalogue` parses `../../policies/authz/data.json` into an engine that mirrors `authz.rego`: `Evaluate(duties, method, resource)` returns allow, the lexically first held duty that grants it, and (on deny) the policy's least-privileged `duty_missing`. `Effective(duties)` evaluates every distinct resource pattern and method in the catalogue at a sample path (`{,/**}` becomes `/item-1`, `*` becomes `item-1`). Globs outside literal text, `*` and a trailing `{,/**}` fail at load so the engine cannot drift from OPA silently.

`NewEffectivePermissionsAPI(pool, catalogue).Register(r)` serves `GET /v1/identity/users/{id}/effective-permissions` (duty `View effective permissions`, OpenAPI in `api/openapi.yaml`). Until the role catalogue exists (#83), `identity_role_assignments.role` holds a duty name. Active assignments (`valid_from <= now() < valid_to`) are resolved: tenant-wide rows always, plus rows for `?legalEntityId=` when given. Roles that are not seeded duties are listed in `ignoredRoles` and grant nothing, as in OPA. A caller without the all-entity scope, scoped or unscoped, receives 403.

`effective_test.go` checks the engine against the real policy (OPA server, same harness as `opa_contract_test.go`): 100 routes sampled evenly across the sorted catalogue, times eight fixture principals, must match OPA on allow and `duty_missing`; the full report is also compared row by row for four principals.

## Tenant, legal-entity and role-assignment APIs

`NewTenantsAPI(pool, operatorTenantID)`, `NewLegalEntitiesAPI(pool)` and `NewRoleAssignmentsAPI(pool, catalogue, operatorTenantID)` serve `/v1/tenants` (create, list, get), `/v1/legal-entities` (create, list, get) and `/v1/authz/assignments` (assign, list by `userId` and/or `role` (one is required), get, `POST .../{id}/end` and its alias `DELETE .../{id}`). They reuse the seeded duties `Maintain tenants`, `Maintain legal entities` and `Maintain security roles` and the catalogue's resource patterns, so the duty count stays unchanged.

All three run on the ordinary `passage_app` pool; no path bypasses RLS. Tenant routes are operator-only: the caller must hold `Maintain tenants` and belong to `operatorTenantID`. Create sets `passage.tenant_id` to the new tenant's id inside its transaction so the row passes its own RLS check (`passage_app` holds INSERT, not UPDATE or DELETE, on `identity_tenants`) and writes the audit row in the operator tenant too. List and get read through the `identity_tenants_operator_read` policy, which admits SELECT only when the caller's tenant is the single row of `identity_operator`; the migrator or deploy seeds that row with the same id as `operatorTenantID`.

Tenant-level writes (creating a legal entity, a tenant-wide assignment, ending one) run with an explicit `*` legal-entity scope set by the handler, and only when the request carries no legal-entity scope and the caller holds the matching admin duty (`Maintain legal entities` or `Maintain security roles`); every other request runs with the caller's own scope. Assigning a role to yourself is refused (403 `IAM-0416`), `Maintain tenants` can only be granted in the operator tenant, and past or inactive-legal-entity assignments are refused.

An assigned role must be a seeded duty name. The no-overlap rule for one user, role and legal entity is the `identity_role_assignments_no_overlap` exclusion constraint (409 `IDN-ASSIGNMENT-OVERLAP`); a revoke is `end` (never a delete), which only shortens; ending a future-dated assignment sets `valid_to = valid_from`, an empty range that overlaps nothing and grants nothing. Every mutation writes an `identity_audit` row (actor from `X-Passage-User`) and an `outbox` event (`dev.passage.identity.{tenant,legalentity,roleassignment}.{created,assigned,ended}.v1`) in the same transaction as the change.

## User directory and role/duty catalogue

`NewUsersAPI(pool)` serves `GET /v1/identity/users` (filters `status`, `q`, `legalEntityId`; keyset paging) and `GET /v1/identity/users/{id}` under the seeded duty `Maintain users`. Users belong to the tenant, not to a legal entity, so every holder sees the whole tenant; `legalEntityId` narrows to users with an active (current, not ended or future) assignment for that entity and is bounded by the caller's own scope through the assignments' RLS.

`NewCatalogueAPI(catalogue)` serves `GET /v1/authz/roles[/{name}]` and `GET /v1/authz/duties[/{name}]` under `Maintain security roles`. Until roles are first-class, a role is an assignable duty (the same names `POST /v1/authz/assignments` accepts); duty detail lists the routes a duty grants. The catalogue is global, read from `data.json`, so it needs no database.

### SCIM 2.0 provisioning

`NewSCIMAPI(pool, verifiers, opa)` serves `/scim/v2/Users`, `/scim/v2/Groups` (list, create, get, `PUT`, `PATCH`, `DELETE`) and the discovery documents `ServiceProviderConfig`, `Schemas` and `ResourceTypes`. It sits outside `tenant.Middleware` (the gateway cannot supply `X-Passage-Tenant` until host-to-tenant routing lands, #213): `cmd/identity` mounts the admin APIs in a group with the header middleware and SCIM in a group with its own bearer middleware.

- **Auth.** The bearer JWT's `tid` claim only selects the tenant's verifier; the issuer (`IDENTITY_SCIM_ISSUER_BASE/<realm_name>`) and JWKS URL come from the tenant registry, only `active` tenants resolve, and an unknown tenant is the same 401 as a bad token. The policy then decides on the request method and path, and the seeded duty `Provision users via SCIM` (`/scim/v2{,/**}`) is a person duty, so an operator assigns it to the IdP's service account like any role. SCIM is mounted only when `IDENTITY_SCIM_ISSUER_BASE`, `IDENTITY_SCIM_AUDIENCE` and `IDENTITY_OPA_URL` are all set.
- **Users.** `userName` (unique per tenant, case-insensitive), `externalId`, `displayName`, `emails` (the primary one is stored), `active`; `name` only defaults `displayName`, and other attributes are ignored. A created user gets the placeholder `idp_subject` `scim:<id>`, to be bound when the Keycloak push (#80) lands. Only users that carry a `userName` are visible to SCIM, so a SCIM client cannot read or deprovision users it did not create.
- **Deactivate.** `DELETE` and `PATCH active=false` (also the string `"False"` Entra sends) set the user `deprovisioned` and end every active and future role assignment in the same transaction; each change writes an audit row and an outbox event (`user.deprovisioned`, `roleassignment.ended`). Reactivating does not restore access.
- **Groups** are provisioning records, not roles: role assignments are dated, scoped and audited with the self-grant guard, so a group sync never grants access. Members must be provisioned users of the tenant.
- **Wire format.** Responses and errors use `application/scim+json` (including 401/403 from the shared middleware); request bodies are decoded leniently. Filters support only `attr eq "value"` on `userName`/`externalId` (users) and `displayName`/`externalId` (groups); paging is `startIndex`/`count` (default and maximum 100 and 200).

## Server binary

`cmd/identity` wires every API above on `httpx.NewRouter()` (gateway-asserted `X-Passage-*` headers). Settings: `DATABASE_URL`, `IDENTITY_DUTY_CATALOGUE_PATH` (the `policies/authz/data.json` the bundle is built from), `IDENTITY_OPERATOR_TENANT_ID` (optional; without it the tenant routes and `Maintain tenants` grants are refused), `MIGRATE_ON_START` with optional `MIGRATE_DATABASE_URL` for the schema owner, `HTTP_ADDR`. `identity_operator` is limited to a single row by a constraint; seeding it with the operator tenant id stays a deploy prerequisite. There is no Dockerfile, chart or gateway route yet.

Not implemented (issue #83 remainder): SCIM 2.0 `/Users` and `/Groups`, OPA bundle build and distribution, Keycloak push (#80), and gateway host-to-tenant mapping (#213).
